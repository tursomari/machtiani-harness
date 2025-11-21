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

func TestDiscoveryStatePendingNormalization(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("MACHTIANI_SESSION_ID", "discovery-session")
	workspaceDir := filepath.Join(t.TempDir(), "workspace")

	pending := []string{" ./foo/bar.go ", "foo/../foo/qux.txt", "dir\\sub\\file.txt", "../escape", "foo/bar.go"}
	if err := AddPendingDiscoveryPaths(pending); err != nil {
		t.Fatalf("AddPendingDiscoveryPaths: %v", err)
	}

	state, err := LoadDiscoveryState()
	if err != nil {
		t.Fatalf("LoadDiscoveryState: %v", err)
	}
	if state == nil {
		t.Fatalf("expected discovery state to be initialized")
	}

	wantPending := []string{"dir/sub/file.txt", "foo/bar.go", "foo/qux.txt"}
	if got := state.PendingPaths; len(got) != len(wantPending) {
		t.Fatalf("unexpected pending paths length: got %d, want %d", len(got), len(wantPending))
	}
	for i, want := range wantPending {
		if state.PendingPaths[i] != want {
			t.Fatalf("pending[%d] = %q, want %q", i, state.PendingPaths[i], want)
		}
	}

	_, err = UpdateDiscoveryState(func(st *FileDiscoveryState) error {
		st.PendingPaths = nil
		st.WorkspacePath = workspaceDir
		st.Files = map[string]FileMeta{
			"foo/bar.go": {Hash: "abc123", Size: 10, ModTime: 42, Tracked: true},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateDiscoveryState: %v", err)
	}

	state, err = LoadDiscoveryState()
	if err != nil {
		t.Fatalf("LoadDiscoveryState after update: %v", err)
	}
	if state == nil {
		t.Fatalf("expected discovery state after update")
	}
	if len(state.PendingPaths) != 0 {
		t.Fatalf("expected pending paths cleared, got %v", state.PendingPaths)
	}
	if state.WorkspacePath != workspaceDir {
		t.Fatalf("workspace path = %q, want %q", state.WorkspacePath, workspaceDir)
	}
	meta, ok := state.Files["foo/bar.go"]
	if !ok {
		t.Fatalf("expected metadata for foo/bar.go")
	}
	if !meta.Tracked || meta.Hash != "abc123" || meta.Size != 10 || meta.ModTime != 42 {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
}
