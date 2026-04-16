package prompt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestPreflightShellRoutingContentSelectsDefault(t *testing.T) {
	stubReply := "Content, include files please."
	old := chatWithResolvedFallback
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		return stubReply, nil
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "Show me the contents of foo.go"

	shellAgent, reply, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if err != nil {
		t.Fatalf("PreflightShellRouting error: %v", err)
	}
	if shellAgent {
		t.Fatalf("expected default routing, got shell-agent")
	}
	if reply != stubReply {
		t.Fatalf("expected reply %q, got %q", stubReply, reply)
	}
}

func TestPreflightShellRoutingCaseInsensitiveContent(t *testing.T) {
	stubReply := "cOnTeNt, thanks"
	old := chatWithResolvedFallback
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		return stubReply, nil
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "Open foo.go"

	shellAgent, _, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if err != nil {
		t.Fatalf("PreflightShellRouting error: %v", err)
	}
	if shellAgent {
		t.Fatalf("expected default routing, got shell-agent")
	}
}

func TestPreflightShellRoutingContentBeatsShell(t *testing.T) {
	stubReply := "Content, shell might also work"
	old := chatWithResolvedFallback
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		return stubReply, nil
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "Pull README.md"

	shellAgent, _, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if err != nil {
		t.Fatalf("PreflightShellRouting error: %v", err)
	}
	if shellAgent {
		t.Fatalf("expected default routing when yes present, got shell-agent")
	}
}

func TestPreflightShellRoutingHeuristicPrefersContentForCodeExplanation(t *testing.T) {
	old := chatWithResolvedFallback
	called := false
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		called = true
		return "shell", nil
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "Summarize how the patcher falls back to the orchestrator model when unspecified."

	shellAgent, reply, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if err != nil {
		t.Fatalf("PreflightShellRouting error: %v", err)
	}
	if shellAgent {
		t.Fatalf("expected default routing, got shell-agent")
	}
	if reply != "content" {
		t.Fatalf("expected reply %q, got %q", "content", reply)
	}
	if called {
		t.Fatalf("expected heuristic to bypass LLM preflight")
	}
}

func TestPreflightShellRoutingHeuristicPrefersContentForFunctionLookup(t *testing.T) {
	old := chatWithResolvedFallback
	called := false
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		called = true
		return "shell", nil
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "In `agent/internal/planner/planner.go`, what function generates the ask prompt?"

	shellAgent, reply, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if err != nil {
		t.Fatalf("PreflightShellRouting error: %v", err)
	}
	if shellAgent {
		t.Fatalf("expected default routing, got shell-agent")
	}
	if reply != "content" {
		t.Fatalf("expected reply %q, got %q", "content", reply)
	}
	if called {
		t.Fatalf("expected heuristic to bypass LLM preflight")
	}
}

func TestPreflightShellRoutingHeuristicPrefersContentForDirectoryScopedExplanation(t *testing.T) {
	old := chatWithResolvedFallback
	called := false
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		called = true
		return "shell", nil
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "Explain the planner ask monitor guardrail flow in the agent/ directory and when it retries."

	shellAgent, reply, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if err != nil {
		t.Fatalf("PreflightShellRouting error: %v", err)
	}
	if shellAgent {
		t.Fatalf("expected default routing, got shell-agent")
	}
	if reply != "content" {
		t.Fatalf("expected reply %q, got %q", "content", reply)
	}
	if called {
		t.Fatalf("expected heuristic to bypass LLM preflight")
	}
}

func TestPreflightShellRoutingHeuristicDoesNotOverrideDirectoryListing(t *testing.T) {
	stubReply := "shell"
	old := chatWithResolvedFallback
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		return stubReply, nil
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "List files in the agent/internal/session directory."

	shellAgent, reply, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if err != nil {
		t.Fatalf("PreflightShellRouting error: %v", err)
	}
	if !shellAgent {
		t.Fatalf("expected shell-agent routing, got default")
	}
	if reply != stubReply {
		t.Fatalf("expected reply %q, got %q", stubReply, reply)
	}
}

func TestPreflightShellRoutingFallbackToShellAgent(t *testing.T) {
	stubReply := "shell"
	old := chatWithResolvedFallback
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		return stubReply, nil
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "Run go test ./..."

	shellAgent, reply, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if err != nil {
		t.Fatalf("PreflightShellRouting error: %v", err)
	}
	if !shellAgent {
		t.Fatalf("expected shell-agent routing, got default")
	}
	if reply != stubReply {
		t.Fatalf("expected reply %q, got %q", stubReply, reply)
	}
}

func TestPreflightShellRoutingUsesFirstLineTokens(t *testing.T) {
	stubReply := "Stub LLM (default) response\n\nShell would be better"
	old := chatWithResolvedFallback
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		return stubReply, nil
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "Need to run a migration"

	shellAgent, reply, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if err != nil {
		t.Fatalf("PreflightShellRouting error: %v", err)
	}
	if !shellAgent {
		t.Fatalf("expected shell-agent routing, got default")
	}
	expectedReply := "Stub LLM (default) response"
	if reply != expectedReply {
		t.Fatalf("expected reply %q, got %q", expectedReply, reply)
	}
}

func TestPreflightShellRoutingBlankReply(t *testing.T) {
	old := chatWithResolvedFallback
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		return "   \n", nil
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "Restart the dev server"

	shellAgent, reply, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if err != nil {
		t.Fatalf("PreflightShellRouting error: %v", err)
	}
	if !shellAgent {
		t.Fatalf("expected shell-agent routing, got default")
	}
	if reply != "" {
		t.Fatalf("expected empty reply, got %q", reply)
	}
}

func TestPreflightShellRoutingError(t *testing.T) {
	expectedErr := errors.New("boom")
	old := chatWithResolvedFallback
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		return "", expectedErr
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "Start the database"

	shellAgent, reply, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if !shellAgent {
		t.Fatalf("expected shell-agent routing on error, got default")
	}
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
	if reply != "" {
		t.Fatalf("expected empty reply, got %q", reply)
	}
}

func TestPreflightShellRoutingIncludesPromptInMessages(t *testing.T) {
	old := chatWithResolvedFallback
	var captured []llm.Message
	chatWithResolvedFallback = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extraParams map[string]any, messages []llm.Message) (string, error) {
		captured = append([]llm.Message(nil), messages...)
		return "shell", nil
	}
	t.Cleanup(func() { chatWithResolvedFallback = old })

	runtime := ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}}
	prompt := "Run gofmt ./..."

	_, _, err := PreflightShellRouting(context.Background(), runtime, prompt)
	if err != nil {
		t.Fatalf("PreflightShellRouting error: %v", err)
	}

	if len(captured) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(captured))
	}
	if captured[0].Role != "system" {
		t.Fatalf("expected first message to be system, got role %q", captured[0].Role)
	}
	if captured[1].Role != "user" {
		t.Fatalf("expected second message to be user, got role %q", captured[1].Role)
	}
	if !strings.Contains(captured[1].Content, prompt) {
		t.Fatalf("expected prompt content to include %q, got %q", prompt, captured[1].Content)
	}
}
