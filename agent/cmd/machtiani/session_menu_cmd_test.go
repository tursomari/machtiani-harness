package main

import (
	"os"
	"strings"
	"testing"
)

func TestSessionMenuNonInteractive(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	useNonTerminalStdin(t)
	sessionID := "agent-menu-archive"
	path := writeSessionArchiveCommandConversation(t, sessionID, "Archive through session menu")

	stdout, stderr := captureOutput(func() {
		if code := handleSessionCommand([]string{"menu", "--no-interactive", "--archive", sessionID}); code != 0 {
			t.Fatalf("session menu non-interactive exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stdout, "Apply this change?") {
		t.Fatalf("non-interactive menu prompted: %q", stdout)
	}
	assertSessionArchiveCommandState(t, path, true)

	_, stderr = captureOutput(func() {
		if code := handleSessionCommand([]string{"menu"}); code != 2 {
			t.Fatalf("session menu non-TTY exit = %d, want 2", code)
		}
	})
	if !strings.Contains(stderr, "pass --no-interactive") {
		t.Fatalf("non-TTY guidance = %q, want --no-interactive guidance", stderr)
	}
}

func useNonTerminalStdin(t *testing.T) {
	t.Helper()
	oldStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = reader.Close()
	})
}
