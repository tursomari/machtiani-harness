package discoveryrunner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestRunUsesModelHostTransportWithoutHTTPFields(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(t.TempDir(), "model-host")
	script := `#!/bin/sh
read request
printf '%s\n' '{"v":1,"id":"generation","event":{"type":"text-delta","index":0,"text":"BEGIN_RELEVANT_FILES[file-discovery]\nREADME.md\nEND_RELEVANT_FILES[file-discovery]"}}'
printf '%s\n' '{"v":1,"id":"generation","result":{"completed":true}}'
`
	if err := os.WriteFile(host, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	budget, err := llm.BudgetForContextLength(8192, llm.ContextSourceSessionFlag)
	if err != nil {
		t.Fatal(err)
	}

	trajectory := filepath.Join(t.TempDir(), "trajectory.jsonl")
	result, err := Run(context.Background(), "find the readme\n\nRG_OUT:\nREADME.md\nEND_RG_OUT\n", ModelSettings{
		Resolved: llm.ResolvedModel{
			Transport: "model-host",
			Profile:   "/private/model-profile.json",
			Command:   host,
			Model:     "fixture",
		},
		InputBudget:        budget,
		TrajectoryOverride: trajectory,
	}, "session", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Paths) != 1 || result.Paths[0] != "README.md" {
		if data, readErr := os.ReadFile(trajectory); readErr == nil {
			t.Logf("trajectory:\n%s", data)
		}
		t.Fatalf("paths = %#v", result.Paths)
	}
}

func TestFitInitialPromptReservesFixedProtocolContent(t *testing.T) {
	budget, err := llm.BudgetForContextLength(8192, llm.ContextSourceSessionFlag)
	if err != nil {
		t.Fatal(err)
	}
	limit, err := InitialPromptTokenLimit(budget)
	if err != nil {
		t.Fatal(err)
	}
	prompt := strings.Repeat("older line material\n", limit) + "newest request\n"
	fitted, truncated, err := FitInitialPrompt(prompt, budget)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || !strings.Contains(fitted, "newest request") {
		t.Fatalf("unexpected fit result: truncated=%t prompt=%q", truncated, fitted)
	}
	if llm.EstimateTokens(fitted) > limit {
		t.Fatalf("prompt estimate = %d, limit = %d", llm.EstimateTokens(fitted), limit)
	}
}

func TestInitialPromptTokenLimitRejectsFixedContentOnlyOverflow(t *testing.T) {
	_, err := InitialPromptTokenLimit(llm.InputBudget{MaxInputTokens: 1})
	if err == nil || !strings.Contains(err.Error(), "fixed system/protocol") {
		t.Fatalf("error = %v", err)
	}
}

func TestEmergencyInitialInputBytes(t *testing.T) {
	tests := []struct {
		name   string
		tokens int
		want   int
	}{
		{name: "floor", tokens: 1, want: 1 << 20},
		{name: "derived", tokens: 100000, want: 1600000},
		{name: "ceiling", tokens: 10000000, want: 64 << 20},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := EmergencyInitialInputBytes(test.tokens); got != test.want {
				t.Fatalf("EmergencyInitialInputBytes(%d) = %d, want %d", test.tokens, got, test.want)
			}
		})
	}
}
