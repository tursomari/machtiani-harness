package session

import (
	"context"
	"io"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/runner"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

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
