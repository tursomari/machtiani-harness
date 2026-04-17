package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/planner"
)

func TestSaveLoadSessionStateRoundTrip(t *testing.T) {
	sessionID := fmt.Sprintf("test-session-%d", time.Now().UnixNano())
	state := SessionState{
		SessionID:        sessionID,
		Goal:             "Review database migrations",
		OriginalPrompt:   "Review database migrations in detail",
		TaskDescription:  "Validate migration ordering",
		PlannerOverlay:   "Prefer migration safety over speed",
		Status:           "suspended_user_input",
		TurnsCompleted:   3,
		TranscriptPath:   "/tmp/mct/transcript.md",
		Transcript:       "# existing transcript\n\ncontent here\n",
		ConversationPath: "/tmp/mct/conversation.json",
		ConversationJSON: "{\n  \"messages\": []\n}",
		PlannerProgress: &PlannerProgressState{
			SuccessFiles:   []string{"README.md", "db/migrations/20240101.sql"},
			AppliedPatches: 2,
			PendingReview: &planner.PendingReview{
				PatchPath:        "patch.diff",
				ReversePatchPath: "patch.diff.reverse",
				Description:      "Update migration",
				Files:            []string{"db/migrations/20240101.sql"},
				Sequence:         3,
			},
		},
		PendingPatchTurn: &PendingPatchTurnState{
			Step:        4,
			Description: "Update migration",
			Answer:      "diff --git a/file b/file",
		},
		SuspendedUserInput: &SuspendedUserInputState{
			Kind:           "user-directed-ask",
			Question:       "Do you want the safer fix, or the faster fix?",
			Context:        "The safer fix preserves behavior.",
			Reason:         "asks for the preferred tradeoff",
			OriginalAsk:    "Do you want the safer fix or the faster fix? I can inspect more logs too.",
			ChildSessionID: "child-123",
		},
	}

	if err := SaveSessionState(state); err != nil {
		t.Fatalf("SaveSessionState returned error: %v", err)
	}

	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("failed to resolve session directory: %v", err)
	}
	t.Cleanup(func() {
		_ = RemoveSessionState(sessionID)
		_ = os.RemoveAll(dir)
	})

	loaded, err := LoadSessionState(sessionID)
	if err != nil {
		t.Fatalf("LoadSessionState returned error: %v", err)
	}

	if loaded.SessionID != sessionID {
		t.Fatalf("unexpected session ID: got %s want %s", loaded.SessionID, sessionID)
	}
	if loaded.Goal != state.Goal {
		t.Fatalf("unexpected goal: got %q want %q", loaded.Goal, state.Goal)
	}
	if loaded.OriginalPrompt != state.OriginalPrompt {
		t.Fatalf("unexpected original prompt: got %q want %q", loaded.OriginalPrompt, state.OriginalPrompt)
	}
	if loaded.TaskDescription != state.TaskDescription {
		t.Fatalf("unexpected task description: got %q want %q", loaded.TaskDescription, state.TaskDescription)
	}
	if loaded.PlannerOverlay != state.PlannerOverlay {
		t.Fatalf("unexpected planner overlay: got %q want %q", loaded.PlannerOverlay, state.PlannerOverlay)
	}
	if loaded.Status != state.Status {
		t.Fatalf("unexpected status: got %q want %q", loaded.Status, state.Status)
	}
	if loaded.TurnsCompleted != state.TurnsCompleted {
		t.Fatalf("unexpected turns completed: got %d want %d", loaded.TurnsCompleted, state.TurnsCompleted)
	}
	if loaded.TranscriptPath != state.TranscriptPath {
		t.Fatalf("unexpected transcript path: got %q want %q", loaded.TranscriptPath, state.TranscriptPath)
	}
	if loaded.Transcript != state.Transcript {
		t.Fatalf("unexpected transcript content: got %q want %q", loaded.Transcript, state.Transcript)
	}
	if loaded.ConversationPath != state.ConversationPath {
		t.Fatalf("unexpected conversation path: got %q want %q", loaded.ConversationPath, state.ConversationPath)
	}
	if loaded.ConversationJSON != state.ConversationJSON {
		t.Fatalf("unexpected conversation json: got %q want %q", loaded.ConversationJSON, state.ConversationJSON)
	}
	if loaded.UpdatedAt.IsZero() {
		t.Fatalf("expected UpdatedAt to be set")
	}
	if loaded.PlannerProgress == nil {
		t.Fatalf("expected planner progress to be set")
	}
	if got, want := loaded.PlannerProgress.SuccessFiles, state.PlannerProgress.SuccessFiles; len(got) != len(want) {
		t.Fatalf("unexpected success files length: got %d want %d", len(got), len(want))
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("unexpected success file at %d: got %q want %q", i, got[i], want[i])
			}
		}
	}
	if loaded.PlannerProgress.AppliedPatches != state.PlannerProgress.AppliedPatches {
		t.Fatalf("unexpected applied patches: got %d want %d", loaded.PlannerProgress.AppliedPatches, state.PlannerProgress.AppliedPatches)
	}
	if loaded.PlannerProgress.PendingReview == nil {
		t.Fatalf("expected pending review to round-trip")
	}
	if loaded.PlannerProgress.PendingReview.Description != state.PlannerProgress.PendingReview.Description {
		t.Fatalf("unexpected pending review description: got %q want %q", loaded.PlannerProgress.PendingReview.Description, state.PlannerProgress.PendingReview.Description)
	}
	if len(loaded.PlannerProgress.PendingReview.Files) != len(state.PlannerProgress.PendingReview.Files) {
		t.Fatalf("unexpected pending review files length")
	}
	if state.PendingPatchTurn == nil {
		t.Fatalf("test setup missing pending patch turn")
	}
	if loaded.PendingPatchTurn == nil {
		t.Fatalf("expected pending patch turn to round-trip")
	}
	if loaded.PendingPatchTurn.Step != state.PendingPatchTurn.Step {
		t.Fatalf("unexpected pending patch step: got %d want %d", loaded.PendingPatchTurn.Step, state.PendingPatchTurn.Step)
	}
	if loaded.PendingPatchTurn.Description != state.PendingPatchTurn.Description {
		t.Fatalf("unexpected pending patch description: got %q want %q", loaded.PendingPatchTurn.Description, state.PendingPatchTurn.Description)
	}
	if loaded.PendingPatchTurn.Answer != state.PendingPatchTurn.Answer {
		t.Fatalf("unexpected pending patch answer: got %q want %q", loaded.PendingPatchTurn.Answer, state.PendingPatchTurn.Answer)
	}
	if loaded.SuspendedUserInput == nil {
		t.Fatalf("expected suspended user input to round-trip")
	}
	if loaded.SuspendedUserInput.Question != state.SuspendedUserInput.Question {
		t.Fatalf("unexpected suspended question: got %q want %q", loaded.SuspendedUserInput.Question, state.SuspendedUserInput.Question)
	}
	if loaded.SuspendedUserInput.ChildSessionID != state.SuspendedUserInput.ChildSessionID {
		t.Fatalf("unexpected child session id: got %q want %q", loaded.SuspendedUserInput.ChildSessionID, state.SuspendedUserInput.ChildSessionID)
	}

	path := filepath.Join(dir, sessionStateFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected session state file to exist: %v", err)
	}
}

func TestLoadSessionStateNotFound(t *testing.T) {
	sessionID := fmt.Sprintf("missing-session-%d", time.Now().UnixNano())
	_, err := LoadSessionState(sessionID)
	if err == nil {
		t.Fatalf("expected error for missing session state")
	}
	if err != ErrSessionStateNotFound {
		t.Fatalf("expected ErrSessionStateNotFound, got %v", err)
	}
}
