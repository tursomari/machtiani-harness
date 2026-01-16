package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

	originalPrompt := "Original prompt"
	taskDescription := "Investigate the Goal"
	started, err := startTranscriptIfNeeded(tr, originalPrompt, taskDescription, sessionID, legacyConfig{}, false)
	if err != nil {
		t.Fatalf("startTranscriptIfNeeded error: %v", err)
	}
	if !started {
		t.Fatalf("expected transcript to start")
	}
	content := tr.Content()
	if !strings.Contains(content, originalPrompt) {
		t.Fatalf("header missing original prompt; got %q", content)
	}
	if !strings.Contains(content, "---\n"+taskDescription+"\n---") {
		t.Fatalf("header missing task description block; got %q", content)
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

	originalPrompt := "Original prompt"
	taskDescription := "Investigate the Goal"
	if started, err := startTranscriptIfNeeded(tr, originalPrompt, taskDescription, sessionID, legacyConfig{}, false); err != nil || !started {
		t.Fatalf("initial start failed: started=%v err=%v", started, err)
	}
	first := tr.Content()

	started, err := startTranscriptIfNeeded(tr, originalPrompt, taskDescription, sessionID, legacyConfig{}, true)
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

func TestWritePatchPlanTranscriptEntryDeduplicates(t *testing.T) {
	sessionID := "patch-plan-dedupe"
	path := filepath.Join(t.TempDir(), "agent-transcript.adoc")
	tr, err := transcript.NewWithPath(path, sessionID)
	if err != nil {
		t.Fatalf("NewWithPath error: %v", err)
	}
	defer tr.Close()

	plan := &PatchPlan{Items: []PatchPlanItem{{Description: "first", Complete: false}}}
	if err := writePatchPlanTranscriptEntry(tr, plan, "created"); err != nil {
		t.Fatalf("first writePatchPlanTranscriptEntry error: %v", err)
	}
	plan.Items[0].Complete = true
	if err := writePatchPlanTranscriptEntry(tr, plan, "updated"); err != nil {
		t.Fatalf("second writePatchPlanTranscriptEntry error: %v", err)
	}

	content := tr.Content()
	if strings.Count(content, "PATCH PLAN") != 2 {
		t.Fatalf("expected two patch plan sections, got %d\n%s", strings.Count(content, "PATCH PLAN"), content)
	}
	if !strings.Contains(content, "PATCH PLAN CREATED") {
		t.Fatalf("expected created plan to remain\n%s", content)
	}
	if !strings.Contains(content, "PATCH PLAN UPDATED") {
		t.Fatalf("expected updated plan to remain\n%s", content)
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

	originalPrompt := "Original prompt"
	taskDescription := "Investigate the Goal"
	cfg := legacyConfig{parentSessionID: "parent", includeBackgroundTurn: true}
	started, err := startTranscriptIfNeeded(tr, originalPrompt, taskDescription, sessionID, cfg, false)
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

type stubPatchPlanClient struct {
	generatedPlan *PatchPlan
	updatedPlan   *PatchPlan
	existing      *PatchPlan
	lastPatched   string
}

type collectingNotifier struct {
	messages []string
}

func (c *collectingNotifier) Notify(msg string) {
	c.messages = append(c.messages, msg)
}

func (s *stubPatchPlanClient) GeneratePatchPlan(context.Context, string, string) (*PatchPlan, error) {
	return s.generatedPlan, nil
}

func (s *stubPatchPlanClient) UpdatePatchPlan(_ context.Context, _ string, _ string, existing *PatchPlan, lastPatchedFile string) (*PatchPlan, error) {
	s.existing = existing
	s.lastPatched = lastPatchedFile
	return s.updatedPlan, nil
}

func TestInvokePatchPlanUpdateHookWritesTranscript(t *testing.T) {
	tempDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})
	cmd := exec.Command("git", "init")
	cmd.Dir = tempDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	tr, err := transcript.NewWithPath(filepath.Join(tempDir, "agent-transcript.adoc"), "session-abc")
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	t.Cleanup(func() {
		tr.Close()
	})

	client := &stubPatchPlanClient{
		generatedPlan: &PatchPlan{Items: []PatchPlanItem{{Description: "add docs"}}},
		updatedPlan:   &PatchPlan{Items: []PatchPlanItem{{Description: "add docs", Complete: true}}},
	}
	notifier := &collectingNotifier{}

	ctx := context.Background()
	createdPlan, err := invokePatchPlanUpdateHook(ctx, client, tr, "session-abc", "goal", "transcript body", "", notifier, true)
	if err != nil {
		t.Fatalf("invokePatchPlanUpdateHook (create) error: %v", err)
	}
	if createdPlan == nil {
		t.Fatalf("expected patch plan to be created")
	}
	first := tr.Content()
	if !strings.Contains(first, "PATCH PLAN CREATED") {
		t.Fatalf("expected creation entry in transcript, got %q", first)
	}
	if !strings.Contains(first, `"description": "add docs"`) {
		t.Fatalf("expected plan JSON in transcript, got %q", first)
	}
	if len(notifier.messages) == 0 || !strings.Contains(notifier.messages[0], "created") {
		t.Fatalf("expected notifier to record creation message, got %+v", notifier.messages)
	}

	updatedPlan, err := invokePatchPlanUpdateHook(ctx, client, tr, "session-abc", "goal", "transcript body", "last.txt", notifier, false)
	if err != nil {
		t.Fatalf("invokePatchPlanUpdateHook (update) error: %v", err)
	}
	if updatedPlan == nil {
		t.Fatalf("expected patch plan to be updated")
	}
	updated := tr.Content()
	if !strings.Contains(updated, "PATCH PLAN UPDATED") {
		t.Fatalf("expected update entry in transcript, got %q", updated)
	}
	if !strings.Contains(updated, `"complete": true`) {
		t.Fatalf("expected updated plan JSON in transcript, got %q", updated)
	}
	if len(notifier.messages) < 2 || !strings.Contains(notifier.messages[1], "updated") {
		t.Fatalf("expected notifier to record update message, got %+v", notifier.messages)
	}
	if client.existing == nil || len(client.existing.Items) != 1 {
		t.Fatalf("expected previous plan passed to update, got %+v", client.existing)
	}
	if client.lastPatched != "last.txt" {
		t.Fatalf("expected last patched file recorded, got %q", client.lastPatched)
	}
}
