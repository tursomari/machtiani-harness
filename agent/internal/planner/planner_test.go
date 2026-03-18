package planner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
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
	conv := conversation.New("sess-plan", "goal")
	dec, payload, err := client.Plan(context.Background(), conv, "goal", "transcript", 1, 3, nil)
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

func TestParseDecisionFinalizeImplicit(t *testing.T) {
	resp := "Finalize: HTML converted; verified with git diff."
	dec, remainder, preamble := parseDecision(resp, true)

	if dec != DecisionFinalize {
		t.Fatalf("expected DecisionFinalize, got %q", dec)
	}
	if !strings.Contains(remainder, "HTML converted") {
		t.Fatalf("expected remainder to include message, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestPlanRequiresConversation(t *testing.T) {
	client := NewClient(ClientConfig{PatchEnabled: true})
	_, _, err := client.Plan(context.Background(), nil, "goal", "transcript", 1, 3, nil)
	if err == nil {
		t.Fatalf("expected error when conversation is nil")
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
		{name: "InstructionInline", resp: "Decision: Instruction: Examine the database schema.", want: DecisionAsk, wantSubstr: "database schema"},
		{name: "QuestionVariant", resp: "Decision: question\nQuestion: What modules exist?", want: DecisionAsk, wantSubstr: "What modules"},
		{name: "MessageVariant", resp: "Decision: message\nMessage: Review the API endpoints.", want: DecisionAsk, wantSubstr: "API endpoints"},
		{name: "AskImplicit", resp: "Ask: Summarize the config loading flow.", want: DecisionAsk, wantSubstr: "config loading"},
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
	dec, reason, err := client.Plan(context.Background(), nil, "goal", "transcript", 2, 5, nil)
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
	dec, note, err := client.Plan(context.Background(), nil, "goal", "transcript", 1, 4, nil)
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
	dec, note, err := client.Plan(context.Background(), nil, "goal", "transcript", 2, 5, nil)
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
	prompt := client.planPrompt(nil, "Update licensing headers", "", 2, 5, nil)
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

func TestParseAskMenuRealisticNoShell(t *testing.T) {
	resp := strings.TrimSpace(`
Ask Mode: no-shell
Ask: How does mct-agent manage context across its orchestrated tools (file-discovery, snippet-discovery, shell-agent, patcher) and the planner, particularly in terms of session state, workspace snapshots, and LLM prompting?
`)
	mode, ask, err := parseAskMenu(resp)
	if err != nil {
		t.Fatalf("parseAskMenu error: %v", err)
	}
	if mode != AskModeNoShell {
		t.Fatalf("expected no-shell mode, got %q", mode)
	}
	if !strings.Contains(ask, "manage context across its orchestrated tools") {
		t.Fatalf("unexpected ask content: %q", ask)
	}
}

func TestParseAskMenuRealisticShell(t *testing.T) {
	resp := strings.TrimSpace(`
Ask Mode: shell
Ask: Run ` + "`" + `grep -n "Decision:" agent/internal/planner/planner.go` + "`" + ` and report the matching sections.
`)
	mode, ask, err := parseAskMenu(resp)
	if err != nil {
		t.Fatalf("parseAskMenu error: %v", err)
	}
	if mode != AskModeShell {
		t.Fatalf("expected shell mode, got %q", mode)
	}
	if !strings.Contains(ask, "grep -n \"Decision:\"") {
		t.Fatalf("unexpected ask content: %q", ask)
	}
}

func TestParseAskMenuRejectsFreeformAnswer(t *testing.T) {
	resp := strings.TrimSpace(`
## Answer

Confidence: 100% - The planner chose ask.

Confidence: 100% - The model then answered instead of asking.
`)
	_, _, err := parseAskMenu(resp)
	if err == nil {
		t.Fatalf("expected parseAskMenu to reject freeform answer")
	}
}

func TestParseAskSplitRealistic(t *testing.T) {
	resp := strings.TrimSpace(`
No-shell: Explain how session history is loaded and used during planning.
Shell: Run ` + "`" + `grep -n "Decision:" agent/internal/planner/planner.go` + "`" + ` and summarize the matching sections.
`)
	noShell, shell, err := parseAskSplit(resp)
	if err != nil {
		t.Fatalf("parseAskSplit error: %v", err)
	}
	if !strings.Contains(noShell, "session history") {
		t.Fatalf("unexpected no-shell ask: %q", noShell)
	}
	if !strings.Contains(shell, "grep -n \"Decision:\"") {
		t.Fatalf("unexpected shell ask: %q", shell)
	}
}

func TestParseAskMenuBothAllowsSingleAsk(t *testing.T) {
	resp := strings.TrimSpace(`
Ask Mode: both
Ask: Explain how session history is loaded, then run ` + "`" + `grep -n "Decision:" agent/internal/planner/planner.go` + "`" + ` and summarize the matching sections.
`)
	mode, ask, err := parseAskMenu(resp)
	if err != nil {
		t.Fatalf("parseAskMenu error: %v", err)
	}
	if mode != AskModeBoth {
		t.Fatalf("expected both mode, got %q", mode)
	}
	if strings.Contains(ask, "No-shell:") || strings.Contains(ask, "Shell:") {
		t.Fatalf("expected single ask output, got %q", ask)
	}
	if !strings.Contains(ask, "session history") || !strings.Contains(ask, "grep -n \"Decision:\"") {
		t.Fatalf("unexpected ask content: %q", ask)
	}
}

func TestParseAskMonitorResponseJSON(t *testing.T) {
	resp := "```json\n{\"has_patch_intent\":false,\"reason\":\"analysis-only\"}\n```"
	monitor, err := parseAskMonitorResponse(resp)
	if err != nil {
		t.Fatalf("parseAskMonitorResponse error: %v", err)
	}
	if monitor.HasPatchIntent {
		t.Fatalf("expected no patch intent, got true")
	}
	if monitor.Reason != "analysis-only" {
		t.Fatalf("unexpected reason: %q", monitor.Reason)
	}
}

func TestGenerateAskRetriesOnPatchIntent(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-ask", "Investigate planner flow")
	transcript := "Planner decision: background\n\n== TURN 1\nQuestion: How does mct-agent manage context across its orchestrated tools?"

	var askCalls int
	var monitorCalls int
	client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
		content := messages[len(messages)-1].Content
		if strings.Contains(content, "You are a guard that checks whether an ask mixes no-shell and shell actions.") {
			return `{"is_mixed":false,"reason":"single ask","rewrite":""}`, nil
		}
		if strings.Contains(content, "You are a guard that checks whether an ask is requesting file changes or patches.") {
			monitorCalls++
			if monitorCalls == 1 {
				return `{"has_patch_intent":true,"reason":"requests updating a template"}`, nil
			}
			return `{"has_patch_intent":false,"reason":"analysis-only"}`, nil
		}
		if strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:") {
			askCalls++
			if askCalls == 1 {
				return "Ask Mode: no-shell\nAsk: Update planner prompts in agent/internal/templates/templates/planner/plan_prompt.tpl.", nil
			}
			return "Ask Mode: no-shell\nAsk: How does mct-agent manage context across its orchestrated tools (file-discovery, snippet-discovery, shell-agent, patcher) and the planner, particularly in terms of session state, workspace snapshots, and LLM prompting?", nil
		}
		return "", nil
	}

	ask, err := client.generateAsk(context.Background(), conv, conv.CurrentGoal(), transcript, 1, 3)
	if err != nil {
		t.Fatalf("generateAsk error: %v", err)
	}
	if strings.Contains(ask, "Update planner prompts") {
		t.Fatalf("expected patch-intent ask to be rejected, got %q", ask)
	}
	if !strings.Contains(ask, "manage context across its orchestrated tools") {
		t.Fatalf("unexpected ask result: %q", ask)
	}
}

func TestGenerateAskRetriesWhenModelReturnsAnswerInsteadOfAsk(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-ask-answer-retry", "Investigate provider flow")
	transcript := "== TURN 0\nQuestion: Trace provider flow."

	var askCalls int
	client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
		content := messages[len(messages)-1].Content
		switch {
		case strings.Contains(content, "You are a guard that checks whether an ask mixes no-shell and shell actions."):
			return `{"is_mixed":false,"reason":"single ask","rewrite":""}`, nil
		case strings.Contains(content, "You are a guard that checks whether an ask is requesting file changes or patches."):
			return `{"has_patch_intent":false,"reason":"analysis-only"}`, nil
		case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
			askCalls++
			if askCalls == 1 {
				return "## Answer\n\nConfidence: 100% - Here is the final explanation.\n\nConfidence: 100% - It is complete.", nil
			}
			return "Ask Mode: no-shell\nAsk: Explain how provider events flow from CodexAppServerManager to the orchestration layer.", nil
		}
		return "", nil
	}

	ask, err := client.generateAsk(context.Background(), conv, conv.CurrentGoal(), transcript, 1, 3)
	if err != nil {
		t.Fatalf("generateAsk error: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(ask), "## Answer") {
		t.Fatalf("expected answer-like output to be rejected, got %q", ask)
	}
	if !strings.Contains(ask, "provider events flow") {
		t.Fatalf("unexpected ask output: %q", ask)
	}
	if askCalls != 2 {
		t.Fatalf("expected ask generation retry, got %d ask calls", askCalls)
	}
}

func TestGenerateAskCollapsesBothModeToSingleAsk(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-ask-split", "Investigate snippet discovery")
	transcript := "== TURN 1\nQuestion: Describe snippet-discovery prompts and tooling."

	var mixedCalls int
	client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
		content := messages[len(messages)-1].Content
		switch {
		case strings.Contains(content, "You are a guard that checks whether an ask mixes no-shell and shell actions."):
			mixedCalls++
			return `{"is_mixed":false,"reason":"already split","rewrite":""}`, nil
		case strings.Contains(content, "You are a guard that checks whether an ask is requesting file changes or patches."):
			return `{"has_patch_intent":false,"reason":"analysis-only"}`, nil
		case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
			return strings.TrimSpace(`
Ask Mode: both
Ask: Explain the snippet-discovery system prompt and output requirements, then run ` + "`" + `git diff --stat` + "`" + ` and report recent changes.
`), nil
		default:
			return "", nil
		}
	}

	ask, err := client.generateAsk(context.Background(), conv, conv.CurrentGoal(), transcript, 2, 4)
	if err != nil {
		t.Fatalf("generateAsk error: %v", err)
	}
	if strings.Contains(ask, "No-shell:") || strings.Contains(ask, "Shell:") {
		t.Fatalf("expected single ask output, got %q", ask)
	}
	if !strings.Contains(ask, "snippet-discovery system prompt") {
		t.Fatalf("unexpected combined content: %q", ask)
	}
	if !strings.Contains(ask, "git diff --stat") {
		t.Fatalf("unexpected combined content: %q", ask)
	}
	if mixedCalls != 0 {
		t.Fatalf("expected both mode to skip mixed guard, got %d calls", mixedCalls)
	}
}

func TestGenerateAskBothModeSkipsMixedGuardEvenIfSplitReturned(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-ask-both-skip-mixed", "Investigate split suppression")
	transcript := "== TURN 1\nQuestion: Explain config loading and inspect recent changes."

	var mixedCalls int
	client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
		content := messages[len(messages)-1].Content
		switch {
		case strings.Contains(content, "You are a guard that checks whether an ask mixes no-shell and shell actions."):
			mixedCalls++
			return `{"is_mixed":true,"reason":"mixed","rewrite":""}`, nil
		case strings.Contains(content, "You are a guard that checks whether an ask is requesting file changes or patches."):
			return `{"has_patch_intent":false,"reason":"analysis-only"}`, nil
		case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
			return strings.TrimSpace(`
Ask Mode: both
No-shell: Explain config loading.
Shell: Run ` + "`" + `git diff --stat` + "`" + ` to review recent changes.
`), nil
		default:
			return "", nil
		}
	}

	ask, err := client.generateAsk(context.Background(), conv, conv.CurrentGoal(), transcript, 1, 3)
	if err != nil {
		t.Fatalf("generateAsk error: %v", err)
	}
	if mixedCalls != 0 {
		t.Fatalf("expected both mode to skip mixed guard, got %d calls", mixedCalls)
	}
	if strings.Contains(ask, "No-shell:") || strings.Contains(ask, "Shell:") {
		t.Fatalf("expected split ask to collapse, got %q", ask)
	}
	if !strings.Contains(ask, "Explain config loading.") || !strings.Contains(ask, "git diff --stat") {
		t.Fatalf("unexpected combined ask: %q", ask)
	}
}

func TestGenerateAskRetriesOnMixedAsk(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-ask-mixed", "Investigate mixed ask guardrail")
	transcript := "== TURN 1\nQuestion: Explain config loading."

	const mixedReason = "combines no-shell and shell actions"
	rewrite := strings.TrimSpace(`
Ask Mode: both
No-shell: Explain config loading.
Shell: Run git diff --stat to review recent changes.
`)

	var (
		askCalls      int
		mixedCalls    int
		patchCalls    int
		guardrailSeen bool
	)
	client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
		content := messages[len(messages)-1].Content
		switch {
		case strings.Contains(content, "You are a guard that checks whether an ask mixes no-shell and shell actions."):
			mixedCalls++
			if mixedCalls == 1 {
				return fmt.Sprintf(`{"is_mixed":true,"reason":"%s","rewrite":%q}`, mixedReason, rewrite), nil
			}
			return `{"is_mixed":false,"reason":"already split","rewrite":""}`, nil
		case strings.Contains(content, "You are a guard that checks whether an ask is requesting file changes or patches."):
			patchCalls++
			return `{"has_patch_intent":false,"reason":"analysis-only"}`, nil
		case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
			askCalls++
			if askCalls == 1 {
				return "Ask Mode: no-shell\nAsk: Explain config loading and run git diff --stat to review recent changes.", nil
			}
			if strings.Contains(content, "Suggested split:") && strings.Contains(content, "No-shell: Explain config loading.") {
				guardrailSeen = true
			}
			return strings.TrimSpace(`
Ask Mode: both
No-shell: Explain config loading.
Shell: Run ` + "`" + `git diff --stat` + "`" + ` to review recent changes.
`), nil
		default:
			return "", nil
		}
	}

	ask, err := client.generateAsk(context.Background(), conv, conv.CurrentGoal(), transcript, 1, 3)
	if err != nil {
		t.Fatalf("generateAsk error: %v", err)
	}
	if !guardrailSeen {
		t.Fatalf("expected mixed guardrail with suggested split to be included")
	}
	if strings.Contains(ask, "No-shell:") || strings.Contains(ask, "Shell:") {
		t.Fatalf("expected collapsed ask output, got %q", ask)
	}
	if strings.Contains(ask, "Ask Mode:") {
		t.Fatalf("expected Ask Mode line to be stripped, got %q", ask)
	}
	if strings.Contains(ask, "Explain config loading and run git diff") {
		t.Fatalf("expected mixed ask to be retried, got %q", ask)
	}
	if !strings.Contains(ask, "Explain config loading.") || !strings.Contains(ask, "git diff --stat") {
		t.Fatalf("unexpected collapsed ask content: %q", ask)
	}
	if askCalls != 2 || mixedCalls != 1 || patchCalls != 1 {
		t.Fatalf("unexpected call counts ask=%d mixed=%d patch=%d", askCalls, mixedCalls, patchCalls)
	}
}

func TestPlanAskLoopIntegration(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-plan-ask-loop", "Investigate ask monitor flow")
	transcript := "== TURN 0\nQuestion: Summarize ask monitor guardrails."

	var (
		planCalls    int
		askCalls     int
		monitorCalls int
	)
	client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
		content := messages[len(messages)-1].Content
		if strings.Contains(content, "Decision menu") && strings.Contains(content, "This step is decision-only") {
			planCalls++
			return "Decision: ask", nil
		}
		switch {
		case strings.Contains(content, "You are a guard that checks whether an ask mixes no-shell and shell actions."):
			return `{"is_mixed":false,"reason":"single ask","rewrite":""}`, nil
		case strings.Contains(content, "You are a guard that checks whether an ask is requesting file changes or patches."):
			monitorCalls++
			return `{"has_patch_intent":false,"reason":"analysis-only"}`, nil
		case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
			askCalls++
			return "Ask Mode: no-shell\nAsk: Explain the ask monitor guardrail and retry behavior.", nil
		}
		return "", nil
	}

	dec, ask, err := client.Plan(context.Background(), conv, conv.CurrentGoal(), transcript, 1, 3, nil)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if dec != DecisionAsk {
		t.Fatalf("expected DecisionAsk, got %q", dec)
	}
	if !strings.Contains(ask, "ask monitor guardrail") {
		t.Fatalf("unexpected ask output: %q", ask)
	}
	if planCalls != 1 || askCalls != 1 || monitorCalls != 1 {
		t.Fatalf("unexpected call counts plan=%d ask=%d monitor=%d", planCalls, askCalls, monitorCalls)
	}
}

func TestGenerateAskMonitorResponseErrorsDoNotBlockAsk(t *testing.T) {
	cases := []struct {
		name       string
		monitorRes string
	}{
		{name: "malformed", monitorRes: "not-json"},
		{name: "empty", monitorRes: "   "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient(ClientConfig{})
			conv := conversation.New("sess-ask-monitor-"+tc.name, "Investigate ask monitor failures")
			transcript := "== TURN 0\nQuestion: Explain the planner ask guardrail."

			var monitorCalls int
			client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
				content := messages[len(messages)-1].Content
				switch {
				case strings.Contains(content, "You are a guard that checks whether an ask mixes no-shell and shell actions."):
					return `{"is_mixed":false,"reason":"single ask","rewrite":""}`, nil
				case strings.Contains(content, "You are a guard that checks whether an ask is requesting file changes or patches."):
					monitorCalls++
					return tc.monitorRes, nil
				case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
					return "Ask Mode: no-shell\nAsk: Explain the ask monitor guardrail and retry behavior.", nil
				}
				return "", nil
			}

			ask, err := client.generateAsk(context.Background(), conv, conv.CurrentGoal(), transcript, 1, 3)
			if err != nil {
				t.Fatalf("generateAsk error: %v", err)
			}
			if !strings.Contains(ask, "ask monitor guardrail") {
				t.Fatalf("unexpected ask output: %q", ask)
			}
			if monitorCalls != 1 {
				t.Fatalf("expected monitor to run once, got %d", monitorCalls)
			}
		})
	}
}

func TestGenerateAskRetryExhaustionReturnsLastAsk(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-ask-retry", "Investigate retry behavior")
	transcript := "== TURN 0\nQuestion: Outline retry behavior."

	var askCalls int
	var monitorCalls int
	client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
		content := messages[len(messages)-1].Content
		switch {
		case strings.Contains(content, "You are a guard that checks whether an ask mixes no-shell and shell actions."):
			return `{"is_mixed":false,"reason":"single ask","rewrite":""}`, nil
		case strings.Contains(content, "You are a guard that checks whether an ask is requesting file changes or patches."):
			monitorCalls++
			return `{"has_patch_intent":true,"reason":"requests updating a file"}`, nil
		case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
			askCalls++
			if askCalls == 1 {
				return "Ask Mode: no-shell\nAsk: Update docs in path/to/file.md.", nil
			}
			return "Ask Mode: no-shell\nAsk: Update docs in path/to/other.md.", nil
		}
		return "", nil
	}

	ask, err := client.generateAsk(context.Background(), conv, conv.CurrentGoal(), transcript, 1, 3)
	if err != nil {
		t.Fatalf("generateAsk error: %v", err)
	}
	if ask != "Update docs in path/to/other.md." {
		t.Fatalf("expected last ask after retries, got %q", ask)
	}
	expectedCalls := askGuardMaxRetries + 1
	if askCalls != expectedCalls || monitorCalls != expectedCalls {
		t.Fatalf("expected %d ask/monitor calls, got ask=%d monitor=%d", expectedCalls, askCalls, monitorCalls)
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
	prompt := c.planPrompt(nil, "goal text", "transcript text", 1, 4, nil)
	want := []string{"Decision: ask|patch", "Use the conversation above", "Step 1 of 4"}
	for _, w := range want {
		if !contains(prompt, w) {
			t.Fatalf("plan prompt missing %q:\n%s", w, prompt)
		}
	}
	if contains(prompt, "Transcript:") || contains(prompt, "transcript text") {
		t.Fatalf("plan prompt should rely on conversation projection instead of transcript text:\n%s", prompt)
	}
}

func TestPlanPromptAllowsFinalizeWhenPatchPlanComplete(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true, PatchEnabled: true})
	plan := &PatchPlan{Items: []PatchPlanItem{{Description: "update README", Complete: true}}}
	prompt := c.planPrompt(nil, "goal text", "transcript text", 1, 4, plan)
	if !strings.Contains(prompt, "Decision: ask|patch|finalize") {
		t.Fatalf("plan prompt should expose finalize when patch plan is complete:\n%s", prompt)
	}
}

func TestPlanPromptBlocksFinalizeWhenPatchPlanIncomplete(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true, PatchEnabled: true})
	plan := &PatchPlan{Items: []PatchPlanItem{{Description: "update README", Complete: false}}}
	prompt := c.planPrompt(nil, "goal text", "transcript text", 1, 4, plan)
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
	prompt := client.planPrompt(nil, "goal text", "transcript text", 1, 4, plan)
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
	prompt := c.planPrompt(nil, "goal", "", 2, 5, nil)
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
	prompt := c.planPrompt(nil, "goal", "transcript", 3, 6, nil)

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
	prompt := c.planPrompt(nil, "goal", "transcript", 2, 4, nil)
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

func TestPlanSystemPromptOmitsTranscript(t *testing.T) {
	client := NewClient(ClientConfig{
		Prompts: &llm.PlannerPromptsConfig{
			SystemTemplate:   "Shell system prompt",
			PlanSystemPrompt: "System prompt: {{.Transcript}}",
			PlanPrompt:       "Plan prompt: {{.Transcript}}",
		},
	})
	conv := conversation.New("sess-system", "Finish docs")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	prompt := client.planSystemPrompt(conv, "Finish docs", 1, 3, nil)
	if strings.Contains(prompt, "Shell system prompt") {
		t.Fatalf("system prompt should ignore shell template, got %q", prompt)
	}
	if strings.Contains(prompt, "Plan prompt") {
		t.Fatalf("expected system template, got %q", prompt)
	}
	if strings.Contains(prompt, "Question: start") {
		t.Fatalf("system prompt should omit transcript body, got %q", prompt)
	}
}

func TestPlanSystemPromptOmitsFullFileTagGuidance(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-system", "Finish docs")
	prompt := client.planSystemPrompt(conv, "Finish docs", 1, 3, nil)
	if strings.Contains(prompt, "<full_file") {
		t.Fatalf("expected system prompt to omit full_file tag guidance, got %q", prompt)
	}
	if strings.Contains(prompt, "Decision menu") {
		t.Fatalf("expected stable system prompt to omit decision menu, got %q", prompt)
	}
	if !strings.Contains(prompt, "Always obey the exact output format requested by the latest user message.") {
		t.Fatalf("expected stable planner identity guidance, got %q", prompt)
	}
}

func TestPlanSystemPromptIncludesPlannerOverlay(t *testing.T) {
	client := NewClient(ClientConfig{PlannerOverlay: "Focus on security review and threat modeling."})
	conv := conversation.New("sess-system-overlay", "Finish docs")
	prompt := client.planSystemPrompt(conv, "Finish docs", 1, 3, nil)
	if !strings.Contains(prompt, "Additional task-specific planner guidance:") {
		t.Fatalf("expected planner overlay heading, got %q", prompt)
	}
	if !strings.Contains(prompt, "Focus on security review and threat modeling.") {
		t.Fatalf("expected planner overlay body, got %q", prompt)
	}
}

func TestBuildAskRequestOmitsTranscript(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-ask-request", "Investigate planner flow")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	request := client.buildAskRequest(conv, conv.CurrentGoal(), 2, 4)
	if strings.Contains(request, "Transcript:") {
		t.Fatalf("ask request should omit transcript label, got %q", request)
	}
	if !strings.Contains(request, "Use the prior planner conversation for context") {
		t.Fatalf("ask request should point the model to the shared conversation, got %q", request)
	}
}

func TestAskPromptPrefersExplanationsOverFullFiles(t *testing.T) {
	client := NewClient(ClientConfig{})
	prompt := client.askPrompt("Explain the repo structure.", "")
	checks := []string{
		"do not ask for full files or large verbatim code snippets",
		"The `shell-agent` must spend output tokens to answer",
		"Explain what sections the README.md contains and which contain HTML.",
	}
	for _, want := range checks {
		if !strings.Contains(prompt, want) {
			t.Fatalf("ask prompt missing %q in %q", want, prompt)
		}
	}
}

func TestPlannerHelperMessagesSharePlanPrefix(t *testing.T) {
	client := NewClient(ClientConfig{PatchEnabled: true})
	conv := conversation.New("sess-prefix", "Finish docs")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", "Answer: done", map[string]any{"type": "answer", "turn": 1})

	planMessages := client.buildPlanMessages(conv, conv.CurrentGoal(), 2, 4, nil)
	askMessages := client.buildPlannerTaskMessages(conv, conv.CurrentGoal(), 2, 4, nil, client.askPrompt(client.buildAskRequest(conv, conv.CurrentGoal(), 2, 4), ""))
	monitorMessages := client.buildPlannerTaskMessages(conv, conv.CurrentGoal(), 2, 4, nil, client.askMonitorPrompt("Explain config loading."))
	mixedMessages := client.buildPlannerTaskMessages(conv, conv.CurrentGoal(), 2, 4, nil, client.askMixedMonitorPrompt("Explain config loading and run git diff --stat."))

	messageSets := [][]llm.Message{askMessages, monitorMessages, mixedMessages}
	for _, messages := range messageSets {
		if len(messages) != len(planMessages) {
			t.Fatalf("expected matching message counts, got plan=%d other=%d", len(planMessages), len(messages))
		}
		for i := 0; i < len(planMessages)-1; i++ {
			if planMessages[i].Role != messages[i].Role {
				t.Fatalf("message %d role mismatch: %q vs %q", i, planMessages[i].Role, messages[i].Role)
			}
			if planMessages[i].Content != messages[i].Content {
				t.Fatalf("message %d content mismatch: %q vs %q", i, planMessages[i].Content, messages[i].Content)
			}
		}
		if planMessages[len(planMessages)-1].Content == messages[len(messages)-1].Content {
			t.Fatalf("expected final user prompt to differ across planner tasks")
		}
	}
}

func TestPlannerOverlayStaysInSharedSystemPrompt(t *testing.T) {
	client := NewClient(ClientConfig{PatchEnabled: true, PlannerOverlay: "Prioritize migration safety checks."})
	conv := conversation.New("sess-overlay-prefix", "Finish docs")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", "Answer: done", map[string]any{"type": "answer", "turn": 1})

	planMessages := client.buildPlanMessages(conv, conv.CurrentGoal(), 2, 4, nil)
	taskMessages := [][]llm.Message{
		client.buildPlannerTaskMessages(conv, conv.CurrentGoal(), 2, 4, nil, client.askPrompt(client.buildAskRequest(conv, conv.CurrentGoal(), 2, 4), "")),
		client.buildPlannerTaskMessages(conv, conv.CurrentGoal(), 2, 4, nil, client.askMonitorPrompt("Explain config loading.")),
		client.buildPlannerTaskMessages(conv, conv.CurrentGoal(), 2, 4, nil, client.askMixedMonitorPrompt("Explain config loading and run git diff --stat.")),
	}

	if len(planMessages) == 0 || planMessages[0].Role != "system" {
		t.Fatalf("expected plan messages to start with system prompt, got %#v", planMessages)
	}
	if !strings.Contains(planMessages[0].Content, "Prioritize migration safety checks.") {
		t.Fatalf("expected planner overlay in shared system prompt, got %q", planMessages[0].Content)
	}
	if strings.Contains(planMessages[len(planMessages)-1].Content, "Prioritize migration safety checks.") {
		t.Fatalf("planner overlay should not appear in final user plan prompt, got %q", planMessages[len(planMessages)-1].Content)
	}

	for _, messages := range taskMessages {
		if messages[0].Content != planMessages[0].Content {
			t.Fatalf("expected shared system prompt prefix, got %q want %q", messages[0].Content, planMessages[0].Content)
		}
		if strings.Contains(messages[len(messages)-1].Content, "Prioritize migration safety checks.") {
			t.Fatalf("planner overlay should stay out of final user task prompt, got %q", messages[len(messages)-1].Content)
		}
	}
}

func TestBuildPlanMessagesNoGoalUpdate(t *testing.T) {
	client := NewClient(ClientConfig{PatchEnabled: true})
	conv := conversation.New("sess-1", "Finish docs")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", "Answer: done", map[string]any{"type": "answer", "turn": 1})
	messages := client.buildPlanMessages(conv, "Finish docs", 2, 4, nil)

	wantRoles := []string{"system", "user", "assistant", "assistant", "user"}
	if len(messages) != len(wantRoles) {
		t.Fatalf("expected %d messages, got %d", len(wantRoles), len(messages))
	}
	for i, role := range wantRoles {
		if messages[i].Role != role {
			t.Fatalf("message %d role = %q, want %q", i, messages[i].Role, role)
		}
	}
	if !strings.Contains(messages[0].Content, "Always obey the exact output format requested by the latest user message.") {
		t.Fatalf("system prompt missing stable planner guidance: %q", messages[0].Content)
	}
	if messages[1].Content != "Finish docs" {
		t.Fatalf("unexpected goal message: %q", messages[1].Content)
	}
	if messages[2].Content != "Question: start" {
		t.Fatalf("unexpected ask message: %q", messages[2].Content)
	}
	if messages[3].Content != "Answer: done" {
		t.Fatalf("unexpected answer message: %q", messages[3].Content)
	}
	if !strings.Contains(messages[4].Content, "Step 2 of 4") {
		t.Fatalf("step message missing progress: %q", messages[4].Content)
	}
	if !strings.Contains(messages[4].Content, "Decision: ask|patch") {
		t.Fatalf("final planner message missing decision schema: %q", messages[4].Content)
	}
}

func TestBuildPlanMessagesUsesConversation(t *testing.T) {
	client := NewClient(ClientConfig{PatchEnabled: true})
	conv := conversation.New("sess-2", "Finish docs")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	messages := client.buildPlanMessages(conv, "Finish docs", 2, 4, nil)
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}
	if messages[2].Role != "assistant" {
		t.Fatalf("expected assistant transcript message, got %q", messages[2].Role)
	}
	if messages[2].Content != "Question: start" {
		t.Fatalf("unexpected ask content: %q", messages[2].Content)
	}
}

func TestBuildPlanMessagesInsertsCacheAnchorMetadata(t *testing.T) {
	model := llm.ResolvedModel{
		CacheKeyName:          "cache_control",
		CacheControl:          map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold: 1,
		CacheLookbackOffset:   1,
	}
	client := NewClient(ClientConfig{Model: model})
	conv := conversation.New("sess-anchor", "Goal")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	client.buildPlanMessages(conv, "Goal", 2, 4, nil)

	anchors := cacheAnchorIndexes(conv)
	if len(anchors) != 1 {
		t.Fatalf("expected 1 cache anchor, got %d", len(anchors))
	}
	metadata := conv.Messages[anchors[0]].Metadata
	if cacheAnchorInt(metadata, llm.CacheAnchorSequenceMetadataKey) != 1 {
		t.Fatalf("expected anchor_seq 1, got %v", metadata[llm.CacheAnchorSequenceMetadataKey])
	}
	if cacheAnchorInt(metadata, llm.CacheAnchorTurnMetadataKey) != 2 {
		t.Fatalf("expected anchor_turn 2, got %v", metadata[llm.CacheAnchorTurnMetadataKey])
	}
	if cacheAnchorInt(metadata, llm.CacheAnchorTokensMetadataKey) <= 0 {
		t.Fatalf("expected anchor_tokens to be set, got %v", metadata[llm.CacheAnchorTokensMetadataKey])
	}
	if cacheAnchorBool(metadata, llm.CacheAnchorRetiredMetadataKey) {
		t.Fatalf("expected anchor to be active")
	}
}

func TestBuildPlanMessagesKeepsAnchorStable(t *testing.T) {
	model := llm.ResolvedModel{
		CacheKeyName:          "cache_control",
		CacheControl:          map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold: 1,
		CacheReanchorTokens:   1000,
		CacheReanchorMessages: 1000,
	}
	client := NewClient(ClientConfig{Model: model})
	conv := conversation.New("sess-stable", "Goal")
	conv.AddMessage("user", llm.CacheAnchorMarkerText, map[string]any{"type": "cache_anchor", llm.CacheAnchorSequenceMetadataKey: 1})
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	client.buildPlanMessages(conv, "Goal", 2, 4, nil)

	anchors := cacheAnchorIndexes(conv)
	if len(anchors) != 1 {
		t.Fatalf("expected 1 cache anchor, got %d", len(anchors))
	}
	if cacheAnchorBool(conv.Messages[anchors[0]].Metadata, llm.CacheAnchorRetiredMetadataKey) {
		t.Fatalf("expected anchor to remain active")
	}
}

func TestBuildPlanMessagesRotatesCacheAnchor(t *testing.T) {
	model := llm.ResolvedModel{
		CacheKeyName:                 "cache_control",
		CacheControl:                 map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold:        1,
		CacheReanchorTokens:          1,
		CacheReanchorMinCachedTokens: 5,
	}
	client := NewClient(ClientConfig{Model: model})
	conv := conversation.New("sess-rotate", "Goal")
	conv.AddMessage("user", llm.CacheAnchorMarkerText, map[string]any{
		"type":                                 "cache_anchor",
		llm.CacheAnchorSequenceMetadataKey:     1,
		llm.CacheAnchorCachedTokensMetadataKey: 5,
	})
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	client.buildPlanMessages(conv, "Goal", 3, 6, nil)

	anchors := cacheAnchorIndexes(conv)
	if len(anchors) != 2 {
		t.Fatalf("expected 2 cache anchors, got %d", len(anchors))
	}
	first := conv.Messages[anchors[0]].Metadata
	second := conv.Messages[anchors[1]].Metadata
	if !cacheAnchorBool(first, llm.CacheAnchorRetiredMetadataKey) {
		t.Fatalf("expected original anchor to be retired")
	}
	if cacheAnchorInt(second, llm.CacheAnchorSequenceMetadataKey) != 2 {
		t.Fatalf("expected new anchor_seq 2, got %v", second[llm.CacheAnchorSequenceMetadataKey])
	}
}

func TestFinalizePromptOmitsTranscript(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true})
	prompt := c.finalizePrompt("goal text", "transcript text")
	if !contains(prompt, "Goal:") || !contains(prompt, "goal text") {
		t.Fatalf("finalize prompt missing goal:\n%s", prompt)
	}
	if contains(prompt, "Transcript:") || contains(prompt, "transcript text") {
		t.Fatalf("finalize prompt should omit transcript:\n%s", prompt)
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

func cacheAnchorIndexes(conv *conversation.Conversation) []int {
	if conv == nil {
		return nil
	}
	indexes := []int{}
	for i, msg := range conv.Messages {
		if msg.Metadata == nil {
			continue
		}
		val, ok := msg.Metadata["type"].(string)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(val), "cache_anchor") {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func cacheAnchorInt(metadata map[string]any, key string) int {
	if metadata == nil {
		return 0
	}
	if raw, ok := metadata[key]; ok {
		switch v := raw.(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func cacheAnchorBool(metadata map[string]any, key string) bool {
	if metadata == nil {
		return false
	}
	if raw, ok := metadata[key]; ok {
		switch v := raw.(type) {
		case bool:
			return v
		case string:
			if parsed, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
				return parsed
			}
		case int:
			return v != 0
		case int64:
			return v != 0
		case float64:
			return v != 0
		}
	}
	return false
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

	conv := conversation.New("sess-plan-rewrite", "goal")
	dec, payload, err := client.Plan(context.Background(), conv, "goal", "transcript", 1, 5, nil)
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

	conv := conversation.New("sess-plan-strict", "goal")
	dec, payload, err := client.Plan(context.Background(), conv, "goal", "transcript", 1, 5, nil)
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
