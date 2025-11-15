package planner

import (
	"strings"
	"testing"
)

func TestParseDecisionAsk(t *testing.T) {
	resp := "Decision: ask\nQuestion: What is the structure of the main module?"
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != DecisionAsk {
		t.Fatalf("expected DecisionAsk, got %q", dec)
	}
	if !strings.Contains(remainder, "What is the structure") {
		t.Fatalf("expected question in remainder, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestParseDecisionPatch(t *testing.T) {
	jsonPayload := `{"metadata": {"description": "Fix typo"}, "edits": []}`
	resp := "Decision: patch\n" + jsonPayload
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != DecisionPatch {
		t.Fatalf("expected DecisionPatch, got %q", dec)
	}
	if !strings.Contains(remainder, "Fix typo") {
		t.Fatalf("expected JSON payload in remainder, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestParseDecisionFinalize(t *testing.T) {
	resp := "Decision: finalize"
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != DecisionFinalize {
		t.Fatalf("expected DecisionFinalize, got %q", dec)
	}
	if remainder != "" {
		t.Fatalf("expected empty remainder for finalize, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestParseDecisionVariants(t *testing.T) {
	tests := []struct {
		name       string
		resp       string
		want       Decision
		wantSubstr string
	}{
		{name: "InstructionVariant", resp: "Decision: instruction\nInstruction: Examine the database schema.", want: DecisionAsk, wantSubstr: "database schema"},
		{name: "QuestionVariant", resp: "Decision: question\nQuestion: What modules exist?", want: DecisionAsk, wantSubstr: "What modules"},
		{name: "MessageVariant", resp: "Decision: message\nMessage: Review the API endpoints.", want: DecisionAsk, wantSubstr: "API endpoints"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, remainder, preamble := parseDecision(tt.resp, true)
			if dec != tt.want {
				t.Fatalf("parseDecision(%q) = %q, want %q", tt.resp, dec, tt.want)
			}
			if tt.wantSubstr != "" && !strings.Contains(remainder, tt.wantSubstr) {
				t.Fatalf("expected remainder to contain %q, got %q", tt.wantSubstr, remainder)
			}
			if preamble != "" {
				t.Fatalf("expected empty preamble, got %q", preamble)
			}
		})
	}
}

func TestParseDecisionCaseInsensitive(t *testing.T) {
	tests := []struct {
		name string
		resp string
		want Decision
	}{
		{name: "UpperAsk", resp: "Decision: ASK\nQuestion: text", want: DecisionAsk},
		{name: "MixedPatch", resp: "Decision: Patch\n{ }", want: DecisionPatch},
		{name: "UpperFinalize", resp: "Decision: FINALIZE", want: DecisionFinalize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, _, _ := parseDecision(tt.resp, true)
			if dec != tt.want {
				t.Fatalf("parseDecision(%q) = %q, want %q", tt.resp, dec, tt.want)
			}
		})
	}
}

func TestParseDecisionMissingDecisionLine(t *testing.T) {
	resp := "Some random text\nNo decision here"
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != "" {
		t.Fatalf("expected empty decision for missing 'Decision:' line, got %q", dec)
	}
	if remainder != "" {
		t.Fatalf("expected empty remainder, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestParseDecisionEmptyResponse(t *testing.T) {
	dec, remainder, preamble := parseDecision("", true)

	if dec != "" {
		t.Fatalf("expected empty decision for empty response, got %q", dec)
	}
	if remainder != "" {
		t.Fatalf("expected empty remainder, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestParseDecisionInvalidDecisionType(t *testing.T) {
	resp := "Decision: unknown\nSome content"
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != "" {
		t.Fatalf("expected empty decision for invalid type, got %q", dec)
	}
	if remainder != "" {
		t.Fatalf("expected empty remainder, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestParseDecisionPreservesMultilineRemainder(t *testing.T) {
	resp := "Decision: ask\nQuestion: Part 1\nPart 2\nPart 3"
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != DecisionAsk {
		t.Fatalf("expected DecisionAsk, got %q", dec)
	}
	if !strings.Contains(remainder, "Part 1") || !strings.Contains(remainder, "Part 2") || !strings.Contains(remainder, "Part 3") {
		t.Fatalf("expected multiline remainder preserved, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestParseDecisionAllowsShortPreamble(t *testing.T) {
	resp := "Note: quick recap.\nDecision: ask\nQuestion: Summarize the safeguards changes."
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != DecisionAsk {
		t.Fatalf("expected DecisionAsk with short preamble allowed, got %q", dec)
	}
	if preamble != "Note: quick recap." {
		t.Fatalf("expected preamble to be returned, got %q", preamble)
	}
	if !strings.Contains(remainder, "safeguards changes") {
		t.Fatalf("expected remainder to include question, got %q", remainder)
	}
}

func TestParseDecisionRejectsTooLongPreamble(t *testing.T) {
	longLine := strings.Repeat("x", maxDecisionPreambleChars+1)
	resp := longLine + "\nDecision: ask\nQuestion: Should fail"
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != "" {
		t.Fatalf("expected empty decision when preamble exceeds limit, got %q", dec)
	}
	if remainder != "" {
		t.Fatalf("expected empty remainder when preamble exceeds limit, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble return when parsing fails, got %q", preamble)
	}
}

func TestParseDecisionRejectsTooManyPreambleLines(t *testing.T) {
	lines := []string{"one", "two", "three", "four"}
	resp := strings.Join(lines, "\n") + "\nDecision: ask\nQuestion: Should fail"
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != "" {
		t.Fatalf("expected empty decision when preamble has too many lines, got %q", dec)
	}
	if remainder != "" {
		t.Fatalf("expected empty remainder when preamble has too many lines, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble return when parsing fails, got %q", preamble)
	}
}

func TestParseDecisionPatchDisabled(t *testing.T) {
	resp := "Decision: patch\n{ }"
	dec, remainder, preamble := parseDecision(resp, false)

	if dec != DecisionAsk {
		t.Fatalf("expected ask decision when patch is disabled; got %v", dec)
	}
	want := "Question: Considering the current transcript, produce the single next high-signal repository-focused prompt for mct."
	if remainder != want {
		t.Fatalf("unexpected remainder. got %q, want %q", remainder, want)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestPlannerRejectsInvalidPatchJSON(t *testing.T) {
	resp := "Decision: patch\n{this is not valid json}"
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != DecisionPatch {
		t.Fatalf("expected DecisionPatch to be parsed despite invalid JSON, got %q", dec)
	}
	if !strings.Contains(remainder, "not valid json") {
		t.Fatalf("expected invalid JSON in remainder, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
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
