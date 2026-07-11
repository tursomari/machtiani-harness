package session

import "testing"

func TestShellActionEventFromPayloadWithBudgetUsage(t *testing.T) {
	payload := map[string]any{
		"description":       "I'll read the server manager.\n\n<command>sed -n '1,200p' apps/server/src/codexAppServerManager.ts</command>",
		"command":           "sed -n '1,200p' apps/server/src/codexAppServerManager.ts",
		"model_calls_used":  3,
		"step_limit":        20,
		"remaining_steps":   17,
		"commands_executed": 3,
	}

	got, ok := shellActionEventFromPayload(payload)
	if !ok {
		t.Fatal("shellActionEventFromPayload() ok = false, want true")
	}
	if got.Step != 3 {
		t.Fatalf("Step = %d, want 3", got.Step)
	}
	if got.StepLimit != 20 {
		t.Fatalf("StepLimit = %d, want 20", got.StepLimit)
	}
	if got.RemainingSteps != 17 {
		t.Fatalf("RemainingSteps = %d, want 17", got.RemainingSteps)
	}
	if got.CommandsExecuted != 3 {
		t.Fatalf("CommandsExecuted = %d, want 3", got.CommandsExecuted)
	}
	if got.Command != "sed -n '1,200p' apps/server/src/codexAppServerManager.ts" {
		t.Fatalf("Command = %q", got.Command)
	}
	if got.Description != "I'll read the server manager." {
		t.Fatalf("Description = %q, want cleaned prose", got.Description)
	}
}

func TestShellActionEventFromPayloadCommandOnlyFallsBackToCommandDisplayData(t *testing.T) {
	payload := map[string]any{
		"command": "ls -la",
	}

	got, ok := shellActionEventFromPayload(payload)
	if !ok {
		t.Fatal("shellActionEventFromPayload() ok = false, want true")
	}
	if got.Command != "ls -la" {
		t.Fatalf("Command = %q, want %q", got.Command, "ls -la")
	}
	if got.Description != "ls -la" {
		t.Fatalf("Description = %q, want command fallback", got.Description)
	}
	if got.Step != 0 {
		t.Fatalf("Step = %d, want 0 without commands_executed", got.Step)
	}
}

func TestShellActionEventFromPayloadUsesCommandsExecutedForDisplayStep(t *testing.T) {
	payload := map[string]any{
		"description":       "Continue after resume.\n\n<command>pwd</command>",
		"command":           "pwd",
		"model_calls_used":  1,
		"step_limit":        110,
		"remaining_steps":   109,
		"commands_executed": 42,
	}

	got, ok := shellActionEventFromPayload(payload)
	if !ok {
		t.Fatal("shellActionEventFromPayload() ok = false, want true")
	}
	if got.Step != 42 {
		t.Fatalf("Step = %d, want commands_executed value 42", got.Step)
	}
	if got.CommandsExecuted != 42 {
		t.Fatalf("CommandsExecuted = %d, want 42", got.CommandsExecuted)
	}
}

func TestShellActionEventFromPayloadDoesNotUseModelCallsUsedForDisplayStep(t *testing.T) {
	payload := map[string]any{
		"description":      "Run a command.",
		"command":          "pwd",
		"model_calls_used": 7,
		"step_limit":       110,
	}

	got, ok := shellActionEventFromPayload(payload)
	if !ok {
		t.Fatal("shellActionEventFromPayload() ok = false, want true")
	}
	if got.Step != 0 {
		t.Fatalf("Step = %d, want 0 when commands_executed is absent", got.Step)
	}
	if got.CommandsExecuted != 0 {
		t.Fatalf("CommandsExecuted = %d, want 0", got.CommandsExecuted)
	}
}

func TestShellActionEventFromPayloadCommandTagOnlyBecomesCommandOnly(t *testing.T) {
	payload := map[string]any{
		"description": "<command>cat agent/internal/shell-agent/internal/agents/loop.go</command>.",
		"command":     "cat agent/internal/shell-agent/internal/agents/loop.go",
	}

	got, ok := shellActionEventFromPayload(payload)
	if !ok {
		t.Fatal("shellActionEventFromPayload() ok = false, want true")
	}
	if got.Description != got.Command {
		t.Fatalf("Description = %q, want command-only fallback %q", got.Description, got.Command)
	}
}
