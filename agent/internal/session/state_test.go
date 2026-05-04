package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/planner"
)

func TestSaveLoadSessionStateRoundTrip(t *testing.T) {
	sessionID := fmt.Sprintf("test-session-%d", time.Now().UnixNano())
	state := SessionState{
		SessionID:       sessionID,
		Goal:            "Review database migrations",
		OriginalPrompt:  "Review database migrations in detail",
		TaskDescription: "Validate migration ordering",
		PlannerOverlay:  "Prefer migration safety over speed",
		Status:          "suspended_user_input",
		TurnsCompleted:  3,
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
			Kind:        "user-directed-ask",
			Question:    "Do you want the safer fix, or the faster fix?",
			Context:     "The safer fix preserves behavior.",
			Reason:      "asks for the preferred tradeoff",
			OriginalAsk: "Do you want the safer fix or the faster fix? I can inspect more logs too.",
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
	if loaded.SuspendedUserInput.OriginalAsk != state.SuspendedUserInput.OriginalAsk {
		t.Fatalf("unexpected original ask: got %q want %q", loaded.SuspendedUserInput.OriginalAsk, state.SuspendedUserInput.OriginalAsk)
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

func TestLoadSessionStateMigratesLegacyConversationJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	sessionID := fmt.Sprintf("legacy-session-%d", time.Now().UnixNano())
	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("SessionDirectory: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	legacyPayload := `{"session_id":"` + sessionID + `","goal":"legacy","turns_completed":1,"conversation_json":"{\"session_id\":\"` + sessionID + `\",\"messages\":[],\"created_at\":\"2026-04-18T00:00:00Z\",\"updated_at\":\"2026-04-18T00:00:00Z\"}","updated_at":"2026-04-18T00:00:00Z"}`
	statePath := filepath.Join(dir, sessionStateFile)
	if err := os.WriteFile(statePath, []byte(legacyPayload), 0o644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if _, err := os.Stat(convPath); !os.IsNotExist(err) {
		t.Fatalf("expected conversation file to be absent before migration, got err=%v", err)
	}

	loaded, err := LoadSessionState(sessionID)
	if err != nil {
		t.Fatalf("LoadSessionState: %v", err)
	}
	if loaded.Goal != "legacy" {
		t.Fatalf("unexpected goal: %q", loaded.Goal)
	}

	migrated, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("expected migrated conversation file to exist: %v", err)
	}
	if !strings.Contains(string(migrated), `"session_id":"`+sessionID+`"`) {
		t.Fatalf("migrated conversation.json missing session id: %s", migrated)
	}
}

func TestLoadSessionStateKeepsExistingConversationJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	sessionID := fmt.Sprintf("legacy-keep-%d", time.Now().UnixNano())
	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("SessionDirectory: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("mkdir conv dir: %v", err)
	}
	authoritative := `{"session_id":"` + sessionID + `","original_goal":"keep","messages":[],"created_at":"2026-04-18T00:00:00Z","updated_at":"2026-04-18T00:00:00Z"}`
	if err := os.WriteFile(convPath, []byte(authoritative), 0o644); err != nil {
		t.Fatalf("write authoritative conv: %v", err)
	}

	legacyInline := `{"session_id":"` + sessionID + `","goal":"keep","turns_completed":1,"conversation_json":"STALE","updated_at":"2026-04-18T00:00:00Z"}`
	statePath := filepath.Join(dir, sessionStateFile)
	if err := os.WriteFile(statePath, []byte(legacyInline), 0o644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	if _, err := LoadSessionState(sessionID); err != nil {
		t.Fatalf("LoadSessionState: %v", err)
	}

	got, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("read conv: %v", err)
	}
	if string(got) != authoritative {
		t.Fatalf("expected on-disk conversation to be preserved, got %q", got)
	}
}

func TestListSessionsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	// Change to temp dir to avoid git repo context
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected empty list, got %d sessions", len(sessions))
	}
}

func TestListSessionsMultiple(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	// Change to temp dir to avoid git repo context
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	// Create 3 sessions with different timestamps
	sessionIDs := []string{}
	for i := 0; i < 3; i++ {
		sessionID := fmt.Sprintf("test-list-session-%d-%d", i, time.Now().UnixNano())
		sessionIDs = append(sessionIDs, sessionID)

		state := SessionState{
			SessionID:      sessionID,
			Goal:           fmt.Sprintf("Test goal %d", i),
			Status:         "completed",
			TurnsCompleted: i + 1,
		}
		if err := SaveSessionState(state); err != nil {
			t.Fatalf("SaveSessionState %d: %v", i, err)
		}
		// Small delay to ensure different UpdatedAt timestamps
		time.Sleep(10 * time.Millisecond)
	}

	// Cleanup
	t.Cleanup(func() {
		for _, id := range sessionIDs {
			_ = RemoveSessionState(id)
			dir, _ := artifacts.SessionDirectory(id)
			_ = os.RemoveAll(dir)
		}
	})

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}

	// Verify sessions are sorted by UpdatedAt descending (most recent first)
	for i := 0; i < len(sessions)-1; i++ {
		if sessions[i].UpdatedAt.Before(sessions[i+1].UpdatedAt) {
			t.Fatalf("sessions not sorted by UpdatedAt descending: session[%d].UpdatedAt (%s) < session[%d].UpdatedAt (%s)",
				i, sessions[i].UpdatedAt, i+1, sessions[i+1].UpdatedAt)
		}
	}

	// Verify all session IDs are present
	sessionIDMap := make(map[string]bool)
	for _, s := range sessions {
		sessionIDMap[s.SessionID] = true
		if s.Goal == "" {
			t.Fatalf("session %s has empty goal", s.SessionID)
		}
	}
	for _, id := range sessionIDs {
		if !sessionIDMap[id] {
			t.Fatalf("session ID %s not found in results", id)
		}
	}
}

func TestListSessionsSkipsCorrupt(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	// Change to temp dir to avoid git repo context
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	// Create one good session
	goodSessionID := fmt.Sprintf("test-good-session-%d", time.Now().UnixNano())
	goodState := SessionState{
		SessionID:      goodSessionID,
		Goal:           "Good session",
		Status:         "completed",
		TurnsCompleted: 1,
	}
	if err := SaveSessionState(goodState); err != nil {
		t.Fatalf("SaveSessionState good: %v", err)
	}

	// Create one corrupt session (invalid JSON)
	corruptSessionID := fmt.Sprintf("test-corrupt-session-%d", time.Now().UnixNano())
	corruptDir, err := artifacts.SessionDirectory(corruptSessionID)
	if err != nil {
		t.Fatalf("SessionDirectory corrupt: %v", err)
	}
	if err := os.MkdirAll(corruptDir, 0o755); err != nil {
		t.Fatalf("mkdir corrupt dir: %v", err)
	}
	corruptPath := filepath.Join(corruptDir, sessionStateFile)
	if err := os.WriteFile(corruptPath, []byte("invalid json{"), 0o644); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}

	// Cleanup
	t.Cleanup(func() {
		_ = RemoveSessionState(goodSessionID)
		goodDir, _ := artifacts.SessionDirectory(goodSessionID)
		_ = os.RemoveAll(goodDir)
		_ = os.RemoveAll(corruptDir)
	})

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 good session, got %d", len(sessions))
	}
	if sessions[0].SessionID != goodSessionID {
		t.Fatalf("expected good session ID %s, got %s", goodSessionID, sessions[0].SessionID)
	}
}

func TestListSessionsSkipsInvalidDirs(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	// Change to temp dir to avoid git repo context
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	// Create a directory without session-state.json
	invalidSessionID := fmt.Sprintf("test-invalid-session-%d", time.Now().UnixNano())
	invalidDir, err := artifacts.SessionDirectory(invalidSessionID)
	if err != nil {
		t.Fatalf("SessionDirectory invalid: %v", err)
	}
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("mkdir invalid dir: %v", err)
	}

	// Cleanup
	t.Cleanup(func() {
		_ = os.RemoveAll(invalidDir)
	})

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestPersistSessionStateNormalExit(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-persist-normal-%d", time.Now().UnixNano())
	runState := &runLifecycleState{
		sessionID:      sessionID,
		goal:           "Test goal",
		originalPrompt: "Test original prompt",
		sessionStatus:  "error",
		turnsCompleted: 2,
		interrupted:    false,
	}

	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("SessionDirectory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	runState.persistSessionState()

	loaded, err := LoadSessionState(sessionID)
	if err != nil {
		t.Fatalf("LoadSessionState returned error: %v", err)
	}
	if loaded.SessionID != sessionID {
		t.Fatalf("unexpected session ID: got %s want %s", loaded.SessionID, sessionID)
	}
	if loaded.Goal != "Test goal" {
		t.Fatalf("unexpected goal: got %q want %q", loaded.Goal, "Test goal")
	}
	if loaded.TurnsCompleted != 2 {
		t.Fatalf("unexpected turns completed: got %d want 2", loaded.TurnsCompleted)
	}
	if loaded.Status != "error" {
		t.Fatalf("unexpected status: got %q want %q", loaded.Status, "error")
	}
}

func TestPersistSessionStateInterrupted(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-persist-interrupted-%d", time.Now().UnixNano())
	runState := &runLifecycleState{
		sessionID:      sessionID,
		goal:           "Interrupted goal",
		originalPrompt: "Interrupted prompt",
		sessionStatus:  "interrupted",
		turnsCompleted: 3,
		interrupted:    true,
	}

	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("SessionDirectory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Capture stdout to verify the resume hint is printed.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		data, _ := io.ReadAll(r)
		done <- data
	}()

	runState.persistSessionState()

	w.Close()
	stdout := string(<-done)
	os.Stdout = origStdout

	// Verify state file exists and is loadable.
	loaded, err := LoadSessionState(sessionID)
	if err != nil {
		t.Fatalf("LoadSessionState returned error: %v", err)
	}
	if loaded.SessionID != sessionID {
		t.Fatalf("unexpected session ID: got %s want %s", loaded.SessionID, sessionID)
	}
	if loaded.TurnsCompleted != 3 {
		t.Fatalf("unexpected turns completed: got %d want 3", loaded.TurnsCompleted)
	}

	// Verify interruption banner was printed.
	if !strings.Contains(stdout, "=== SESSION INTERRUPTED ===") {
		t.Fatalf("expected interruption banner in stdout, got: %s", stdout)
	}
	if !strings.Contains(stdout, sessionID) {
		t.Fatalf("expected session ID %s in stdout, got: %s", sessionID, stdout)
	}
}

func TestPersistSessionStatePendingStateOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-persist-override-%d", time.Now().UnixNano())
	overrideID := "override-id"
	runState := &runLifecycleState{
		sessionID:      sessionID,
		goal:           "Original goal",
		originalPrompt: "Original prompt",
		sessionStatus:  "error",
		turnsCompleted: 1,
		interrupted:    false,
		pendingState: &SessionState{
			SessionID:      overrideID,
			Goal:           "Overridden goal",
			OriginalPrompt: "Overridden prompt",
			Status:         "success",
			TurnsCompleted: 5,
		},
	}

	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("SessionDirectory: %v", err)
	}
	overrideDir, err := artifacts.SessionDirectory(overrideID)
	if err != nil {
		t.Fatalf("SessionDirectory override: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		_ = os.RemoveAll(overrideDir)
	})

	runState.persistSessionState()

	// The saved state should reflect the pendingState override.
	// Since pendingState.SessionID is "override-id", the file was saved
	// under that ID, not the original sessionID.
	loaded, err := LoadSessionState(overrideID)
	if err != nil {
		t.Fatalf("LoadSessionState returned error: %v", err)
	}
	// The saved state should reflect the pendingState override.
	if loaded.SessionID != overrideID {
		t.Fatalf("expected overridden session ID: got %s want %s", loaded.SessionID, overrideID)
	}
	if loaded.Goal != "Overridden goal" {
		t.Fatalf("expected overridden goal: got %q want %q", loaded.Goal, "Overridden goal")
	}
	if loaded.TurnsCompleted != 5 {
		t.Fatalf("expected overridden turns: got %d want 5", loaded.TurnsCompleted)
	}
	if loaded.Status != "success" {
		t.Fatalf("expected overridden status: got %q want %q", loaded.Status, "success")
	}
}

func TestPersistSessionStateEmptySessionID(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	runState := &runLifecycleState{
		sessionID:      "",
		goal:           "Goal",
		originalPrompt: "Prompt",
		sessionStatus:  "error",
		turnsCompleted: 0,
		interrupted:    false,
	}

	// Should not panic and should not create any file.
	runState.persistSessionState()

	// Verify no session-state.json was created in the sessions root.
	sessionsDir, err := artifacts.SessionsRoot()
	if err != nil {
		t.Fatalf("SessionsRoot: %v", err)
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return // No sessions dir at all, which is fine.
		}
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no session directories, got %d", len(entries))
	}
}

func TestPersistSessionStateSaveFailure(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-persist-failure-%d", time.Now().UnixNano())

	// Pre-create the sessions root and then place a file at the session
	// directory path to block MkdirAll inside SaveSessionState.
	sessionsDir, err := artifacts.SessionsRoot()
	if err != nil {
		t.Fatalf("SessionsRoot: %v", err)
	}
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions root: %v", err)
	}
	dir := filepath.Join(sessionsDir, sessionID)
	// Write a file where the session directory should be, blocking MkdirAll.
	if err := os.WriteFile(dir, []byte("block"), 0o444); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	runState := &runLifecycleState{
		sessionID:      sessionID,
		goal:           "Goal",
		originalPrompt: "Prompt",
		sessionStatus:  "error",
		turnsCompleted: 1,
		interrupted:    false,
	}

	// Capture stderr to verify the warning is printed.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	done := make(chan []byte)
	go func() {
		data, _ := io.ReadAll(r)
		done <- data
	}()

	// Should not panic.
	runState.persistSessionState()

	w.Close()
	stderr := string(<-done)
	os.Stderr = origStderr

	if !strings.Contains(stderr, "Warning: failed to save session state") {
		t.Fatalf("expected save failure warning in stderr, got: %s", stderr)
	}
}
