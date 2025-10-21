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
		"## Turn 0",
		"Question: background?",
		"Answer: details",
		"## Turn 1",
		"Question: real",
		"Answer: yes",
		"## Turn 2",
		"Question: next",
		"Answer: ok",
	}, "\n")

	if got, want := countTurns(transcript), 2; got != want {
		t.Fatalf("countTurns returned %d, want %d", got, want)
	}

	if got := countTurns("## Turn 0\nQuestion: background\nAnswer: n/a"); got != 0 {
		t.Fatalf("countTurns should ignore lone Turn 0, got %d", got)
	}
}
