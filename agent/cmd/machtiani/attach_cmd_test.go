package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/session"
)

func TestAttachFinishedSessionReplay(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	prepareTestConfig(t)
	sessionID := "agent-attach-finished"
	writeAttachCommandConversation(t, sessionID)

	originalRun := sessionRunFn
	t.Cleanup(func() { sessionRunFn = originalRun })
	runCalled := false
	sessionRunFn = func(context.Context, session.Options) session.Result {
		runCalled = true
		t.Fatalf("sessionRunFn called during attach")
		return session.Result{}
	}

	stdout, stderr := captureOutput(func() {
		if code := handleRunCommand([]string{"--attach", "--resume", sessionID}); code != 0 {
			t.Fatalf("handleRunCommand() exit = %d, want 0", code)
		}
	})
	if runCalled {
		t.Fatal("sessionRunFn was called")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{"Attach to a finished session", "What was completed?", "The replay path was implemented.", "Show the persisted result.", "Finished successfully."} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q in:\n%s", want, stdout)
		}
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("stdout does not end with a newline: %q", stdout)
	}

	scratchDir, err := artifacts.SessionScratchDirectory(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(scratchDir, "session.lock")); !os.IsNotExist(err) {
		t.Fatalf("attach created session.lock: %v", err)
	}
}

func TestAttachRejectsDisplayOnlyConflicts(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantError string
	}{
		{name: "missing resume", args: []string{"--attach"}, wantError: "--attach"},
		{name: "prompt", args: []string{"--attach", "--resume", "agent-attach", "-p", "hi"}, wantError: "--attach"},
		{name: "file", args: []string{"--attach", "--resume", "agent-attach", "--file", "path"}, wantError: "--attach"},
		{name: "exec", args: []string{"--attach", "--resume", "agent-attach", "-x"}, wantError: "--attach"},
		{name: "mode", args: []string{"--attach", "--resume", "agent-attach", "--mode", "somemode"}, wantError: "--attach"},
		{name: "model", args: []string{"--attach", "--resume", "agent-attach", "--model", "m1"}, wantError: "--attach"},
		{name: "positional", args: []string{"--attach", "--resume", "agent-attach", "unexpected"}, wantError: "unexpected positional arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupSessionArchiveCommandTest(t)
			prepareTestConfig(t)
			originalRun := sessionRunFn
			t.Cleanup(func() { sessionRunFn = originalRun })
			runCalled := false
			sessionRunFn = func(context.Context, session.Options) session.Result {
				runCalled = true
				t.Fatalf("sessionRunFn called for rejected attach")
				return session.Result{}
			}

			_, stderr := captureOutput(func() {
				if code := handleRunCommand(tt.args); code != 2 {
					t.Fatalf("handleRunCommand() exit = %d, want 2", code)
				}
			})
			if runCalled {
				t.Fatal("sessionRunFn was called")
			}
			if !strings.Contains(stderr, tt.wantError) {
				t.Fatalf("stderr = %q, want error containing %q", stderr, tt.wantError)
			}
		})
	}
}

func TestAttachMissingConversation(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	prepareTestConfig(t)

	stdout, stderr := captureOutput(func() {
		if code := handleRunCommand([]string{"--attach", "--resume", "agent-attach-missing"}); code != 1 {
			t.Fatalf("handleRunCommand() exit = %d, want 1", code)
		}
	})
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "Error:") {
		t.Fatalf("stderr = %q, want Error", stderr)
	}
}

func TestAttachCorruptedConversation(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	prepareTestConfig(t)
	sessionID := "agent-attach-corrupt"
	path, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{invalid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := captureOutput(func() {
		if code := handleRunCommand([]string{"--attach", "--resume", sessionID}); code != 1 {
			t.Fatalf("handleRunCommand() exit = %d, want 1", code)
		}
	})
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "Error:") {
		t.Fatalf("stderr = %q, want Error", stderr)
	}
}

func TestAttachRunEmptySessionID(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := runAttach("   ", &stdout, &stderr); code != 1 {
		t.Fatalf("runAttach() exit = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Error:") {
		t.Fatalf("stderr = %q, want Error", stderr.String())
	}
}

func writeAttachCommandConversation(t *testing.T, sessionID string) {
	t.Helper()
	path := writeSessionArchiveCommandConversation(t, sessionID, "Attach to a finished session")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	conv.AddMessage("assistant", "What was completed?", map[string]any{
		"type":     "work_request",
		"turn":     1,
		"decision": "Show the persisted result.",
	})
	conv.AddMessage("assistant", "The replay path was implemented.", map[string]any{
		"type": "work_result",
		"turn": 1,
	})
	conv.AddMessage("assistant", "Finished successfully.", map[string]any{
		"type":   "final",
		"turns":  1,
		"capped": false,
	})
	data, err = conv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
