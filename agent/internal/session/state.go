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
	"github.com/tursomari/machtiani/agent/internal/planner"
)

const sessionStateFile = "session-state.json"

var ErrSessionStateNotFound = errors.New("session state not found")

type PatchPlanItem = planner.PatchPlanItem
type PatchPlan = planner.PatchPlan

type SessionState struct {
	SessionID          string                 `json:"session_id"`
	Goal               string                 `json:"goal"`
	OriginalPrompt     string                 `json:"original_prompt,omitempty"`
	TaskDescription    string                 `json:"task_description,omitempty"`
	TurnsCompleted     int                    `json:"turns_completed"`
	TranscriptPath     string                 `json:"transcript_path,omitempty"`
	Transcript         string                 `json:"transcript"`
	UpdatedAt          time.Time              `json:"updated_at"`
	ParentSessionID    string                 `json:"parent_session_id,omitempty"`
	MetaModes          []string               `json:"meta_modes,omitempty"`
	MetaInstructionDir string                 `json:"meta_instruction_dir,omitempty"`
	PlannerProgress    *PlannerProgressState  `json:"planner_progress,omitempty"`
	PendingPatchTurn   *PendingPatchTurnState `json:"pending_patch_turn,omitempty"`
}

// PlannerProgressState captures planner-visible progress across turns so
// retries avoid re-targeting files that already patched successfully.
type PlannerProgressState struct {
	SuccessFiles   []string               `json:"success_files,omitempty"`
	AppliedPatches int                    `json:"applied_patches,omitempty"`
	PendingReview  *planner.PendingReview `json:"pending_review,omitempty"`
}

type PendingPatchTurnState struct {
	Step        int    `json:"step,omitempty"`
	Description string `json:"description,omitempty"`
	Answer      string `json:"answer,omitempty"`
}

func (p *PlannerProgressState) Clone() *PlannerProgressState {
	if p == nil {
		return nil
	}
	clone := &PlannerProgressState{AppliedPatches: p.AppliedPatches}
	if len(p.SuccessFiles) > 0 {
		clone.SuccessFiles = append([]string(nil), p.SuccessFiles...)
	}
	if p.PendingReview != nil {
		clone.PendingReview = p.PendingReview.Clone()
	}
	return clone
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

// LoadPatchPlan returns the cached patch plan for a session. When the patch
// plan file is missing, it returns (nil, nil).
func LoadPatchPlan(sessionID string) (*PatchPlan, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id required to load patch plan")
	}
	path, err := artifacts.SessionPatchPlanFile(sessionID)
	if err != nil {
		return nil, fmt.Errorf("resolve patch plan path: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read patch plan: %w", err)
	}
	var plan PatchPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("unmarshal patch plan: %w", err)
	}
	return &plan, nil
}

// SavePatchPlan persists the provided patch plan for the session.
func SavePatchPlan(sessionID string, plan *PatchPlan) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id required to save patch plan")
	}
	if plan == nil {
		return errors.New("patch plan required")
	}
	path, err := artifacts.SessionPatchPlanFile(sessionID)
	if err != nil {
		return fmt.Errorf("resolve patch plan path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ensure patch plan directory: %w", err)
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal patch plan: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write patch plan: %w", err)
	}
	return nil
}
