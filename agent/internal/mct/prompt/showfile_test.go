package prompt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func writeTestRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	for path, content := range files {
		full := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return repo
}

func TestDetectShowFileRequestParsesTrue(t *testing.T) {
	t.Cleanup(func() { chatWithResolvedFallback = llm.ChatWithResolvedFallback })
	chatWithResolvedFallback = func(ctx context.Context, resolved llm.ResolvedModel, fallbackAliases []string, fallbackResolved []llm.ResolvedModel, extras map[string]any, messages []llm.Message) (string, error) {
		return `{"is_show_file_request":true,"filepath":"agent/internal/patcher/patcher.go","reason":"review patcher"}`, nil
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
	if det.Reason != "review patcher" {
		t.Fatalf("unexpected reason: %q", det.Reason)
	}
	if len(det.Filepaths) != 1 || det.Filepaths[0] != "agent/internal/patcher/patcher.go" {
		t.Fatalf("unexpected filepaths: %v", det.Filepaths)
	}
}

func TestDetectShowFileRequestParsesMultipleFiles(t *testing.T) {
	t.Cleanup(func() { chatWithResolvedFallback = llm.ChatWithResolvedFallback })
	chatWithResolvedFallback = func(ctx context.Context, resolved llm.ResolvedModel, fallbackAliases []string, fallbackResolved []llm.ResolvedModel, extras map[string]any, messages []llm.Message) (string, error) {
		return `{"is_show_file_request":true,"filepaths":["a.go","b.go"],"reason":"compare"}`, nil
	}

	det, _, err := DetectShowFileRequest(context.Background(), ModelRuntime{}, "Show me both files")
	if err != nil {
		t.Fatalf("DetectShowFileRequest error: %v", err)
	}
	if !det.IsShowFileRequest {
		t.Fatalf("expected is_show_file_request=true")
	}
	if det.Filepath != "a.go" {
		t.Fatalf("unexpected filepath: %q", det.Filepath)
	}
	if len(det.Filepaths) != 2 || det.Filepaths[0] != "a.go" || det.Filepaths[1] != "b.go" {
		t.Fatalf("unexpected filepaths: %v", det.Filepaths)
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
		return `{"is_show_file_request":true,"filepaths":[]}`, nil
	}

	det, _, err := DetectShowFileRequest(context.Background(), ModelRuntime{}, "Show me the file")
	if err != nil {
		t.Fatalf("DetectShowFileRequest error: %v", err)
	}
	if det.IsShowFileRequest {
		t.Fatalf("expected normalization to disable request when filepath missing")
	}
}

func TestFetchFileSnippetsSingleFile(t *testing.T) {
	originalRunner := runSnippetDiscovery
	t.Cleanup(func() { runSnippetDiscovery = originalRunner })
	normalizedPath := normalizeShowFilePath("agent/internal/patcher/patcher.go")
	if normalizedPath == "" {
		t.Fatal("failed to normalize test path")
	}
	repoRoot := writeTestRepo(t, map[string]string{normalizedPath: "package patcher"})
	var gotReason string
	var gotRepoRoot string
	var gotPaths []string
	var gotModelAlias string
	var gotOverrides map[string]string
	runSnippetDiscovery = func(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
		gotReason = reason
		gotRepoRoot = repoRoot
		gotPaths = append([]string(nil), filepaths...)
		gotModelAlias = modelAlias
		gotOverrides = apiKeyOverrides
		return fmt.Sprintf(`{"%s":[{"start":1,"end":2}]}`, normalizedPath), nil
	}

	det := ShowFileDetection{IsShowFileRequest: true, Filepaths: []string{normalizedPath}, Reason: "patch logic"}
	overrides := map[string]string{"openrouter": "token"}
	snippets, err := FetchFileSnippets(context.Background(), det, repoRoot, "file-discovery-model", overrides, false)
	if err != nil {
		t.Fatalf("FetchFileSnippets error: %v", err)
	}
	if gotReason != "patch logic" {
		t.Fatalf("expected reason to be passed through, got %q", gotReason)
	}
	if gotRepoRoot != repoRoot {
		t.Fatalf("expected repo root %q, got %q", repoRoot, gotRepoRoot)
	}
	if len(gotPaths) != 1 || gotPaths[0] != normalizedPath {
		t.Fatalf("unexpected filepaths: %v", gotPaths)
	}
	if gotModelAlias != "file-discovery-model" {
		t.Fatalf("unexpected model alias: %q", gotModelAlias)
	}
	if len(gotOverrides) != 1 || gotOverrides["openrouter"] != "token" {
		t.Fatalf("unexpected api key overrides: %v", gotOverrides)
	}
	ranges := snippets[normalizedPath]
	if len(ranges) != 1 || ranges[0].Start != 1 || ranges[0].End != 2 {
		t.Fatalf("unexpected snippets: %v", snippets)
	}
}

func TestFetchFileSnippetsUsesVerbatimQuestion(t *testing.T) {
	originalRunner := runSnippetDiscovery
	t.Cleanup(func() { runSnippetDiscovery = originalRunner })
	normalizedPath := normalizeShowFilePath("agent/internal/patcher/patcher.go")
	if normalizedPath == "" {
		t.Fatal("failed to normalize test path")
	}
	repoRoot := writeTestRepo(t, map[string]string{normalizedPath: "package patcher"})
	var gotReason string
	runSnippetDiscovery = func(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
		gotReason = reason
		return fmt.Sprintf(`{"%s":[{"start":1,"end":2}]}`, normalizedPath), nil
	}

	det := ShowFileDetection{
		IsShowFileRequest: true,
		Filepaths:         []string{normalizedPath},
		Reason:            "generated reason",
		VerbatimQuestion:  "Question: show me the patcher file",
	}
	if _, err := FetchFileSnippets(context.Background(), det, repoRoot, "", nil, false); err != nil {
		t.Fatalf("FetchFileSnippets error: %v", err)
	}
	if gotReason != det.VerbatimQuestion {
		t.Fatalf("expected verbatim reason %q, got %q", det.VerbatimQuestion, gotReason)
	}
}

func TestFetchFileSnippetsPreflightInvalidPath(t *testing.T) {
	originalRunner := runSnippetDiscovery
	t.Cleanup(func() { runSnippetDiscovery = originalRunner })
	repoRoot := writeTestRepo(t, map[string]string{"a.go": "package a"})
	var gotPaths []string
	runSnippetDiscovery = func(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
		gotPaths = append([]string(nil), filepaths...)
		return `{"a.go":[{"start":1,"end":1}]}`, nil
	}

	det := ShowFileDetection{IsShowFileRequest: true, Filepaths: []string{"a.go", "missing.go"}, Reason: "check"}
	snippets, err := FetchFileSnippets(context.Background(), det, repoRoot, "", nil, false)
	if err == nil {
		t.Fatalf("expected partial error")
	}
	var partial *SnippetDiscoveryPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("expected partial error type, got %v", err)
	}
	if len(gotPaths) != 1 || gotPaths[0] != "a.go" {
		t.Fatalf("expected preflight to pass only a.go, got %v", gotPaths)
	}
	if _, ok := snippets["a.go"]; !ok {
		t.Fatalf("expected snippets for a.go")
	}
	if partial.Invalid == nil || partial.Invalid["missing.go"] == "" {
		t.Fatalf("expected invalid entry for missing.go")
	}
}

func TestFetchFileSnippetsRetriesPerFile(t *testing.T) {
	originalRunner := runSnippetDiscovery
	t.Cleanup(func() { runSnippetDiscovery = originalRunner })
	repoRoot := writeTestRepo(t, map[string]string{"a.go": "package a", "b.go": "package b"})
	var calls [][]string
	runSnippetDiscovery = func(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
		calls = append(calls, append([]string(nil), filepaths...))
		if len(filepaths) > 1 {
			return "", errors.New("bulk failure")
		}
		switch filepaths[0] {
		case "a.go":
			return `{"a.go":[{"start":1,"end":1}]}`, nil
		case "b.go":
			return "", errors.New("per-file boom")
		default:
			return "", errors.New("unexpected path")
		}
	}

	det := ShowFileDetection{IsShowFileRequest: true, Filepaths: []string{"a.go", "b.go"}, Reason: "check"}
	snippets, err := FetchFileSnippets(context.Background(), det, repoRoot, "", nil, false)
	if err == nil {
		t.Fatalf("expected partial error")
	}
	var partial *SnippetDiscoveryPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("expected partial error type, got %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 snippet-discovery calls, got %d", len(calls))
	}
	if _, ok := snippets["a.go"]; !ok {
		t.Fatalf("expected snippets for a.go")
	}
	if partial.Invalid == nil || partial.Invalid["b.go"] == "" {
		t.Fatalf("expected invalid entry for b.go")
	}
}

func TestFetchFileSnippetsPartialMissing(t *testing.T) {
	originalRunner := runSnippetDiscovery
	t.Cleanup(func() { runSnippetDiscovery = originalRunner })
	repoRoot := writeTestRepo(t, map[string]string{"a.go": "package a", "b.go": "package b"})
	runSnippetDiscovery = func(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
		return `{"a.go":[{"start":3,"end":4}]}`, nil
	}

	det := ShowFileDetection{IsShowFileRequest: true, Filepaths: []string{"a.go", "b.go"}, Reason: "compare"}
	snippets, err := FetchFileSnippets(context.Background(), det, repoRoot, "", nil, false)
	if err == nil {
		t.Fatalf("expected partial error")
	}
	var partial *SnippetDiscoveryPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("expected partial error type, got %v", err)
	}
	if len(partial.Missing) != 1 || partial.Missing[0] != "b.go" {
		t.Fatalf("unexpected missing list: %v", partial.Missing)
	}
	if _, ok := snippets["a.go"]; !ok {
		t.Fatalf("expected snippets for a.go")
	}
}

func TestFetchFileSnippetsEmptyRanges(t *testing.T) {
	originalRunner := runSnippetDiscovery
	t.Cleanup(func() { runSnippetDiscovery = originalRunner })
	repoRoot := writeTestRepo(t, map[string]string{"a.go": "package a"})
	runSnippetDiscovery = func(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
		return `{"a.go":[]}`, nil
	}

	det := ShowFileDetection{IsShowFileRequest: true, Filepaths: []string{"a.go"}, Reason: "check"}
	snippets, err := FetchFileSnippets(context.Background(), det, repoRoot, "", nil, false)
	if err != nil {
		t.Fatalf("FetchFileSnippets error: %v", err)
	}
	if ranges, ok := snippets["a.go"]; !ok {
		t.Fatalf("expected snippets for a.go")
	} else if len(ranges) != 0 {
		t.Fatalf("expected empty snippets for a.go, got %v", ranges)
	}
}

func TestFetchFileSnippetsError(t *testing.T) {
	originalRunner := runSnippetDiscovery
	t.Cleanup(func() { runSnippetDiscovery = originalRunner })
	repoRoot := writeTestRepo(t, map[string]string{"a.go": "package a"})
	runSnippetDiscovery = func(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
		return "", errors.New("boom")
	}

	det := ShowFileDetection{IsShowFileRequest: true, Filepaths: []string{"a.go"}, Reason: "check"}
	if _, err := FetchFileSnippets(context.Background(), det, repoRoot, "", nil, false); err == nil {
		t.Fatalf("expected error")
	}
}
