package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/transcript"
)

// setupSessionScratch creates a temporary directory structure that mimics a
// real session: a scratch directory with session ID, an artifacts directory,
// and the necessary environment. It follows the pattern from
// prepareSessionEnvironment tests in runner_state_test.go.
func setupSessionScratch(t *testing.T) (repo string, sessionDir string, cleanup func()) {
	t.Helper()

	repo = t.TempDir()
	sessionID := "test-session-" + t.Name()
	sessionDir = filepath.Join(repo, ".machtiani", "sessions", sessionID)
	artifactsDir := filepath.Join(sessionDir, "artifacts")

	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}

	scratchDir := filepath.Join(repo, ".machtiani", "tmp", sessionID)
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}

	cwd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}

	t.Setenv("HOME", t.TempDir())
	t.Setenv("MACHTIANI_CONFIG", "")
	t.Setenv("MACHTIANI_TMP_ROOT", "")
	t.Setenv("MACHTIANI_SESSION_TEMP_ROOT", "")
	llm.ResetConfigForTesting()

	cleanup = func() {
		_ = os.Chdir(cwd)
		llm.ResetConfigForTesting()
	}
	t.Cleanup(cleanup)

	return repo, sessionDir, cleanup
}

// TestTranscriptConversationConsistencyAfterCrash verifies that after a
// simulated crash — where the conversation was saved but the transcript
// delta was not appended — a resumed recorder detects and repairs the
// desync so that the transcript content matches the conversation content.
func TestTranscriptConversationConsistencyAfterCrash(t *testing.T) {
	const sessionID = "crash-recovery-test"
	goal := formatGoalText("Keep transcripts consistent", "")

	// Create a temporary directory structure and chdir into it so that
	// transcript.New writes into a disposable location.
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Setenv("HOME", tmp)
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)

	// Phase 1 – normal operation: create recorder, write several turns.
	tr1, err := transcript.New(sessionID)
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	defer tr1.Close()

	// Place conversation.json in a known location so Save() persists to disk.
	convDir := filepath.Join(tmp, ".machtiani", "sessions", sessionID)
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatalf("mkdir conv dir: %v", err)
	}
	convPath := filepath.Join(convDir, "conversation.json")

	rec1 := newConversationRecorder(tr1, sessionID, goal, convPath, false, nil)
	if err := rec1.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := rec1.WriteTurn(1, "Question1", "/tmp/save1", []string{"file1.go"}, "Answer1", "ask"); err != nil {
		t.Fatalf("WriteTurn1: %v", err)
	}
	if err := rec1.WriteTurn(2, "Question2", "/tmp/save2", []string{"file2.go"}, "Answer2", "ask"); err != nil {
		t.Fatalf("WriteTurn2: %v", err)
	}

	// Capture the transcript content at this point — this is what
	// would be on disk before the crash.
	transcriptAfterTurns := tr1.Content()

	// Simulate a crash: the conversation receives a third turn and is
	// persisted, but the transcript delta is never written.
	rec1.conversation.AddMessage("assistant", "Question3", map[string]any{
		"type":     "work_request",
		"turn":     3,
		"decision": "ask",
	})
	rec1.conversation.AddMessage("assistant", "Answer3", map[string]any{
		"type":            "work_result",
		"turn":            3,
		"retrieved_files": []string{"file3.go"},
	})
	if err := rec1.Save(); err != nil {
		t.Fatalf("Save after crash: %v", err)
	}

	// Phase 2 – resume: create a fresh transcript seeded with the stale
	// (pre-crash) content, then create a new recorder that loads the
	// authoritative conversation from disk.
	tr2, err := transcript.New(sessionID + "-resume")
	if err != nil {
		t.Fatalf("transcript init for resume: %v", err)
	}
	defer tr2.Close()

	if err := tr2.Restore(transcriptAfterTurns); err != nil {
		t.Fatalf("restore stale transcript: %v", err)
	}

	rec2 := newConversationRecorder(tr2, sessionID, goal, convPath, true, nil)
	if err := rec2.Load(); err != nil {
		t.Fatalf("Load for resume: %v", err)
	}

	// Repair: rewrite the transcript from the authoritative conversation.
	if err := restoreTranscriptFromConversation(tr2, rec2.Rendered(), true); err != nil {
		t.Fatalf("restoreTranscriptFromConversation: %v", err)
	}

	// The transcript must now match the full conversation content.
	if tr2.Content() != rec2.Rendered() {
		t.Fatalf("transcript not repaired after crash:\nwant:\n%s\n\ngot:\n%s",
			rec2.Rendered(), tr2.Content())
	}

	content := tr2.Content()
	if !strings.Contains(content, "Question3") {
		t.Fatalf("repaired transcript missing Question3:\n%s", content)
	}
	if !strings.Contains(content, "Answer3") {
		t.Fatalf("repaired transcript missing Answer3:\n%s", content)
	}
}

// TestGoalPreservationDoubleResume verifies that OriginalPrompt is preserved
// across multiple suspend/resume cycles and is never overwritten, even when
// the session goal changes.
func TestGoalPreservationDoubleResume(t *testing.T) {
	// Step 1: Create a SessionState with an original goal.
	originalGoal := "Fix all lint issues in the project"
	state := SessionState{
		SessionID:      "goal-preserve-test",
		Goal:           originalGoal,
		OriginalPrompt: originalGoal,
		Status:         "running",
		TurnsCompleted: 2,
	}

	if state.OriginalPrompt != originalGoal {
		t.Fatalf("OriginalPrompt not set correctly: got %q, want %q",
			state.OriginalPrompt, originalGoal)
	}

	// Step 2: Simulate first suspend — user provides input that
	// promotes the goal. The current Goal changes, but OriginalPrompt
	// must retain the original goal.
	newGoal := originalGoal + "\n\n--- NEXT ---\n\nAlso update comments"
	state.Goal = newGoal
	state.Status = "suspended_user_input"

	if state.OriginalPrompt != originalGoal {
		t.Fatalf("OriginalPrompt changed after first goal promotion: got %q, want %q",
			state.OriginalPrompt, originalGoal)
	}
	if state.Goal != newGoal {
		t.Fatalf("Goal not promoted: got %q, want %q", state.Goal, newGoal)
	}

	// Step 3: Serialize to JSON and reload.
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}

	var reloaded SessionState
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}

	if reloaded.OriginalPrompt != originalGoal {
		t.Fatalf("OriginalPrompt lost after reload: got %q, want %q",
			reloaded.OriginalPrompt, originalGoal)
	}
	if reloaded.Goal != newGoal {
		t.Fatalf("Goal lost after reload: got %q, want %q", reloaded.Goal, newGoal)
	}

	// Step 4: Simulate a second suspend/resume cycle. The goal is
	// promoted again, but OriginalPrompt must still be unchanged.
	newGoal2 := newGoal + "\n\n--- NEXT ---\n\nAnd run tests"
	reloaded.Goal = newGoal2
	reloaded.Status = "suspended_user_input"

	if reloaded.OriginalPrompt != originalGoal {
		t.Fatalf("OriginalPrompt changed after second goal promotion: got %q, want %q",
			reloaded.OriginalPrompt, originalGoal)
	}
	if reloaded.Goal != newGoal2 {
		t.Fatalf("Goal not promoted on second cycle: got %q, want %q",
			reloaded.Goal, newGoal2)
	}

	// Serialize and reload again to confirm OriginalPrompt is stable.
	data2, err := json.MarshalIndent(reloaded, "", "  ")
	if err != nil {
		t.Fatalf("marshal state (second): %v", err)
	}

	var reloaded2 SessionState
	if err := json.Unmarshal(data2, &reloaded2); err != nil {
		t.Fatalf("unmarshal state (second): %v", err)
	}

	if reloaded2.OriginalPrompt != originalGoal {
		t.Fatalf("OriginalPrompt overwritten after second cycle: got %q, want %q",
			reloaded2.OriginalPrompt, originalGoal)
	}
	if reloaded2.Goal != newGoal2 {
		t.Fatalf("Goal lost after second reload: got %q, want %q",
			reloaded2.Goal, newGoal2)
	}
}



// TestStateTransitionValidation verifies the session state machine
// transition rules using a table-driven test that covers all possible
// (from, to) combinations across the four real states.
func TestStateTransitionValidation(t *testing.T) {
	tests := []struct {
		from SessionStatus
		to   SessionStatus
		want error // nil means transition is allowed
	}{
		// From running ("error", the initial state) →
		{StateError, StateError, ErrInvalidStateTransition},
		{StateError, StateInterrupted, nil},
		{StateError, StateSuccess, nil},
		{StateError, StateSuspendedUserInput, nil},

		// From suspended_user_input →
		{StateSuspendedUserInput, StateError, nil},
		{StateSuspendedUserInput, StateInterrupted, ErrInvalidStateTransition},
		{StateSuspendedUserInput, StateSuccess, ErrInvalidStateTransition},
		{StateSuspendedUserInput, StateSuspendedUserInput, ErrInvalidStateTransition},

		// From interrupted (terminal) →
		{StateInterrupted, StateError, ErrInvalidStateTransition},
		{StateInterrupted, StateInterrupted, ErrInvalidStateTransition},
		{StateInterrupted, StateSuccess, ErrInvalidStateTransition},
		{StateInterrupted, StateSuspendedUserInput, ErrInvalidStateTransition},

		// From success (terminal) →
		{StateSuccess, StateError, ErrInvalidStateTransition},
		{StateSuccess, StateInterrupted, ErrInvalidStateTransition},
		{StateSuccess, StateSuccess, ErrInvalidStateTransition},
		{StateSuccess, StateSuspendedUserInput, ErrInvalidStateTransition},
	}

	for _, tt := range tests {
		t.Run(tt.from.String()+"→"+tt.to.String(), func(t *testing.T) {
			got := tt.from.Transition(tt.to)
			if tt.want == nil && got != nil {
				t.Fatalf("Transition(%s) unexpectedly returned error: %v", tt.to, got)
			}
			if tt.want != nil && !errors.Is(got, tt.want) {
				t.Fatalf("Transition(%s) error = %v, want %v", tt.to, got, tt.want)
			}
		})
	}
}

// TestOriginalGoalDoubleInterrupt verifies that OriginalGoal is preserved
// across a suspend/resume cycle. After a session is suspended and then
// resumed with a loaded state that carries the first goal but an empty
// OriginalGoal, the resumed runLifecycleState's baseSessionState must have
// OriginalGoal set to the first goal.
func TestOriginalGoalDoubleInterrupt(t *testing.T) {
	t.Run("suspend and resume restores original goal", func(t *testing.T) {
		ctx := context.Background()
		cfg := newLegacyConfig(Config{})
		sessionID := "original-goal-double-interrupt-test"

		firstGoal := "Fix all lint issues in the project"
		firstOriginalPrompt := "Fix all lint issues"

		// Step 1: Create the initial runLifecycleState.
		r1 := newRunLifecycleState(ctx, cfg, sessionID, firstGoal, firstOriginalPrompt, "", "", "", nil)

		// Step 2: Simulate a suspend by transitioning to suspended_user_input.
		if err := r1.transition(StateSuspendedUserInput); err != nil {
			t.Fatalf("transition to suspended_user_input: %v", err)
		}

		// Step 3: Build a loadedState that represents the suspended
		// session. It has the first goal but an empty OriginalGoal
		// (mimicking a session that was created fresh and then
		// suspended, where OriginalGoal was never explicitly set).
		loadedState := &SessionState{
			SessionID:      sessionID,
			Goal:           firstGoal,
			OriginalGoal:   "",
			OriginalPrompt: firstOriginalPrompt,
			Status:         string(StateSuspendedUserInput),
		}

		// Step 4: Resume with a new goal. The loaded state carries the
		// first goal but no OriginalGoal, so the fallback logic in
		// newRunLifecycleState should derive OriginalGoal from the
		// loaded state's Goal.
		secondGoal := "Also update comments"
		secondOriginalPrompt := firstOriginalPrompt
		r2 := newRunLifecycleState(ctx, cfg, sessionID, secondGoal, secondOriginalPrompt, "", "", "", loadedState)

		// Step 5: Verify that OriginalGoal on the resumed state equals
		// the first goal.
		base := r2.baseSessionState()
		if base.OriginalGoal != firstGoal {
			t.Errorf("OriginalGoal mismatch: got %q, want %q", base.OriginalGoal, firstGoal)
		}
	})

	t.Run("resume with explicit OriginalGoal in loaded state", func(t *testing.T) {
		ctx := context.Background()
		cfg := newLegacyConfig(Config{})
		sessionID := "original-goal-explicit-test"

		firstGoal := "Fix all lint issues"
		firstOriginalGoal := "Fix all lint issues in the entire project"

		loadedState := &SessionState{
			SessionID:      sessionID,
			Goal:           firstGoal,
			OriginalGoal:   firstOriginalGoal,
			OriginalPrompt: firstGoal,
			Status:         string(StateSuspendedUserInput),
		}

		secondGoal := "Also update comments"
		r2 := newRunLifecycleState(ctx, cfg, sessionID, secondGoal, firstGoal, "", "", "", loadedState)

		base := r2.baseSessionState()
		if base.OriginalGoal != firstOriginalGoal {
			t.Errorf("OriginalGoal mismatch: got %q, want %q", base.OriginalGoal, firstOriginalGoal)
		}
	})
}

// TestStateTransitionTable tests all 16 possible state transitions using the
// SessionStatus.Transition method. Legal transitions: error→interrupted,
// error→success, error→suspended_user_input, suspended_user_input→error.
// All other combinations must return ErrInvalidStateTransition.
func TestStateTransitionTable(t *testing.T) {
	tests := []struct {
		from        SessionStatus
		to          SessionStatus
		expectError bool
	}{
		// From StateError
		{StateError, StateError, true},
		{StateError, StateInterrupted, false},
		{StateError, StateSuccess, false},
		{StateError, StateSuspendedUserInput, false},
		// From StateSuspendedUserInput
		{StateSuspendedUserInput, StateError, false},
		{StateSuspendedUserInput, StateInterrupted, true},
		{StateSuspendedUserInput, StateSuccess, true},
		{StateSuspendedUserInput, StateSuspendedUserInput, true},
		// From StateInterrupted
		{StateInterrupted, StateError, true},
		{StateInterrupted, StateInterrupted, true},
		{StateInterrupted, StateSuccess, true},
		{StateInterrupted, StateSuspendedUserInput, true},
		// From StateSuccess
		{StateSuccess, StateError, true},
		{StateSuccess, StateInterrupted, true},
		{StateSuccess, StateSuccess, true},
		{StateSuccess, StateSuspendedUserInput, true},
	}

	for _, tt := range tests {
		t.Run(tt.from.String()+"→"+tt.to.String(), func(t *testing.T) {
			err := tt.from.Transition(tt.to)
			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error for %s→%s, got nil", tt.from, tt.to)
				}
				if !errors.Is(err, ErrInvalidStateTransition) {
					t.Fatalf("expected ErrInvalidStateTransition for %s→%s, got %v", tt.from, tt.to, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for %s→%s: %v", tt.from, tt.to, err)
				}
			}
		})
	}
}
