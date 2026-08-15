package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/runner"
)

func TestDeleteSessionSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-delete-success-%s", runner.GenerateSessionID())
	conv := conversation.New(sessionID, "Test delete goal")
	conv.Goal = "Test delete goal"
	conv.OriginalPrompt = "Test delete prompt"
	conv.Status = "completed"
	conv.TurnsCompleted = 2

	marshaled, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal conversation: %v", err)
	}
	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("mkdir conv dir: %v", err)
	}
	if err := os.WriteFile(convPath, marshaled, 0o644); err != nil {
		t.Fatalf("write conversation: %v", err)
	}

	sessionDir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("SessionDirectory: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(sessionDir, "trajectory"), 0o755); err != nil {
		t.Fatalf("mkdir session trajectory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "trajectory", "agent.jsonl"), []byte("agent\n"), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	if err := DeleteSession(sessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("session dir should not exist: %v", err)
	}
}

func TestDeleteSessionActiveRefused(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-delete-active-%s", runner.GenerateSessionID())
	conv := conversation.New(sessionID, "Active delete goal")
	conv.Goal = "Active delete goal"
	conv.OriginalPrompt = "Active delete prompt"
	conv.Status = "active"
	marshaled, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal conversation: %v", err)
	}
	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("mkdir conv dir: %v", err)
	}
	if err := os.WriteFile(convPath, marshaled, 0o644); err != nil {
		t.Fatalf("write conversation: %v", err)
	}

	scratchDir, err := artifacts.SessionScratchDirectory(sessionID)
	if err != nil {
		t.Fatalf("SessionScratchDirectory: %v", err)
	}
	lock, err := acquireSessionLock(sessionID, scratchDir)
	if err != nil {
		t.Fatalf("acquireSessionLock: %v", err)
	}
	defer lock.Close()

	sessionDir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("SessionDirectory: %v", err)
	}
	if _, err := os.Stat(sessionDir); err != nil {
		t.Fatalf("setup session dir: %v", err)
	}

	if err := DeleteSession(sessionID); err == nil {
		t.Fatalf("expected error for active session, got nil")
	}

	if _, err := os.Stat(sessionDir); err != nil {
		t.Fatalf("expected session dir to remain: %v", err)
	}
}

func TestDeleteSessionNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("nonexistent-%s", runner.GenerateSessionID())

	if err := DeleteSession(sessionID); err == nil {
		t.Fatalf("expected error for non-existent session, got nil")
	}
}
