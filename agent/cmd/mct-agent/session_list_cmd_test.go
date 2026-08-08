package main

import (
	"strings"
	"testing"

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
