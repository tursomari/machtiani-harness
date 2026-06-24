package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/conversation"
)

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



// SaveSessionState is a deprecated no-op kept for API compatibility.
func SaveSessionState(state SessionState) error {
	return nil
}

func LoadSessionState(sessionID string) (*SessionState, error) {
	return nil, errors.New("LoadSessionState is removed; use loadOrMigrateSessionState")
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
	return nil, fmt.Errorf("no session state available from conversation for session %s", sessionID)
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
	path := filepath.Join(dir, "session-state.json")
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
		convPath, err := artifacts.SessionConversationFile(sessionID)
		if err != nil {
			continue
		}
		convData, err := os.ReadFile(convPath)
		if err != nil {
			continue
		}
		conv, err := conversation.Unmarshal(convData)
		if err != nil {
			continue
		}
		state, err := loadOrMigrateSessionState(conv, sessionID)
		if err != nil {
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

