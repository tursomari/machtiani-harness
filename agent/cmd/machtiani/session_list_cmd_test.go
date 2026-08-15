package main

import (
	"os"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/session"
)

func TestSessionListAlignedTable(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	longID := "agent-1234567890123456789"
	if len(longID) != 25 {
		t.Fatalf("test session ID length = %d, want 25", len(longID))
	}
	writeSessionArchiveCommandConversation(t, longID, "A deliberately long session goal that must be truncated cleanly in the table")
	writeSessionArchiveCommandConversation(t, "agent-short", "Short goal")
	if err := session.ArchiveSession(longID); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := captureOutput(func() {
		if code := handleSessionListCommand([]string{"--all"}); code != 0 {
			t.Fatalf("session list --all exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 4 {
		t.Fatalf("table lines = %d, want header, separator, and 2 rows:\n%s", len(lines), stdout)
	}
	statusOffset := strings.Index(lines[0], "STATUS")
	if statusOffset < 0 {
		t.Fatalf("STATUS missing from header %q", lines[0])
	}
	if !strings.Contains(stdout, longID+" [archived]") {
		t.Fatalf("archived badge missing from table:\n%s", stdout)
	}
	for _, row := range lines[2:] {
		if got := strings.Index(row, "completed"); got != statusOffset {
			t.Fatalf("STATUS value offset = %d, want %d in row %q\nfull table:\n%s", got, statusOffset, row, stdout)
		}
	}
}

func TestSessionListForkedFilter(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	nonForkID := "agent-list-non-fork"
	activeForkID := "agent-list-active-fork"
	archivedForkID := "agent-list-archived-fork"
	writeSessionArchiveCommandConversation(t, nonForkID, "Not forked")
	activeForkPath := writeSessionArchiveCommandConversation(t, activeForkID, "Active fork")
	archivedForkPath := writeSessionArchiveCommandConversation(t, archivedForkID, "Archived fork")
	updateSessionListConversation(t, activeForkPath, func(conv *conversation.Conversation) {
		conv.ForkedFrom = "agent-parent"
	})
	updateSessionListConversation(t, archivedForkPath, func(conv *conversation.Conversation) {
		conv.Archived = true
		conv.ForkedFrom = "agent-parent"
	})

	stdout, stderr := captureOutput(func() {
		if code := handleSessionListCommand([]string{"--forked"}); code != 0 {
			t.Fatalf("session list --forked exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stdout, nonForkID) || !strings.Contains(stdout, activeForkID) || !strings.Contains(stdout, archivedForkID) {
		t.Fatalf("list --forked output = %q", stdout)
	}

	stdout, stderr = captureOutput(func() {
		if code := handleSessionListCommand([]string{"--archived", "--forked"}); code != 0 {
			t.Fatalf("session list --archived --forked exit = %d, want 0", code)
		}
	})
	if stderr != "" || strings.Contains(stdout, activeForkID) || !strings.Contains(stdout, archivedForkID) {
		t.Fatalf("combined filter stdout=%q stderr=%q", stdout, stderr)
	}

	stdout, stderr = captureOutput(func() {
		if code := handleSessionListCommand([]string{"--all", "--archived", "--forked"}); code != 0 {
			t.Fatalf("session list --all --archived --forked exit = %d, want 0", code)
		}
	})
	if stderr != "" || !strings.Contains(stdout, nonForkID) || !strings.Contains(stdout, activeForkID) || !strings.Contains(stdout, archivedForkID) {
		t.Fatalf("--all precedence stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestSessionListAllBadgesForkedAndArchived(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	bothID := "agent-1234567890123456789"
	if len(bothID) != 25 {
		t.Fatalf("test session ID length = %d, want 25", len(bothID))
	}
	bothPath := writeSessionArchiveCommandConversation(t, bothID, "Both archived and forked")
	forkedID := "agent-forked-only"
	forkedPath := writeSessionArchiveCommandConversation(t, forkedID, "Forked only")
	updateSessionListConversation(t, bothPath, func(conv *conversation.Conversation) {
		conv.Archived = true
		conv.ForkedFrom = "agent-original"
	})
	updateSessionListConversation(t, forkedPath, func(conv *conversation.Conversation) {
		conv.ForkedFrom = "agent-original"
	})

	stdout, stderr := captureOutput(func() {
		if code := handleSessionListCommand([]string{"--all"}); code != 0 {
			t.Fatalf("session list --all exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, bothID+" [archived] [forked]") {
		t.Fatalf("combined badges missing or out of order:\n%s", stdout)
	}
	if !strings.Contains(stdout, forkedID+" [forked]") {
		t.Fatalf("forked badge missing:\n%s", stdout)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	statusOffset := strings.Index(lines[0], "STATUS")
	if statusOffset < 0 {
		t.Fatalf("STATUS missing from header %q", lines[0])
	}
	for _, line := range lines {
		if len(line) > 80 {
			t.Fatalf("table line is %d columns, want at most 80: %q", len(line), line)
		}
	}
	for _, row := range lines[2:] {
		if got := strings.Index(row, "completed"); got != statusOffset {
			t.Fatalf("STATUS value offset = %d, want %d in row %q\nfull table:\n%s", got, statusOffset, row, stdout)
		}
	}
}

func updateSessionListConversation(t *testing.T, path string, update func(*conversation.Conversation)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	update(conv)
	data, err = conv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
