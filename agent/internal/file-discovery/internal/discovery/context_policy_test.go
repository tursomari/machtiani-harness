package discovery

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	cfgpkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/config"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

func testDiscoveryBudget(t *testing.T) llm.InputBudget {
	t.Helper()
	budget, err := llm.BudgetForContextLength(128000, llm.ContextSourceModel)
	if err != nil {
		t.Fatal(err)
	}
	return budget
}

func structuredOverflowError() error {
	return &llm.HTTPResponseError{
		Status: 400,
		Body:   `{"error":{"code":"context_length_exceeded","type":"invalid_request_error"}}`,
	}
}

func TestFitDiscoveryMessagesCompactsHistoryAndKeepsNewestExchanges(t *testing.T) {
	core := "Protocol reminder:\ncore"
	large := strings.Repeat("tool output line\n", 800)
	messages := []chatMessage{
		{Role: "system", Content: "system core"},
		{Role: "user", Content: core + "\ninitial cue"},
		{Role: "assistant", Content: "old-command"},
		{Role: "user", Content: "RG_OUT:\nold.go\n" + large},
		{Role: "assistant", Content: "new-command-one"},
		{Role: "user", Content: "SED_OUT[new.go]:\n" + large},
		{Role: "assistant", Content: "new-command-two"},
		{Role: "user", Content: "LS_OUT[src]:\n" + large},
	}
	state := discoveryState{
		seenPaths:      map[string]struct{}{"old.go": {}, "new.go": {}},
		failedPatterns: []string{"missing-pattern"},
		recentCommands: []string{"file_search pattern=old", "read_file path=new.go"},
	}

	fitted, report, err := fitDiscoveryMessages(messages, core, state, 700)
	if err != nil {
		t.Fatal(err)
	}
	if estimateChatMessages(fitted) > 700 {
		t.Fatalf("fitted history = %d tokens, want <= 700", estimateChatMessages(fitted))
	}
	if !report.Compacted {
		t.Fatal("expected older history compaction")
	}
	joined := ""
	for _, message := range fitted {
		joined += "\n" + message.Content
	}
	if strings.Contains(joined, "old-command") {
		t.Fatalf("old exchange survived compaction: %s", joined)
	}
	for _, required := range []string{"system core", core, "new-command-one", "new-command-two", "new.go", "missing-pattern", "read_file path=new.go"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("required compacted material %q missing: %s", required, joined)
		}
	}
}

func TestFitDiscoveryMessagesTrimsToolOutputBeforeInitialCue(t *testing.T) {
	core := "Protocol reminder:\ncore"
	cue := strings.Repeat("initial cue stays\n", 40)
	messages := []chatMessage{
		{Role: "system", Content: "system core"},
		{Role: "user", Content: core + "\n" + cue},
		{Role: "assistant", Content: "command-one"},
		{Role: "user", Content: "RG_OUT:\n" + strings.Repeat("path/to/file.go\n", 1000)},
		{Role: "assistant", Content: "command-two"},
		{Role: "user", Content: "SED_OUT[path/to/file.go]:\n" + strings.Repeat("source line\n", 1000)},
	}

	fitted, report, err := fitDiscoveryMessages(messages, core, discoveryState{}, 650)
	if err != nil {
		t.Fatal(err)
	}
	if report.ToolOutputsTrimmed == 0 {
		t.Fatal("expected retained tool output to be trimmed")
	}
	if report.InitialCueTrimmed {
		t.Fatal("initial cue was trimmed before tool output made the request fit")
	}
	if !strings.Contains(fitted[1].Content, cue) {
		t.Fatal("initial cue content was not preserved")
	}
}

func TestFitDiscoveryMessagesFitsForcedFinalization(t *testing.T) {
	core := "Protocol reminder:\ncore"
	finalize := "Max rounds reached. Do NOT call any function. No other text."
	messages := []chatMessage{
		{Role: "system", Content: "system core"},
		{Role: "user", Content: core + "\n" + strings.Repeat("initial detail\n", 500)},
		{Role: "assistant", Content: "first command"},
		{Role: "user", Content: "RG_OUT:\n" + strings.Repeat("old.go\n", 500)},
		{Role: "assistant", Content: "second command"},
		{Role: "user", Content: "SED_OUT[old.go]:\n" + strings.Repeat("line\n", 500)},
		{Role: "user", Content: finalize},
	}
	fitted, _, err := fitDiscoveryMessages(messages, core, discoveryState{}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if estimateChatMessages(fitted) > 500 {
		t.Fatalf("forced-finalization request = %d tokens, want <= 500", estimateChatMessages(fitted))
	}
	if !strings.Contains(fitted[len(fitted)-1].Content, "Max rounds reached") {
		t.Fatalf("forced-finalization instruction lost: %#v", fitted)
	}
}

func TestDiscoveryRequestPolicyRecoversStructuredOverflowAndPersistsCorrectAlias(t *testing.T) {
	originalChat := chatInvoker
	originalPersist := persistLearnedContext
	t.Cleanup(func() {
		chatInvoker = originalChat
		persistLearnedContext = originalPersist
	})

	calls := 0
	chatInvoker = func(_ context.Context, _ LLMSettings, messages []chatMessage) (string, error) {
		calls++
		if calls == 1 {
			return "", structuredOverflowError()
		}
		if estimateChatMessages(messages) > 52799 {
			t.Fatalf("reduced request still exceeds expected cap: %d", estimateChatMessages(messages))
		}
		return "success", nil
	}
	var persistedModel llm.ResolvedModel
	var previous, learned int
	persistLearnedContext = func(model llm.ResolvedModel, old, next int) (bool, error) {
		persistedModel, previous, learned = model, old, next
		return true, nil
	}

	budget := testDiscoveryBudget(t)
	policy := discoveryRequestPolicy{active: budget}
	model := llm.ResolvedModel{Alias: "discovery", ProviderName: "provider", Model: "model"}
	content, _, err := policy.call(context.Background(), cfgpkg.Config{}, LLMSettings{
		Model:                      model,
		InputBudget:                budget,
		ContextIdentityUnambiguous: true,
	}, []chatMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "core"}}, "core", discoveryState{}, nil, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if content != "success" || calls != 2 {
		t.Fatalf("content=%q calls=%d", content, calls)
	}
	if persistedModel.Alias != "discovery" || previous != 128000 || learned != 63999 {
		t.Fatalf("persisted model=%+v previous=%d learned=%d", persistedModel, previous, learned)
	}
}

func TestDiscoveryRequestPolicyDoesNotPersistAmbiguousFallback(t *testing.T) {
	originalChat := chatInvoker
	originalPersist := persistLearnedContext
	t.Cleanup(func() {
		chatInvoker = originalChat
		persistLearnedContext = originalPersist
	})
	calls := 0
	chatInvoker = func(_ context.Context, _ LLMSettings, _ []chatMessage) (string, error) {
		calls++
		if calls == 1 {
			return "", structuredOverflowError()
		}
		return "success", nil
	}
	persistLearnedContext = func(llm.ResolvedModel, int, int) (bool, error) {
		t.Fatal("ambiguous fallback chain must not persist learned context")
		return false, nil
	}
	budget := testDiscoveryBudget(t)
	policy := discoveryRequestPolicy{active: budget}
	_, _, err := policy.call(context.Background(), cfgpkg.Config{}, LLMSettings{
		Model:                      llm.ResolvedModel{Alias: "primary"},
		FallbackAliases:            []string{"fallback"},
		InputBudget:                budget,
		ContextIdentityUnambiguous: false,
	}, []chatMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "core"}}, "core", discoveryState{}, nil, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryRequestPolicyExhaustsFourOverflowReductions(t *testing.T) {
	originalChat := chatInvoker
	t.Cleanup(func() { chatInvoker = originalChat })
	calls := 0
	chatInvoker = func(_ context.Context, _ LLMSettings, _ []chatMessage) (string, error) {
		calls++
		return "", structuredOverflowError()
	}
	budget := testDiscoveryBudget(t)
	policy := discoveryRequestPolicy{active: budget}
	_, _, err := policy.call(context.Background(), cfgpkg.Config{}, LLMSettings{InputBudget: budget}, []chatMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "core"}}, "core", discoveryState{}, nil, 1, nil)
	if !llm.IsContextOverflow(err) {
		t.Fatalf("error = %v, want structured overflow", err)
	}
	if calls != 5 {
		t.Fatalf("calls = %d, want initial plus four reductions", calls)
	}
}

func TestDiscoveryRequestPolicyDoesNotRetryOrdinaryErrors(t *testing.T) {
	originalChat := chatInvoker
	t.Cleanup(func() { chatInvoker = originalChat })
	calls := 0
	wantErr := errors.New("ordinary provider failure")
	chatInvoker = func(_ context.Context, _ LLMSettings, _ []chatMessage) (string, error) {
		calls++
		return "", wantErr
	}
	budget := testDiscoveryBudget(t)
	policy := discoveryRequestPolicy{active: budget}
	_, _, err := policy.call(context.Background(), cfgpkg.Config{}, LLMSettings{InputBudget: budget}, []chatMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "core"}}, "core", discoveryState{}, nil, 1, nil)
	if !errors.Is(err, wantErr) || calls != 1 {
		t.Fatalf("error=%v calls=%d, want ordinary error after one call", err, calls)
	}
}

func TestRunEmbeddedRejectsEmergencyInitialInput(t *testing.T) {
	originalChat := chatInvoker
	t.Cleanup(func() { chatInvoker = originalChat })
	chatInvoker = func(context.Context, LLMSettings, []chatMessage) (string, error) {
		t.Fatal("oversized embedded input reached the provider")
		return "", nil
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	originalStderr := os.Stderr
	os.Stderr = devNull
	t.Cleanup(func() {
		os.Stderr = originalStderr
		_ = devNull.Close()
	})
	exit := RunEmbedded(context.Background(), cfgpkg.Config{
		MaxRounds:            1,
		MaxInitialInputBytes: 8,
		NoTrajectory:         true,
		ToolCallMode:         cfgpkg.ToolCallModeJSON,
	}, LLMSettings{}, "this input is too large")
	if exit == 0 {
		t.Fatal("oversized embedded input unexpectedly succeeded")
	}
}
