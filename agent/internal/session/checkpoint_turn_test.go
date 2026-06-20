package session

import (
	"fmt"
	"os"
	"path/filepath"
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

	// Verify session-state.json exists.
	statePath := filepath.Join(dir, sessionStateFile)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("session-state.json does not exist after first checkpointTurn: %v", err)
	}

	// Second call: both files should still exist.
	runState.checkpointTurn(nil, os.Stderr)

	if _, err := os.Stat(convPath); err != nil {
		t.Fatalf("conversation.json does not exist after second checkpointTurn: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("session-state.json does not exist after second checkpointTurn: %v", err)
	}
}
