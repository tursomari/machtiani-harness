package prompt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/discoveryrunner"
)

func TestRunUsesAnswerRuntime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MACHTIANI_SESSION_ID", "test-answer-runtime")

	original := chatStreamWithRuntime
	t.Cleanup(func() { chatStreamWithRuntime = original })

	called := false
	chatStreamWithRuntime = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extra map[string]any, messages []llm.Message, onToken func(string)) (string, error) {
		called = true
		if got := model.Model; got != "answer-model" {
			t.Fatalf("expected answer model, got %q", got)
		}
		if len(fallbackAliases) != 0 {
			t.Fatalf("expected no fallback aliases, got %v", fallbackAliases)
		}
		if len(fallbackModels) != 0 {
			t.Fatalf("expected no fallback models, got %d", len(fallbackModels))
		}
		if len(messages) != 1 {
			t.Fatalf("expected single message, got %d", len(messages))
		}
		if got := extra["temperature"]; got != 0.7 {
			t.Fatalf("expected temperature override, got %#v", got)
		}
		if _, exists := extra["top_p"]; exists {
			t.Fatalf("did not expect planner extras to bleed into answer runtime")
		}
		if onToken != nil {
			onToken("stub")
		}
		return "answer", nil
	}

	res, err := Run(context.Background(), RunOptions{
		Prompt: "Hello",
		Mode:   "answer-only",
		Runtime: ModelRuntime{
			Resolved: llm.CloneResolvedModel(llm.ResolvedModel{Model: "planner-model"}),
			Extras:   map[string]any{"top_p": 0.1},
		},
		AnswerRuntime: ModelRuntime{
			Resolved: llm.CloneResolvedModel(llm.ResolvedModel{Model: "answer-model"}),
			Extras:   map[string]any{"temperature": 0.7},
		},
		Prompts: testPromptsConfig(),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !called {
		t.Fatalf("expected chat invocation")
	}
	if res.Assistant != "answer" {
		t.Fatalf("expected assistant text 'answer', got %q", res.Assistant)
	}
}

func TestRunFallsBackToPrimaryRuntime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MACHTIANI_SESSION_ID", "test-primary-runtime")

	original := chatStreamWithRuntime
	t.Cleanup(func() { chatStreamWithRuntime = original })

	called := false
	chatStreamWithRuntime = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extra map[string]any, messages []llm.Message, onToken func(string)) (string, error) {
		called = true
		if got := model.Model; got != "planner-model" {
			t.Fatalf("expected planner model, got %q", got)
		}
		if len(fallbackAliases) != 1 || fallbackAliases[0] != "orch-fallback" {
			t.Fatalf("unexpected fallback aliases: %v", fallbackAliases)
		}
		if len(fallbackModels) != 1 || fallbackModels[0].Model != "fallback-model" {
			t.Fatalf("unexpected fallback models: %+v", fallbackModels)
		}
		if got := extra["top_p"]; got != 0.2 {
			t.Fatalf("expected planner extras to propagate, got %#v", got)
		}
		return "primary", nil
	}

	res, err := Run(context.Background(), RunOptions{
		Prompt: "World",
		Mode:   "answer-only",
		Runtime: ModelRuntime{
			Resolved:         llm.ResolvedModel{Model: "planner-model"},
			Extras:           map[string]any{"top_p": 0.2},
			FallbackAliases:  []string{"orch-fallback"},
			FallbackResolved: []llm.ResolvedModel{{Model: "fallback-model"}},
		},
		Prompts: testPromptsConfig(),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !called {
		t.Fatalf("expected chat invocation")
	}
	if res.Assistant != "primary" {
		t.Fatalf("expected assistant text 'primary', got %q", res.Assistant)
	}
}

func TestRunIncludesResponseDirectives(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MACHTIANI_SESSION_ID", "test-response-directives")

	original := chatStreamWithRuntime
	t.Cleanup(func() { chatStreamWithRuntime = original })

	var captured string
	chatStreamWithRuntime = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extra map[string]any, messages []llm.Message, onToken func(string)) (string, error) {
		if len(messages) != 1 {
			t.Fatalf("expected single message, got %d", len(messages))
		}
		captured = messages[0].Content
		return "ok", nil
	}

	res, err := Run(context.Background(), RunOptions{
		Prompt: "Summarize the relevant changes.",
		Mode:   "answer-only",
		Runtime: ModelRuntime{
			Resolved: llm.CloneResolvedModel(llm.ResolvedModel{Model: "planner-model"}),
		},
		ResponseDirectives: []string{"use_tag_format"},
		Prompts:            testPromptsConfig(),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(captured, "Response Rules:") {
		t.Fatalf("expected response rules block in prompt: %q", captured)
	}
	if !strings.Contains(captured, "[path/to/file | start:end]") {
		t.Fatalf("expected tag format directive in prompt: %q", captured)
	}
	if strings.TrimSpace(res.DirectiveBlock) == "" {
		t.Fatalf("expected directive block in result")
	}
	if !strings.Contains(res.DirectiveBlock, "Response Rules:") {
		t.Fatalf("directive block missing header: %q", res.DirectiveBlock)
	}
}

func TestRunInjectsTagSnippets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MACHTIANI_SESSION_ID", "test-tag-snippets")

	tmpDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})

	samplePath := filepath.Join("types", "errors.d.ts")
	if err := os.MkdirAll(filepath.Dir(samplePath), 0o755); err != nil {
		t.Fatalf("mkdir sample dir: %v", err)
	}
	content := "export class Foo {}\nexport class Bar {}\n"
	if err := os.WriteFile(samplePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write sample file: %v", err)
	}

	origChat := chatStreamWithRuntime
	chatStreamWithRuntime = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extra map[string]any, messages []llm.Message, onToken func(string)) (string, error) {
		return "Specifically at [types/errors.d.ts | 1:2], the type is defined.", nil
	}
	t.Cleanup(func() { chatStreamWithRuntime = origChat })

	origDisco := discoveryRunnerRun
	discoveryRunnerRun = func(ctx context.Context, prompt string, model discoveryrunner.ModelSettings, sessionID string, verbose bool) (discoveryrunner.Result, error) {
		return discoveryrunner.Result{Paths: []string{"types/errors.d.ts"}}, nil
	}
	t.Cleanup(func() { discoveryRunnerRun = origDisco })

	res, err := Run(context.Background(), RunOptions{
		Prompt: "Where is ResponseError defined?",
		Mode:   "default",
		Runtime: ModelRuntime{
			Resolved: llm.CloneResolvedModel(llm.ResolvedModel{Model: "planner-model"}),
		},
		ResponseDirectives: []string{"use_tag_format"},
		SessionID:          "test-tag-snippets",
		Prompts:            testPromptsConfig(),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(res.Assistant, "[types/errors.d.ts | 1:2]") {
		t.Fatalf("expected tag reference to remain in assistant text: %q", res.Assistant)
	}
	if !strings.Contains(res.Assistant, "```typescript") {
		t.Fatalf("expected injected code fence for snippet, got %q", res.Assistant)
	}
	if !strings.Contains(res.Assistant, "export class Foo {}") || !strings.Contains(res.Assistant, "export class Bar {}") {
		t.Fatalf("expected raw lines from snippet, got %q", res.Assistant)
	}
	if strings.Contains(res.Assistant, "1  export class Foo") {
		t.Fatalf("did not expect line numbering in snippet, got %q", res.Assistant)
	}
}

func TestRunShowFileInvalidPathsAsksForFilepaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sessionID := "test-show-file-invalid"
	t.Setenv("MACHTIANI_SESSION_ID", sessionID)

	workDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})

	origDisco := discoveryRunnerRun
	discoveryRunnerRun = func(ctx context.Context, prompt string, model discoveryrunner.ModelSettings, sessionID string, verbose bool) (discoveryrunner.Result, error) {
		return discoveryrunner.Result{}, nil
	}
	t.Cleanup(func() { discoveryRunnerRun = origDisco })

	origDetect := detectShowFileRequest
	detectShowFileRequest = func(ctx context.Context, runtime ModelRuntime, plannerPrompt string) (ShowFileDetection, string, error) {
		return ShowFileDetection{IsShowFileRequest: true, Filepaths: []string{"missing.go"}}, "", nil
	}
	t.Cleanup(func() { detectShowFileRequest = origDetect })

	origChat := chatStreamWithRuntime
	chatStreamWithRuntime = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extra map[string]any, messages []llm.Message, onToken func(string)) (string, error) {
		t.Fatalf("chat stream should be skipped when asking for file paths")
		return "", nil
	}
	t.Cleanup(func() { chatStreamWithRuntime = origChat })

	res, err := Run(context.Background(), RunOptions{
		Prompt: "Show me missing.go",
		Mode:   "default",
		Runtime: ModelRuntime{
			Resolved: llm.CloneResolvedModel(llm.ResolvedModel{Model: "planner-model"}),
		},
		SessionID: sessionID,
		Prompts:   testPromptsConfig(),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(res.Assistant, "missing.go") {
		t.Fatalf("expected invalid path in assistant message, got %q", res.Assistant)
	}
	if !strings.Contains(res.Assistant, "repository-relative") {
		t.Fatalf("expected request for valid file paths, got %q", res.Assistant)
	}
}

func TestRunShowFileInvalidPathsFromDiscoveryAsksForFilepaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sessionID := "test-show-file-invalid-discovery"
	t.Setenv("MACHTIANI_SESSION_ID", sessionID)

	workDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})

	origDisco := discoveryRunnerRun
	discoveryRunnerRun = func(ctx context.Context, prompt string, model discoveryrunner.ModelSettings, sessionID string, verbose bool) (discoveryrunner.Result, error) {
		return discoveryrunner.Result{Paths: []string{"missing.go"}}, nil
	}
	t.Cleanup(func() { discoveryRunnerRun = origDisco })

	origDetect := detectShowFileRequest
	detectCalled := false
	detectShowFileRequest = func(ctx context.Context, runtime ModelRuntime, plannerPrompt string) (ShowFileDetection, string, error) {
		detectCalled = true
		if got := runtime.Resolved.Model; got != "file-model" {
			t.Fatalf("expected file discovery model, got %q", got)
		}
		return ShowFileDetection{IsShowFileRequest: true}, "", nil
	}
	t.Cleanup(func() { detectShowFileRequest = origDetect })

	origChat := chatStreamWithRuntime
	chatStreamWithRuntime = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extra map[string]any, messages []llm.Message, onToken func(string)) (string, error) {
		t.Fatalf("chat stream should be skipped when asking for file paths")
		return "", nil
	}
	t.Cleanup(func() { chatStreamWithRuntime = origChat })

	res, err := Run(context.Background(), RunOptions{
		Prompt: "Show me missing.go",
		Mode:   "default",
		Runtime: ModelRuntime{
			Resolved: llm.CloneResolvedModel(llm.ResolvedModel{Model: "planner-model"}),
		},
		FileDiscoveryRuntime: ModelRuntime{
			Resolved: llm.CloneResolvedModel(llm.ResolvedModel{Model: "file-model"}),
		},
		SessionID: sessionID,
		Prompts:   testPromptsConfig(),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !detectCalled {
		t.Fatalf("expected show-file detection to run")
	}
	if !strings.Contains(res.Assistant, "missing.go") {
		t.Fatalf("expected invalid path in assistant message, got %q", res.Assistant)
	}
	if !strings.Contains(res.Assistant, "repository-relative") {
		t.Fatalf("expected request for valid file paths, got %q", res.Assistant)
	}
}

func TestRunShowFileSkipsAnswerGenerationEvenWhenExplainRequested(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sessionID := "test-show-file-terminal"
	t.Setenv("MACHTIANI_SESSION_ID", sessionID)

	workDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})

	if err := os.WriteFile("a.go", []byte("package demo\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	origDisco := discoveryRunnerRun
	discoveryRunnerRun = func(ctx context.Context, prompt string, model discoveryrunner.ModelSettings, sessionID string, verbose bool) (discoveryrunner.Result, error) {
		return discoveryrunner.Result{Paths: []string{"a.go"}}, nil
	}
	t.Cleanup(func() { discoveryRunnerRun = origDisco })

	origDetect := detectShowFileRequest
	detectShowFileRequest = func(ctx context.Context, runtime ModelRuntime, plannerPrompt string) (ShowFileDetection, string, error) {
		return ShowFileDetection{
			IsShowFileRequest: true,
			Filepaths:         []string{"a.go"},
			IncludeExplain:    true,
			ExplainPrompt:     "Explain this file.",
			VerbatimQuestion:  `Show a.go. <full_file path="a.go" />`,
		}, "", nil
	}
	t.Cleanup(func() { detectShowFileRequest = origDetect })

	origChat := chatStreamWithRuntime
	chatStreamWithRuntime = func(ctx context.Context, model llm.ResolvedModel, fallbackAliases []string, fallbackModels []llm.ResolvedModel, extra map[string]any, messages []llm.Message, onToken func(string)) (string, error) {
		t.Fatalf("chat stream should be skipped for show-file terminal output")
		return "", nil
	}
	t.Cleanup(func() { chatStreamWithRuntime = origChat })

	res, err := Run(context.Background(), RunOptions{
		Prompt: "Show a.go and explain it.",
		Mode:   "default",
		Runtime: ModelRuntime{
			Resolved: llm.CloneResolvedModel(llm.ResolvedModel{Model: "planner-model"}),
		},
		SessionID: sessionID,
		Prompts:   testPromptsConfig(),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(res.Assistant, "## File Context") {
		t.Fatalf("expected show-file content block, got %q", res.Assistant)
	}
	if !strings.Contains(res.Assistant, "func A() {}") {
		t.Fatalf("expected file contents in assistant output, got %q", res.Assistant)
	}
}
