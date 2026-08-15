package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
)

func TestLoadHistoryReturnsEmptyWithoutSessionID(t *testing.T) {
	t.Setenv("MACHTIANI_SESSION_ID", "")
	t.Setenv("HOME", t.TempDir())

	history, err := LoadHistory()
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("expected empty history, got %d entries", len(history))
	}
}

func TestLoadHistoryDerivesFromConversationJSON(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("MACHTIANI_SESSION_ID", "derived-session")
	// Force global (non-local) artifact layout by pointing cwd at a
	// non-git directory.
	nonGit := t.TempDir()
	prevDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(nonGit); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevDir) })

	conv := conversation.New("test-session", "goal")
	conv.AddMessage("user", "hello from user", nil)
	conv.AddMessage("assistant", "Shall I proceed?", map[string]any{"type": "work_request", "turn": 1})
	conv.AddMessage("assistant", "Findings summarised.", map[string]any{"type": "work_result", "turn": 1})
	conv.AddMessage("assistant", "irrelevant bookkeeping", map[string]any{"type": "patch_validation"})
	conv.AddMessage("assistant", "Final wrap up.", map[string]any{"type": "final_answer"})

	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal conversation: %v", err)
	}

	convPath := filepath.Join(tempHome, ".machtiani", "sessions", "derived-session", "artifacts", "conversation.json")
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	if err := os.WriteFile(convPath, data, 0o644); err != nil {
		t.Fatalf("write conversation: %v", err)
	}

	history, err := LoadHistory()
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history entries (work_request + work_result), got %d: %+v", len(history), history)
	}
	if history[0].Role != "assistant" || history[0].Content != "[work_request] Shall I proceed?" {
		t.Fatalf("unexpected first entry: %+v", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "[work_result] Findings summarised." {
		t.Fatalf("unexpected second entry: %+v", history[1])
	}

	// Legacy on-disk session file must not be written.
	legacyPath := filepath.Join(tempHome, ".machtiani", "sessions", "session-derived-session.json")
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("expected legacy session file to be absent, stat err = %v", err)
	}
}

func TestLoadHistoryMissingConversationFileReturnsEmpty(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("MACHTIANI_SESSION_ID", "missing")
	nonGit := t.TempDir()
	prevDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(nonGit); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevDir) })

	history, err := LoadHistory()
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("expected empty history when conversation missing, got %+v", history)
	}
}
