package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/transcript"
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
		"=== Answer",
		"",
		"details",
		"",
		"== Turn 1",
		"real",
		"=== Answer",
		"",
		"yes",
		"",
		"== Turn 2",
		"next",
		"=== Answer",
		"",
		"ok",
	}, "\n")

	if got, want := countTurns(transcript), 2; got != want {
		t.Fatalf("countTurns returned %d, want %d", got, want)
	}

	if got := countTurns("== Turn 0\nbackground\n=== Answer\n\nn/a"); got != 0 {
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

func TestStartTranscriptIfNeededWritesHeader(t *testing.T) {
	sessionID := "child-session"
	path := filepath.Join(t.TempDir(), "agent-transcript.adoc")
	tr, err := transcript.NewWithPath(path, sessionID)
	if err != nil {
		t.Fatalf("NewWithPath error: %v", err)
	}
	defer tr.Close()

	goal := "***Investigate the Goal***\n\nTask details: Investigate the Goal\n\nOriginal prompt"
	started, err := startTranscriptIfNeeded(tr, goal, sessionID, legacyConfig{}, false)
	if err != nil {
		t.Fatalf("startTranscriptIfNeeded error: %v", err)
	}
	if !started {
		t.Fatalf("expected transcript to start")
	}
	content := tr.Content()
	if !strings.Contains(content, "Investigate the Goal") {
		t.Fatalf("header missing description; got %q", content)
	}
	if !strings.Contains(content, "== PROBLEM:\n\nOriginal prompt") {
		t.Fatalf("header missing problem context; got %q", content)
	}
}

func TestStartTranscriptIfNeededSkipsWhenResuming(t *testing.T) {
	sessionID := "resume-session"
	path := filepath.Join(t.TempDir(), "agent-transcript.adoc")
	tr, err := transcript.NewWithPath(path, sessionID)
	if err != nil {
		t.Fatalf("NewWithPath error: %v", err)
	}
	defer tr.Close()

	goal := "***Investigate the Goal***\n\nOriginal prompt"
	if started, err := startTranscriptIfNeeded(tr, goal, sessionID, legacyConfig{}, false); err != nil || !started {
		t.Fatalf("initial start failed: started=%v err=%v", started, err)
	}
	first := tr.Content()

	started, err := startTranscriptIfNeeded(tr, goal, sessionID, legacyConfig{}, true)
	if err != nil {
		t.Fatalf("resume startTranscriptIfNeeded error: %v", err)
	}
	if started {
		t.Fatalf("expected resume call to skip header write")
	}
	if got := tr.Content(); got != first {
		t.Fatalf("expected transcript content unchanged on resume; got %q want %q", got, first)
	}
}

func TestStartTranscriptIfNeededChildIncludesBackgroundWhenRequested(t *testing.T) {
	sessionID := "child-with-bg"
	path := filepath.Join(t.TempDir(), "agent-transcript.adoc")
	tr, err := transcript.NewWithPath(path, sessionID)
	if err != nil {
		t.Fatalf("NewWithPath error: %v", err)
	}
	defer tr.Close()

	goal := "***Investigate the Goal***\n\nTask details: Investigate the Goal\n\nOriginal prompt"
	cfg := legacyConfig{parentSessionID: "parent", includeBackgroundTurn: true}
	started, err := startTranscriptIfNeeded(tr, goal, sessionID, cfg, false)
	if err != nil {
		t.Fatalf("startTranscriptIfNeeded error: %v", err)
	}
	if !started {
		t.Fatalf("expected transcript to start")
	}
	if err := writeInitialBackgroundIfNeeded(tr, ".", cfg, true, started); err != nil {
		t.Fatalf("writeInitialBackgroundIfNeeded error: %v", err)
	}
	content := tr.Content()
	if !strings.Contains(content, backgroundQuestionPrompt) {
		t.Fatalf("expected background question in transcript; got %q", content)
	}
	if !strings.Contains(content, backgroundFallbackAnswer) {
		t.Fatalf("expected fallback background answer in transcript; got %q", content)
	}
}
