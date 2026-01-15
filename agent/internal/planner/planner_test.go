package planner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
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

func TestParseDecisionPatchShorthand(t *testing.T) {
	resp := "Patch: src/main.go"
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != DecisionPatch {
		t.Fatalf("expected DecisionPatch, got %q", dec)
	}
	if remainder != "src/main.go" {
		t.Fatalf("expected shorthand remainder to be patch path, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestPlanPatchShorthandRoutesToStrictPatchFlow(t *testing.T) {
	client := NewClient(ClientConfig{PatchEnabled: true, StrictPatchMode: true, RepoRoot: t.TempDir()})
	client.chatFn = func(context.Context, []llm.Message) (string, error) {
		return "Patch: src/main.go", nil
	}
	dec, payload, err := client.Plan(context.Background(), "goal", "transcript", 1, 3, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if dec != DecisionAsk {
		t.Fatalf("expected shorthand to fall back to ask without repo files, got %q", dec)
	}
	if payload == "" {
		t.Fatalf("expected fallback question to be non-empty")
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

func TestParseDecisionAcceptReject(t *testing.T) {
	tests := []struct {
		name            string
		resp            string
		want            Decision
		expectRemainder bool
	}{
		{name: "AcceptSimple", resp: "Decision: accept\nReason: looks good", want: DecisionAccept, expectRemainder: true},
		{name: "RejectMixedCase", resp: "Decision: ReJeCt\nReason: undo", want: DecisionReject, expectRemainder: true},
		{name: "AcceptWithExtra", resp: "Decision: I accept this patch", want: DecisionAccept, expectRemainder: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, remainder, _ := parseDecision(tt.resp, true)
			if dec != tt.want {
				t.Fatalf("parseDecision(%q) = %q, want %q", tt.resp, dec, tt.want)
			}
			if (remainder != "") != tt.expectRemainder {
				t.Fatalf("expected remainder presence %v, got %q", tt.expectRemainder, remainder)
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

func TestReviewPromptIncludesReviewDetails(t *testing.T) {
	client := NewClient(ClientConfig{})
	client.UpdateProgress(Progress{
		PendingReview: &PendingReview{
			Description:      "Fix login handler",
			PatchPath:        "/tmp/patch.diff",
			ReversePatchPath: "/tmp/patch.diff.reverse",
			Files:            []string{"pkg/auth/login.go"},
			Sequence:         4,
			Insertions:       12,
			Deletions:        3,
		},
	})
	prompt := client.reviewPrompt("Improve auth flow", "Transcript body", 3, 6)
	for _, substr := range []string{"Accept", "Reject", "Undo patch file", "pkg/auth/login.go", "Patch sequence: 4"} {
		if !strings.Contains(prompt, substr) {
			t.Fatalf("expected review prompt to contain %q, got %q", substr, prompt)
		}
	}
}

func TestReviewPromptIncludesPatchDiffPreview(t *testing.T) {
	client := NewClient(ClientConfig{})
	patchDir := t.TempDir()
	patchPath := filepath.Join(patchDir, "change.patch")
	diff := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-Old line\n+New line\n"
	if err := os.WriteFile(patchPath, []byte(diff), 0o644); err != nil {
		t.Fatalf("write patch: %v", err)
	}
	client.UpdateProgress(Progress{
		PendingReview: &PendingReview{
			Description: "Doc tweak",
			PatchPath:   patchPath,
		},
	})
	prompt := client.reviewPrompt("Doc goal", "Transcript body", 2, 4)
	if !strings.Contains(prompt, "Patch diff preview:") {
		t.Fatalf("expected diff preview header, got %q", prompt)
	}
	if !strings.Contains(prompt, "```diff") {
		t.Fatalf("expected diff code fence, got %q", prompt)
	}
	if !strings.Contains(prompt, "+New line") {
		t.Fatalf("expected diff content in prompt, got %q", prompt)
	}
}

func TestPlanReviewModeParsesAccept(t *testing.T) {
	client := NewClient(ClientConfig{})
	client.chatFn = func(context.Context, []llm.Message) (string, error) {
		return "Decision: accept\nReason: ship it", nil
	}
	client.UpdateProgress(Progress{
		PendingReview: &PendingReview{PatchPath: "patch.diff"},
	})
	dec, reason, err := client.Plan(context.Background(), "goal", "transcript", 2, 5, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if dec != DecisionAccept {
		t.Fatalf("expected DecisionAccept, got %q", dec)
	}
	if !strings.Contains(reason, "Reason:") {
		t.Fatalf("expected reason to include explanation, got %q", reason)
	}
}

func TestPlanReviewModeDefaultsToAcceptWhenMissingDecision(t *testing.T) {
	client := NewClient(ClientConfig{})
	client.chatFn = func(context.Context, []llm.Message) (string, error) {
		return "Looks great!", nil
	}
	client.UpdateProgress(Progress{
		PendingReview: &PendingReview{PatchPath: "patch.diff"},
	})
	dec, note, err := client.Plan(context.Background(), "goal", "transcript", 1, 4, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if dec != DecisionAccept {
		t.Fatalf("expected DecisionAccept fallback, got %q", dec)
	}
	if !strings.Contains(strings.ToLower(note), "auto-accepted") {
		t.Fatalf("expected note to mention auto-accept, got %q", note)
	}
}

func TestPlanReviewModeCoercesNonReviewDecisionToAccept(t *testing.T) {
	client := NewClient(ClientConfig{})
	client.chatFn = func(context.Context, []llm.Message) (string, error) {
		return "Decision: ask\nQuestion: what's next?", nil
	}
	client.UpdateProgress(Progress{
		PendingReview: &PendingReview{PatchPath: "patch.diff"},
	})
	dec, note, err := client.Plan(context.Background(), "goal", "transcript", 2, 5, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if dec != DecisionAccept {
		t.Fatalf("expected DecisionAccept fallback, got %q", dec)
	}
	if !strings.Contains(strings.ToLower(note), "auto-accepted") {
		t.Fatalf("expected note to mention auto-accept, got %q", note)
	}
}

func TestPlanPromptIncludesProgressSection(t *testing.T) {
	client := NewClient(ClientConfig{})
	client.UpdateProgress(Progress{
		SuccessFiles:   []string{"LICENSE", "lib/web/fetch/LICENSE", "LICENSE"},
		AppliedPatches: 3,
	})
	prompt := client.planPrompt("Update licensing headers", "", 2, 5, nil)
	if strings.Contains(prompt, "Files already updated successfully this session") {
		t.Fatalf("expected prompt to omit success files section, got %q", prompt)
	}
	if strings.Contains(prompt, "Strict patch successes so far") {
		t.Fatalf("expected prompt to omit patch success heuristic, got %q", prompt)
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
	prompt := c.planPrompt("goal text", "transcript text", 1, 4, nil)
	want := []string{"Decision: ask|patch", "Goal:", "Transcript:", "Step 1 of 4"}
	for _, w := range want {
		if !contains(prompt, w) {
			t.Fatalf("plan prompt missing %q:\n%s", w, prompt)
		}
	}
}

func TestPlanPromptAllowsFinalizeWhenPatchPlanComplete(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true, PatchEnabled: true})
	plan := &PatchPlan{Items: []PatchPlanItem{{Description: "update README", Complete: true}}}
	prompt := c.planPrompt("goal text", "transcript text", 1, 4, plan)
	if !strings.Contains(prompt, "Decision: ask|patch|finalize") {
		t.Fatalf("plan prompt should expose finalize when patch plan is complete:\n%s", prompt)
	}
}

func TestPlanPromptBlocksFinalizeWhenPatchPlanIncomplete(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true, PatchEnabled: true})
	plan := &PatchPlan{Items: []PatchPlanItem{{Description: "update README", Complete: false}}}
	prompt := c.planPrompt("goal text", "transcript text", 1, 4, plan)
	if strings.Contains(prompt, "Decision: ask|patch|finalize") {
		t.Fatalf("plan prompt should hide finalize when patch plan is incomplete:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Decision: ask|patch") {
		t.Fatalf("plan prompt should still expose ask|patch when plan incomplete:\n%s", prompt)
	}
}

func TestPlanPromptFromFileGatesFinalize(t *testing.T) {
	root := t.TempDir()
	templatePath := filepath.Join(root, "templates", "planner", "plan_prompt.tpl")
	if err := os.MkdirAll(filepath.Dir(templatePath), 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	templateContent := strings.TrimSpace(`EXTERNAL TEMPLATE
<reply-format>
  <output>
    {{- if and .PatchEnabled .AllowFinalize }}
    <line position="1">Decision: ask|patch|finalize</line>
    {{- else if .PatchEnabled }}
    <line position="1">Decision: ask|patch</line>
    {{- else }}
    <line position="1">Decision: ask</line>
    {{- end }}
  </output>
</reply-format>
`) + "\n"
	if err := os.WriteFile(templatePath, []byte(templateContent), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	configPath := filepath.Join(root, "config.toml")
	configContent := `listen = "127.0.0.1:0"

[prompts.planner]
plan_prompt = { file = "templates/planner/plan_prompt.tpl" }
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)

	cfg, _, err := llm.LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig: %v", err)
	}
	if cfg.Prompts == nil || cfg.Prompts.Planner == nil {
		t.Fatalf("expected planner prompts from file config")
	}
	client := NewClient(ClientConfig{DryRun: true, PatchEnabled: true, Prompts: cfg.Prompts.Planner})
	plan := &PatchPlan{Items: []PatchPlanItem{{Description: "update README", Complete: false}}}
	prompt := client.planPrompt("goal text", "transcript text", 1, 4, plan)
	if !strings.Contains(prompt, "EXTERNAL TEMPLATE") {
		t.Fatalf("expected file-based template content in prompt, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "Decision: ask|patch|finalize") {
		t.Fatalf("file-based plan prompt should hide finalize when patch plan is incomplete:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Decision: ask|patch") {
		t.Fatalf("file-based plan prompt should still expose ask|patch when plan incomplete:\n%s", prompt)
	}
}

func TestPlanPromptHighlightsPatchShorthand(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true, PatchEnabled: true})
	prompt := c.planPrompt("goal", "", 2, 5, nil)
	want := []string{
		"Patch: <repo-relative filepath>",
	}
	for _, w := range want {
		if !contains(prompt, w) {
			t.Fatalf("plan prompt missing %q:\n%s", w, prompt)
		}
	}
}

func TestPlanPromptStrictModeDefersPatchDetails(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true, PatchEnabled: true, StrictPatchMode: true})
	prompt := c.planPrompt("goal", "transcript", 3, 6, nil)

	disallowed := []string{
		"Strict patch planner flow:",
		"Patch JSON schema",
	}
	for _, bad := range disallowed {
		if contains(prompt, bad) {
			t.Fatalf("plan prompt should defer detailed patch guidance and omit %q:\n%s", bad, prompt)
		}
	}
}

func TestPlanPromptDisabledOmitsPatchInstructions(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true})
	prompt := c.planPrompt("goal", "transcript", 2, 4, nil)
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

func TestBuildPlanMessagesNoGoalUpdate(t *testing.T) {
	client := NewClient(ClientConfig{PatchEnabled: true})
	messages := client.buildPlanMessages("Finish docs", "Transcript body", 2, 4, nil)

	wantRoles := []string{"system", "user", "assistant", "user"}
	if len(messages) != len(wantRoles) {
		t.Fatalf("expected %d messages, got %d", len(wantRoles), len(messages))
	}
	for i, role := range wantRoles {
		if messages[i].Role != role {
			t.Fatalf("message %d role = %q, want %q", i, messages[i].Role, role)
		}
	}
	if !strings.Contains(messages[0].Content, "Decision: ask|patch") {
		t.Fatalf("system prompt missing decision line: %q", messages[0].Content)
	}
	if messages[1].Content != "Finish docs" {
		t.Fatalf("unexpected goal message: %q", messages[1].Content)
	}
	if messages[2].Content != "Transcript body" {
		t.Fatalf("unexpected transcript message: %q", messages[2].Content)
	}
	if !strings.Contains(messages[3].Content, "Step 2 of 4") {
		t.Fatalf("step message missing progress: %q", messages[3].Content)
	}
}

func TestBuildPlanMessagesStripsGoalPreamble(t *testing.T) {
	client := NewClient(ClientConfig{PatchEnabled: true})
	transcript := strings.Join([]string{
		"= MCT-AGENT TRANSCRIPT",
		"",
		"== GOAL:",
		"",
		"Finish docs",
		"",
		"== TURN 0",
		"Question: start",
	}, "\n")

	messages := client.buildPlanMessages("Finish docs", transcript, 2, 4, nil)
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}
	if messages[2].Role != "assistant" {
		t.Fatalf("expected assistant transcript message, got %q", messages[2].Role)
	}
	if strings.Contains(messages[2].Content, "GOAL:") {
		t.Fatalf("goal header should be stripped from transcript: %q", messages[2].Content)
	}
	if !strings.Contains(messages[2].Content, "TURN 0") {
		t.Fatalf("transcript content missing turn data: %q", messages[2].Content)
	}
}

func TestBuildPlanMessagesSplitsGoalUpdate(t *testing.T) {
	client := NewClient(ClientConfig{PatchEnabled: false})
	transcript := strings.Join([]string{
		"= MCT-AGENT TRANSCRIPT",
		"",
		"== GOAL:",
		"",
		"Starting objective",
		"",
		"== TURN 0",
		"Question: start",
		"",
		"=== GOAL UPDATE",
		"Refined objective",
		"",
		"== TURN 1",
		"Assistant: next steps",
	}, "\n")

	messages := client.buildPlanMessages("Initial goal", transcript, 1, 3, nil)
	wantRoles := []string{"system", "user", "assistant", "user", "assistant", "user"}
	if len(messages) != len(wantRoles) {
		t.Fatalf("expected %d messages, got %d", len(wantRoles), len(messages))
	}
	for i, role := range wantRoles {
		if messages[i].Role != role {
			t.Fatalf("message %d role = %q, want %q", i, messages[i].Role, role)
		}
	}
	if strings.Contains(messages[2].Content, "GOAL:") {
		t.Fatalf("expected goal header stripped from transcript: %q", messages[2].Content)
	}
	if !strings.Contains(messages[2].Content, "TURN 0") {
		t.Fatalf("expected pre-update transcript in assistant message: %q", messages[2].Content)
	}
	if messages[3].Content != "Refined objective" {
		t.Fatalf("unexpected goal update content: %q", messages[3].Content)
	}
	if !strings.Contains(messages[4].Content, "TURN 1") {
		t.Fatalf("expected post-update transcript in assistant message: %q", messages[4].Content)
	}
	if !strings.Contains(messages[5].Content, "Step 1 of 3") {
		t.Fatalf("step message missing progress: %q", messages[5].Content)
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

func TestPlanReroutesRewriteToFullMode(t *testing.T) {
	repoRoot := t.TempDir()
	relPath := "LICENSE"
	content := "Original content"
	if err := os.WriteFile(filepath.Join(repoRoot, relPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := NewClient(ClientConfig{
		RepoRoot:        repoRoot,
		PatchEnabled:    true,
		StrictPatchMode: true,
	})

	rewriteJSON := `{
		"edits": [
			{
				"path": "LICENSE",
				"mode": "rewrite",
				"new_content": "New content"
			}
		]
	}`

	var mu sync.Mutex
	call := 0
	client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		prompt := renderMessagesForLogging(messages)
		// 1. Plan prompt
		if strings.Contains(prompt, "Decision: ask|patch") {
			return "Decision: patch\n{}", nil
		}
		// 2. Strict patch path selection (first attempt)
		if strings.Contains(prompt, "strict patch path selector") {
			return `{"path":"LICENSE","reason":"rewrite"}`, nil
		}
		// 3, 4, 5. Strict patch generation (first attempt retries)
		if strings.Contains(prompt, "strict patch planner") {
			// We return the same rewrite JSON which triggers validation error
			return rewriteJSON, nil
		}
		t.Fatalf("unexpected call %d with prompt: %s", call, prompt)
		return "", nil
	}

	dec, payload, err := client.Plan(context.Background(), "goal", "transcript", 1, 5, nil)
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if dec != DecisionPatch {
		t.Fatalf("expected DecisionPatch, got %q", dec)
	}
	if !strings.Contains(payload, "rewrite") {
		t.Fatalf("expected rewrite payload, got %q", payload)
	}
	if client.cfg.StrictPatchMode {
		t.Fatalf("expected StrictPatchMode to be disabled")
	}
	if !client.cfg.PatchFull {
		t.Fatalf("expected PatchFull to be enabled")
	}
}

func TestPlanProactivelyBypassesStrictPatchForRewrite(t *testing.T) {
	client := NewClient(ClientConfig{
		StrictPatchMode: true,
		PatchEnabled:    true,
	})

	rewriteJSON := `{
		"edits": [
			{
				"path": "LICENSE",
				"mode": "rewrite",
				"new_content": "New content"
			}
		]
	}`

	var mu sync.Mutex
	call := 0
	client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		prompt := renderMessagesForLogging(messages)
		// 1. Plan prompt returns rewrite directly
		if strings.Contains(prompt, "Decision: ask|patch") {
			return "Decision: patch\n" + rewriteJSON, nil
		}
		t.Fatalf("unexpected call %d with prompt: %s", call, prompt)
		return "", nil
	}

	dec, payload, err := client.Plan(context.Background(), "goal", "transcript", 1, 5, nil)
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	if dec != DecisionPatch {
		t.Fatalf("expected DecisionPatch, got %q", dec)
	}
	if !strings.Contains(payload, "rewrite") {
		t.Fatalf("expected rewrite payload, got %q", payload)
	}
	if call != 1 {
		t.Fatalf("expected only 1 call (plan prompt), got %d", call)
	}
	if client.cfg.StrictPatchMode {
		t.Fatalf("expected StrictPatchMode to be disabled")
	}
	if !client.cfg.PatchFull {
		t.Fatalf("expected PatchFull to be enabled")
	}
}
