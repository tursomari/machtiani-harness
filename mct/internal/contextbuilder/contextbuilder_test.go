package contextbuilder

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIncludesConversationHistory(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "Initial goal"},
		{Role: "assistant", Content: "First answer", Files: []string{"file.go"}},
	}

	combined, included := Build("Follow-up question?", nil, history, Options{IncludeHistory: true})

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

	combined, _ := Build("Follow-up question?", nil, history, Options{})

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

	combined, included := Build("Check file", []string{"src/main.go"}, nil, Options{})
	if len(included) != 1 || included[0] != "src/main.go" {
		t.Fatalf("expected included files to contain src/main.go, got %v", included)
	}
	if !strings.Contains(combined, "### src/main.go") {
		t.Fatalf("expected prompt to include file header, got %q", combined)
	}
	if !strings.Contains(combined, fileContent) {
		t.Fatalf("expected prompt to include file content, got %q", combined)
	}
	if strings.Contains(combined, "[ERROR: could not read file]") {
		t.Fatalf("did not expect file read error in prompt: %q", combined)
	}
}
