package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func TestListSessionsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
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

		goal := fmt.Sprintf("Test goal %d", i)
		conv := conversation.New(sessionID, goal)
		conv.Goal = goal
		conv.Status = "completed"
		conv.TurnsCompleted = i + 1
		convData, err := conv.Marshal()
		if err != nil {
			t.Fatalf("marshal conv %d: %v", i, err)
		}
		convPath, err := artifacts.SessionConversationFile(sessionID)
		if err != nil {
			t.Fatalf("SessionConversationFile %d: %v", i, err)
		}
		if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
			t.Fatalf("mkdir conv dir %d: %v", i, err)
		}
		if err := os.WriteFile(convPath, convData, 0o644); err != nil {
			t.Fatalf("write conv %d: %v", i, err)
		}
		// Small delay to ensure different UpdatedAt timestamps
		time.Sleep(10 * time.Millisecond)
	}

	// Cleanup
	t.Cleanup(func() {
		for _, id := range sessionIDs {
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
	conv := conversation.New(goodSessionID, "Good session")
	conv.Goal = "Good session"
	conv.Status = "completed"
	conv.TurnsCompleted = 1
	convData, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal good conv: %v", err)
	}
	convPath, err := artifacts.SessionConversationFile(goodSessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile good: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("mkdir good conv dir: %v", err)
	}
	if err := os.WriteFile(convPath, convData, 0o644); err != nil {
		t.Fatalf("write good conv: %v", err)
	}

	// Create one corrupt session (invalid JSON)
	corruptSessionID := fmt.Sprintf("test-corrupt-session-%d", time.Now().UnixNano())
	corruptConvPath, err := artifacts.SessionConversationFile(corruptSessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile corrupt: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(corruptConvPath), 0o755); err != nil {
		t.Fatalf("mkdir corrupt conv dir: %v", err)
	}
	if err := os.WriteFile(corruptConvPath, []byte("invalid json{"), 0o644); err != nil {
		t.Fatalf("write corrupt conv: %v", err)
	}

	// Cleanup
	t.Cleanup(func() {
		goodDir, _ := artifacts.SessionDirectory(goodSessionID)
		_ = os.RemoveAll(goodDir)
		corruptDir, _ := artifacts.SessionDirectory(corruptSessionID)
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

	runState.persistSessionState(nil)

	// Verify session-state.json is NOT created.
	statePath := filepath.Join(dir, "session-state.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("session-state.json unexpectedly exists: %v", err)
	}

}

func TestRuntimeStatsSeedAndHydrateConversation(t *testing.T) {
	loaded := &SessionState{
		SessionID: "runtime-seed",
		Goal:      "Goal",
		RuntimeStats: conversation.NewRuntimeStatsState(
			65_000,
			1_234,
			56_789,
			1_000,
		),
	}
	runState := newRunLifecycleState(context.Background(), legacyConfig{}, loaded.SessionID, loaded.Goal, loaded.Goal, "", "", "", 0, "", loaded)
	runState.recorder = &conversationRecorder{
		conversation: conversation.New(loaded.SessionID, loaded.Goal),
	}
	runState.updateRuntimeTokenUsage(ui.TokenUsageUpdatedEvent{
		InputHit:  2_000,
		InputMiss: 60_000,
		Output:    1_500,
	})

	state := runState.baseSessionState()
	runState.hydrateState(&state, io.Discard)

	if state.RuntimeStats == nil {
		t.Fatal("expected runtime stats")
	}
	if state.RuntimeStats.ActiveElapsedMS < 65_000 {
		t.Fatalf("elapsed = %d, want at least seed", state.RuntimeStats.ActiveElapsedMS)
	}
	if state.RuntimeStats.InputHitTokens != 2_000 || state.RuntimeStats.InputMissTokens != 60_000 || state.RuntimeStats.OutputTokens != 1_500 {
		t.Fatalf("runtime token stats = %+v", state.RuntimeStats)
	}
	if got := state.RuntimeStats.InputMissTokensDisplay; got != "60,000" {
		t.Fatalf("InputMissTokensDisplay = %q, want 60,000", got)
	}
	if runState.recorder.conversation.RuntimeStats == nil {
		t.Fatal("expected runtime stats to hydrate conversation")
	}
	if got := runState.recorder.conversation.RuntimeStats.OutputTokensDisplay; got != "1,500" {
		t.Fatalf("conversation OutputTokensDisplay = %q, want 1,500", got)
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

	runState.persistSessionState(nil)

	// Verify session-state.json is NOT created.
	statePath := filepath.Join(dir, "session-state.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("session-state.json unexpectedly exists: %v", err)
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

	runState.persistSessionState(nil)

	// Verify session-state.json is NOT created for the override directory.
	statePath := filepath.Join(overrideDir, "session-state.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("session-state.json unexpectedly exists for override: %v", err)
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
	runState.persistSessionState(os.Stderr)

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
	// directory path to block MkdirAll inside session directory creation.
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
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	runState.persistSessionState(nil)
}

// TestSessionStateFromConversationFields verifies that when
// a Conversation pointer with all top-level fields populated is passed to
// sessionStateFromConversation, the returned SessionState matches those fields
// exactly without requiring a session-state.json file on disk.
func TestSessionStateFromConversationFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sessionID := fmt.Sprintf("test-migrate-from-conv-%d", time.Now().UnixNano())

	now := time.Now().UTC()
	conv := &conversation.Conversation{
		SessionID:                sessionID,
		Goal:                     "Investigate database performance",
		OriginalGoal:             "Review slow queries",
		OriginalPrompt:           "Please investigate database performance issues",
		ShellAgentResumable:      true,
		ShellAgentTrajectoryPath: "/tmp/test-trajectory.json",
		ShellAgentInterruptStep:  3,
		TurnsCompleted:           5,
		SuspendedUserInput: &conversation.SuspendedUserInputState{
			Kind:        "user-directed-ask",
			Question:    "Which index strategy do you prefer?",
			Context:     "We have two options: B-tree or hash index.",
			Reason:      "User needs to choose indexing strategy",
			OriginalAsk: "Do you want B-tree or hash index?",
		},
		PlannerProgress: &conversation.PlannerProgressState{
			SuccessFiles: []string{"db/migrations/001.sql", "README.md"},
		},
		Modes:              []string{"coding", "database"},
		ModeInstructionDir: "/modes/database",
		PlannerOverlay:     "Prefer index-only scans",
		TaskDescription:    "Analyze and optimize slow queries",
		Status:             "suspended_user_input",
		UpdatedAt:          now,
	}

	state, err := sessionStateFromConversation(conv, sessionID)
	if err != nil {
		t.Fatalf("sessionStateFromConversation returned error: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil SessionState")
	}

	// Verify all fields match.
	if state.SessionID != conv.SessionID {
		t.Fatalf("SessionID mismatch: got %q want %q", state.SessionID, conv.SessionID)
	}
	if state.Goal != conv.Goal {
		t.Fatalf("Goal mismatch: got %q want %q", state.Goal, conv.Goal)
	}
	if state.OriginalGoal != conv.OriginalGoal {
		t.Fatalf("OriginalGoal mismatch: got %q want %q", state.OriginalGoal, conv.OriginalGoal)
	}
	if state.OriginalPrompt != conv.OriginalPrompt {
		t.Fatalf("OriginalPrompt mismatch: got %q want %q", state.OriginalPrompt, conv.OriginalPrompt)
	}
	if state.ShellAgentResumable != conv.ShellAgentResumable {
		t.Fatalf("ShellAgentResumable mismatch: got %v want %v", state.ShellAgentResumable, conv.ShellAgentResumable)
	}
	if state.ShellAgentTrajectoryPath != conv.ShellAgentTrajectoryPath {
		t.Fatalf("ShellAgentTrajectoryPath mismatch: got %q want %q", state.ShellAgentTrajectoryPath, conv.ShellAgentTrajectoryPath)
	}
	if state.ShellAgentInterruptStep != conv.ShellAgentInterruptStep {
		t.Fatalf("ShellAgentInterruptStep mismatch: got %d want %d", state.ShellAgentInterruptStep, conv.ShellAgentInterruptStep)
	}
	if state.TurnsCompleted != conv.TurnsCompleted {
		t.Fatalf("TurnsCompleted mismatch: got %d want %d", state.TurnsCompleted, conv.TurnsCompleted)
	}
	if state.SuspendedUserInput == nil {
		t.Fatal("expected SuspendedUserInput to be set")
	}
	if state.SuspendedUserInput.Kind != conv.SuspendedUserInput.Kind {
		t.Fatalf("SuspendedUserInput.Kind mismatch: got %q want %q", state.SuspendedUserInput.Kind, conv.SuspendedUserInput.Kind)
	}
	if state.SuspendedUserInput.Question != conv.SuspendedUserInput.Question {
		t.Fatalf("SuspendedUserInput.Question mismatch: got %q want %q", state.SuspendedUserInput.Question, conv.SuspendedUserInput.Question)
	}
	if state.SuspendedUserInput.Context != conv.SuspendedUserInput.Context {
		t.Fatalf("SuspendedUserInput.Context mismatch: got %q want %q", state.SuspendedUserInput.Context, conv.SuspendedUserInput.Context)
	}
	if state.SuspendedUserInput.Reason != conv.SuspendedUserInput.Reason {
		t.Fatalf("SuspendedUserInput.Reason mismatch: got %q want %q", state.SuspendedUserInput.Reason, conv.SuspendedUserInput.Reason)
	}
	if state.SuspendedUserInput.OriginalAsk != conv.SuspendedUserInput.OriginalAsk {
		t.Fatalf("SuspendedUserInput.OriginalAsk mismatch: got %q want %q", state.SuspendedUserInput.OriginalAsk, conv.SuspendedUserInput.OriginalAsk)
	}
	if state.PlannerProgress == nil {
		t.Fatal("expected PlannerProgress to be set")
	}
	if len(state.PlannerProgress.SuccessFiles) != len(conv.PlannerProgress.SuccessFiles) {
		t.Fatalf("PlannerProgress.SuccessFiles length mismatch: got %d want %d",
			len(state.PlannerProgress.SuccessFiles), len(conv.PlannerProgress.SuccessFiles))
	}
	for i, f := range conv.PlannerProgress.SuccessFiles {
		if state.PlannerProgress.SuccessFiles[i] != f {
			t.Fatalf("PlannerProgress.SuccessFiles[%d] mismatch: got %q want %q",
				i, state.PlannerProgress.SuccessFiles[i], f)
		}
	}
	if len(state.Modes) != len(conv.Modes) {
		t.Fatalf("Modes length mismatch: got %d want %d", len(state.Modes), len(conv.Modes))
	}
	for i, m := range conv.Modes {
		if state.Modes[i] != m {
			t.Fatalf("Modes[%d] mismatch: got %q want %q", i, state.Modes[i], m)
		}
	}
	if state.ModeInstructionDir != conv.ModeInstructionDir {
		t.Fatalf("ModeInstructionDir mismatch: got %q want %q", state.ModeInstructionDir, conv.ModeInstructionDir)
	}
	if state.PlannerOverlay != conv.PlannerOverlay {
		t.Fatalf("PlannerOverlay mismatch: got %q want %q", state.PlannerOverlay, conv.PlannerOverlay)
	}
	if state.TaskDescription != conv.TaskDescription {
		t.Fatalf("TaskDescription mismatch: got %q want %q", state.TaskDescription, conv.TaskDescription)
	}
	if state.Status != conv.Status {
		t.Fatalf("Status mismatch: got %q want %q", state.Status, conv.Status)
	}
	if !state.UpdatedAt.Equal(conv.UpdatedAt) {
		t.Fatalf("UpdatedAt mismatch: got %v want %v", state.UpdatedAt, conv.UpdatedAt)
	}
}
