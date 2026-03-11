package session

import "testing"

func TestFormatShellActionLineWithBudgetUsage(t *testing.T) {
	payload := map[string]any{
		"description":       "sed -n '1,200p' apps/server/src/codexAppServerManager.ts",
		"model_calls_used":  3,
		"step_limit":        20,
		"commands_executed": 3,
	}

	got := formatShellActionLine(payload)
	want := "[shell step 3/20 cmd 3] sed -n '1,200p' apps/server/src/codexAppServerManager.ts"
	if got != want {
		t.Fatalf("formatShellActionLine() = %q, want %q", got, want)
	}
}

func TestFormatShellActionLineFallsBackWithoutBudgetUsage(t *testing.T) {
	payload := map[string]any{
		"command": "ls -la",
	}

	got := formatShellActionLine(payload)
	want := "[shell] ls -la"
	if got != want {
		t.Fatalf("formatShellActionLine() = %q, want %q", got, want)
	}
}
