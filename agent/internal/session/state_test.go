package session

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
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
		PlannerProgress: &conversation.PlannerProgressState{
			SuccessFiles:   []string{"README.md", "db/migrations/20240101.sql"},
			},
		SuspendedUserInput: &conversation.SuspendedUserInputState{
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

	var diagBuf bytes.Buffer
	runState.persistSessionState(nil, &diagBuf)

	// Verify session-state.json is NOT created.
	statePath := filepath.Join(dir, sessionStateFile)
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("session-state.json unexpectedly exists: %v", err)
	}

	// Verify deprecation log message is written.
	output := diagBuf.String()
	if !strings.Contains(output, "session-state.json persistence disabled; using conversation.json") {
		t.Fatalf("expected deprecation message in diagWriter, got: %s", output)
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

	// Capture diagWriter output to verify the resume hint is printed.
	var diagBuf bytes.Buffer
	runState.persistSessionState(nil, &diagBuf)

	stdout := diagBuf.String()

	// Verify session-state.json is NOT created.
	statePath := filepath.Join(dir, sessionStateFile)
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("session-state.json unexpectedly exists: %v", err)
	}

	// Verify deprecation log message and interruption banner were printed.
	if !strings.Contains(stdout, "session-state.json persistence disabled; using conversation.json") {
		t.Fatalf("expected deprecation message in diagWriter, got: %s", stdout)
	}
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

	var diagBuf bytes.Buffer
	runState.persistSessionState(nil, &diagBuf)

	// Verify session-state.json is NOT created for the override directory.
	statePath := filepath.Join(overrideDir, sessionStateFile)
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("session-state.json unexpectedly exists for override: %v", err)
	}

	// Verify deprecation log message is written.
	output := diagBuf.String()
	if !strings.Contains(output, "session-state.json persistence disabled; using conversation.json") {
		t.Fatalf("expected deprecation message in diagWriter, got: %s", output)
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
	runState.persistSessionState(nil, os.Stderr)

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

	// Inject a bytes.Buffer as the diagWriter to capture the warning directly.
	var diagBuf bytes.Buffer
	runState.persistSessionState(nil, &diagBuf)

	output := diagBuf.String()
	if !strings.Contains(output, "session-state.json persistence disabled; using conversation.json") {
		t.Fatalf("expected deprecation message in diagWriter, got: %s", output)
	}
}

// TestLoadOrMigrateSessionStateFromConversationFields verifies that when
// a Conversation pointer with all top-level fields populated is passed to
// loadOrMigrateSessionState, the returned SessionState matches those fields
// exactly without requiring a session-state.json file on disk.
func TestLoadOrMigrateSessionStateFromConversationFields(t *testing.T) {
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
		Modes:             []string{"coding", "database"},
		ModeInstructionDir: "/modes/database",
		PlannerOverlay:    "Prefer index-only scans",
		TaskDescription:   "Analyze and optimize slow queries",
		Status:            "suspended_user_input",
		UpdatedAt:         now,
	}

	state, err := loadOrMigrateSessionState(conv, sessionID)
	if err != nil {
		t.Fatalf("loadOrMigrateSessionState returned error: %v", err)
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

// TestLoadOrMigrateSessionStateLegacyFallback verifies that when a
// Conversation with empty Goal is passed, loadOrMigrateSessionState falls
// back to loading from the session-state.json file on disk.
func TestLoadOrMigrateSessionStateLegacyFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	sessionID := fmt.Sprintf("test-legacy-fallback-%d", time.Now().UnixNano())

	// Save a SessionState to disk (legacy path).
	state := SessionState{
		SessionID:                sessionID,
		Goal:                     "Legacy goal from file",
		OriginalGoal:             "Legacy original goal",
		OriginalPrompt:           "Legacy original prompt",
		TaskDescription:          "Legacy task",
		PlannerOverlay:           "Legacy overlay",
		Status:                   "suspended_user_input",
		TurnsCompleted:           4,
		ShellAgentResumable:      true,
		ShellAgentTrajectoryPath: "/tmp/legacy-trajectory.json",
		ShellAgentInterruptStep:  2,
		Modes:                    []string{"legacy-mode"},
		ModeInstructionDir:       "/legacy/modes",
		PlannerProgress: &conversation.PlannerProgressState{
			SuccessFiles: []string{"legacy.go"},
		},
		SuspendedUserInput: &conversation.SuspendedUserInputState{
			Kind:        "legacy-ask",
			Question:    "Legacy question?",
			Context:     "Legacy context.",
			Reason:      "Legacy reason.",
			OriginalAsk: "Legacy original ask?",
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

	// Create a Conversation with empty Goal – this should trigger the
	// legacy fallback path.
	conv := &conversation.Conversation{
		SessionID: sessionID,
		Goal:      "",
	}

	loaded, err := loadOrMigrateSessionState(conv, sessionID)
	if err != nil {
		t.Fatalf("loadOrMigrateSessionState returned error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil SessionState")
	}

	// Verify all fields match the legacy saved state.
	if loaded.SessionID != sessionID {
		t.Fatalf("SessionID mismatch: got %q want %q", loaded.SessionID, sessionID)
	}
	if loaded.Goal != state.Goal {
		t.Fatalf("Goal mismatch: got %q want %q", loaded.Goal, state.Goal)
	}
	if loaded.OriginalGoal != state.OriginalGoal {
		t.Fatalf("OriginalGoal mismatch: got %q want %q", loaded.OriginalGoal, state.OriginalGoal)
	}
	if loaded.OriginalPrompt != state.OriginalPrompt {
		t.Fatalf("OriginalPrompt mismatch: got %q want %q", loaded.OriginalPrompt, state.OriginalPrompt)
	}
	if loaded.TaskDescription != state.TaskDescription {
		t.Fatalf("TaskDescription mismatch: got %q want %q", loaded.TaskDescription, state.TaskDescription)
	}
	if loaded.PlannerOverlay != state.PlannerOverlay {
		t.Fatalf("PlannerOverlay mismatch: got %q want %q", loaded.PlannerOverlay, state.PlannerOverlay)
	}
	if loaded.Status != state.Status {
		t.Fatalf("Status mismatch: got %q want %q", loaded.Status, state.Status)
	}
	if loaded.TurnsCompleted != state.TurnsCompleted {
		t.Fatalf("TurnsCompleted mismatch: got %d want %d", loaded.TurnsCompleted, state.TurnsCompleted)
	}
	if loaded.ShellAgentResumable != state.ShellAgentResumable {
		t.Fatalf("ShellAgentResumable mismatch: got %v want %v", loaded.ShellAgentResumable, state.ShellAgentResumable)
	}
	if loaded.ShellAgentTrajectoryPath != state.ShellAgentTrajectoryPath {
		t.Fatalf("ShellAgentTrajectoryPath mismatch: got %q want %q", loaded.ShellAgentTrajectoryPath, state.ShellAgentTrajectoryPath)
	}
	if loaded.ShellAgentInterruptStep != state.ShellAgentInterruptStep {
		t.Fatalf("ShellAgentInterruptStep mismatch: got %d want %d", loaded.ShellAgentInterruptStep, state.ShellAgentInterruptStep)
	}
	if len(loaded.Modes) != len(state.Modes) {
		t.Fatalf("Modes length mismatch: got %d want %d", len(loaded.Modes), len(state.Modes))
	}
	for i, m := range state.Modes {
		if loaded.Modes[i] != m {
			t.Fatalf("Modes[%d] mismatch: got %q want %q", i, loaded.Modes[i], m)
		}
	}
	if loaded.ModeInstructionDir != state.ModeInstructionDir {
		t.Fatalf("ModeInstructionDir mismatch: got %q want %q", loaded.ModeInstructionDir, state.ModeInstructionDir)
	}
	if loaded.PlannerProgress == nil {
		t.Fatal("expected PlannerProgress to be set")
	}
	if len(loaded.PlannerProgress.SuccessFiles) != len(state.PlannerProgress.SuccessFiles) {
		t.Fatalf("PlannerProgress.SuccessFiles length mismatch: got %d want %d",
			len(loaded.PlannerProgress.SuccessFiles), len(state.PlannerProgress.SuccessFiles))
	}
	for i, f := range state.PlannerProgress.SuccessFiles {
		if loaded.PlannerProgress.SuccessFiles[i] != f {
			t.Fatalf("PlannerProgress.SuccessFiles[%d] mismatch: got %q want %q",
				i, loaded.PlannerProgress.SuccessFiles[i], f)
		}
	}
	if loaded.SuspendedUserInput == nil {
		t.Fatal("expected SuspendedUserInput to be set")
	}
	if loaded.SuspendedUserInput.Kind != state.SuspendedUserInput.Kind {
		t.Fatalf("SuspendedUserInput.Kind mismatch: got %q want %q", loaded.SuspendedUserInput.Kind, state.SuspendedUserInput.Kind)
	}
	if loaded.SuspendedUserInput.Question != state.SuspendedUserInput.Question {
		t.Fatalf("SuspendedUserInput.Question mismatch: got %q want %q", loaded.SuspendedUserInput.Question, state.SuspendedUserInput.Question)
	}
	if loaded.SuspendedUserInput.Context != state.SuspendedUserInput.Context {
		t.Fatalf("SuspendedUserInput.Context mismatch: got %q want %q", loaded.SuspendedUserInput.Context, state.SuspendedUserInput.Context)
	}
	if loaded.SuspendedUserInput.Reason != state.SuspendedUserInput.Reason {
		t.Fatalf("SuspendedUserInput.Reason mismatch: got %q want %q", loaded.SuspendedUserInput.Reason, state.SuspendedUserInput.Reason)
	}
	if loaded.SuspendedUserInput.OriginalAsk != state.SuspendedUserInput.OriginalAsk {
		t.Fatalf("SuspendedUserInput.OriginalAsk mismatch: got %q want %q", loaded.SuspendedUserInput.OriginalAsk, state.SuspendedUserInput.OriginalAsk)
	}
	// UpdatedAt should be set by SaveSessionState (non-zero).
	if loaded.UpdatedAt.IsZero() {
		t.Fatalf("expected UpdatedAt to be set")
	}
}
