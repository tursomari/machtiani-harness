package session

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/planner"
)

type failingRecoveryPlanner struct{}

func (*failingRecoveryPlanner) Plan(context.Context, *conversation.Conversation, string, string, int, int) (planner.Decision, string, error) {
	return "", "", errors.New("simulated provider failure")
}

func (*failingRecoveryPlanner) Finalize(context.Context, *conversation.Conversation, string) (string, error) {
	return "", errors.New("unexpected finalize")
}

func (*failingRecoveryPlanner) UpdateProgress(planner.Progress) {}

func (*failingRecoveryPlanner) AnalyzeUserDirectedAsk(context.Context, *conversation.Conversation, string, string, int, int) (planner.UserDirectedAskOutcome, error) {
	return planner.UserDirectedAskOutcome{}, nil
}

func TestRecoveryFailureRestartThenSuccessDoesNotDuplicateMessages(t *testing.T) {
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

	const (
		sessionID      = "recovery-restart-e2e"
		originalGoal   = "Complete the pending email request."
		recoveryPrompt = "Continue the interrupted request and return the pending answer."
	)
	conv := conversation.New(sessionID, originalGoal)
	conv.Goal = originalGoal
	conv.OriginalPrompt = originalGoal
	conv.Status = "error"
	conversationPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(conversationPath), 0o755); err != nil {
		t.Fatalf("mkdir conversation directory: %v", err)
	}
	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal seed conversation: %v", err)
	}
	if err := os.WriteFile(conversationPath, data, 0o644); err != nil {
		t.Fatalf("write seed conversation: %v", err)
	}
	t.Cleanup(func() {
		dir, _ := artifacts.SessionDirectory(sessionID)
		_ = os.RemoveAll(dir)
	})

	baseConfig := Config{
		SessionID:     sessionID,
		MaxTurns:      3,
		DryRun:        true,
		ShellAgent:    true,
		NoTrajectory:  true,
		NoBanner:      true,
		NoCursor:      true,
		OpenAIAPIKey:  "test-key",
		OpenAIBaseURL: "https://example.invalid/v1",
		OpenAIModel:   "test-model",
	}
	run := func(pl Planner) Result {
		return Run(context.Background(), Options{
			Config:          baseConfig,
			Goal:            recoveryPrompt,
			PlannerOverride: pl,
			Diagnostics:     io.Discard,
		})
	}
	countRecoveryMessages := func() int {
		t.Helper()
		persisted, err := os.ReadFile(conversationPath)
		if err != nil {
			t.Fatalf("read conversation: %v", err)
		}
		loaded, err := conversation.Unmarshal(persisted)
		if err != nil {
			t.Fatalf("unmarshal conversation: %v", err)
		}
		count := 0
		for _, msg := range loaded.Messages {
			if msgMetaType(msg.Metadata) == "recovery" {
				count++
			}
		}
		return count
	}

	for attempt := 1; attempt <= 2; attempt++ {
		result := run(&failingRecoveryPlanner{})
		if result.Err == nil || result.ExitCode == 0 {
			t.Fatalf("failure attempt %d result = %+v, want planner failure", attempt, result)
		}
		if got := countRecoveryMessages(); got != 2 {
			t.Fatalf("recovery messages after failure attempt %d = %d, want 2", attempt, got)
		}
	}

	successPlanner := &scriptedPlanner{
		decisions: []planner.Decision{planner.DecisionAskWorker, planner.DecisionAnswerUser},
		questions: []string{"Perform the pending recovery work.", ""},
	}
	result := run(successPlanner)
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("successful recovery result = %+v", result)
	}
	if got := countRecoveryMessages(); got != 2 {
		t.Fatalf("recovery messages after success = %d, want 2", got)
	}
}

func TestAnsweredUserInputFailureRestartDoesNotRemainSuspended(t *testing.T) {
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

	const (
		sessionID    = "answered-input-restart-e2e"
		originalGoal = "Complete the authorized repository task."
		question     = "Do you authorize the requested merge?"
		answer       = "Yes!"
	)
	conv := conversation.New(sessionID, originalGoal)
	conv.Goal = originalGoal
	conv.OriginalPrompt = originalGoal
	conv.Status = string(StateSuspendedUserInput)
	conv.SuspendedUserInput = &conversation.SuspendedUserInputState{
		Kind:     "user-directed-ask",
		Question: question,
	}
	conv.AddMessage("assistant", question, map[string]any{"type": "user_input_request"})
	conversationPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(conversationPath), 0o755); err != nil {
		t.Fatalf("mkdir conversation directory: %v", err)
	}
	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal seed conversation: %v", err)
	}
	if err := os.WriteFile(conversationPath, data, 0o644); err != nil {
		t.Fatalf("write seed conversation: %v", err)
	}
	t.Cleanup(func() {
		dir, _ := artifacts.SessionDirectory(sessionID)
		_ = os.RemoveAll(dir)
	})

	baseConfig := Config{
		SessionID:     sessionID,
		MaxTurns:      3,
		DryRun:        true,
		ShellAgent:    true,
		NoTrajectory:  true,
		NoBanner:      true,
		NoCursor:      true,
		OpenAIAPIKey:  "test-key",
		OpenAIBaseURL: "https://example.invalid/v1",
		OpenAIModel:   "test-model",
	}
	result := Run(context.Background(), Options{
		Config:          baseConfig,
		Goal:            answer,
		HasNewInput:     true,
		PlannerOverride: &failingRecoveryPlanner{},
		Diagnostics:     io.Discard,
	})
	if result.Err == nil || result.ExitCode == 0 {
		t.Fatalf("resume result = %+v, want planner failure after input consumption", result)
	}

	persisted, err := os.ReadFile(conversationPath)
	if err != nil {
		t.Fatalf("read conversation after failed resume: %v", err)
	}
	reloaded, err := conversation.Unmarshal(persisted)
	if err != nil {
		t.Fatalf("unmarshal conversation after failed resume: %v", err)
	}
	if reloaded.Status != string(StateError) || reloaded.SuspendedUserInput != nil {
		t.Fatalf("answered question was not durably consumed: status=%q suspended=%+v", reloaded.Status, reloaded.SuspendedUserInput)
	}
	if reloaded.Goal != originalGoal || reloaded.OriginalPrompt != originalGoal {
		t.Fatalf("answer replaced durable goal: goal=%q original_prompt=%q", reloaded.Goal, reloaded.OriginalPrompt)
	}
	requests, responses := 0, 0
	for _, msg := range reloaded.Messages {
		switch msgMetaType(msg.Metadata) {
		case "user_input_request":
			requests++
		case "user_input_response":
			responses++
		}
	}
	if requests != 1 || responses != 1 {
		t.Fatalf("request/response counts after failed resume = %d/%d, want 1/1", requests, responses)
	}

	bootstrap, bootstrapResult, ok := prepareRunBootstrap(context.Background(), Options{
		Config:      baseConfig,
		Goal:        answer,
		HasNewInput: true,
	}, io.Discard)
	if !ok {
		t.Fatalf("restart bootstrap failed: %+v", bootstrapResult)
	}
	if bootstrap.resumeSuspendedInput != nil {
		t.Fatalf("restart recovered an already answered question: %+v", bootstrap.resumeSuspendedInput)
	}
}
