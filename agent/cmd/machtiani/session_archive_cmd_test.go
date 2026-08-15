package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
	"github.com/tursomari/machtiani/agent/internal/session"
)

func TestSessionArchiveUnarchiveRestoresShow(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-unarchive-show"
	writeSessionArchiveCommandConversation(t, sessionID, "Show this restored session")

	if err := session.ArchiveSession(sessionID); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := captureOutput(func() {
		if code := handleSessionShowCommand([]string{sessionID}); code != 0 {
			t.Fatalf("archived session show exit = %d, want 0", code)
		}
	})
	if stderr != "" || !strings.Contains(stdout, "Archived:        true") {
		t.Fatalf("archived session show stdout=%q stderr=%q", stdout, stderr)
	}

	if err := session.UnarchiveSession(sessionID); err != nil {
		t.Fatal(err)
	}

	stdout, stderr = captureOutput(func() {
		if code := handleSessionShowCommand([]string{sessionID}); code != 0 {
			t.Fatalf("session show exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, sessionID) || !strings.Contains(stdout, "Show this restored session") || !strings.Contains(stdout, "Archived:        false") {
		t.Fatalf("session show output = %q", stdout)
	}
}

func TestSessionArchiveCommandsByIDAndDateRange(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-command-archive"
	path := writeSessionArchiveCommandConversation(t, sessionID, "Archive from the command line")

	stdout, stderr := captureOutput(func() {
		if code := handleSessionCommand([]string{"archive", sessionID}); code != 0 {
			t.Fatalf("session archive exit = %d, want 0", code)
		}
	})
	if stderr != "" || !strings.Contains(stdout, sessionID) {
		t.Fatalf("session archive stdout=%q stderr=%q", stdout, stderr)
	}
	assertSessionArchiveCommandState(t, path, true)

	_, stderr = captureOutput(func() {
		if code := handleSessionCommand([]string{"unarchive", sessionID}); code != 0 {
			t.Fatalf("session unarchive exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("session unarchive stderr=%q", stderr)
	}
	assertSessionArchiveCommandState(t, path, false)

	convData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := conversation.Unmarshal(convData)
	if err != nil {
		t.Fatal(err)
	}
	conv.UpdatedAt = time.Date(2026, time.March, 5, 18, 30, 0, 0, time.UTC)
	convData, err = conv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, convData, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr = captureOutput(func() {
		if code := handleSessionCommand([]string{"archive", "--since", "2026-03-05", "--until", "2026-03-05"}); code != 0 {
			t.Fatalf("session archive date range exit = %d, want 0", code)
		}
	})
	if stderr != "" || !strings.Contains(stdout, "Archived 1 session") {
		t.Fatalf("bulk archive stdout=%q stderr=%q", stdout, stderr)
	}
	assertSessionArchiveCommandState(t, path, true)

	stdout, stderr = captureOutput(func() {
		if code := handleSessionCommand([]string{"unarchive", "--since", "2026-03-05", "--until", "2026-03-05"}); code != 0 {
			t.Fatalf("session unarchive date range exit = %d, want 0", code)
		}
	})
	if stderr != "" || !strings.Contains(stdout, "Unarchived 1 session") {
		t.Fatalf("bulk unarchive stdout=%q stderr=%q", stdout, stderr)
	}
	assertSessionArchiveCommandState(t, path, false)
}

func TestSessionListArchivedFlag(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	activeID := "agent-list-command-active"
	archivedID := "agent-list-command-archived"
	writeSessionArchiveCommandConversation(t, activeID, "Active list entry")
	writeSessionArchiveCommandConversation(t, archivedID, "Archived list entry")
	if err := session.ArchiveSession(archivedID); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := captureOutput(func() {
		if code := handleSessionListCommand(nil); code != 0 {
			t.Fatalf("session list exit = %d, want 0", code)
		}
	})
	if stderr != "" || !strings.Contains(stdout, activeID) || strings.Contains(stdout, archivedID) {
		t.Fatalf("default list stdout=%q stderr=%q", stdout, stderr)
	}

	stdout, stderr = captureOutput(func() {
		if code := handleSessionListCommand([]string{"--archived"}); code != 0 {
			t.Fatalf("session list --archived exit = %d, want 0", code)
		}
	})
	if stderr != "" || strings.Contains(stdout, activeID) || !strings.Contains(stdout, archivedID) {
		t.Fatalf("list --archived stdout=%q stderr=%q", stdout, stderr)
	}

	stdout, stderr = captureOutput(func() {
		if code := handleSessionListCommand([]string{"--all"}); code != 0 {
			t.Fatalf("session list --all exit = %d, want 0", code)
		}
	})
	if stderr != "" || !strings.Contains(stdout, activeID) || !strings.Contains(stdout, archivedID) {
		t.Fatalf("list --all stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stdout, archivedID+" [archived]") {
		t.Fatalf("list --all missing archived badge: %q", stdout)
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

func setupSessionArchiveCommandTest(t *testing.T) {
	t.Helper()
	_, project := setupMigrationTest(t)
	if err := projectstore.WriteProjectUUID(project, uuid.New()); err != nil {
		t.Fatal(err)
	}
}

func assertSessionArchiveCommandState(t *testing.T, path string, wantArchived bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if conv.Archived != wantArchived {
		t.Fatalf("conversation Archived = %v, want %v", conv.Archived, wantArchived)
	}
}
