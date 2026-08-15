package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/runner"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

type scriptedPlanner struct {
	decisions        []planner.Decision
	questions        []string
	planCalls        int
	finalizeCalls    int
	workResultCounts []int
}

func (p *scriptedPlanner) Plan(_ context.Context, conv *conversation.Conversation, _ string, _ string, _, _ int) (planner.Decision, string, error) {
	workResults := 0
	for _, msg := range conv.Messages {
		if msgMetaType(msg.Metadata) == "work_result" {
			workResults++
		}
	}
	p.workResultCounts = append(p.workResultCounts, workResults)
	if p.planCalls >= len(p.decisions) {
		return "", "", fmt.Errorf("unexpected planner call %d", p.planCalls+1)
	}
	decision := p.decisions[p.planCalls]
	question := ""
	if p.planCalls < len(p.questions) {
		question = p.questions[p.planCalls]
	}
	p.planCalls++
	return decision, question, nil
}

func (p *scriptedPlanner) Finalize(_ context.Context, _ *conversation.Conversation, _ string) (string, error) {
	p.finalizeCalls++
	return "done", nil
}

func (*scriptedPlanner) UpdateProgress(planner.Progress) {}

func (*scriptedPlanner) AnalyzeUserDirectedAsk(_ context.Context, _ *conversation.Conversation, _, _ string, _, _ int) (planner.UserDirectedAskOutcome, error) {
	return planner.UserDirectedAskOutcome{}, nil
}

func isolateReplanRun(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MACHTIANI_CONFIG", filepath.Join(home, "missing-config.toml"))
	t.Setenv("MACHTIANI_SESSION_ID", "")
	t.Setenv("MACHTIANI_SESSION_TEMP_ROOT", "")
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)

	workDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
}

func TestRunReplansAfterSuccessfulWorkerWithoutNewInput(t *testing.T) {
	isolateReplanRun(t)

	pl := &scriptedPlanner{
		decisions: []planner.Decision{
			planner.DecisionAskWorker,
			planner.DecisionAskWorker,
			planner.DecisionAnswerUser,
		},
		questions: []string{
			"Inspect the first area.",
			"Inspect the second area.",
			"",
		},
	}

	result := Run(context.Background(), Options{
		Config: Config{
			MaxTurns:      5,
			DryRun:        true,
			ShellAgent:    true,
			NoTrajectory:  true,
			NoBanner:      true,
			NoCursor:      true,
			OpenAIAPIKey:  "test-key",
			OpenAIBaseURL: "https://example.invalid/v1",
			OpenAIModel:   "test-model",
		},
		Goal:            "Use two worker turns before answering.",
		PlannerOverride: pl,
		Diagnostics:     io.Discard,
		HasNewInput:     false,
	})

	if result.ExitCode != 0 || result.Err != nil {
		t.Fatalf("Run result = %+v, want success", result)
	}
	if result.Turns != 2 {
		t.Fatalf("completed turns = %d, want 2", result.Turns)
	}
	if pl.planCalls != 3 {
		t.Fatalf("planner calls = %d, want 3", pl.planCalls)
	}
	if pl.finalizeCalls != 1 {
		t.Fatalf("finalize calls = %d, want 1", pl.finalizeCalls)
	}
	if len(pl.workResultCounts) != 3 ||
		pl.workResultCounts[1] != pl.workResultCounts[0]+1 ||
		pl.workResultCounts[2] != pl.workResultCounts[1]+1 {
		t.Fatalf("work results observed by planner = %v, want one additional result after each worker", pl.workResultCounts)
	}
}

func TestRunStillFinalizesAtMaxTurns(t *testing.T) {
	isolateReplanRun(t)

	pl := &scriptedPlanner{
		decisions: []planner.Decision{
			planner.DecisionAskWorker,
			planner.DecisionAskWorker,
		},
		questions: []string{
			"Inspect the only permitted worker turn.",
			"This decision must not be requested.",
		},
	}

	result := Run(context.Background(), Options{
		Config: Config{
			MaxTurns:      1,
			DryRun:        true,
			ShellAgent:    true,
			NoTrajectory:  true,
			NoBanner:      true,
			NoCursor:      true,
			OpenAIAPIKey:  "test-key",
			OpenAIBaseURL: "https://example.invalid/v1",
			OpenAIModel:   "test-model",
		},
		Goal:            "Stop after the configured worker limit.",
		PlannerOverride: pl,
		Diagnostics:     io.Discard,
		HasNewInput:     false,
	})

	if result.ExitCode != 0 || result.Err != nil {
		t.Fatalf("Run result = %+v, want success", result)
	}
	if result.Turns != 1 {
		t.Fatalf("completed turns = %d, want 1", result.Turns)
	}
	if pl.planCalls != 1 {
		t.Fatalf("planner calls = %d, want 1", pl.planCalls)
	}
	if pl.finalizeCalls != 1 {
		t.Fatalf("finalize calls = %d, want 1", pl.finalizeCalls)
	}
}

func TestRecoveredWorkerReturnsToPlannerWithoutNewInput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionID := "test-recovered-worker-replans"
	trajectoryPath, err := artifacts.ShellAgentTrajectoryPath(sessionID, 1)
	if err != nil {
		t.Fatalf("ShellAgentTrajectoryPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(trajectoryPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(trajectoryPath, []byte(`{"messages":[]}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	conv := conversation.New(sessionID, "Resume the pending worker.")
	recorder := &conversationRecorder{conversation: conv}
	turnsCompleted := 0
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
		rootCtx:                           context.Background(),
		cfg:                               legacyConfig{maxTurns: 3, dryRun: true, shellAgent: true, maxInputTokens: 4096},
		sessionID:                         sessionID,
		goal:                              "Resume the pending worker.",
		step:                              1,
		sessionErr:                        &sessionErr,
		turnsCompleted:                    &turnsCompleted,
		bus:                               bus,
		activities:                        ui.NewActivityTracker(bus),
		diagWriter:                        io.Discard,
		hasNewInput:                       false,
		isResumingTurn:                    true,
		resumableShellAgentTrajectoryPath: trajectoryPath,
		turnDecision:                      string(planner.DecisionAskWorker),
		turnInfo:                          map[string]any{},
		writeTurn:                         recorder.WriteTurn,
		interruptedResult:                 func(err error) Result { return Result{ExitCode: 1, Err: err} },
		isContextCancelled:                func(error) bool { return false },
		mctRunner:                         mctRunner,
		recorder:                          recorder,
	}

	outcome := executeAskDecision(env, "Finish the recovered worker request.")
	if outcome.action != turnLoopAskWorker {
		t.Fatalf("post-recovery action = %q, want %q", outcome.action, turnLoopAskWorker)
	}
	if turnsCompleted != 1 {
		t.Fatalf("completed turns = %d, want 1", turnsCompleted)
	}
	if sessionErr != nil {
		t.Fatalf("session error = %v, want nil", sessionErr)
	}
}
