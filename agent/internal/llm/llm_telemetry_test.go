package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

func TestEmitLLMEventUsesLLMComponent(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "traj.jsonl")
	writer, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("trajectory.New error: %v", err)
	}

	ctx := trajectory.ContextWithWriter(context.Background(), writer)
	emitLLMEvent(ctx, "error", "llm.test", map[string]any{"foo": "bar"}, context.DeadlineExceeded)

	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trajectory file: %v", err)
	}
	lines := bytesFields(data)
	if len(lines) != 1 {
		t.Fatalf("expected 1 event, got %d", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if got := rec["component"]; got != "llm" {
		t.Fatalf("expected component 'llm', got %v", got)
	}
}

func TestEmitCacheUsageEmitsPlainUsageWithoutCacheDetails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traj.jsonl")
	writer, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("trajectory.New error: %v", err)
	}

	ctx := trajectory.ContextWithWriter(context.Background(), writer)
	emitCacheUsage(ctx, ResolvedModel{Alias: "test", Model: "example/model"}, &responseUsage{
		PromptTokens:     123,
		CompletionTokens: 45,
		TotalTokens:      168,
	})

	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trajectory file: %v", err)
	}
	lines := bytesFields(data)
	if len(lines) != 1 {
		t.Fatalf("expected 1 event, got %d", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if got := rec["kind"]; got != "llm.cache.usage" {
		t.Fatalf("kind = %v, want llm.cache.usage", got)
	}
	payload, ok := rec["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload has type %T, want object", rec["payload"])
	}
	if got := payload["prompt_tokens"]; got != float64(123) {
		t.Fatalf("prompt_tokens = %v, want 123", got)
	}
	if got := payload["completion_tokens"]; got != float64(45) {
		t.Fatalf("completion_tokens = %v, want 45", got)
	}
	if _, ok := payload["cached_tokens"]; ok {
		t.Fatalf("cached_tokens should be absent for plain usage payload")
	}
}

func TestEmitCacheUsageNotifiesGeneralUsageObserver(t *testing.T) {
	var gotModel ResolvedModel
	var got UsageInfo
	ctx := WithStage(context.Background(), "answer")
	ctx = WithUsageObserver(ctx, func(model ResolvedModel, usage UsageInfo) {
		gotModel = model
		got = usage
	})
	discount := 0.5
	emitCacheUsage(ctx, ResolvedModel{ProviderName: "example", Model: "answer-model"}, &responseUsage{
		PromptTokens:     100,
		CompletionTokens: 25,
		ReasoningTokens:  7,
		CacheDiscount:    &discount,
		PromptTokensDetails: &promptTokensDetails{
			CachedTokens:     40,
			CacheWriteTokens: 10,
		},
	})

	if gotModel.Model != "answer-model" || gotModel.ProviderName != "example" {
		t.Fatalf("observer model = %+v", gotModel)
	}
	if !got.UsageAvailable || got.Stage != "answer" || got.PromptTokens != 100 || got.CompletionTokens != 25 || got.ReasoningTokens != 7 || got.CachedTokens != 40 || got.CacheWriteTokens != 10 {
		t.Fatalf("observer usage = %+v", got)
	}
	if got.CacheDiscount == nil || *got.CacheDiscount != discount {
		t.Fatalf("observer discount = %v", got.CacheDiscount)
	}
}

func TestEmitCacheUsageNotifiesGeneralObserverWithoutProviderUsage(t *testing.T) {
	called := false
	ctx := WithStage(context.Background(), "file-discovery")
	ctx = WithUsageObserver(ctx, func(model ResolvedModel, usage UsageInfo) {
		called = true
		if model.Model != "fallback-model" {
			t.Fatalf("observer model = %+v", model)
		}
		if usage.UsageAvailable || usage.Stage != "file-discovery" {
			t.Fatalf("observer usage = %+v", usage)
		}
	})
	emitCacheUsage(ctx, ResolvedModel{Model: "fallback-model"}, nil)
	if !called {
		t.Fatal("usage observer was not called")
	}
}

func bytesFields(data []byte) [][]byte {
	rows := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	out := make([][]byte, 0, len(rows))
	for _, row := range rows {
		trimmed := bytes.TrimSpace(row)
		if len(trimmed) > 0 {
			out = append(out, trimmed)
		}
	}
	return out
}
