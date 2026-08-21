package main

import (
	"os"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
)

func TestSessionDeleteResolvesShortQuery(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "DM1-KYF1E4CZE7XRSTU123456789ABCD"
	writeSessionArchiveCommandConversation(t, sessionID, "Delete resolved session")
	sessionDir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr := captureOutput(func() {
		if code := handleSessionDeleteCommand([]string{"dm1-kyf1e-4cze7x"}); code != 0 {
			t.Fatalf("session delete exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, sessionID) {
		t.Fatalf("stdout = %q, want resolved session ID %q", stdout, sessionID)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("session directory still exists after delete: %v", err)
	}
}

func TestSessionForkResolvesShortQuery(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "DM1-KYF1E4CZE7XRSTU123456789ABCD"
	writeSessionArchiveCommandConversation(t, sessionID, "Fork resolved session")

	stdout, stderr := captureOutput(func() {
		if code := handleSessionForkCommand([]string{"dm1-kyf1e-4cze7x"}); code != 0 {
			t.Fatalf("session fork exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	newSessionID := strings.TrimSpace(stdout)
	if newSessionID == "" || newSessionID == sessionID {
		t.Fatalf("forked session ID = %q", newSessionID)
	}
	forkPath, err := artifacts.SessionConversationFile(newSessionID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(forkPath)
	if err != nil {
		t.Fatal(err)
	}
	forked, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if forked.ForkedFrom != sessionID {
		t.Fatalf("ForkedFrom = %q, want resolved source %q", forked.ForkedFrom, sessionID)
	}
}
