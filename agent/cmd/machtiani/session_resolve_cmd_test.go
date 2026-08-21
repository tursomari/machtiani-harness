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
	if !strings.HasPrefix(newSessionID, "agent-") {
		t.Fatalf("forked session ID = %q, want generated agent ID", newSessionID)
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

func TestSessionForkUsesProvidedDestination(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "DM1-KYF1E4CZE7XRSTU123456789ABCD"
	destinationID := "stable-cli-fork"
	writeSessionArchiveCommandConversation(t, sessionID, "Fork to explicit destination")

	stdout, stderr := captureOutput(func() {
		if code := handleSessionForkCommand([]string{"dm1-kyf1e-4cze7x", destinationID}); code != 0 {
			t.Fatalf("session fork exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := strings.TrimSpace(stdout); got != destinationID {
		t.Fatalf("stdout = %q, want destination ID %q", stdout, destinationID)
	}
	forkPath, err := artifacts.SessionConversationFile(destinationID)
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
	if forked.SessionID != destinationID || forked.ForkedFrom != sessionID {
		t.Fatalf("forked metadata = SessionID %q, ForkedFrom %q", forked.SessionID, forked.ForkedFrom)
	}
}

func TestSessionForkRejectsInvalidDestination(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-cli-invalid-destination-source"
	writeSessionArchiveCommandConversation(t, sessionID, "Reject invalid destination")

	for _, destinationID := range []string{"", "   ", "a/b", `a\b`, ".", "..", "a b", "a\nb", "a\x00b"} {
		t.Run(destinationID, func(t *testing.T) {
			_, stderr := captureOutput(func() {
				if code := handleSessionForkCommand([]string{sessionID, destinationID}); code == 0 {
					t.Fatal("session fork exit = 0, want failure")
				}
			})
			if !strings.Contains(strings.ToLower(stderr), "destination") {
				t.Fatalf("stderr = %q, want destination error", stderr)
			}
		})
	}
}

func TestSessionForkRejectsSourceAsDestination(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "DM1-KYF1E4CZE7XRSTU123456789ABCD"
	writeSessionArchiveCommandConversation(t, sessionID, "Reject source destination")

	_, stderr := captureOutput(func() {
		if code := handleSessionForkCommand([]string{"dm1-kyf1e-4cze7x", sessionID}); code == 0 {
			t.Fatal("session fork exit = 0, want failure")
		}
	})
	if !strings.Contains(stderr, "destination must differ from source") {
		t.Fatalf("stderr = %q, want source/destination mismatch error", stderr)
	}
}
