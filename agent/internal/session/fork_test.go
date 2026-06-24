package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/runner"
)

func TestForkSessionNonExistentSource(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	_, err := ForkSession("nonexistent-session-id")
	if err == nil {
		t.Fatalf("expected error for nonexistent source session, got nil")
	}
}

func TestForkSessionActiveSource(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sourceID := fmt.Sprintf("test-fork-active-%s", runner.GenerateSessionID())
	state := SessionState{
		SessionID:      sourceID,
		Goal:           "Test goal",
		OriginalPrompt: "Test prompt",
		Status:         "active",
		TurnsCompleted: 1,
	}
	if err := SaveSessionState(state); err != nil {
		t.Fatalf("SaveSessionState: %v", err)
	}

	scratchDir, err := artifacts.SessionScratchDirectory(sourceID)
	if err != nil {
		t.Fatalf("SessionScratchDirectory: %v", err)
	}

	lock, err := acquireSessionLock(sourceID, scratchDir)
	if err != nil {
		t.Fatalf("acquireSessionLock: %v", err)
	}
	defer lock.Close()

	_, err = ForkSession(sourceID)
	if err == nil {
		t.Fatalf("expected error for active source session, got nil")
	}
	if err.Error() == "" {
		t.Fatalf("expected non-empty error message")
	}
}

func TestForkSessionSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sourceID := fmt.Sprintf("test-fork-source-%s", runner.GenerateSessionID())
	state := SessionState{
		SessionID:       sourceID,
		Goal:            "Original goal",
		OriginalPrompt:  "Original prompt",
		TaskDescription: "Test task",
		Status:          "completed",
		TurnsCompleted:  3,
	}
	if err := SaveSessionState(state); err != nil {
		t.Fatalf("SaveSessionState: %v", err)
	}

	conv := conversation.New(sourceID, "Original goal")
	conv.AddMessage("assistant", "Hello", nil)

	marshaled, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal conversation: %v", err)
	}

	convPath, err := artifacts.SessionConversationFile(sourceID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("mkdir conv dir: %v", err)
	}
	if err := os.WriteFile(convPath, marshaled, 0o644); err != nil {
		t.Fatalf("write conversation: %v", err)
	}

	newID, err := ForkSession(sourceID)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}

	if newID == "" {
		t.Fatalf("expected non-empty new session ID")
	}
	if newID == sourceID {
		t.Fatalf("expected new session ID to differ from source, got %s", newID)
	}

	// Verify forked conversation.json.
	forkedConvPath, err := artifacts.SessionConversationFile(newID)
	if err != nil {
		t.Fatalf("SessionConversationFile forked: %v", err)
	}
	forkedConvData, err := os.ReadFile(forkedConvPath)
	if err != nil {
		t.Fatalf("read forked conversation: %v", err)
	}
	forkedConv, err := conversation.Unmarshal(forkedConvData)
	if err != nil {
		t.Fatalf("unmarshal forked conversation: %v", err)
	}
	if forkedConv.SessionID != newID {
		t.Fatalf("forked conversation session ID: got %s want %s", forkedConv.SessionID, newID)
	}
	if forkedConv.OriginalGoal != "Original goal" {
		t.Fatalf("forked conversation original goal: got %q want %q", forkedConv.OriginalGoal, "Original goal")
	}
	if len(forkedConv.Messages) != 2 {
		t.Fatalf("forked conversation messages: got %d want 2", len(forkedConv.Messages))
	}

	// Verify source session state is unmodified.
	sourceState, err := LoadSessionState(sourceID)
	if err != nil {
		t.Fatalf("LoadSessionState source: %v", err)
	}
	if sourceState.SessionID != sourceID {
		t.Fatalf("source state session ID: got %s want %s", sourceState.SessionID, sourceID)
	}
	if sourceState.Goal != "Original goal" {
		t.Fatalf("source state goal modified: got %q want %q", sourceState.Goal, "Original goal")
	}
	if sourceState.TurnsCompleted != 3 {
		t.Fatalf("source state turns completed modified: got %d want 3", sourceState.TurnsCompleted)
	}

	// Verify source conversation is unmodified.
	sourceConvData, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("read source conversation: %v", err)
	}
	sourceConv, err := conversation.Unmarshal(sourceConvData)
	if err != nil {
		t.Fatalf("unmarshal source conversation: %v", err)
	}
	if sourceConv.SessionID != sourceID {
		t.Fatalf("source conversation session ID modified: got %s want %s", sourceConv.SessionID, sourceID)
	}
	if sourceConv.OriginalGoal != "Original goal" {
		t.Fatalf("source conversation original goal modified: got %q want %q", sourceConv.OriginalGoal, "Original goal")
	}
	if len(sourceConv.Messages) != 2 {
		t.Fatalf("source conversation messages modified: got %d want 2", len(sourceConv.Messages))
	}

	// Cleanup forked session directory.
	srcDir, _ := artifacts.SessionDirectory(sourceID)
	dstDir, _ := artifacts.SessionDirectory(newID)
	t.Cleanup(func() {
		_ = RemoveSessionState(sourceID)
		_ = RemoveSessionState(newID)
		_ = os.RemoveAll(srcDir)
		_ = os.RemoveAll(dstDir)
	})
}

func TestForkSessionSkipsLockFiles(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sourceID := fmt.Sprintf("test-fork-locks-%s", runner.GenerateSessionID())
	state := SessionState{
		SessionID:      sourceID,
		Goal:           "Lock test goal",
		OriginalPrompt: "Lock test prompt",
		Status:         "completed",
		TurnsCompleted: 1,
	}
	if err := SaveSessionState(state); err != nil {
		t.Fatalf("SaveSessionState: %v", err)
	}

	// Write a lock file inside the source session directory.
	srcDir, err := artifacts.SessionDirectory(sourceID)
	if err != nil {
		t.Fatalf("SessionDirectory: %v", err)
	}
	lockPath := filepath.Join(srcDir, "session.lock")
	if err := os.WriteFile(lockPath, []byte("fake lock content"), 0o644); err != nil {
		t.Fatalf("write session.lock: %v", err)
	}

	// Create a minimal conversation file (required by ForkSession).
	conv := conversation.New(sourceID, "Lock test goal")
	marshaled, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal conversation: %v", err)
	}
	convPath, err := artifacts.SessionConversationFile(sourceID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("mkdir conv dir: %v", err)
	}
	if err := os.WriteFile(convPath, marshaled, 0o644); err != nil {
		t.Fatalf("write conversation: %v", err)
	}

	newID, err := ForkSession(sourceID)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if newID == "" {
		t.Fatalf("expected non-empty new session ID")
	}

	// Verify lock file was NOT copied to destination.
	dstDir, err := artifacts.SessionDirectory(newID)
	if err != nil {
		t.Fatalf("SessionDirectory forked: %v", err)
	}
	dstLockPath := filepath.Join(dstDir, "session.lock")
	if _, err := os.Stat(dstLockPath); err == nil {
		t.Fatalf("expected session.lock to NOT be copied to destination: %s", dstLockPath)
	}

	t.Cleanup(func() {
		_ = RemoveSessionState(sourceID)
		_ = RemoveSessionState(newID)
		_ = os.RemoveAll(srcDir)
		_ = os.RemoveAll(dstDir)
	})
}

// Ensure encoding/json is used (satisfies the import requirement).
var _ = json.Marshal
