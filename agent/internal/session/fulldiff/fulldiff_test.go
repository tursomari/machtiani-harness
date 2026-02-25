package fulldiff

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/transcript"
)

type stubTranscript struct {
	deduped []string
	writes  []stubWrite
}

type stubWrite struct {
	step     int
	question string
	files    []string
	summary  string
	decision string
}

func (s *stubTranscript) DeduplicateFullDiffByFile(path string) error {
	s.deduped = append(s.deduped, path)
	return nil
}

func (s *stubTranscript) WriteTurn(step int, question, savedPath string, retrieved []string, summary string, decision string) error {
	s.writes = append(s.writes, stubWrite{step: step, question: question, files: retrieved, summary: summary, decision: decision})
	return nil
}

type stubTracker struct {
	state map[string]string
}

func (s *stubTracker) ShouldDeduplicateFile(path string, contentHash string) bool {
	if s.state == nil {
		s.state = map[string]string{}
	}
	return s.state[path] == contentHash
}

func (s *stubTracker) UpdateDeduplicationState(path string, contentHash string) {
	if s.state == nil {
		s.state = map[string]string{}
	}
	s.state[path] = contentHash
}

func TestStubTrackerPerFileDedup(t *testing.T) {
	tracker := &stubTracker{}
	if tracker.ShouldDeduplicateFile("a.txt", "hash1") {
		t.Fatalf("expected no dedup hit for unseen file")
	}
	tracker.UpdateDeduplicationState("a.txt", "hash1")
	if !tracker.ShouldDeduplicateFile("a.txt", "hash1") {
		t.Fatalf("expected dedup hit for same file+hash")
	}
	if tracker.ShouldDeduplicateFile("b.txt", "hash1") {
		t.Fatalf("expected other file not to dedup")
	}
}

func TestInjectRemovesPriorFullDiffsForSameFile(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	cmds := [][]string{
		{"git", "-C", repo, "init"},
		{"git", "-C", repo, "config", "user.email", "test@example.com"},
		{"git", "-C", repo, "config", "user.name", "test"},
	}
	for _, args := range cmds {
		if err := exec.Command(args[0], args[1:]...).Run(); err != nil {
			t.Fatalf("git setup failed: %v", err)
		}
	}

	baselineContent := "old content\n"
	workspaceFile := filepath.Join(repo, "README.md")
	if err := os.WriteFile(workspaceFile, []byte(baselineContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "add", "README.md").Run(); err != nil {
		t.Fatalf("git add failed: %v", err)
	}
	if err := exec.Command("git", "-C", repo, "commit", "-m", "init").Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	baseline, err := patchersvc.EnsureBaseline("sess-full-diff", repo, time.Now())
	if err != nil {
		t.Fatalf("baseline capture failed: %v", err)
	}

	tr, err := transcript.NewWithPath(filepath.Join(repo, "transcript.adoc"), "sess-full-diff")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	fullDiffNote := transcript.FullDiffNoteText(nil)
	if err := os.WriteFile(workspaceFile, []byte("first change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Inject(4, repo, []string{"README.md"}, tr, nil, Options{Baseline: baseline, FullDiffNote: fullDiffNote})

	if err := os.WriteFile(workspaceFile, []byte("second change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Inject(6, repo, []string{"README.md"}, tr, nil, Options{Baseline: baseline, FullDiffNote: fullDiffNote})

	content := tr.Content()
	if count := strings.Count(content, "File: README.md"); count != 1 {
		t.Fatalf("expected single full diff, got %d:\n%s", count, content)
	}
	if !strings.Contains(content, "Full diff:") {
		t.Fatalf("expected full diff label to remain:\n%s", content)
	}
	if fullDiffNote == "" {
		t.Fatalf("expected default full diff note to be set")
	}
	if !strings.Contains(content, strings.SplitN(fullDiffNote, "\n", 2)[0]) {
		t.Fatalf("expected full diff note to be present:\n%s", content)
	}
	if strings.Contains(content, "Retrieved File Paths:") {
		t.Fatalf("did not expect retrieved file paths in full diff turn:\n%s", content)
	}
	if strings.Contains(content, "Planner decision: full_diff") {
		t.Fatalf("did not expect planner decision tag in full diff turn:\n%s", content)
	}
	if !strings.Contains(content, "second change") {
		t.Fatalf("expected latest diff content to remain:\n%s", content)
	}
	if strings.Contains(content, "== TURN 5") {
		t.Fatalf("expected earlier full diff turn to be removed:\n%s", content)
	}
}
