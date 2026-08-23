package session

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
)

type SessionState struct {
	SessionID                string                                `json:"session_id"`
	Archived                 bool                                  `json:"archived,omitempty"`
	ForkedFrom               string                                `json:"forked_from,omitempty"`
	ForkedHash               string                                `json:"forked_hash,omitempty"`
	Goal                     string                                `json:"goal"`
	OriginalGoal             string                                `json:"original_goal,omitempty"`
	OriginalPrompt           string                                `json:"original_prompt,omitempty"`
	TaskDescription          string                                `json:"task_description,omitempty"`
	PlannerOverlay           string                                `json:"planner_overlay,omitempty"`
	Status                   string                                `json:"status,omitempty"`
	TurnsCompleted           int                                   `json:"turns_completed"`
	UpdatedAt                time.Time                             `json:"updated_at"`
	Modes                    []string                              `json:"modes,omitempty"`
	ModeInstructionDir       string                                `json:"mode_instruction_dir,omitempty"`
	PlannerProgress          *conversation.PlannerProgressState    `json:"planner_progress,omitempty"`
	SuspendedUserInput       *conversation.SuspendedUserInputState `json:"suspended_user_input,omitempty"`
	RuntimeStats             *conversation.RuntimeStatsState       `json:"runtime_stats,omitempty"`
	ShellAgentResumable      bool                                  `json:"shell_agent_resumable"`
	ShellAgentTrajectoryPath string                                `json:"shell_agent_trajectory_path"`
	ShellAgentInterruptStep  int                                   `json:"shell_agent_interrupt_step"`
	AnswerTag                string                                `json:"answer_tag,omitempty"`
	CommandTag               string                                `json:"command_tag,omitempty"`
	ModelSelection           *conversation.ModelSelectionState     `json:"model_selection,omitempty"`
	MagnificaHumanitas       *conversation.MagnificaHumanitas      `json:"magnifica_humanitas,omitempty"`
}

type SessionListMode int

const (
	ListActive SessionListMode = iota
	ListArchived
	ListForked
	ListArchivedForked
	ListAll
)

type SessionListOptions struct {
	Mode SessionListMode
}

func sessionStateFromConversation(conv *conversation.Conversation, sessionID string) (*SessionState, error) {
	if conv != nil && conv.Goal != "" {
		ss := SessionState{
			SessionID:                conv.SessionID,
			Archived:                 conv.Archived,
			ForkedFrom:               conv.ForkedFrom,
			ForkedHash:               conv.ForkedHash,
			Goal:                     conv.Goal,
			OriginalGoal:             conv.OriginalGoal,
			OriginalPrompt:           conv.OriginalPrompt,
			ShellAgentResumable:      conv.ShellAgentResumable,
			ShellAgentTrajectoryPath: conv.ShellAgentTrajectoryPath,
			ShellAgentInterruptStep:  conv.ShellAgentInterruptStep,
			AnswerTag:                conv.AnswerTag,
			CommandTag:               conv.CommandTag,
			ModelSelection:           conv.ModelSelection.Clone(),
			MagnificaHumanitas:       conv.MagnificaHumanitas.Clone(),
			TurnsCompleted:           conv.TurnsCompleted,
			SuspendedUserInput:       conv.SuspendedUserInput,
			PlannerProgress:          conv.PlannerProgress,
			RuntimeStats:             conv.RuntimeStats.Clone(),
			Modes:                    conv.Modes,
			ModeInstructionDir:       conv.ModeInstructionDir,
			PlannerOverlay:           conv.PlannerOverlay,
			TaskDescription:          conv.TaskDescription,
			Status:                   conv.Status,
			UpdatedAt:                conv.UpdatedAt,
		}
		return &ss, nil
	}
	return nil, fmt.Errorf("no session state available from conversation for session %s", sessionID)
}

// SessionStateFromConversation is the public wrapper that derives a
// SessionState from a parsed conversation. It delegates to the
// unexported sessionStateFromConversation.
func SessionStateFromConversation(conv *conversation.Conversation, sessionID string) (*SessionState, error) {
	return sessionStateFromConversation(conv, sessionID)
}

// ListSessions returns active session states found in the sessions directory,
// sorted by UpdatedAt in descending order (most recent first).
// Sessions that fail to load (corrupt or missing files) are skipped.
func ListSessions() ([]SessionState, error) {
	return ListSessionsWithOptions(SessionListOptions{})
}

// ListSessionsWithOptions returns session states matching the requested mode.
// Results are sorted by UpdatedAt descending.
func ListSessionsWithOptions(options SessionListOptions) ([]SessionState, error) {
	if options.Mode < ListActive || options.Mode > ListAll {
		return nil, fmt.Errorf("invalid session list mode: %d", options.Mode)
	}

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
		state, err := sessionStateFromConversation(conv, sessionID)
		if err != nil {
			continue
		}
		if !sessionMatchesListMode(state, options.Mode) {
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

func sessionMatchesListMode(state *SessionState, mode SessionListMode) bool {
	switch mode {
	case ListActive:
		return !state.Archived
	case ListArchived:
		return state.Archived
	case ListForked:
		return state.ForkedFrom != ""
	case ListArchivedForked:
		return state.Archived && state.ForkedFrom != ""
	case ListAll:
		return true
	default:
		return false
	}
}
