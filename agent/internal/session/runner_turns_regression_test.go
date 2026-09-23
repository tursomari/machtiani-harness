package session

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/runner"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func TestShellAgentFailureIsPersistedBeforePlannerContinues(t *testing.T) {
	for _, code := range []string{"FormatErrorLoop", "TRANSIENT_ERROR", "EMPTY_RESPONSE", "AUTH_REQUIRED"} {
		t.Run(code, func(t *testing.T) {
			conv := conversation.New("test-format-failure", "Complete the work")
			path := filepath.Join(t.TempDir(), "conversation.json")
			recorder := &conversationRecorder{conversation: conv, conversationPath: path, sessionID: conv.SessionID}
			if err := recorder.PreWriteTurn(1, "Do the work", "test-trajectory"); err != nil {
				t.Fatal(err)
			}
			completed := 0
			var sessionErr error
			env := &runTurnEnv{cfg: legacyConfig{maxTurns: 3}, step: 1, recorder: recorder, writeTurn: recorder.WriteTurn, turnsCompleted: &completed, sessionErr: &sessionErr, turnInfo: map[string]any{}}
			failure := &shellagent.Failure{Status: "failed", Code: code, Attempts: 3, Message: "No valid final answer", Diagnostic: "invalid tags", TrajectoryPath: "test-trajectory"}
			result := env.recordShellAgentFailure("Do the work", failure)
			if result.action != turnLoopAskWorker || sessionErr != nil || completed != 1 {
				t.Fatalf("result = %+v, err = %v, completed = %d", result, sessionErr, completed)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := conversation.Unmarshal(data)
			if err != nil {
				t.Fatal(err)
			}
			if saved.ShellAgentResumable || saved.TurnsCompleted != 1 {
				t.Fatalf("saved state = %+v", saved)
			}
			var results int
			for _, msg := range saved.Messages {
				if msg.Metadata["type"] != "work_result" {
					continue
				}
				results++
				var got shellagent.Failure
				if err := json.Unmarshal([]byte(msg.Content), &got); err != nil {
					t.Fatal(err)
				}
				if got.Code != code || got.Attempts != 3 || msg.Metadata["status"] != "failed" || msg.Metadata["failure"] == nil {
					t.Fatalf("work result = %+v", msg)
				}
			}
			if results != 1 {
				t.Fatalf("work results = %d", results)
			}
		})
	}
}

// TestExecuteAskDecisionTerminatesAtOrOverCap verifies that when a session
// resumes with turns_completed already at or above the configured turn cap,
// the post-worker guard terminates the run loop instead of requesting another
// worker turn. The >= guard must fire and return turnLoopAnswerUser; returning
// turnLoopAskWorker here would loop until the harness timeout.
func TestExecuteAskDecisionTerminatesAtOrOverCap(t *testing.T) {
	for _, tc := range []struct {
		name         string
		initialTurns int
		maxTurns     int
	}{
		{name: "at-cap", initialTurns: 2, maxTurns: 2},
		{name: "over-cap", initialTurns: 3, maxTurns: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			sessionID := "test-resume-" + tc.name
			conv := conversation.New(sessionID, "Resume at or over the turn cap.")
			recorder := &conversationRecorder{conversation: conv}

			turnsCompleted := tc.initialTurns
			var sessionErr error
			bus := ui.NewEventBus(32)
			mctRunner := &runner.Runner{
				DryRun:     true,
				ShellAgent: true,
				ShellAgentLibrary: &shellagent.ShellAgentLibrary{
					Config:     &minisweagent.ShellAgentConfig{},
					Prompts:    &minisweagent.PromptsConfig{},
					CommandTag: "command",
				},
			}

			env := &runTurnEnv{
				rootCtx:            context.Background(),
				cfg:                legacyConfig{maxTurns: tc.maxTurns, dryRun: true, shellAgent: true, maxInputTokens: 4096},
				sessionID:          sessionID,
				goal:               "Resume at or over the turn cap.",
				step:               1,
				sessionErr:         &sessionErr,
				turnsCompleted:     &turnsCompleted,
				bus:                bus,
				activities:         ui.NewActivityTracker(bus),
				diagWriter:         io.Discard,
				hasNewInput:        false,
				turnDecision:       string(planner.DecisionAskWorker),
				turnInfo:           map[string]any{},
				writeTurn:          recorder.WriteTurn,
				interruptedResult:  func(err error) Result { return Result{ExitCode: 1, Err: err} },
				isContextCancelled: func(error) bool { return false },
				mctRunner:          mctRunner,
				recorder:           recorder,
			}

			outcome := executeAskDecision(env, "Continue the resumed worker turn.")
			if outcome.action != turnLoopAnswerUser {
				t.Fatalf("post-worker action = %q, want %q (initial turns %d, cap %d)",
					outcome.action, turnLoopAnswerUser, tc.initialTurns, tc.maxTurns)
			}
			if turnsCompleted != tc.initialTurns+1 {
				t.Fatalf("completed turns = %d, want %d", turnsCompleted, tc.initialTurns+1)
			}
			if sessionErr != nil {
				t.Fatalf("session error = %v, want nil", sessionErr)
			}
		})
	}
}
