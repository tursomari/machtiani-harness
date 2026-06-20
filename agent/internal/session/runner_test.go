package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/transcript"
)

func TestIsLocalSessionEnvironment(t *testing.T) {
	tests := []struct {
		name string
		cfg  *llm.Config
		want bool
	}{
		{name: "nil config defaults local", cfg: nil, want: true},
		{name: "nil environment defaults local", cfg: &llm.Config{}, want: true},
		{name: "empty environment type defaults local", cfg: &llm.Config{Environment: &llm.EnvironmentConfig{}}, want: true},
		{name: "local environment", cfg: &llm.Config{Environment: &llm.EnvironmentConfig{Type: "local"}}, want: true},
		{name: "case insensitive local environment", cfg: &llm.Config{Environment: &llm.EnvironmentConfig{Type: " LoCaL "}}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLocalSessionEnvironment(tt.cfg); got != tt.want {
				t.Fatalf("isLocalSessionEnvironment() = %v, want %v", got, tt.want)
			}
		})
	}
}

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
	cfg := legacyConfig{}
	started, err := startTranscriptIfNeeded(tr, originalPrompt, taskDescription, sessionID, cfg, false)
	if err != nil {
		t.Fatalf("startTranscriptIfNeeded error: %v", err)
	}
	if !started {
		t.Fatalf("expected transcript to start")
	}
	if err := writeInitialBackgroundIfNeeded(tr, ".", cfg, started, nil, os.Stderr); err != nil {
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

func TestConversationDualWriteMatchesLegacyTranscript(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	sessionID := "conv-sess"
	goal := "Finish documentation"
	formattedGoal := formatGoalText(goal, "")

	legacy, err := transcript.New("legacy-" + sessionID)
	if err != nil {
		t.Fatalf("legacy transcript init: %v", err)
	}
	if err := legacy.WriteHeader(goal, "", sessionID, nil); err != nil {
		legacy.Close()
		t.Fatalf("legacy WriteHeader: %v", err)
	}
	if err := legacy.WriteTurn(1, "What should we document?", "/tmp/chat.md", []string{"README.md"}, "Document usage", "ask"); err != nil {
		legacy.Close()
		t.Fatalf("legacy WriteTurn: %v", err)
	}
	expected := legacy.Content()
	legacy.Close()

	conv := conversation.New(sessionID, formattedGoal)
	conversationRendered, err := conv.ToTranscript()
	if err != nil {
		t.Fatalf("conversation ToTranscript: %v", err)
	}

	tr, err := transcript.New(sessionID)
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	defer tr.Close()
	if err := tr.WriteHeader(goal, "", sessionID, nil); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}

	renderDelta := func() (string, string, error) {
		rendered, err := conv.ToTranscript()
		if err != nil {
			return "", "", err
		}
		if conversationRendered != "" && !strings.HasPrefix(rendered, conversationRendered) {
			return rendered, "", errors.New("conversation render lost prefix")
		}
		return rendered, rendered[len(conversationRendered):], nil
	}

	conv.AddMessage("assistant", "What should we document?", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", "Document usage", map[string]any{"type": "answer", "turn": 1, "chat_path": "/tmp/chat.md", "retrieved_files": []string{"README.md"}})
	rendered, delta, err := renderDelta()
	if err != nil {
		t.Fatalf("renderDelta error: %v", err)
	}
	if err := tr.AppendBlock(delta); err != nil {
		t.Fatalf("AppendBlock error: %v", err)
	}
	conversationRendered = rendered

	if got := tr.Content(); got != expected {
		t.Fatalf("transcript mismatch:\nwant:\n%s\n----\n got:\n%s", expected, got)
	}
}

func TestFormatGoalTextOmitsTaskDescription(t *testing.T) {
	goal := "Finish documentation"
	formatted := formatGoalText(goal, "Task: focus on README links")
	if formatted != goal {
		t.Fatalf("expected clean goal %q, got %q", goal, formatted)
	}
}

func TestConversationResumeRestoresTranscript(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	sessionID := "resume-conv"
	goal := "Stabilize builds"
	formattedGoal := formatGoalText(goal, "")

	legacy, err := transcript.New(sessionID)
	if err != nil {
		t.Fatalf("legacy transcript init: %v", err)
	}
	if err := legacy.WriteHeader(goal, "", sessionID, nil); err != nil {
		legacy.Close()
		t.Fatalf("legacy WriteHeader: %v", err)
	}
	if err := legacy.WriteTurn(1, "Check pipeline?", "/tmp/chat.md", []string{"pipeline.md"}, "Yes", "ask"); err != nil {
		legacy.Close()
		t.Fatalf("legacy WriteTurn: %v", err)
	}
	expected := legacy.Content()
	legacyPath := legacy.Path()
	legacy.Close()

	conv := conversation.New(sessionID, formattedGoal)
	conv.AddMessage("assistant", "Check pipeline?", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", "Yes", map[string]any{"type": "answer", "turn": 1, "chat_path": "/tmp/chat.md", "retrieved_files": []string{"pipeline.md"}})
	convJSON, err := conv.Marshal()
	if err != nil {
		t.Fatalf("Marshal conversation: %v", err)
	}

	convLoaded, err := conversation.Unmarshal(convJSON)
	if err != nil {
		t.Fatalf("Unmarshal conversation: %v", err)
	}
	rendered, err := convLoaded.ToTranscript()
	if err != nil {
		t.Fatalf("ToTranscript: %v", err)
	}

	restored, err := transcript.NewWithPath(legacyPath, sessionID)
	if err != nil {
		t.Fatalf("NewWithPath: %v", err)
	}
	defer restored.Close()
	if err := restored.Restore(rendered); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if got := restored.Content(); got != expected {
		t.Fatalf("restored transcript mismatch:\nwant:\n%s\n----\n got:\n%s", expected, got)
	}
}

func TestRestoreTranscriptFromConversationRewritesStaleContent(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	sessionID := "stale-restore"
	goal := "Keep transcripts consistent"
	formattedGoal := formatGoalText(goal, "")

	conv := conversation.New(sessionID, formattedGoal)
	convRendered, err := conv.ToTranscript()
	if err != nil {
		t.Fatalf("ToTranscript: %v", err)
	}

	tr, err := transcript.New(sessionID)
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	defer tr.Close()

	if err := tr.Restore("stale transcript\n"); err != nil {
		t.Fatalf("seed stale transcript: %v", err)
	}

	if err := restoreTranscriptFromConversation(tr, convRendered, true); err != nil {
		t.Fatalf("restoreTranscriptFromConversation: %v", err)
	}

	if tr.Content() != convRendered {
		t.Fatalf("transcript not rewritten from conversation:\nwant:\n%s\n----\n got:\n%s", convRendered, tr.Content())
	}
	data, err := os.ReadFile(tr.Path())
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if string(data) != convRendered {
		t.Fatalf("on-disk transcript not rewritten; got:\n%s", string(data))
	}
}

func TestRestoreTranscriptFromConversationIncludesFinalConclusion(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	sessionID := "final-restore"
	goal := "Keep transcripts consistent"
	formattedGoal := formatGoalText(goal, "")

	conv := conversation.New(sessionID, formattedGoal)
	conv.AddMessage("assistant", "Final answer", map[string]any{"type": "final", "turns": 2, "capped": false})
	convRendered, err := conv.ToTranscript()
	if err != nil {
		t.Fatalf("ToTranscript: %v", err)
	}

	tr, err := transcript.New(sessionID)
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	defer tr.Close()

	if err := tr.Restore("stale transcript\n"); err != nil {
		t.Fatalf("seed stale transcript: %v", err)
	}

	if err := restoreTranscriptFromConversation(tr, convRendered, true); err != nil {
		t.Fatalf("restoreTranscriptFromConversation: %v", err)
	}

	if !strings.Contains(tr.Content(), "== CONCLUSION") {
		t.Fatalf("restored transcript missing conclusion:\n%s", tr.Content())
	}
	if !strings.Contains(tr.Content(), "Final answer") {
		t.Fatalf("restored transcript missing final answer:\n%s", tr.Content())
	}
	if tr.Content() != convRendered {
		t.Fatalf("transcript not rewritten from conversation:\nwant:\n%s\n----\n got:\n%s", convRendered, tr.Content())
	}
}
