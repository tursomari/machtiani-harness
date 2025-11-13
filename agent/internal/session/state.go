package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

const sessionStateFile = "session-state.json"

var ErrSessionStateNotFound = errors.New("session state not found")

type SessionState struct {
	SessionID          string    `json:"session_id"`
	Goal               string    `json:"goal"`
	TurnsCompleted     int       `json:"turns_completed"`
	TranscriptPath     string    `json:"transcript_path,omitempty"`
	Transcript         string    `json:"transcript"`
	UpdatedAt          time.Time `json:"updated_at"`
	ParentSessionID    string    `json:"parent_session_id,omitempty"`
	MetaModes          []string  `json:"meta_modes,omitempty"`
	MetaInstructionDir string    `json:"meta_instruction_dir,omitempty"`
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
	return &state, nil
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
