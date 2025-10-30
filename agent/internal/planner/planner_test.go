package planner

import (
	"testing"
)

func TestParseDecisionVariants(t *testing.T) {
	cases := []struct {
		input string
		want  Decision
	}{
		{"Decision: ask\nQuestion: text", DecisionAsk},
		{"Decision: ask\nInstruction: text", DecisionAsk},
		{"Decision: ask\nMessage: text", DecisionAsk},
		{"Decision: instruction\nInstruction: text", DecisionAsk},
		{"Decision: question\nQuestion: text", DecisionAsk},
		{"Decision: message\nMessage: text", DecisionAsk},
		{"Decision: patch\n{ }", DecisionPatch},
		{"Decision: finalize", DecisionFinalize},
	}
	for _, tc := range cases {
		got, _ := parseDecision(tc.input, true)
		if got != tc.want {
			t.Fatalf("parseDecision(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestParseDecisionDisabledFallsBackToAsk(t *testing.T) {
	dec, rest := parseDecision("Decision: patch\n{ }", false)
	if dec != DecisionAsk {
		t.Fatalf("expected ask decision when patch is disabled; got %v", dec)
	}
	want := "Question: Considering the current transcript, produce the single next high-signal repository-focused prompt for mct."
	if rest != want {
		t.Fatalf("unexpected remainder. got %q, want %q", rest, want)
	}
}

func TestPlanPromptIncludesMetadata(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true, PatchEnabled: true})
	prompt := c.planPrompt("goal text", "transcript text", 1, 4)
	want := []string{"Decision: ask|patch|finalize", "Goal:", "Transcript:", "Step 1 of 4"}
	for _, w := range want {
		if !contains(prompt, w) {
			t.Fatalf("plan prompt missing %q:\n%s", w, prompt)
		}
	}
}

func TestPlanPromptDisabledOmitsPatchInstructions(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true})
	prompt := c.planPrompt("goal", "transcript", 2, 4)
	if contains(prompt, "Decision: ask|patch|finalize") {
		t.Fatalf("prompt should not list patch option when patching is disabled:\n%s", prompt)
	}
	if !contains(prompt, "Decision: ask|finalize") {
		t.Fatalf("prompt should list ask|finalize when patching is disabled:\n%s", prompt)
	}
	if contains(prompt, "Patch JSON schema") {
		t.Fatalf("prompt should not include patch schema when patching is disabled:\n%s", prompt)
	}
	if !contains(prompt, "Patch requests are disabled for this run") {
		t.Fatalf("prompt should call out that patch requests are disabled:\n%s", prompt)
	}
}

func TestFinalizePromptRetainsTranscript(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true})
	prompt := c.finalizePrompt("goal text", "transcript text")
	want := []string{"Goal:", "goal text", "Transcript:", "transcript text"}
	for _, w := range want {
		if !contains(prompt, w) {
			t.Fatalf("finalize prompt missing %q:\n%s", w, prompt)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || index(s, sub) >= 0
}

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
