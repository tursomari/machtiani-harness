package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
	"github.com/tursomari/machtiani/agent/internal/session"
)

func TestSessionArchiveUnarchiveRestoresShow(t *testing.T) {
	_, project := setupMigrationTest(t)
	if err := projectstore.WriteProjectUUID(project, uuid.New()); err != nil {
		t.Fatal(err)
	}
	sessionID := "agent-unarchive-show"
	writeSessionArchiveCommandConversation(t, sessionID, "Show this restored session")

	if err := session.ArchiveSession(sessionID); err != nil {
		t.Fatal(err)
	}
	if err := session.UnarchiveSession(sessionID); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := captureOutput(func() {
		if code := handleSessionShowCommand([]string{sessionID}); code != 0 {
			t.Fatalf("session show exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, sessionID) || !strings.Contains(stdout, "Show this restored session") {
		t.Fatalf("session show output = %q", stdout)
	}
}

func writeSessionArchiveCommandConversation(t *testing.T, sessionID, goal string) string {
	t.Helper()
	conv := conversation.New(sessionID, goal)
	conv.Goal = goal
	conv.Status = "completed"
	data, err := conv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
