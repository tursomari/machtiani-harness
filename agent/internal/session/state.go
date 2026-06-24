package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/conversation"
)

const sessionStateFile = "session-state.json"

var ErrSessionStateNotFound = errors.New("session state not found")

type SessionState struct {
	SessionID          string                   `json:"session_id"`
	Goal               string                   `json:"goal"`
	OriginalGoal       string                   `json:"original_goal,omitempty"`
	OriginalPrompt     string                   `json:"original_prompt,omitempty"`
	TaskDescription    string                   `json:"task_description,omitempty"`
	PlannerOverlay     string                   `json:"planner_overlay,omitempty"`
	Status             string                   `json:"status,omitempty"`
	TurnsCompleted     int                      `json:"turns_completed"`
	UpdatedAt          time.Time                `json:"updated_at"`
	Modes              []string                 `json:"modes,omitempty"`
	ModeInstructionDir string                   `json:"mode_instruction_dir,omitempty"`
	PlannerProgress    *conversation.PlannerProgressState    `json:"planner_progress,omitempty"`
	SuspendedUserInput  *conversation.SuspendedUserInputState `json:"suspended_user_input,omitempty"`
	ShellAgentResumable         bool                     `json:"shell_agent_resumable"`
	ShellAgentTrajectoryPath    string                   `json:"shell_agent_trajectory_path"`
	ShellAgentInterruptStep     int                      `json:"shell_agent_interrupt_step"`
}



func SaveSessionState(state SessionState) error {
	state.SessionID = strings.TrimSpace(state.SessionID)
	if state.SessionID == "" {
		return errors.New("session id required to save state")
	}
	dir, err := artifacts.SessionDirectory(state.SessionID)
	if err != nil {
		return fmt.Errorf("resolve session directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure session directory: %w", err)
	}
	state.UpdatedAt = time.Now().UTC()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session state: %w", err)
	}

	path := filepath.Join(dir, sessionStateFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write session state: %w", err)
	}
	return nil
}

func LoadSessionState(sessionID string) (*SessionState, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id required to load state")
	}
	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		return nil, fmt.Errorf("resolve session directory: %w", err)
	}
	path := filepath.Join(dir, sessionStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrSessionStateNotFound
		}
		return nil, fmt.Errorf("read session state: %w", err)
	}
	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal session state: %w", err)
	}
	if state.SessionID != "" && state.SessionID != sessionID {
		return nil, fmt.Errorf("session state mismatch: expected %s, found %s", sessionID, state.SessionID)
	}
	if state.SessionID == "" {
		state.SessionID = sessionID
	}

	// One-time migration for sessions saved under the older schema, which
	// embedded the full conversation JSON inline in session-state.json. If
	// the on-disk conversation.json is missing but an inline copy is
	// present, materialize it to disk before the inline copy is discarded.
	// The on-disk file is authoritative when both are present.
	migrateLegacyConversationJSON(sessionID, data)

	return &state, nil
}

func loadOrMigrateSessionState(conv *conversation.Conversation, sessionID string) (*SessionState, error) {
	if conv != nil && conv.Goal != "" {
		ss := SessionState{
			SessionID:                  conv.SessionID,
			Goal:                       conv.Goal,
			OriginalGoal:               conv.OriginalGoal,
			OriginalPrompt:             conv.OriginalPrompt,
			ShellAgentResumable:        conv.ShellAgentResumable,
			ShellAgentTrajectoryPath:   conv.ShellAgentTrajectoryPath,
			ShellAgentInterruptStep:    conv.ShellAgentInterruptStep,
			TurnsCompleted:             conv.TurnsCompleted,
			SuspendedUserInput:         conv.SuspendedUserInput,
			PlannerProgress:            conv.PlannerProgress,
			Modes:                      conv.Modes,
			ModeInstructionDir:         conv.ModeInstructionDir,
			PlannerOverlay:             conv.PlannerOverlay,
			TaskDescription:            conv.TaskDescription,
			Status:                     conv.Status,
			UpdatedAt:                  conv.UpdatedAt,
		}
		return &ss, nil
	}
	return LoadSessionState(sessionID)
}

// migrateLegacyConversationJSON inspects the raw session-state.json bytes for a
// legacy "conversation_json" field and writes it to the canonical
// conversation.json path when the on-disk file is missing. This is a no-op for
// states written under the slim schema.
func migrateLegacyConversationJSON(sessionID string, raw []byte) {
	if len(raw) == 0 {
		return
	}
	var legacy struct {
		ConversationJSON json.RawMessage `json:"conversation_json"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return
	}
	if len(legacy.ConversationJSON) == 0 {
		return
	}
	// If the value is a JSON string, unquote it so we write valid JSON to disk.
	// A raw JSON object or array is written as-is.
	var convBytes []byte
	if legacy.ConversationJSON[0] == '"' {
		var s string
		if err := json.Unmarshal(legacy.ConversationJSON, &s); err != nil {
			return
		}
		convBytes = []byte(s)
	} else {
		convBytes = legacy.ConversationJSON
	}
	if len(bytes.TrimSpace(convBytes)) == 0 {
		return
	}
	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		return
	}
	if _, err := os.Stat(convPath); err == nil {
		// On-disk conversation wins; discard the inline copy silently.
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(convPath, convBytes, 0o644)
}

func RemoveSessionState(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id required to remove state")
	}
	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		return fmt.Errorf("resolve session directory: %w", err)
	}
	path := filepath.Join(dir, sessionStateFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove session state: %w", err)
	}
	return nil
}

// ListSessions returns all session states found in the sessions directory,
// sorted by UpdatedAt in descending order (most recent first).
// Sessions that fail to load (corrupt or missing files) are skipped.
func ListSessions() ([]SessionState, error) {
	sessionsDir, err := artifacts.SessionsRoot()
	if err != nil {
		return nil, fmt.Errorf("resolve sessions root: %w", err)
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No sessions directory means no sessions
			return []SessionState{}, nil
		}
		return nil, fmt.Errorf("read sessions directory: %w", err)
	}

	var sessions []SessionState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionID := entry.Name()
		state, err := LoadSessionState(sessionID)
		if err != nil {
			// Skip sessions that fail to load (corrupt or missing)
			continue
		}
		sessions = append(sessions, *state)
	}

	// Sort by UpdatedAt descending (most recent first)
	for i := 0; i < len(sessions); i++ {
		for j := i + 1; j < len(sessions); j++ {
			if sessions[j].UpdatedAt.After(sessions[i].UpdatedAt) {
				sessions[i], sessions[j] = sessions[j], sessions[i]
			}
		}
	}

	return sessions, nil
}

