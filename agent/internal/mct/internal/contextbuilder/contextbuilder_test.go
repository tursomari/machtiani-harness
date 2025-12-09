package contextbuilder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

const testConversationTemplate = `{{- if .IncludeHistory -}}
Conversation History:
{{- range .History }}
{{.Index}}. {{.DisplayRole}}{{if .Files}} (Files: {{join .Files ", "}}){{end}}:
{{.Content}}{{- end}}
Current Request:
{{.UserPrompt}}
{{- else -}}
{{.UserPrompt}}
{{- end }}`

func withPrelude(opts Options) Options {
	if strings.TrimSpace(opts.PreludeTemplate) == "" {
		opts.PreludeTemplate = testConversationTemplate
	}
	return opts
}

func TestBuildIncludesConversationHistory(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "Initial goal"},
		{Role: "assistant", Content: "First answer", Files: []string{"file.go"}},
	}

	combined, included, err := Build("Follow-up question?", nil, history, withPrelude(Options{IncludeHistory: true}))
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if len(included) != 0 {
		t.Fatalf("expected no included files, got %v", included)
	}

	for _, snippet := range []string{
		"Conversation History:",
		"1. User:\nInitial goal",
		"2. Assistant (Files: file.go):\nFirst answer",
		"Current Request:\nFollow-up question?",
	} {
		if !strings.Contains(combined, snippet) {
			t.Fatalf("expected combined prompt to contain %q, got %q", snippet, combined)
		}
	}
}

func TestBuildSkipsHistoryWhenNotRequested(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "Initial goal"},
		{Role: "assistant", Content: "First answer", Files: []string{"file.go"}},
	}

	combined, _, err := Build("Follow-up question?", nil, history, withPrelude(Options{}))
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if strings.Contains(combined, "Conversation History:") {
		t.Fatalf("did not expect conversation history, got %q", combined)
	}
	if !strings.Contains(combined, "Follow-up question?") {
		t.Fatalf("expected combined prompt to contain the current request, got %q", combined)
	}
}

func TestBuildResolvesPathsFromRepoRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	repoDir := t.TempDir()
	srcDir := filepath.Join(repoDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("failed to create src dir: %v", err)
	}

	filePath := filepath.Join(srcDir, "main.go")
	fileContent := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filePath, []byte(fileContent), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v, output: %s", err, string(output))
	}

	subDir := filepath.Join(repoDir, "nested")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("failed to change dir: %v", err)
	}
	t.Cleanup(func() {
		if chErr := os.Chdir(prevWD); chErr != nil {
			t.Errorf("failed to restore working dir: %v", chErr)
		}
	})

	combined, included, err := Build("Check file", []string{"src/main.go"}, nil, withPrelude(Options{}))
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(included) != 1 || included[0] != "src/main.go" {
		t.Fatalf("expected included files to contain src/main.go, got %v", included)
	}
	if !strings.Contains(combined, "### src/main.go") {
		t.Fatalf("expected prompt to include file header, got %q", combined)
	}
	if !strings.Contains(combined, "```go 1:3") {
		t.Fatalf("expected prompt to include go code fence with line range, got %q", combined)
	}
	if !strings.Contains(combined, "\npackage main\n") {
		t.Fatalf("expected prompt to contain package declaration, got %q", combined)
	}
	if !strings.Contains(combined, "\n\nfunc main() {}") {
		t.Fatalf("expected prompt to contain function body, got %q", combined)
	}
	if strings.Contains(combined, "[ERROR: could not read file]") {
		t.Fatalf("did not expect file read error in prompt: %q", combined)
	}
}

func TestBuildAppliesTokenLimit(t *testing.T) {
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

	lines := make([]string, 5)
	for i := 1; i <= 5; i++ {
		lines[i-1] = fmt.Sprintf("alpha bravo charlie delta echo foxtrot golf hotel %d", i)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile("sample.txt", []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	basePrompt, _, err := Build("Hello", []string{"sample.txt"}, nil, withPrelude(Options{}))
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	baseTokens := llm.EstimateTokens(basePrompt)

	lineFive := lines[4] + "\n"
	lineFiveTokens := llm.EstimateTokens(lineFive)
	limit := baseTokens - (lineFiveTokens / 2)
	if limit <= 0 {
		t.Fatalf("unexpected token limit: %d", limit)
	}

	limitedPrompt, included, err := Build("Hello", []string{"sample.txt"}, nil, withPrelude(Options{MaxInputTokens: limit}))
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(included) != 1 || included[0] != "sample.txt" {
		t.Fatalf("expected included sample.txt, got %v", included)
	}

	limitedTokens := llm.EstimateTokens(limitedPrompt)
	if limitedTokens > limit {
		t.Fatalf("expected prompt tokens <= %d, got %d", limit, limitedTokens)
	}

	if !strings.Contains(limitedPrompt, "TRUNCATED") {
		t.Fatalf("expected truncation stamp, got\n%s", limitedPrompt)
	}
	stampIdx := strings.Index(limitedPrompt, "omitted lines ")
	if stampIdx == -1 {
		t.Fatalf("expected omitted lines stamp, got\n%s", limitedPrompt)
	}
	stampLine := limitedPrompt[stampIdx:]
	stampLine = strings.SplitN(stampLine, "\n", 2)[0]
	rangePart := strings.TrimPrefix(stampLine, "omitted lines ")
	parts := strings.Split(rangePart, "...")
	if len(parts) != 2 {
		t.Fatalf("unexpected stamp format: %s", stampLine)
	}
	startVal, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		t.Fatalf("invalid start line in stamp: %v", err)
	}
	endVal, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		t.Fatalf("invalid end line in stamp: %v", err)
	}
	if endVal != 5 {
		t.Fatalf("expected end line 5 in stamp, got %d", endVal)
	}
	if startVal < 1 || startVal > 5 {
		t.Fatalf("unexpected start line %d", startVal)
	}
	if strings.Contains(limitedPrompt, lineFive) {
		t.Fatalf("expected last line to be truncated, got\n%s", limitedPrompt)
	}
}
