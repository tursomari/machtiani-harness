package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tursomari/machtiani/agent/internal/mct/internal/contextbuilder"
)

const (
	sessionFileName = ".machtiani-session.json"
	historyLimit    = 10 // Maximum number of conversation turns to keep
)

// loadHistory loads the conversation history from the session file
func LoadHistory() ([]contextbuilder.Message, error) {
	sessionPath := getSessionPath()
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		// Return empty history if file doesn't exist
		return []contextbuilder.Message{}, nil
	}

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	var history []contextbuilder.Message
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, fmt.Errorf("failed to parse session data: %w", err)
	}

	// Limit history length
	if len(history) > historyLimit {
		history = history[len(history)-historyLimit:]
	}

	return history, nil
}

// SaveHistory saves the conversation history to the session file
func SaveHistory(history []contextbuilder.Message) error {
	sessionPath := getSessionPath()

	// Ensure directory exists
	dir := filepath.Dir(sessionPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	// Limit history before saving
	if len(history) > historyLimit {
		history = history[len(history)-historyLimit:]
	}

	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode session data: %w", err)
	}

	if err := os.WriteFile(sessionPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}

	return nil
}

// AddMessage adds a new message to the history and saves it.
// The files slice is optional and only relevant for assistant messages.
func AddMessage(role, content string, files []string) error {
	history, err := LoadHistory()
	if err != nil {
		return err
	}

	var filesCopy []string
	if len(files) > 0 {
		filesCopy = append([]string(nil), files...)
	}

	history = append(history, contextbuilder.Message{
		Role:    role,
		Content: content,
		Files:   filesCopy,
	})

	return SaveHistory(history)
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
