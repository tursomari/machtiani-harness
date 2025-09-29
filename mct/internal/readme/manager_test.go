package readme

import "testing"

const systemPromptText = "You are Machtiani's internal documentation agent. Write a precise, factual internal README for the engineering team. Capture architecture, key services, and any material code changes relevant to this commit. Keep it under 600 words. Use markdown."

func TestComposeMCTPromptInitialGeneration(t *testing.T) {
	dynamicContext := "Project commit: abc123\n"
	got := composeMCTPrompt(systemPromptText, dynamicContext, "")
	if got != systemPromptText {
		t.Fatalf("expected only system prompt for initial generation, got %q", got)
	}
}

func TestComposeMCTPromptIncrementalGeneration(t *testing.T) {
	dynamicContext := "Project commit: abc123\nPrevious internal README: ..."
	got := composeMCTPrompt(systemPromptText, dynamicContext, "abc123")
	expected := systemPromptText + "\n\n" + dynamicContext
	if got != expected {
		t.Fatalf("expected system prompt plus context, got %q", got)
	}
}

func TestComposeMCTPromptIncrementalWithoutContext(t *testing.T) {
	dynamicContext := "\n"
	got := composeMCTPrompt(systemPromptText, dynamicContext, "abc123")
	if got != systemPromptText {
		t.Fatalf("expected system prompt when dynamic context empty, got %q", got)
	}
}
