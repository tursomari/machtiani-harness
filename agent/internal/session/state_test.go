package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

func TestSaveLoadSessionStateRoundTrip(t *testing.T) {
	sessionID := fmt.Sprintf("test-session-%d", time.Now().UnixNano())
	state := SessionState{
		SessionID:      sessionID,
		Goal:           "Review database migrations",
		TurnsCompleted: 3,
		TranscriptPath: "/tmp/mct/transcript.md",
		Transcript:     "# existing transcript\n\ncontent here\n",
		PlannerProgress: &PlannerProgressState{
			SuccessFiles:   []string{"README.md", "db/migrations/20240101.sql"},
			AppliedPatches: 2,
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
	if loaded.TurnsCompleted != state.TurnsCompleted {
		t.Fatalf("unexpected turns completed: got %d want %d", loaded.TurnsCompleted, state.TurnsCompleted)
	}
	if loaded.TranscriptPath != state.TranscriptPath {
		t.Fatalf("unexpected transcript path: got %q want %q", loaded.TranscriptPath, state.TranscriptPath)
	}
	if loaded.Transcript != state.Transcript {
		t.Fatalf("unexpected transcript content: got %q want %q", loaded.Transcript, state.Transcript)
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
