package prompt

import (
	"context"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestDetectShowFileRequestParsesTrue(t *testing.T) {
	t.Cleanup(func() { chatWithResolvedFallback = llm.ChatWithResolvedFallback })
	chatWithResolvedFallback = func(ctx context.Context, resolved llm.ResolvedModel, fallbackAliases []string, fallbackResolved []llm.ResolvedModel, extras map[string]any, messages []llm.Message) (string, error) {
		return `{"is_show_file_request":true,"filepath":"agent/internal/patcher/patcher.go"}`, nil
	}

	det, raw, err := DetectShowFileRequest(context.Background(), ModelRuntime{}, "Show me the full content")
	if err != nil {
		t.Fatalf("DetectShowFileRequest error: %v", err)
	}
	if strings.TrimSpace(raw) == "" {
		t.Fatalf("expected raw JSON")
	}
	if !det.IsShowFileRequest {
		t.Fatalf("expected is_show_file_request=true")
	}
	if det.Filepath != "agent/internal/patcher/patcher.go" {
		t.Fatalf("unexpected filepath: %q", det.Filepath)
	}
}

func TestDetectShowFileRequestParsesFalse(t *testing.T) {
	t.Cleanup(func() { chatWithResolvedFallback = llm.ChatWithResolvedFallback })
	chatWithResolvedFallback = func(ctx context.Context, resolved llm.ResolvedModel, fallbackAliases []string, fallbackResolved []llm.ResolvedModel, extras map[string]any, messages []llm.Message) (string, error) {
		return `{"is_show_file_request":false}`, nil
	}

	det, _, err := DetectShowFileRequest(context.Background(), ModelRuntime{}, "What should I do next?")
	if err != nil {
		t.Fatalf("DetectShowFileRequest error: %v", err)
	}
	if det.IsShowFileRequest {
		t.Fatalf("expected is_show_file_request=false")
	}
}

func TestDetectShowFileRequestNormalizesMissingPath(t *testing.T) {
	t.Cleanup(func() { chatWithResolvedFallback = llm.ChatWithResolvedFallback })
	chatWithResolvedFallback = func(ctx context.Context, resolved llm.ResolvedModel, fallbackAliases []string, fallbackResolved []llm.ResolvedModel, extras map[string]any, messages []llm.Message) (string, error) {
		return `{"is_show_file_request":true}`, nil
	}

	det, _, err := DetectShowFileRequest(context.Background(), ModelRuntime{}, "Show me the file")
	if err != nil {
		t.Fatalf("DetectShowFileRequest error: %v", err)
	}
	if det.IsShowFileRequest {
		t.Fatalf("expected normalization to disable request when filepath missing")
	}
}
