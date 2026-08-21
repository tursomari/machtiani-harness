package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/runner"
)

func TestForkSessionNonExistentSource(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	_, err := ForkSession("nonexistent-session-id", "")
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

	conv := conversation.New(sourceID, "Test goal")
	conv.Goal = "Test goal"
	conv.OriginalPrompt = "Test prompt"
	conv.Status = "active"
	conv.TurnsCompleted = 1
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

	scratchDir, err := artifacts.SessionScratchDirectory(sourceID)
	if err != nil {
		t.Fatalf("SessionScratchDirectory: %v", err)
	}

	lock, err := acquireSessionLock(sourceID, scratchDir)
	if err != nil {
		t.Fatalf("acquireSessionLock: %v", err)
	}
	defer lock.Close()

	_, err = ForkSession(sourceID, "")
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

	conv := conversation.New(sourceID, "Original goal")
	conv.Goal = "Original goal"
	conv.OriginalPrompt = "Original prompt"
	conv.TaskDescription = "Test task"
	conv.Status = "completed"
	conv.TurnsCompleted = 3
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
	srcDir, err := artifacts.SessionDirectory(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(srcDir, "trajectory", "agent.jsonl"):           "agent\n",
		filepath.Join(srcDir, "artifacts", "llm", "inputs.jsonl"):    "full input\n",
		filepath.Join(srcDir, "shell-agent", "1", "trajectory.json"): "trajectory\n",
		filepath.Join(srcDir, "shell-agent", "1", "state.json"):      "state\n",
		filepath.Join(srcDir, "shell-agent", "2", "state.json"):      "state-only\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	newID, err := ForkSession(sourceID, "")
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
	dstDir, err := artifacts.SessionDirectory(newID)
	if err != nil {
		t.Fatal(err)
	}
	for _, retained := range []string{
		filepath.Join(dstDir, "trajectory", "agent.jsonl"),
		filepath.Join(dstDir, "shell-agent", "1", "trajectory.json"),
	} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("retained fork path missing %s: %v", retained, err)
		}
	}
	for _, excluded := range []string{
		filepath.Join(dstDir, "artifacts", "llm"),
		filepath.Join(dstDir, "shell-agent", "1", "state.json"),
		filepath.Join(dstDir, "shell-agent", "2"),
	} {
		if _, err := os.Lstat(excluded); !os.IsNotExist(err) {
			t.Fatalf("disposable fork path exists %s: %v", excluded, err)
		}
	}

	// Verify source conversation state from disk.
	reloadedConvData, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("read source conversation: %v", err)
	}
	reloadedConv, err := conversation.Unmarshal(reloadedConvData)
	if err != nil {
		t.Fatalf("unmarshal source conversation: %v", err)
	}
	if reloadedConv.Goal != "Original goal" {
		t.Fatalf("source goal: got %q want %q", reloadedConv.Goal, "Original goal")
	}
	if reloadedConv.OriginalGoal != "Original goal" {
		t.Fatalf("source original goal: got %q want %q", reloadedConv.OriginalGoal, "Original goal")
	}
	if reloadedConv.TurnsCompleted != 3 {
		t.Fatalf("source turns completed: got %d want 3", reloadedConv.TurnsCompleted)
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
	t.Cleanup(func() {
		_ = os.RemoveAll(srcDir)
		_ = os.RemoveAll(dstDir)
	})
}

func TestForkSessionUsesProvidedDestination(t *testing.T) {
	setupForkTestWorkingDirectory(t)
	sourceID := "agent-explicit-fork-source"
	destinationID := "stable-explicit-fork"
	writeForkTestConversation(t, conversation.New(sourceID, "Explicit destination"))

	newID, err := ForkSession(sourceID, destinationID)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if newID != destinationID {
		t.Fatalf("ForkSession returned %q, want %q", newID, destinationID)
	}
	destinationDir, err := artifacts.SessionDirectory(destinationID)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(destinationDir); err != nil || !info.IsDir() {
		t.Fatalf("destination directory stat = %v, %v; want directory", info, err)
	}
	forked := readForkTestConversation(t, destinationID)
	if forked.SessionID != destinationID {
		t.Fatalf("forked SessionID = %q, want %q", forked.SessionID, destinationID)
	}
	if forked.ForkedFrom != sourceID {
		t.Fatalf("forked ForkedFrom = %q, want %q", forked.ForkedFrom, sourceID)
	}
}

func TestForkSessionRejectsOccupiedDestination(t *testing.T) {
	setupForkTestWorkingDirectory(t)
	sourceID := "agent-occupied-fork-source"
	destinationID := "occupied-fork-destination"
	writeForkTestConversation(t, conversation.New(sourceID, "Occupied destination"))
	destinationDir, err := artifacts.SessionDirectory(destinationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err = ForkSession(sourceID, destinationID)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("ForkSession error = %v, want already exists error", err)
	}
}

func TestForkSessionRejectsPathUnsafeDestination(t *testing.T) {
	setupForkTestWorkingDirectory(t)
	sourceID := "agent-unsafe-fork-source"
	writeForkTestConversation(t, conversation.New(sourceID, "Unsafe destination"))

	for _, destinationID := range []string{
		"a/b",
		`a\b`,
		"..",
		".",
		"a b",
		"a\tb",
		"a\x00b",
		"a\x7fb",
	} {
		t.Run(fmt.Sprintf("%q", destinationID), func(t *testing.T) {
			if newID, err := ForkSession(sourceID, destinationID); err == nil {
				t.Fatalf("ForkSession returned %q, want invalid destination error", newID)
			}
		})
	}
}

func TestForkRecordsEffectiveParent(t *testing.T) {
	setupForkTestWorkingDirectory(t)
	originalID := "agent-fork-original"
	writeForkTestConversation(t, conversation.New(originalID, "Original goal"))

	firstForkID, err := ForkSession(originalID, "")
	if err != nil {
		t.Fatalf("ForkSession(original): %v", err)
	}
	firstFork := readForkTestConversation(t, firstForkID)
	if firstFork.ForkedFrom != originalID {
		t.Fatalf("first fork ForkedFrom = %q, want %q", firstFork.ForkedFrom, originalID)
	}
	if firstFork.ForkedHash == "" {
		t.Fatal("first fork ForkedHash is empty")
	}

	secondForkID, err := ForkSession(firstForkID, "")
	if err != nil {
		t.Fatalf("ForkSession(first fork): %v", err)
	}
	secondFork := readForkTestConversation(t, secondForkID)
	if secondFork.ForkedFrom != originalID {
		t.Fatalf("pristine fork-of-fork ForkedFrom = %q, want effective parent %q", secondFork.ForkedFrom, originalID)
	}
	if secondFork.ForkedHash != firstFork.ForkedHash {
		t.Fatalf("pristine fork hashes differ: second = %q first = %q", secondFork.ForkedHash, firstFork.ForkedHash)
	}
}

func TestForkRecordsDivergedParent(t *testing.T) {
	setupForkTestWorkingDirectory(t)
	originalID := "agent-diverged-original"
	original := conversation.New(originalID, "Original goal")
	original.Goal = "Original goal"
	writeForkTestConversation(t, original)

	firstForkID, err := ForkSession(originalID, "")
	if err != nil {
		t.Fatalf("ForkSession(original): %v", err)
	}
	firstFork := readForkTestConversation(t, firstForkID)
	firstFork.Goal = "Diverged goal"
	firstFork.AddMessage("assistant", "Diverged content", nil)
	writeForkTestConversation(t, firstFork)

	secondForkID, err := ForkSession(firstForkID, "")
	if err != nil {
		t.Fatalf("ForkSession(diverged fork): %v", err)
	}
	secondFork := readForkTestConversation(t, secondForkID)
	if secondFork.ForkedFrom != firstForkID {
		t.Fatalf("diverged fork ForkedFrom = %q, want immediate parent %q", secondFork.ForkedFrom, firstForkID)
	}
	if secondFork.ForkedHash == firstFork.ForkedHash {
		t.Fatalf("diverged fork retained inherited hash %q", secondFork.ForkedHash)
	}
}

func TestForkOfArchivedSessionResetsArchived(t *testing.T) {
	setupForkTestWorkingDirectory(t)
	sourceID := "agent-archived-fork-source"
	source := conversation.New(sourceID, "Archived source")
	source.Goal = "Archived source"
	source.Archived = true
	writeForkTestConversation(t, source)

	forkID, err := ForkSession(sourceID, "")
	if err != nil {
		t.Fatalf("ForkSession(archived): %v", err)
	}
	forked := readForkTestConversation(t, forkID)
	if forked.Archived {
		t.Fatal("fork of archived session remained archived")
	}
}

func TestForkSessionSkipsLockFiles(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sourceID := fmt.Sprintf("test-fork-locks-%s", runner.GenerateSessionID())

	// Write a lock file inside the source session directory.
	srcDir, err := artifacts.SessionDirectory(sourceID)
	if err != nil {
		t.Fatalf("SessionDirectory: %v", err)
	}
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir srcDir: %v", err)
	}
	lockPath := filepath.Join(srcDir, "session.lock")
	if err := os.WriteFile(lockPath, []byte("fake lock content"), 0o644); err != nil {
		t.Fatalf("write session.lock: %v", err)
	}

	// Create a minimal conversation file (required by ForkSession).
	conv := conversation.New(sourceID, "Lock test goal")
	conv.Goal = "Lock test goal"
	conv.OriginalPrompt = "Lock test prompt"
	conv.Status = "completed"
	conv.TurnsCompleted = 1
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

	newID, err := ForkSession(sourceID, "")
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
		_ = os.RemoveAll(srcDir)
		_ = os.RemoveAll(dstDir)
	})
}

func setupForkTestWorkingDirectory(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
}

func writeForkTestConversation(t *testing.T, conv *conversation.Conversation) {
	t.Helper()
	if conv.Goal == "" {
		conv.Goal = conv.OriginalGoal
	}
	data, err := conv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path, err := artifacts.SessionConversationFile(conv.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readForkTestConversation(t *testing.T, sessionID string) *conversation.Conversation {
	t.Helper()
	path, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return conv
}

// Ensure encoding/json is used (satisfies the import requirement).
var _ = json.Marshal
