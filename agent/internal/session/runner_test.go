package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withReadmeFunctionStubs(t *testing.T, head func() (string, error), commit func(string) (string, error), checkout func(string) error) {
	t.Helper()
	originalHead := readmeHeadCommitFn
	originalCommit := readmeCommitForProjectFn
	originalCheckout := readmeCheckoutReadonlyFn
	readmeHeadCommitFn = head
	readmeCommitForProjectFn = commit
	readmeCheckoutReadonlyFn = checkout
	t.Cleanup(func() {
		readmeHeadCommitFn = originalHead
		readmeCommitForProjectFn = originalCommit
		readmeCheckoutReadonlyFn = originalCheckout
	})
}

func TestLoadProjectBackgroundSuccess(t *testing.T) {
	repoRoot := t.TempDir()
	readmePath := filepath.Join(repoRoot, ".machtiani", "artifacts", "readme")
	if err := os.MkdirAll(readmePath, 0o755); err != nil {
		t.Fatalf("failed to create readme directory: %v", err)
	}
	content := "Project background content\nSecond line"
	if err := os.WriteFile(filepath.Join(readmePath, "internal-readme.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write internal README: %v", err)
	}

	withReadmeFunctionStubs(t,
		func() (string, error) { return "commit123", nil },
		func(projectCommit string) (string, error) {
			if projectCommit != "commit123" {
				t.Fatalf("unexpected project commit: %s", projectCommit)
			}
			return "readme456", nil
		},
		func(commit string) error {
			if commit != "commit123" {
				t.Fatalf("unexpected checkout commit: %s", commit)
			}
			return nil
		},
	)

	got, err := loadProjectBackground(repoRoot)
	if err != nil {
		t.Fatalf("loadProjectBackground returned error: %v", err)
	}
	if got != content {
		t.Fatalf("unexpected background content:\nwant: %q\n got: %q", content, got)
	}
}

func TestLoadProjectBackgroundMissingReadme(t *testing.T) {
	repoRoot := t.TempDir()
	withReadmeFunctionStubs(t,
		func() (string, error) { return "commitABC", nil },
		func(projectCommit string) (string, error) {
			return "", errors.New("no readme for commit")
		},
		func(string) error { return nil },
	)

	_, err := loadProjectBackground(repoRoot)
	if err == nil {
		t.Fatalf("expected error when README commit is missing")
	}
	if !strings.Contains(err.Error(), "no readme") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadProjectBackgroundEmptyFile(t *testing.T) {
	repoRoot := t.TempDir()
	readmeDir := filepath.Join(repoRoot, ".machtiani", "artifacts", "readme")
	if err := os.MkdirAll(readmeDir, 0o755); err != nil {
		t.Fatalf("failed to create readme directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readmeDir, "internal-readme.md"), []byte("   \n"), 0o644); err != nil {
		t.Fatalf("failed to write empty README: %v", err)
	}

	withReadmeFunctionStubs(t,
		func() (string, error) { return "commitXYZ", nil },
		func(projectCommit string) (string, error) {
			return "readme000", nil
		},
		func(string) error { return nil },
	)

	_, err := loadProjectBackground(repoRoot)
	if err == nil {
		t.Fatalf("expected error for empty README content")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCountTurnsSkipsBackgroundTurn(t *testing.T) {
	transcript := strings.Join([]string{
		"== Turn 0",
		"background?",
		"Answer: details",
		"== Turn 1",
		"real",
		"Answer: yes",
		"== Turn 2",
		"next",
		"Answer: ok",
	}, "\n")

	if got, want := countTurns(transcript), 2; got != want {
		t.Fatalf("countTurns returned %d, want %d", got, want)
	}

	if got := countTurns("== Turn 0\nbackground\nAnswer: n/a"); got != 0 {
		t.Fatalf("countTurns should ignore lone Turn 0, got %d", got)
	}
}

func TestAnalyzeTagFormatValid(t *testing.T) {
	answer := "Check [src/foo.go | 10:12] and [pkg/bar.go | 1:2]."
	retrieved := []string{"src/foo.go", "pkg/bar.go"}

	stats, warnings := analyzeTagFormat(answer, retrieved)

	if got := stats["tag_format_total"].(int); got != 2 {
		t.Fatalf("expected total 2, got %d", got)
	}
	if got := stats["tag_format_valid"].(int); got != 2 {
		t.Fatalf("expected valid 2, got %d", got)
	}
	if stats["tag_format_detected"].(bool) != true {
		t.Fatalf("expected detected=true")
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
}

func TestAnalyzeTagFormatInvalid(t *testing.T) {
	answer := "Review [src/foo.go | 20:10] and [missing.go | 1:2] plus [bad | a:b]."
	retrieved := []string{"src/foo.go"}

	stats, warnings := analyzeTagFormat(answer, retrieved)

	if got := stats["tag_format_total"].(int); got != 3 {
		t.Fatalf("expected total 3, got %d", got)
	}
	if got := stats["tag_format_invalid"].(int); got != 3 {
		t.Fatalf("expected invalid 3, got %d", got)
	}
	missing := stats["tag_format_missing_paths"].([]string)
	if len(missing) != 1 || missing[0] != "missing.go" {
		t.Fatalf("unexpected missing paths: %v", missing)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warnings for invalid tags")
	}
	foundMissing := false
	for _, w := range warnings {
		if strings.Contains(w, "missing.go") {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatalf("expected warning mentioning missing.go, got %v", warnings)
	}
}
