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

func TestDetectShowFileRequestParsesExplainPrompt(t *testing.T) {
	t.Cleanup(func() { chatWithResolvedFallback = llm.ChatWithResolvedFallback })
	chatWithResolvedFallback = func(ctx context.Context, resolved llm.ResolvedModel, fallbackAliases []string, fallbackResolved []llm.ResolvedModel, extras map[string]any, messages []llm.Message) (string, error) {
		return `{"is_show_file_request":true,"filepaths":["README.md"],"reason":"review readme","include_explain":true,"explain_prompt":"Explain the architecture section."}`, nil
	}

	det, _, err := DetectShowFileRequest(context.Background(), ModelRuntime{}, "Show README.md and explain the architecture section.")
	if err != nil {
		t.Fatalf("DetectShowFileRequest error: %v", err)
	}
	if !det.IsShowFileRequest {
		t.Fatalf("expected is_show_file_request=true")
	}
	if !det.IncludeExplain {
		t.Fatalf("expected include_explain=true")
	}
	if det.ExplainPrompt != "Explain the architecture section." {
		t.Fatalf("unexpected explain_prompt: %q", det.ExplainPrompt)
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

func TestDetectShowFileRequestSkipsWithoutShowCue(t *testing.T) {
	t.Cleanup(func() { chatWithResolvedFallback = llm.ChatWithResolvedFallback })
	chatWithResolvedFallback = func(ctx context.Context, resolved llm.ResolvedModel, fallbackAliases []string, fallbackResolved []llm.ResolvedModel, extras map[string]any, messages []llm.Message) (string, error) {
		t.Fatalf("expected show-file detection to short-circuit before LLM call")
		return "", nil
	}

	det, raw, err := DetectShowFileRequest(context.Background(), ModelRuntime{}, "Explain the planning architecture and agentic loop in Codex.")
	if err != nil {
		t.Fatalf("DetectShowFileRequest error: %v", err)
	}
	if strings.TrimSpace(raw) != "" {
		t.Fatalf("expected empty raw response, got %q", raw)
	}
	if det.IsShowFileRequest {
		t.Fatalf("expected is_show_file_request=false")
	}
}

func TestDetectShowFileRequestSkipsShowContextCue(t *testing.T) {
	t.Cleanup(func() { chatWithResolvedFallback = llm.ChatWithResolvedFallback })
	chatWithResolvedFallback = func(ctx context.Context, resolved llm.ResolvedModel, fallbackAliases []string, fallbackResolved []llm.ResolvedModel, extras map[string]any, messages []llm.Message) (string, error) {
		t.Fatalf("expected show-file detection to short-circuit before LLM call")
		return "", nil
	}

	det, raw, err := DetectShowFileRequest(context.Background(), ModelRuntime{}, "Show relevant context for requested files, then explain the planning loop.")
	if err != nil {
		t.Fatalf("DetectShowFileRequest error: %v", err)
	}
	if strings.TrimSpace(raw) != "" {
		t.Fatalf("expected empty raw response, got %q", raw)
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

func TestParseFullFileTagsSurroundedByText(t *testing.T) {
	cases := []struct {
		name     string
		question string
	}{
		{name: "beginning", question: `<full_file path="a.go" /> please show this`},
		{name: "middle", question: `please show <full_file path="a.go" /> with context`},
		{name: "end", question: `please show this file <full_file path="a.go" />`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := parseFullFileTags(tc.question)
			if len(paths) != 1 || paths[0] != "a.go" {
				t.Fatalf("unexpected parsed paths: %v", paths)
			}
		})
	}
}

func TestParseFullFileTagsCapsAtMax(t *testing.T) {
	question := `<full_file path="a.go" /> <full_file path="b.go" /> <full_file path="c.go" /> <full_file path="d.go" />`
	paths := parseFullFileTags(question)
	if len(paths) != maxFullFileTags {
		t.Fatalf("expected %d paths, got %d", maxFullFileTags, len(paths))
	}
	if paths[len(paths)-1] != "c.go" {
		t.Fatalf("expected c.go as last parsed path, got %v", paths)
	}
}

func TestParseFullFileTagsIgnoresInvalidPaths(t *testing.T) {
	question := `<full_file path="/abs.go" /> <full_file path="../bad.go" /> <full_file path="a.go" />`
	paths := parseFullFileTags(question)
	if len(paths) != 1 || paths[0] != "a.go" {
		t.Fatalf("unexpected parsed paths: %v", paths)
	}
}

func TestFetchFileSnippetsFullFileTagSkipsSnippetDiscovery(t *testing.T) {
	originalRunner := runSnippetDiscovery
	t.Cleanup(func() { runSnippetDiscovery = originalRunner })
	repoRoot := writeTestRepo(t, map[string]string{"a.go": "package a\n"})
	called := false
	runSnippetDiscovery = func(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
		called = true
		return "", errors.New("snippet discovery should not run for full file tags")
	}

	det := ShowFileDetection{
		IsShowFileRequest: true,
		Filepaths:         []string{"a.go"},
		VerbatimQuestion:  `Question: <full_file path="a.go" />`,
	}
	snippets, err := FetchFileSnippets(context.Background(), det, repoRoot, "", nil, false)
	if err != nil {
		t.Fatalf("FetchFileSnippets error: %v", err)
	}
	if called {
		t.Fatalf("expected snippet discovery to be skipped")
	}
	ranges := snippets["a.go"]
	if len(ranges) != 1 || ranges[0].Start != 1 || ranges[0].End != maxFullFileRangeEnd {
		t.Fatalf("unexpected full file snippets: %v", ranges)
	}
}

func TestFetchFileSnippetsFullFileTagUsesReasonWhenVerbatimEmpty(t *testing.T) {
	originalRunner := runSnippetDiscovery
	t.Cleanup(func() { runSnippetDiscovery = originalRunner })
	repoRoot := writeTestRepo(t, map[string]string{"a.go": "package a\n"})
	called := false
	runSnippetDiscovery = func(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
		called = true
		return "", errors.New("snippet discovery should not run for full file tags")
	}

	det := ShowFileDetection{
		IsShowFileRequest: true,
		Filepaths:         []string{"a.go"},
		Reason:            `show please <full_file path="a.go" />`,
	}
	snippets, err := FetchFileSnippets(context.Background(), det, repoRoot, "", nil, false)
	if err != nil {
		t.Fatalf("FetchFileSnippets error: %v", err)
	}
	if called {
		t.Fatalf("expected snippet discovery to be skipped")
	}
	ranges := snippets["a.go"]
	if len(ranges) != 1 || ranges[0].Start != 1 || ranges[0].End != maxFullFileRangeEnd {
		t.Fatalf("unexpected full file snippets: %v", ranges)
	}
}

func TestFetchFileSnippetsFullFileTagMixedWithSnippets(t *testing.T) {
	originalRunner := runSnippetDiscovery
	t.Cleanup(func() { runSnippetDiscovery = originalRunner })
	repoRoot := writeTestRepo(t, map[string]string{"a.go": "package a\n", "b.go": "package b\n"})
	var gotPaths []string
	runSnippetDiscovery = func(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
		gotPaths = append([]string(nil), filepaths...)
		return `{"b.go":[{"start":1,"end":1}]}`, nil
	}

	det := ShowFileDetection{
		IsShowFileRequest: true,
		Filepaths:         []string{"a.go", "b.go"},
		VerbatimQuestion:  `Question: show both <full_file path="a.go" />`,
	}
	snippets, err := FetchFileSnippets(context.Background(), det, repoRoot, "", nil, false)
	if err != nil {
		t.Fatalf("FetchFileSnippets error: %v", err)
	}
	if len(gotPaths) != 1 || gotPaths[0] != "b.go" {
		t.Fatalf("expected snippet discovery to run only for b.go, got %v", gotPaths)
	}
	if fullRanges := snippets["a.go"]; len(fullRanges) != 1 || fullRanges[0].End != maxFullFileRangeEnd {
		t.Fatalf("expected full file ranges for a.go, got %v", fullRanges)
	}
	if bRanges := snippets["b.go"]; len(bRanges) != 1 || bRanges[0].Start != 1 || bRanges[0].End != 1 {
		t.Fatalf("unexpected b.go ranges: %v", bRanges)
	}
}

func TestFetchFileSnippetsFullFileTagTooLargeFallsBack(t *testing.T) {
	originalRunner := runSnippetDiscovery
	t.Cleanup(func() { runSnippetDiscovery = originalRunner })
	largeContent := strings.Repeat("a", maxFullFileBytes+1)
	repoRoot := writeTestRepo(t, map[string]string{"a.go": largeContent})
	called := false
	runSnippetDiscovery = func(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
		called = true
		return `{"a.go":[{"start":1,"end":1}]}`, nil
	}

	det := ShowFileDetection{
		IsShowFileRequest: true,
		Filepaths:         []string{"a.go"},
		VerbatimQuestion:  `Question: <full_file path="a.go" />`,
	}
	snippets, err := FetchFileSnippets(context.Background(), det, repoRoot, "", nil, false)
	if err != nil {
		t.Fatalf("FetchFileSnippets error: %v", err)
	}
	if !called {
		t.Fatalf("expected snippet discovery to run for oversized full file")
	}
	ranges := snippets["a.go"]
	if len(ranges) != 1 || ranges[0].End == maxFullFileRangeEnd {
		t.Fatalf("expected snippet ranges for a.go, got %v", ranges)
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

func TestFormatSnippetsResponseFullFileNoLineNumbers(t *testing.T) {
	repoRoot := writeTestRepo(t, map[string]string{"a.go": "alpha\nbeta\n"})
	snippets := map[string][]LineRange{
		"a.go": {{Start: 1, End: maxFullFileRangeEnd}},
	}
	text, _, _ := FormatSnippetsResponse(snippets, "check", repoRoot)
	if strings.Contains(text, "Lines 1-") {
		t.Fatalf("expected no line range header, got: %s", text)
	}
	if strings.Contains(text, "1: alpha") || strings.Contains(text, "2: beta") {
		t.Fatalf("expected no line numbering, got: %s", text)
	}
	if !strings.Contains(text, "alpha\nbeta") {
		t.Fatalf("expected file contents, got: %s", text)
	}
}
