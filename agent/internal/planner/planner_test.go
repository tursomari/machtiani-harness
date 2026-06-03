package planner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

const answerTheUserPrompt = "[answer_the_user] Reply to the user now based on the conversation so far.\n\nAnswer for the user's current need. Do not make further work requests. Use relevant prior `work_result` messages when helpful. If the latest user turn calls for a narrow or conversational reply, answer naturally instead of re-summarizing the whole session. If the latest user turn asks for a summary or wrap-up, provide it. If important uncertainty remains, mention it briefly."

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	path := filepath.Join(repoRoot, filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestParseDecisionAskWorker(t *testing.T) {
	resp := "Decision: ask\nQuestion: What is the structure of the main module?"
	dec, remainder, preamble := parseDecision(resp)

	if dec != DecisionAskWorker {
		t.Fatalf("expected DecisionAskWorker, got %q", dec)
	}
	if !strings.Contains(remainder, "What is the structure") {
		t.Fatalf("expected question in remainder, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}




func TestParseDecisionAnswerUser(t *testing.T) {
	resp := "Decision: finalize"
	dec, remainder, preamble := parseDecision(resp)

	if dec != DecisionAnswerUser {
		t.Fatalf("expected DecisionAnswerUser, got %q", dec)
	}
	if remainder != "" {
		t.Fatalf("expected empty remainder for finalize, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestParseDecisionAnswerUserImplicit(t *testing.T) {
	resp := "Finalize: HTML converted; verified with git diff."
	dec, remainder, preamble := parseDecision(resp)

	if dec != DecisionAnswerUser {
		t.Fatalf("expected DecisionAnswerUser, got %q", dec)
	}
	if !strings.Contains(remainder, "HTML converted") {
		t.Fatalf("expected remainder to include message, got %q", remainder)
	}
	if preamble != "" {
		t.Fatalf("expected empty preamble, got %q", preamble)
	}
}

func TestPlanRequiresConversation(t *testing.T) {
	client := NewClient(ClientConfig{})
	_, _, err := client.Plan(context.Background(), nil, "goal", "transcript", 1, 3)
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
		{name: "InstructionVariant", resp: "Decision: instruction\nInstruction: Examine the database schema.", want: DecisionAskWorker, wantSubstr: "database schema"},
		{name: "InstructionInline", resp: "Decision: Instruction: Examine the database schema.", want: DecisionAskWorker, wantSubstr: "database schema"},
		{name: "QuestionVariant", resp: "Decision: question\nQuestion: What modules exist?", want: DecisionAskWorker, wantSubstr: "What modules"},
		{name: "MessageVariant", resp: "Decision: message\nMessage: Review the API endpoints.", want: DecisionAskWorker, wantSubstr: "API endpoints"},
		{name: "AskImplicit", resp: "Ask: Summarize the config loading flow.", want: DecisionAskWorker, wantSubstr: "config loading"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, remainder, preamble := parseDecision(tt.resp)
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
		{name: "UpperAsk", resp: "Decision: ASK\nQuestion: text", want: DecisionAskWorker},
		{name: "UpperFinalize", resp: "Decision: FINALIZE", want: DecisionAnswerUser},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, _, _ := parseDecision(tt.resp)
			if dec != tt.want {
				t.Fatalf("parseDecision(%q) = %q, want %q", tt.resp, dec, tt.want)
			}
		})
	}
}

func TestParseDecisionMissingDecisionLine(t *testing.T) {
	resp := "Some random text\nNo decision here"
	dec, remainder, preamble := parseDecision(resp)

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
	dec, remainder, preamble := parseDecision("")

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
	dec, remainder, preamble := parseDecision(resp)

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
	dec, remainder, preamble := parseDecision(resp)

	if dec != DecisionAskWorker {
		t.Fatalf("expected DecisionAskWorker, got %q", dec)
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
	dec, remainder, preamble := parseDecision(resp)

	if dec != DecisionAskWorker {
		t.Fatalf("expected DecisionAskWorker with short preamble allowed, got %q", dec)
	}
	if preamble != "Note: quick recap." {
		t.Fatalf("expected preamble to be returned, got %q", preamble)
	}
	if !strings.Contains(remainder, "safeguards changes") {
		t.Fatalf("expected remainder to include question, got %q", remainder)
	}
}

func TestParseAskMenuRealisticNoShell(t *testing.T) {
	resp := strings.TrimSpace(`
Ask Mode: no-shell
Ask: How does mct-agent manage context across its orchestrated tools (file-discovery, snippet-discovery, shell-agent) and the planner, particularly in terms of session state, workspace snapshots, and LLM prompting?
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

func TestParseAskMenuBothNormalizesToShell(t *testing.T) {
	resp := strings.TrimSpace(`
Ask Mode: both
Ask: Explain how session history is loaded, then run ` + "`" + `grep -n "Decision:" agent/internal/planner/planner.go` + "`" + ` and summarize the matching sections.
`)
	mode, ask, err := parseAskMenu(resp)
	if err != nil {
		t.Fatalf("parseAskMenu error: %v", err)
	}
	if mode != AskModeShell {
		t.Fatalf("expected legacy both mode to normalize to shell, got %q", mode)
	}
	if strings.Contains(ask, "No-shell:") || strings.Contains(ask, "Shell:") {
		t.Fatalf("expected single ask output, got %q", ask)
	}
	if !strings.Contains(ask, "session history") || !strings.Contains(ask, "grep -n \"Decision:\"") {
		t.Fatalf("unexpected ask content: %q", ask)
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
		case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
			askCalls++
			if askCalls == 1 {
				return "## Answer\n\nConfidence: 100% - Here is the final explanation.\n\nConfidence: 100% - It is complete.", nil
			}
			return "Ask Mode: no-shell\nAsk: Explain how provider events flow from CodexAppServerManager to the orchestration layer.", nil
		}
		return "", nil
	}

	ask, err := client.generateAsk(context.Background(), conv, conv.OriginalGoal, transcript, 1, 3)
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
		case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
			return strings.TrimSpace(`
Ask Mode: both
Ask: Explain the snippet-discovery system prompt and output requirements, then run ` + "`" + `git diff --stat` + "`" + ` and report recent changes.
`), nil
		default:
			return "", nil
		}
	}

	ask, err := client.generateAsk(context.Background(), conv, conv.OriginalGoal, transcript, 2, 4)
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

	ask, err := client.generateAsk(context.Background(), conv, conv.OriginalGoal, transcript, 1, 3)
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
		case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
			askCalls++
			if askCalls == 1 {
				return "Ask Mode: no-shell\nAsk: Explain config loading and run git diff --stat to review recent changes.", nil
			}
			if strings.Contains(content, "Suggested shell ask:") && strings.Contains(content, "Explain config loading.") && strings.Contains(content, "git diff --stat") {
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

	ask, err := client.generateAsk(context.Background(), conv, conv.OriginalGoal, transcript, 1, 3)
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
	if askCalls != 2 || mixedCalls != 1 {
		t.Fatalf("unexpected call counts ask=%d mixed=%d", askCalls, mixedCalls)
	}
}

func TestPlanAskLoopIntegration(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-plan-ask-loop", "Investigate ask monitor flow")
	transcript := "== TURN 0\nQuestion: Summarize ask monitor guardrails."

	var (
		planCalls    int
		askCalls     int
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
		case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
			askCalls++
			return "Ask Mode: no-shell\nAsk: Explain the ask monitor guardrail and retry behavior.", nil
		}
		return "", nil
	}

	dec, ask, err := client.Plan(context.Background(), conv, conv.OriginalGoal, transcript, 1, 3)
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if dec != DecisionAskWorker {
		t.Fatalf("expected DecisionAskWorker, got %q", dec)
	}
	if !strings.Contains(ask, "ask monitor guardrail") {
		t.Fatalf("unexpected ask output: %q", ask)
	}
	if planCalls != 1 || askCalls != 1 {
		t.Fatalf("unexpected call counts plan=%d ask=%d", planCalls, askCalls)
	}
}

func TestParseAskMixedMonitorResponseUsesTolerantJSONExtraction(t *testing.T) {
	t.Run("fenced leading trailing", func(t *testing.T) {
		resp := "Here you go\n```json\n{\"is_mixed\": true, \"reason\": \" combines explanation and commands \", \"rewrite\": \"No-shell: Explain it.\\nShell: Run git diff --stat.\"}\n```\nthanks"
		got, err := parseAskMixedMonitorResponse(resp)
		if err != nil {
			t.Fatalf("parseAskMixedMonitorResponse error: %v", err)
		}
		if !got.IsMixed {
			t.Fatalf("expected is_mixed=true")
		}
		if got.Reason != "combines explanation and commands" {
			t.Fatalf("unexpected reason: %q", got.Reason)
		}
		if !strings.HasPrefix(got.Rewrite, "No-shell:") {
			t.Fatalf("unexpected rewrite: %q", got.Rewrite)
		}
	})

	t.Run("braces inside strings", func(t *testing.T) {
		resp := `{"is_mixed":false,"reason":"already split with {labels}","rewrite":""}`
		got, err := parseAskMixedMonitorResponse(resp)
		if err != nil {
			t.Fatalf("parseAskMixedMonitorResponse error: %v", err)
		}
		if got.IsMixed {
			t.Fatalf("expected is_mixed=false")
		}
		if got.Reason != "already split with {labels}" {
			t.Fatalf("unexpected reason: %q", got.Reason)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		if _, err := parseAskMixedMonitorResponse(`{"is_mixed":`); err == nil {
			t.Fatalf("expected invalid JSON error")
		}
	})
}

func TestAnalyzeUserDirectedAsk(t *testing.T) {
	t.Run("monitor false", func(t *testing.T) {
		client := NewClient(ClientConfig{InternetAccess: true})
		conv := conversation.New("sess-user-directed-false", "Investigate issue")
		client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
			content := messages[len(messages)-1].Content
			if strings.Contains(content, "You are a guard for user-directed asks.") {
				if !strings.Contains(content, "Internet Access: true") {
					t.Fatalf("expected Internet Access capability in monitor prompt, got %q", content)
				}
				return `{"is_user_directed":false,"reason":"implementation choice"}`, nil
			}
			t.Fatalf("unexpected prompt: %s", content)
			return "", nil
		}
		got, err := client.AnalyzeUserDirectedAsk(context.Background(), conv, conv.OriginalGoal, "Should I inspect logs first?", 1, 4)
		if err != nil {
			t.Fatalf("AnalyzeUserDirectedAsk error: %v", err)
		}
		if got.ShouldSuspend {
			t.Fatalf("expected no suspension, got %+v", got)
		}
	})

	t.Run("purifier success", func(t *testing.T) {
		client := NewClient(ClientConfig{})
		conv := conversation.New("sess-user-directed-true", "Investigate issue")
		client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
			content := messages[len(messages)-1].Content
			switch {
			case strings.Contains(content, "You are a guard for user-directed asks."):
				return "monitor result\n```json\n{\"is_user_directed\":true,\"reason\":\"asks for the preferred tradeoff\"}\n```", nil
			case strings.Contains(content, "You are a purifier for flagged user-directed asks."):
				return "```json\n{\"should_suspend\":true,\"purified_question\":\"Do you want the safer fix, or the faster fix?\",\"context\":\"\",\"reason\":\"extracted the tradeoff\"}\n```", nil
			default:
				t.Fatalf("unexpected prompt: %s", content)
				return "", nil
			}
		}
		got, err := client.AnalyzeUserDirectedAsk(context.Background(), conv, conv.OriginalGoal, "Do you want the safer fix or the faster fix? I can inspect more logs too.", 1, 4)
		if err != nil {
			t.Fatalf("AnalyzeUserDirectedAsk error: %v", err)
		}
		if !got.ShouldSuspend {
			t.Fatalf("expected suspension, got %+v", got)
		}
		if got.Question != "Do you want the safer fix, or the faster fix?" {
			t.Fatalf("unexpected purified question: %q", got.Question)
		}
	})

	t.Run("purifier decline with empty reason", func(t *testing.T) {
		client := NewClient(ClientConfig{})
		conv := conversation.New("sess-user-directed-decline", "Investigate issue")
		client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
			content := messages[len(messages)-1].Content
			switch {
			case strings.Contains(content, "You are a guard for user-directed asks."):
				return `{"is_user_directed":true,"reason":""}`, nil
			case strings.Contains(content, "You are a purifier for flagged user-directed asks."):
				if !strings.Contains(content, "Reason: ") {
					t.Fatalf("expected purifier prompt to include empty reason field")
				}
				return `{"should_suspend":false,"purified_question":"","context":"","reason":"no clean user-owned question"}`, nil
			default:
				t.Fatalf("unexpected prompt: %s", content)
				return "", nil
			}
		}
		got, err := client.AnalyzeUserDirectedAsk(context.Background(), conv, conv.OriginalGoal, "Explain the auth flow and run git diff --stat.", 1, 4)
		if err != nil {
			t.Fatalf("AnalyzeUserDirectedAsk error: %v", err)
		}
		if got.ShouldSuspend {
			t.Fatalf("expected purifier decline, got %+v", got)
		}
	})

	t.Run("monitor format retry", func(t *testing.T) {
		client := NewClient(ClientConfig{})
		conv := conversation.New("sess-user-directed-retry-monitor", "Fix the flaky test without changing behavior")
		callCount := 0
		client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
			callCount++
			switch callCount {
			case 1:
				return "I think this is user-directed because the user must choose a tradeoff.", nil
			case 2:
				if got := messages[len(messages)-1].Content; got != userDirectedJSONFormatRetryPrompt() {
					t.Fatalf("expected retry prompt %q, got %q", userDirectedJSONFormatRetryPrompt(), got)
				}
				return `{"is_user_directed":true,"reason":"user must choose the tradeoff"}`, nil
			case 3:
				return `{"should_suspend":true,"purified_question":"Do you want the safer fix or the faster fix?","context":"","reason":"user must decide the tradeoff"}`, nil
			default:
				t.Fatalf("unexpected chat call %d", callCount)
				return "", nil
			}
		}

		got, err := client.AnalyzeUserDirectedAsk(context.Background(), conv, conv.OriginalGoal, "Do you want the safer fix or the faster fix?", 1, 4)
		if err != nil {
			t.Fatalf("AnalyzeUserDirectedAsk error: %v", err)
		}
		if !got.ShouldSuspend {
			t.Fatalf("expected suspension outcome, got %#v", got)
		}
		if got.Question != "Do you want the safer fix or the faster fix?" {
			t.Fatalf("unexpected question %q", got.Question)
		}
		if callCount != 3 {
			t.Fatalf("expected 3 chat calls, got %d", callCount)
		}
	})

	t.Run("purifier format retry", func(t *testing.T) {
		client := NewClient(ClientConfig{})
		conv := conversation.New("sess-user-directed-retry-purifier", "Fix the flaky test without changing behavior")
		callCount := 0
		client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
			callCount++
			switch callCount {
			case 1:
				return `{"is_user_directed":true,"reason":"user must choose the tradeoff"}`, nil
			case 2:
				return "The ask should suspend and be rewritten as a clean user question.", nil
			case 3:
				if got := messages[len(messages)-1].Content; got != userDirectedJSONFormatRetryPrompt() {
					t.Fatalf("expected retry prompt %q, got %q", userDirectedJSONFormatRetryPrompt(), got)
				}
				return `{"should_suspend":true,"purified_question":"Do you want the safer fix or the faster fix?","context":"","reason":"user must decide the tradeoff"}`, nil
			default:
				t.Fatalf("unexpected chat call %d", callCount)
				return "", nil
			}
		}

		got, err := client.AnalyzeUserDirectedAsk(context.Background(), conv, conv.OriginalGoal, "Do you want the safer fix or the faster fix?", 1, 4)
		if err != nil {
			t.Fatalf("AnalyzeUserDirectedAsk error: %v", err)
		}
		if !got.ShouldSuspend {
			t.Fatalf("expected suspension outcome, got %#v", got)
		}
		if got.Question != "Do you want the safer fix or the faster fix?" {
			t.Fatalf("unexpected question %q", got.Question)
		}
		if callCount != 3 {
			t.Fatalf("expected 3 chat calls, got %d", callCount)
		}
	})
}

func TestParseAskUserDirectedResponsesUseTolerantJSONExtraction(t *testing.T) {
	monitor, err := parseAskUserDirectedMonitorResponse("Monitor\n```json\n{\"is_user_directed\":true,\"reason\":\" user must choose goal \"}\n```\nextra")
	if err != nil {
		t.Fatalf("parseAskUserDirectedMonitorResponse error: %v", err)
	}
	if !monitor.IsUserDirected || monitor.Reason != "user must choose goal" {
		t.Fatalf("unexpected monitor result: %+v", monitor)
	}

	purifier, err := parseAskUserDirectedPurifierResponse("```json\n{\"should_suspend\":true,\"purified_question\":\"Is your goal A or B?\",\"context\":\"\",\"reason\":\" extracted \"}\n``` trailing")
	if err != nil {
		t.Fatalf("parseAskUserDirectedPurifierResponse error: %v", err)
	}
	if !purifier.ShouldSuspend || purifier.PurifiedQuestion != "Is your goal A or B?" || purifier.Reason != "extracted" {
		t.Fatalf("unexpected purifier result: %+v", purifier)
	}
}

func TestGenerateAskRetryExhaustionReturnsLastAsk(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-ask-retry", "Investigate retry behavior")
	transcript := "== TURN 0\nQuestion: Outline retry behavior."

	var askCalls int
	var mixedCalls int
	client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
		content := messages[len(messages)-1].Content
		switch {
		case strings.Contains(content, "You are a guard that checks whether an ask mixes no-shell and shell actions."):
			mixedCalls++
			return `{"is_mixed":true,"reason":"mixed ask","rewrite":""}`, nil
		case strings.Contains(content, "Ask Mode:") && strings.Contains(content, "Ask request:"):
			askCalls++
			if askCalls == 1 {
				return "Ask Mode: no-shell\nAsk: Explain config loading and run git diff --stat.", nil
			}
			return "Ask Mode: no-shell\nAsk: Summarize retry behavior using evidence from the repo.", nil
		}
		return "", nil
	}

	ask, err := client.generateAsk(context.Background(), conv, conv.OriginalGoal, transcript, 1, 3)
	if err != nil {
		t.Fatalf("generateAsk error: %v", err)
	}
	if ask != "Summarize retry behavior using evidence from the repo." {
		t.Fatalf("expected last ask after retries, got %q", ask)
	}
	expectedCalls := askGuardMaxRetries + 1
	if askCalls != expectedCalls || mixedCalls != expectedCalls {
		t.Fatalf("expected %d ask/mixed calls, got ask=%d mixed=%d", expectedCalls, askCalls, mixedCalls)
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
	prompt := client.planSystemPrompt(conv, "Finish docs", 1, 3)
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
	prompt := client.planSystemPrompt(conv, "Finish docs", 1, 3)
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

func TestPlanSystemPromptDefinesAnswerTheUserContract(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-answer-the-user", "Finish docs")
	prompt := client.planSystemPrompt(conv, "Finish docs", 1, 3)
	checks := []string{
		"A message tagged `answer_the_user` means produce the assistant's actual user-facing reply now, grounded in the conversation so far.",
		"<ANSWER_THE_USER_BEHAVIOR>",
		"When the latest message is tagged `answer_the_user`, produce the assistant's actual user-facing reply now.",
		"Prefer a natural conversational reply when the latest user turn is narrow, incremental, or conversational.",
		"If the latest real user turn asks for a summary, wrap-up, or overall conclusion, provide that broader response.",
		"Do not make further `work_request` messages in this step.",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Fatalf("plan system prompt missing %q\n%s", check, prompt)
		}
	}
}

func TestPlanSystemPromptIncludesPlannerOverlay(t *testing.T) {
	client := NewClient(ClientConfig{PlannerOverlay: "Focus on security review and threat modeling."})
	conv := conversation.New("sess-system-overlay", "Finish docs")
	prompt := client.planSystemPrompt(conv, "Finish docs", 1, 3)
	if !strings.Contains(prompt, "<CORE_SAFETY_RULES>") {
		t.Fatalf("expected core safety rules section, got %q", prompt)
	}
	if !strings.Contains(prompt, "<PLANNER_OPERATING_RULES>") {
		t.Fatalf("expected planner operating rules section, got %q", prompt)
	}
	if !strings.Contains(prompt, "<REPO_MODE_GUIDANCE>") {
		t.Fatalf("expected planner overlay section, got %q", prompt)
	}
	if !strings.Contains(prompt, "</PLANNER_SYSTEM_PROMPT>") {
		t.Fatalf("expected planner system prompt boundary, got %q", prompt)
	}
	if strings.Contains(prompt, "Task-Specific Guidance") {
		t.Fatalf("expected old overlay heading to be removed, got %q", prompt)
	}
	if !strings.Contains(prompt, "Focus on security review and threat modeling.") {
		t.Fatalf("expected planner overlay body, got %q", prompt)
	}
}

func TestBuildAskRequestOmitsTranscript(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-ask-request", "Investigate planner flow")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	request := client.buildAskRequest(conv, conv.OriginalGoal, 2, 4)
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
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-prefix", "Finish docs")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", "Answer: done", map[string]any{"type": "answer", "turn": 1})

	planMessages := client.buildPlanMessages(context.Background(), conv, conv.OriginalGoal, 2, 4)
	askMessages := client.buildPlannerTaskMessages(context.Background(), conv, conv.OriginalGoal, 2, 4, client.askPrompt(client.buildAskRequest(conv, conv.OriginalGoal, 2, 4), ""))
	monitorMessages := client.buildPlannerTaskMessages(context.Background(), conv, conv.OriginalGoal, 2, 4, client.askUserDirectedMonitorPrompt("Explain config loading."))
	mixedMessages := client.buildPlannerTaskMessages(context.Background(), conv, conv.OriginalGoal, 2, 4, client.askMixedMonitorPrompt("Explain config loading and run git diff --stat."))

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
	client := NewClient(ClientConfig{PlannerOverlay: "Prioritize migration safety checks."})
	conv := conversation.New("sess-overlay-prefix", "Finish docs")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", "Answer: done", map[string]any{"type": "answer", "turn": 1})

	planMessages := client.buildPlanMessages(context.Background(), conv, conv.OriginalGoal, 2, 4)
	taskMessages := [][]llm.Message{
		client.buildPlannerTaskMessages(context.Background(), conv, conv.OriginalGoal, 2, 4, client.askPrompt(client.buildAskRequest(conv, conv.OriginalGoal, 2, 4), "")),
		client.buildPlannerTaskMessages(context.Background(), conv, conv.OriginalGoal, 2, 4, client.askUserDirectedMonitorPrompt("Explain config loading.")),
		client.buildPlannerTaskMessages(context.Background(), conv, conv.OriginalGoal, 2, 4, client.askMixedMonitorPrompt("Explain config loading and run git diff --stat.")),
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
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-1", "Finish docs")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", "Answer: done", map[string]any{"type": "answer", "turn": 1})
	messages := client.buildPlanMessages(context.Background(), conv, "Finish docs", 2, 4)

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
	if messages[2].Content != "[work_request] Question: start" {
		t.Fatalf("unexpected ask message: %q", messages[2].Content)
	}
	if messages[3].Content != "[work_result] Answer: done" {
		t.Fatalf("unexpected answer message: %q", messages[3].Content)
	}
	if !strings.Contains(messages[4].Content, "Step 2 of 4") {
		t.Fatalf("step message missing progress: %q", messages[4].Content)
	}
	if !strings.Contains(messages[4].Content, "Decision: ask_worker|ask_user|answer_user") {
		t.Fatalf("final planner message missing decision schema: %q", messages[4].Content)
	}
}

func TestBuildPlanMessagesUsesConversation(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-2", "Finish docs")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	messages := client.buildPlanMessages(context.Background(), conv, "Finish docs", 2, 4)
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}
	if messages[2].Role != "assistant" {
		t.Fatalf("expected assistant transcript message, got %q", messages[2].Role)
	}
	if messages[2].Content != "[work_request] Question: start" {
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
	client.buildPlanMessages(context.Background(), conv, "Goal", 2, 4)

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
	client.buildPlanMessages(context.Background(), conv, "Goal", 2, 4)

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
	client.buildPlanMessages(context.Background(), conv, "Goal", 3, 6)

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

func TestFinalizeMessagesReusePlannerSystemPrompt(t *testing.T) {
	client := NewClient(ClientConfig{PlannerOverlay: "Prioritize migration safety checks."})
	conv := conversation.New("sess-finalize-prefix", "Initial goal")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("user", "Updated goal", nil)
	conv.AddMessage("assistant", "Answer: done", map[string]any{"type": "answer", "turn": 1})

	planMessages := client.buildPlanMessages(context.Background(), conv, "stale goal", 2, 4)
	finalizeMessages := client.buildFinalizeMessages(context.Background(), conv, "stale goal")
	if len(finalizeMessages) != len(planMessages) {
		t.Fatalf("expected matching message counts, got finalize=%d plan=%d", len(finalizeMessages), len(planMessages))
	}
	if finalizeMessages[0].Role != "system" {
		t.Fatalf("expected finalize messages to start with system prompt, got %#v", finalizeMessages)
	}
	if finalizeMessages[0].Content != planMessages[0].Content {
		t.Fatalf("expected finalize to reuse shared planner system prompt")
	}
	if !strings.Contains(finalizeMessages[0].Content, "Prioritize migration safety checks.") {
		t.Fatalf("expected planner overlay in finalize system prompt, got %q", finalizeMessages[0].Content)
	}
	if !strings.Contains(finalizeMessages[0].Content, "<ANSWER_THE_USER_BEHAVIOR>") {
		t.Fatalf("expected answer-the-user behavior section in finalize system prompt, got %q", finalizeMessages[0].Content)
	}
	last := finalizeMessages[len(finalizeMessages)-1]
	if last.Role != "user" {
		t.Fatalf("expected finalize request to be a user message, got %q", last.Role)
	}
	if last.Content != answerTheUserPrompt {
		t.Fatalf("unexpected finalize request, got %q", last.Content)
	}
	if strings.Contains(last.Content, "Prioritize migration safety checks.") {
		t.Fatalf("planner overlay should stay out of finalize user prompt, got %q", last.Content)
	}
	if strings.Contains(finalizeMessages[0].Content, "You are the composer agent") {
		t.Fatalf("finalize should not use the old composer system prompt, got %q", finalizeMessages[0].Content)
	}
}

func TestFinalizeDoesNotPersistEphemeralRequest(t *testing.T) {
	client := NewClient(ClientConfig{PlannerOverlay: "Focus on migration safety."})
	conv := conversation.New("sess-finalize-ephemeral", "Initial goal")
	conv.AddMessage("assistant", "Question: start", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	before := len(conv.Messages)
	var captured []llm.Message
	client.chatFn = func(_ context.Context, messages []llm.Message) (string, error) {
		captured = append([]llm.Message(nil), messages...)
		return "done", nil
	}

	resp, err := client.Finalize(context.Background(), conv, "Initial goal")
	if err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if resp != "done" {
		t.Fatalf("unexpected finalize response %q", resp)
	}
	if len(conv.Messages) != before {
		t.Fatalf("expected finalize request to stay ephemeral, got %d messages want %d", len(conv.Messages), before)
	}
	if len(captured) == 0 {
		t.Fatalf("expected finalize request to be sent to model")
	}
	last := captured[len(captured)-1]
	if last.Role != "user" || last.Content != answerTheUserPrompt {
		t.Fatalf("unexpected finalize request %#v", last)
	}
}

func TestFinalizePromptOmitsTranscript(t *testing.T) {
	c := NewClient(ClientConfig{DryRun: true})
	prompt := c.finalizePrompt("goal text", "transcript text")
	if contains(prompt, "Goal:") || contains(prompt, "goal text") {
		t.Fatalf("finalize prompt should not inject goal anchors:\n%s", prompt)
	}
	if contains(prompt, "Transcript:") || contains(prompt, "transcript text") {
		t.Fatalf("finalize prompt should omit transcript:\n%s", prompt)
	}
	if contains(prompt, "You are the composer agent") {
		t.Fatalf("finalize prompt should now be a request prompt, got:\n%s", prompt)
	}
	if !contains(prompt, "[answer_the_user] Reply to the user now based on the conversation so far.") {
		t.Fatalf("finalize prompt should use the answer_the_user contract:\n%s", prompt)
	}
	if !contains(prompt, "Do not make further work requests.") {
		t.Fatalf("finalize prompt should block further work requests:\n%s", prompt)
	}
}

func TestBuildFinalizeMessagesForNarrowFollowUpUsesAnswerTheUserContract(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-narrow-follow-up", "Investigate internet access")
	conv.AddMessage("assistant", "[work_request] Test internet connectivity with one safe ping.", map[string]any{"type": "work_request", "turn": 1})
	conv.AddMessage("assistant", "[work_result] Ping failed with timeout.", map[string]any{"type": "work_result", "turn": 1})
	conv.AddMessage("user", "So do you have internet access or not?", nil)

	messages := client.buildFinalizeMessages(context.Background(), conv, "stale goal")
	if got := messages[len(messages)-2]; got.Role != "user" || got.Content != "So do you have internet access or not?" {
		t.Fatalf("expected narrow follow-up to remain the latest real user turn, got %#v", got)
	}
	if got := messages[len(messages)-1].Content; got != answerTheUserPrompt {
		t.Fatalf("unexpected finalize prompt, got %q", got)
	}
	if got := messages[0].Content; !strings.Contains(got, "Prefer a natural conversational reply when the latest user turn is narrow, incremental, or conversational.") {
		t.Fatalf("system prompt missing narrow-reply guidance\n%s", got)
	}
}

func TestBuildFinalizeMessagesForWrapUpRequestUsesAnswerTheUserContract(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-wrap-up", "Investigate internet access")
	conv.AddMessage("assistant", "[work_request] Test internet connectivity with one safe ping.", map[string]any{"type": "work_request", "turn": 1})
	conv.AddMessage("assistant", "[work_result] Ping failed with timeout.", map[string]any{"type": "work_result", "turn": 1})
	conv.AddMessage("user", "Please summarize the overall conclusion.", nil)

	messages := client.buildFinalizeMessages(context.Background(), conv, "stale goal")
	if got := messages[len(messages)-2]; got.Role != "user" || got.Content != "Please summarize the overall conclusion." {
		t.Fatalf("expected wrap-up request to remain the latest real user turn, got %#v", got)
	}
	if got := messages[len(messages)-1].Content; got != answerTheUserPrompt {
		t.Fatalf("unexpected finalize prompt, got %q", got)
	}
	if got := messages[0].Content; !strings.Contains(got, "If the latest real user turn asks for a summary, wrap-up, or overall conclusion, provide that broader response.") {
		t.Fatalf("system prompt missing wrap-up guidance\n%s", got)
	}
}

func TestBuildFinalizeMessagesPreservesCleanConversationOrdering(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-clean-order", "Determine if the session is running in a local environment. Provide evidence for or against.")
	conv.AddMessage("assistant", "## Determination: Running in a local environment.", map[string]any{"type": "final", "turns": 1, "capped": false})
	conv.AddMessage("user", "List the last 3 commit messages in the project root and also the gitsubmodule `agent/internal/shell-agent/`.", nil)
	conv.AddMessage("assistant", "Run `git -C /repo log --oneline -3` for the project root, then `git -C /repo/agent/internal/shell-agent log --oneline -3` for the submodule.", map[string]any{"type": "ask", "turn": 2, "decision": "ask"})
	conv.AddMessage("assistant", "## Answer\n- Confidence: 100% - Retrieved the last 3 commits for both repositories.", map[string]any{"type": "answer", "turn": 2})
	conv.AddMessage("assistant", "Revised Goal: List the last 3 commit messages for the project root and the `agent/internal/shell-agent/` submodule.", map[string]any{"type": "final", "turns": 2, "capped": false})
	conv.AddMessage("user", "list any untracked files or modified tracked files in project root and in git submoodule", nil)
	conv.AddMessage("assistant", "Run `git -C /workspace status --short` for the project root, then `git -C /workspace/agent/internal/shell-agent status --short` for the submodule.", map[string]any{"type": "ask", "turn": 3, "decision": "ask"})
	conv.AddMessage("assistant", "## Answer\n- Confidence: 100% - Reported modified and untracked files for the project root and submodule.", map[string]any{"type": "answer", "turn": 3})

	messages := client.buildFinalizeMessages(context.Background(), conv, "stale goal")
	if len(messages) != 11 {
		t.Fatalf("expected 11 messages, got %d", len(messages))
	}

	wantRoles := []string{"system", "user", "assistant", "user", "assistant", "assistant", "assistant", "user", "assistant", "assistant", "user"}
	for i, want := range wantRoles {
		if messages[i].Role != want {
			t.Fatalf("message %d role = %q, want %q", i, messages[i].Role, want)
		}
	}

	if got := messages[3].Content; strings.Contains(got, "Reevaluate the task in light of this guidance") || strings.Contains(got, `"""`) {
		t.Fatalf("expected cleaned first goal update, got %q", got)
	}
	if got := messages[7].Content; strings.Contains(got, "Reevaluate the task in light of this guidance") || strings.Contains(got, `"""`) {
		t.Fatalf("expected cleaned latest goal update, got %q", got)
	}
	if got := messages[10].Content; got != answerTheUserPrompt {
		t.Fatalf("unexpected finalize prompt, got %q", got)
	} else if strings.Contains(got, "Reevaluate the task in light of this guidance") || strings.Contains(got, "Begin by writing a single line starting with \"Revised Goal:\"") {
		t.Fatalf("expected finalize prompt to omit ephemeral revised-goal instructions, got %q", got)
	}
}

func TestBuildFinalizeMessagesMatchesCleanedSessionWireTranscript(t *testing.T) {
	client := NewClient(ClientConfig{})
	conv := conversation.New("sess-clean-wire", "Determine if the session is running in a local environment. Provide evidence for or against.")
	conv.AddMessage("assistant", "## Determination: Running in a local environment.\n\nThe session is running in a local environment.", map[string]any{"type": "final", "turns": 1, "capped": false})
	conv.AddMessage("user", "List the last 3 commit messages in the project root and also the gitsubmodule `agent/internal/shell-agent/`.", nil)
	conv.AddMessage("assistant", "Run `git -C /repo log --oneline -3` for the project root, then `git -C /repo/agent/internal/shell-agent log --oneline -3` for the submodule. Report the 3 commit messages from each location.\n\nI understand that I don't have access to a shell directly. The `shell-agent` will carry out my request and report back with explanation of the results and not necessarily the full output of commands it executes.", map[string]any{"type": "ask", "turn": 2, "decision": "ask"})
	conv.AddMessage("assistant", "## Answer\n- Confidence: 100% - The project root (`/workspace`) shows these 3 most recent commits: (1) `c8c48c0 Refactor planner prompt layering`, (2) `c442854 chore: untrack a refactor todo.`, (3) `f808483 chore(templates): sync local overrides with embedded defaults`.\n- Confidence: 100% - The submodule (`/workspace/agent/internal/shell-agent`) shows these 3 most recent commits: (1) `1deb3e5 fix: tighten repo-root recursive grep validation`, (2) `2b1cfb9 fix: reject broad recursive grep from repo root`, (3) `0ff19c3 chore: add dockerfile for useful tools for handling documents.`", map[string]any{"type": "answer", "turn": 2})
	conv.AddMessage("assistant", "Revised Goal: List the last 3 commit messages for the project root and the `agent/internal/shell-agent/` submodule.\n\n## Last 3 Commits — Project Root (`/workspace`)\n\n| # | Hash | Message |\n|---|---|---|\n| 1 | `c8c48c0` | Refactor planner prompt layering |\n| 2 | `c442854` | chore: untrack a refactor todo. |\n| 3 | `f808483` | chore(templates): sync local overrides with embedded defaults |\n\n## Last 3 Commits — Submodule (`agent/internal/shell-agent/`)\n\n| # | Hash | Message |\n|---|---|---|\n| 1 | `1deb3e5` | fix: tighten repo-root recursive grep validation |\n| 2 | `2b1cfb9` | fix: reject broad recursive grep from repo root |\n| 3 | `0ff19c3` | chore: add dockerfile for useful tools for handling documents. |\n\nNo gaps or uncertainty remain — both commit histories were retrieved directly from `git log` in the respective directories.", map[string]any{"type": "final", "turns": 2, "capped": false})
	conv.AddMessage("user", "list any untracked files or modified tracked files in project root and in git submoodule", nil)
	conv.AddMessage("assistant", "Run `git -C /workspace status --short` for the project root, then `git -C /workspace/agent/internal/shell-agent status --short` for the submodule. Report any untracked or modified files found in each location.\n\nI understand that I don't have access to a shell directly. The `shell-agent` will carry out my request and report back with explanation of the results and not necessarily the full output of commands it executes.", map[string]any{"type": "ask", "turn": 3, "decision": "ask"})
	conv.AddMessage("assistant", "## Answer\n- Confidence: 100% - The project root (`/workspace`) has the following modified/untracked files: `README.md` (staged modification), `agent/internal/llm/config.go` (unstaged modification), `agent/internal/session/runner_state.go` (unstaged modification), `agent/internal/session/runner_state_test.go` (unstaged modification), `agent/internal/shell-agent` (submodule pointer modified), and `.git.hydrated/` (untracked directory).\n- Confidence: 100% - The submodule (`/workspace/agent/internal/shell-agent`) has one modified file: `internal/environments/tempdir.go` (unstaged modification).", map[string]any{"type": "answer", "turn": 3})

	messages := client.buildFinalizeMessages(context.Background(), conv, "stale goal")
	if len(messages) != 11 {
		t.Fatalf("expected 11 messages, got %d", len(messages))
	}
	if strings.TrimSpace(messages[0].Role) != "system" || strings.TrimSpace(messages[0].Content) == "" {
		t.Fatalf("expected non-empty system prompt at message 0, got %#v", messages[0])
	}

	type wantMessage struct {
		role    string
		content string
	}
	want := []wantMessage{
		{role: "user", content: "Determine if the session is running in a local environment. Provide evidence for or against."},
		{role: "assistant", content: "## Determination: Running in a local environment.\n\nThe session is running in a local environment."},
		{role: "user", content: "List the last 3 commit messages in the project root and also the gitsubmodule `agent/internal/shell-agent/`."},
		{role: "assistant", content: "[work_request] Run `git -C /repo log --oneline -3` for the project root, then `git -C /repo/agent/internal/shell-agent log --oneline -3` for the submodule. Report the 3 commit messages from each location.\n\nI understand that I don't have access to a shell directly. The `shell-agent` will carry out my request and report back with explanation of the results and not necessarily the full output of commands it executes."},
		{role: "assistant", content: "[work_result] ## Answer\n- Confidence: 100% - The project root (`/workspace`) shows these 3 most recent commits: (1) `c8c48c0 Refactor planner prompt layering`, (2) `c442854 chore: untrack a refactor todo.`, (3) `f808483 chore(templates): sync local overrides with embedded defaults`.\n- Confidence: 100% - The submodule (`/workspace/agent/internal/shell-agent`) shows these 3 most recent commits: (1) `1deb3e5 fix: tighten repo-root recursive grep validation`, (2) `2b1cfb9 fix: reject broad recursive grep from repo root`, (3) `0ff19c3 chore: add dockerfile for useful tools for handling documents.`"},
		{role: "assistant", content: "Revised Goal: List the last 3 commit messages for the project root and the `agent/internal/shell-agent/` submodule.\n\n## Last 3 Commits — Project Root (`/workspace`)\n\n| # | Hash | Message |\n|---|---|---|\n| 1 | `c8c48c0` | Refactor planner prompt layering |\n| 2 | `c442854` | chore: untrack a refactor todo. |\n| 3 | `f808483` | chore(templates): sync local overrides with embedded defaults |\n\n## Last 3 Commits — Submodule (`agent/internal/shell-agent/`)\n\n| # | Hash | Message |\n|---|---|---|\n| 1 | `1deb3e5` | fix: tighten repo-root recursive grep validation |\n| 2 | `2b1cfb9` | fix: reject broad recursive grep from repo root |\n| 3 | `0ff19c3` | chore: add dockerfile for useful tools for handling documents. |\n\nNo gaps or uncertainty remain — both commit histories were retrieved directly from `git log` in the respective directories."},
		{role: "user", content: "list any untracked files or modified tracked files in project root and in git submoodule"},
		{role: "assistant", content: "[work_request] Run `git -C /workspace status --short` for the project root, then `git -C /workspace/agent/internal/shell-agent status --short` for the submodule. Report any untracked or modified files found in each location.\n\nI understand that I don't have access to a shell directly. The `shell-agent` will carry out my request and report back with explanation of the results and not necessarily the full output of commands it executes."},
		{role: "assistant", content: "[work_result] ## Answer\n- Confidence: 100% - The project root (`/workspace`) has the following modified/untracked files: `README.md` (staged modification), `agent/internal/llm/config.go` (unstaged modification), `agent/internal/session/runner_state.go` (unstaged modification), `agent/internal/session/runner_state_test.go` (unstaged modification), `agent/internal/shell-agent` (submodule pointer modified), and `.git.hydrated/` (untracked directory).\n- Confidence: 100% - The submodule (`/workspace/agent/internal/shell-agent`) has one modified file: `internal/environments/tempdir.go` (unstaged modification)."},
		{role: "user", content: answerTheUserPrompt},
	}

	for i, wantMsg := range want {
		got := messages[i+1]
		if got.Role != wantMsg.role {
			t.Fatalf("message %d role = %q, want %q", i+1, got.Role, wantMsg.role)
		}
		if got.Content != wantMsg.content {
			t.Fatalf("message %d content mismatch\nwant:\n%s\n\ngot:\n%s", i+1, wantMsg.content, got.Content)
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


