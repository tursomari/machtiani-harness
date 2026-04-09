package session

import (
	"os"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/transcript"
)

func withTempSessionRecorderEnv(t *testing.T) {
	t.Helper()
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Setenv("HOME", tmp)
}

func TestConversationRecorderWriteTurnFallsBackOnDesync(t *testing.T) {
	withTempSessionRecorderEnv(t)

	tr, err := transcript.New("desync-write-turn")
	if err != nil {
		t.Fatalf("transcript.New: %v", err)
	}
	defer tr.Close()

	recorder := newConversationRecorder(tr, "desync-write-turn", formatGoalText("Keep transcript stable", ""), "", false, nil)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	recorder.conversation.AddMessage("user", "external mutation", map[string]any{"type": "user_feedback"})

	if err := recorder.WriteTurn(1, "Question", "", []string{"runner.go"}, "Answer", "ask"); err != nil {
		t.Fatalf("WriteTurn: %v", err)
	}

	content := tr.Content()
	if !strings.Contains(content, "Question") {
		t.Fatalf("transcript missing question after fallback:\n%s", content)
	}
	if !strings.Contains(content, "Answer") {
		t.Fatalf("transcript missing answer after fallback:\n%s", content)
	}
	if !strings.Contains(recorder.JSON(), "external mutation") {
		t.Fatalf("conversation json missing external mutation after fallback: %s", recorder.JSON())
	}
}

func TestConversationRecorderAppendRawFallsBackOnDesync(t *testing.T) {
	withTempSessionRecorderEnv(t)

	tr, err := transcript.New("desync-append-raw")
	if err != nil {
		t.Fatalf("transcript.New: %v", err)
	}
	defer tr.Close()

	recorder := newConversationRecorder(tr, "desync-append-raw", formatGoalText("Keep transcript stable", ""), "", false, nil)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	recorder.conversation.AddMessage("assistant", "out-of-band", map[string]any{"type": "note"})

	if err := recorder.AppendRaw("user", "Need a tighter scope", "goal_update"); err != nil {
		t.Fatalf("AppendRaw: %v", err)
	}

	content := tr.Content()
	if !strings.Contains(content, "=== GOAL UPDATE") {
		t.Fatalf("transcript missing goal-update block after fallback:\n%s", content)
	}
	if !strings.Contains(content, "Need a tighter scope") {
		t.Fatalf("transcript missing goal-update content after fallback:\n%s", content)
	}
	if !strings.Contains(recorder.JSON(), "Need a tighter scope") {
		t.Fatalf("conversation json missing appended raw content after fallback: %s", recorder.JSON())
	}
}
