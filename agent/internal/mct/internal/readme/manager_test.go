package readme

import "testing"

const systemPromptText = "You are Machtiani's internal documentation agent. Write a cohesive internal README that reflects the current system state for engineers. Incorporate material architectural or service updates implied by the context, but do not mention commits, hashes, diffs, or change logs. The README must stand on its own, stay under 600 words, and use markdown."

func TestComposeMCTPromptInitialGeneration(t *testing.T) {
	dynamicContext := "Current commit (context only): abc123\n"
	got := composeMCTPrompt(systemPromptText, dynamicContext, "")
	if got != systemPromptText {
		t.Fatalf("expected only system prompt for initial generation, got %q", got)
	}
}

func TestComposeMCTPromptIncrementalGeneration(t *testing.T) {
	dynamicContext := "Current commit (context only): abc123\nPrevious internal README: ..."
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
