package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestClassifyFinalAnswer_ReadError verifies that ClassifyFinalAnswer returns
// an error when the session directory does not exist (and therefore the
// agent-final-answer.md file cannot be read).
func TestClassifyFinalAnswer_ReadError(t *testing.T) {
	dir, err := os.MkdirTemp("", "classifier-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(dir)
	})

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(origDir)
	})

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	ctx := context.Background()
	_, _, err = ClassifyFinalAnswer(ctx, "nonexistent-session", "")
	if err == nil {
		t.Fatal("expected an error but got nil")
	}
}

// TestClassifyFinalAnswer_EmptyFile verifies that ClassifyFinalAnswer returns
// an error when the agent-final-answer.md file exists but is empty.
func TestClassifyFinalAnswer_EmptyFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "classifier-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(dir)
	})

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(origDir)
	})

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	sessionDir := filepath.Join(".machtiani", "sessions", "test-session", "chat")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	answerPath := filepath.Join(sessionDir, "agent-final-answer.md")
	if err := os.WriteFile(answerPath, []byte{}, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx := context.Background()
	_, _, err = ClassifyFinalAnswer(ctx, "test-session", "")
	if err == nil {
		t.Fatal("expected an error but got nil")
	}
}
