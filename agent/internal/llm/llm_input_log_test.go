package llm

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLLMInputLogHasNoImplicitSessionFallback(t *testing.T) {
	t.Setenv(llmInputLogEnv, "")
	t.Setenv("MACHTIANI_SESSION_ID", "session-implicit")
	if got := llmInputLogPath(context.Background()); got != "" {
		t.Fatalf("llmInputLogPath = %q, want empty", got)
	}
}

func TestWithInputLogWritesRedactedPayload(t *testing.T) {
	t.Setenv(llmInputLogEnv, "")
	path := filepath.Join(t.TempDir(), "artifacts", "llm", "inputs.jsonl")
	ctx := WithInputLog(context.Background(), path, nil)
	appendLLMInputLog(ctx, map[string]any{
		"api_key": "secret",
		"payload": map[string]any{"messages": []any{"hello"}},
	})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "secret") || !strings.Contains(text, "[redacted]") || !strings.Contains(text, "hello") {
		t.Fatalf("unexpected input log: %s", text)
	}
}

func TestWithInputLogEnvironmentPathWins(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "environment.jsonl")
	t.Setenv(llmInputLogEnv, envPath)
	ctx := WithInputLog(context.Background(), filepath.Join(t.TempDir(), "canonical.jsonl"), nil)
	if got := llmInputLogPath(ctx); got != envPath {
		t.Fatalf("llmInputLogPath = %q, want %q", got, envPath)
	}
}

func TestInputLogWriteFailureWarnsOnceAndDisablesRecorder(t *testing.T) {
	t.Setenv(llmInputLogEnv, "")
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings bytes.Buffer
	ctx := WithInputLog(context.Background(), filepath.Join(blocker, "inputs.jsonl"), &warnings)
	appendLLMInputLog(ctx, map[string]any{"payload": "first"})
	appendLLMInputLog(ctx, map[string]any{"payload": "second"})

	if got := strings.Count(warnings.String(), "disabling LLM input logging"); got != 1 {
		t.Fatalf("warning count = %d, want 1; output: %s", got, warnings.String())
	}
}
