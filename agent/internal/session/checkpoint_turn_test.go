package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

func TestCheckpointTurn_callsBothPersistMethods(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	sessionID := fmt.Sprintf("checkpoint-turn-test-%d", time.Now().UnixNano())

	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}

	recorder := newConversationRecorder(nil, sessionID, "Test checkpoint goal", convPath, false, nil)
	conv := &conversation.Conversation{
		SessionID:    sessionID,
		OriginalGoal: "OriginalGoal checkpoint",
		Messages:     nil,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	recorder.conversation = conv

	runState := &runLifecycleState{
		sessionID:      sessionID,
		goal:           "Test checkpoint goal",
		originalGoal:   "OriginalGoal checkpoint",
		originalPrompt: "Test prompt",
		recorder:       recorder,
	}

	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("SessionDirectory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// First call: both conversation.json and session-state.json should be created.
	runState.checkpointTurn(nil, os.Stderr)

	// Verify conversation.json exists.
	if _, err := os.Stat(convPath); err != nil {
		t.Fatalf("conversation.json does not exist after first checkpointTurn: %v", err)
	}

	// Verify session-state.json is NOT created.
	statePath := filepath.Join(dir, "session-state.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("session-state.json unexpectedly exists after first checkpointTurn")
	}

	// Second call: both files should still exist.
	runState.checkpointTurn(nil, os.Stderr)

	if _, err := os.Stat(convPath); err != nil {
		t.Fatalf("conversation.json does not exist after second checkpointTurn: %v", err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("session-state.json unexpectedly exists after second checkpointTurn")
	}
}

func TestCheckpointTurnDiagWriterCapturesSaveFailure(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("checkpoint-diag-%d", time.Now().UnixNano())

	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}

	recorder := newConversationRecorder(nil, sessionID, "Test diag goal", convPath, false, nil)
	conv := &conversation.Conversation{
		SessionID:    sessionID,
		OriginalGoal: "OriginalGoal diag",
		Messages:     nil,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	recorder.conversation = conv

	runState := &runLifecycleState{
		sessionID:      sessionID,
		goal:           "Test diag goal",
		originalGoal:   "OriginalGoal diag",
		originalPrompt: "Test prompt",
		recorder:       recorder,
	}

	// Block SaveSessionState by placing a file where the session directory should be.
	sessionsDir, err := artifacts.SessionsRoot()
	if err != nil {
		t.Fatalf("SessionsRoot: %v", err)
	}
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions root: %v", err)
	}
	dir := filepath.Join(sessionsDir, sessionID)
	if err := os.WriteFile(dir, []byte("block"), 0o444); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	var diagBuf bytes.Buffer
	runState.checkpointTurn(nil, &diagBuf)

	output := diagBuf.String()
	if !strings.Contains(output, "session-state.json persistence disabled; using conversation.json") {
		t.Fatalf("expected deprecation message in diagWriter, got: %s", output)
	}
}
