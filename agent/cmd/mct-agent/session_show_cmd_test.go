package main

import (
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
)

func TestSessionShowPrintsForkedFrom(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	forkID := "agent-show-fork"
	parentID := "agent-show-parent"
	forkPath := writeSessionArchiveCommandConversation(t, forkID, "Show fork metadata")
	updateSessionListConversation(t, forkPath, func(conv *conversation.Conversation) {
		conv.ForkedFrom = parentID
		conv.ForkedHash = strings.Repeat("a", 64)
	})
	nonForkID := "agent-show-non-fork"
	writeSessionArchiveCommandConversation(t, nonForkID, "Show no fork metadata")

	stdout, stderr := captureOutput(func() {
		if code := handleSessionShowCommand([]string{forkID}); code != 0 {
			t.Fatalf("session show fork exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, "Forked from:") || !strings.Contains(stdout, parentID) {
		t.Fatalf("forked parent missing from session show:\n%s", stdout)
	}

	stdout, stderr = captureOutput(func() {
		if code := handleSessionShowCommand([]string{nonForkID}); code != 0 {
			t.Fatalf("session show non-fork exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stdout, "Forked from:") {
		t.Fatalf("non-fork show includes fork metadata:\n%s", stdout)
	}

	stdout, stderr = captureOutput(func() {
		if code := handleSessionShowCommand([]string{"--json", forkID}); code != 0 {
			t.Fatalf("session show --json fork exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, `"forked_from": "`+parentID+`"`) {
		t.Fatalf("forked_from missing from JSON session show:\n%s", stdout)
	}
}
