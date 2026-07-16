package discoveryrunner

import (
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

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
