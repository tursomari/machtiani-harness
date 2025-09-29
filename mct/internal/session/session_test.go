package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddMessagePersistsFiles(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("MACHTIANI_SESSION_ID", "test-session")

	if err := AddMessage("user", "user prompt", nil); err != nil {
		t.Fatalf("AddMessage user: %v", err)
	}

	files := []string{"path/to/file1.go", "docs/readme.md"}
	if err := AddMessage("assistant", "assistant response", files); err != nil {
		t.Fatalf("AddMessage assistant: %v", err)
	}

	// Mutate the original slice to ensure the stored history keeps its own copy.
	files[0] = "mutated"

	history, err := LoadHistory()
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}

	if len(history) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(history))
	}

	if history[0].Role != "user" || history[0].Content != "user prompt" {
		t.Fatalf("unexpected first history entry: %+v", history[0])
	}
	if history[0].Files != nil {
		t.Fatalf("expected no files for user entry, got %v", history[0].Files)
	}

	if history[1].Role != "assistant" || history[1].Content != "assistant response" {
		t.Fatalf("unexpected second history entry: %+v", history[1])
	}

	expectedFiles := []string{"path/to/file1.go", "docs/readme.md"}
	if len(history[1].Files) != len(expectedFiles) {
		t.Fatalf("expected %d files, got %d", len(expectedFiles), len(history[1].Files))
	}
	for i, want := range expectedFiles {
		if history[1].Files[i] != want {
			t.Fatalf("file[%d] = %q, want %q", i, history[1].Files[i], want)
		}
	}

	sessionPath := filepath.Join(tempHome, ".machtiani", "sessions", "session-test-session.json")
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("ReadFile session: %v", err)
	}

	if count := strings.Count(string(data), "\"Files\""); count != 1 {
		t.Fatalf("expected session JSON to contain Files exactly once, got %d occurrences", count)
	}
}
