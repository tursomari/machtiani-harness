package prompt

import (
	"context"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/mct/llm"
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
