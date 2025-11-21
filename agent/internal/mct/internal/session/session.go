package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/mct/internal/contextbuilder"
)

const (
	sessionFileName = ".machtiani-session.json"
	historyLimit    = 10 // Maximum number of conversation turns to keep
)

// FileMeta captures the metadata recorded for a file mirrored into the
// discovery workspace. The hash currently reflects the SHA256 digest of the
// file contents, allowing change detection between sync events.
type FileMeta struct {
	Hash    string `json:"hash,omitempty"`
	Size    int64  `json:"size,omitempty"`
	ModTime int64  `json:"mod_time,omitempty"`
	Tracked bool   `json:"tracked,omitempty"`
}

// FileDiscoveryState persists incremental sync metadata for the filtered
// workspace used by the embedded file-discovery runtime.
type FileDiscoveryState struct {
	WorkspacePath string              `json:"workspace_path,omitempty"`
	PendingPaths  []string            `json:"pending_paths,omitempty"`
	Files         map[string]FileMeta `json:"files,omitempty"`
}

type sessionFile struct {
	History       []contextbuilder.Message `json:"history"`
	FileDiscovery *FileDiscoveryState      `json:"file_discovery,omitempty"`
}

// LoadHistory loads the conversation history from the session file.
func LoadHistory() ([]contextbuilder.Message, error) {
	state, err := loadSessionFile()
	if err != nil {
		return nil, err
	}
	if len(state.History) == 0 {
		return []contextbuilder.Message{}, nil
	}
	return cloneMessages(state.History), nil
}

// SaveHistory saves the conversation history to the session file.
func SaveHistory(history []contextbuilder.Message) error {
	return updateSession(func(st *sessionFile) error {
		st.History = cloneMessages(limitHistory(history))
		return nil
	})
}

// AddMessage adds a new message to the history and saves it.
// The files slice is optional and only relevant for assistant messages.
func AddMessage(role, content string, files []string) error {
	return updateSession(func(st *sessionFile) error {
		entry := contextbuilder.Message{Role: role, Content: content}
		if len(files) > 0 {
			entry.Files = append([]string(nil), files...)
		}
		st.History = append(st.History, entry)
		st.History = limitHistory(st.History)
		return nil
	})
}

// LoadDiscoveryState returns a deep copy of the discovery state persisted in
// the session file. A nil state is returned when no discovery metadata exists
// yet.
func LoadDiscoveryState() (*FileDiscoveryState, error) {
	state, err := loadSessionFile()
	if err != nil {
		return nil, err
	}
	if state.FileDiscovery == nil {
		return nil, nil
	}
	copy := cloneDiscoveryState(state.FileDiscovery)
	return &copy, nil
}

// UpdateDiscoveryState loads, mutates, and persists the discovery state. The
// provided function may return an error to abort the update.
func UpdateDiscoveryState(fn func(*FileDiscoveryState) error) (*FileDiscoveryState, error) {
	var result *FileDiscoveryState
	err := updateSession(func(st *sessionFile) error {
		if st.FileDiscovery == nil {
			st.FileDiscovery = &FileDiscoveryState{}
		}
		if err := fn(st.FileDiscovery); err != nil {
			return err
		}
		copy := cloneDiscoveryState(st.FileDiscovery)
		result = &copy
		return nil
	})
	return result, err
}

// AddPendingDiscoveryPaths ensures the provided paths are staged for the next
// sync with the filtered workspace.
func AddPendingDiscoveryPaths(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return updateSession(func(st *sessionFile) error {
		if st.FileDiscovery == nil {
			st.FileDiscovery = &FileDiscoveryState{}
		}
		st.FileDiscovery.PendingPaths = appendPending(st.FileDiscovery.PendingPaths, paths)
		return nil
	})
}

// getSessionPath returns the path to the session file
// Behavior:
//   - If MACHTIANI_SESSION_ID is set, scope history to that stable session ID
//     at ~/.machtiani/sessions/session-<id>.json
//   - Otherwise, fall back to a per-day file (legacy behavior) to avoid breaking
//     existing flows where the caller doesn't set the session.
func getSessionPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}

	// Base dir for all sessions
	sessionDir := filepath.Join(homeDir, ".machtiani", "sessions")

	// Prefer explicit session ID provided by an agent or the CLI bootstrap
	if sid := sanitizeID(os.Getenv("MACHTIANI_SESSION_ID")); sid != "" {
		return filepath.Join(sessionDir, fmt.Sprintf("session-%s.json", sid))
	}

	// Legacy fallback: per-day file if no session id exists
	timestamp := time.Now().Format("2006-01-02")
	return filepath.Join(sessionDir, fmt.Sprintf("session-%s.json", timestamp))
}

// sanitizeID restricts session id to a filesystem-friendly subset
func sanitizeID(in string) string {
	if in == "" {
		return ""
	}
	out := make([]rune, 0, len(in))
	for _, r := range in {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return ""
	}
	return string(out)
}

func loadSessionFile() (*sessionFile, error) {
	sessionPath := getSessionPath()
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		return &sessionFile{History: []contextbuilder.Message{}}, nil
	}

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return &sessionFile{History: []contextbuilder.Message{}}, nil
	}

	var st sessionFile
	if err := json.Unmarshal(trimmed, &st); err == nil {
		// If both history and discovery are absent but the payload includes a
		// top-level history key, we still treat it as a valid struct.
		if len(st.History) > 0 || st.FileDiscovery != nil || bytes.Contains(trimmed, []byte("\"history\"")) {
			if st.History == nil {
				st.History = []contextbuilder.Message{}
			}
			return &st, nil
		}
	}

	var legacy []contextbuilder.Message
	if err := json.Unmarshal(trimmed, &legacy); err != nil {
		return nil, fmt.Errorf("failed to parse session data: %w", err)
	}
	return &sessionFile{History: limitHistory(legacy)}, nil
}

func saveSessionFile(st *sessionFile) error {
	sessionPath := getSessionPath()
	dir := filepath.Dir(sessionPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	if st.History == nil {
		st.History = []contextbuilder.Message{}
	}

	output, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode session data: %w", err)
	}
	if err := os.WriteFile(sessionPath, output, 0o644); err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}
	return nil
}

func updateSession(fn func(*sessionFile) error) error {
	state, err := loadSessionFile()
	if err != nil {
		return err
	}
	if err := fn(state); err != nil {
		return err
	}
	return saveSessionFile(state)
}

func limitHistory(history []contextbuilder.Message) []contextbuilder.Message {
	if len(history) <= historyLimit {
		return cloneMessages(history)
	}
	return cloneMessages(history[len(history)-historyLimit:])
}

func cloneMessages(history []contextbuilder.Message) []contextbuilder.Message {
	if len(history) == 0 {
		return []contextbuilder.Message{}
	}
	out := make([]contextbuilder.Message, 0, len(history))
	for _, msg := range history {
		dup := contextbuilder.Message{Role: msg.Role, Content: msg.Content}
		if len(msg.Files) > 0 {
			dup.Files = append([]string(nil), msg.Files...)
		}
		out = append(out, dup)
	}
	return out
}

func cloneDiscoveryState(st *FileDiscoveryState) FileDiscoveryState {
	if st == nil {
		return FileDiscoveryState{}
	}
	copy := FileDiscoveryState{
		WorkspacePath: st.WorkspacePath,
	}
	if len(st.PendingPaths) > 0 {
		copy.PendingPaths = append([]string(nil), st.PendingPaths...)
	}
	if len(st.Files) > 0 {
		copy.Files = make(map[string]FileMeta, len(st.Files))
		for k, v := range st.Files {
			copy.Files[k] = v
		}
	}
	return copy
}

func appendPending(existing []string, add []string) []string {
	set := make(map[string]struct{}, len(existing)+len(add))
	for _, p := range existing {
		norm := normalizePath(p)
		if norm != "" {
			set[norm] = struct{}{}
		}
	}
	for _, p := range add {
		norm := normalizePath(p)
		if norm == "" {
			continue
		}
		set[norm] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	merged := make([]string, 0, len(set))
	for p := range set {
		merged = append(merged, p)
	}
	sort.Strings(merged)
	return merged
}

func normalizePath(in string) string {
	trimmed := strings.TrimSpace(in)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." {
		return ""
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, `..`+string(os.PathSeparator)) {
		return ""
	}
	normalized := filepath.ToSlash(cleaned)
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	return normalized
}
