package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/session"
	"github.com/tursomari/machtiani/agent/internal/shellaction"
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
	if !strings.Contains(stdout, "Following session "+sessionID) {
		t.Errorf("stdout missing follow notice for %q in:\n%s", sessionID, stdout)
	}
	if strings.Contains(stdout, "\x1b") {
		t.Errorf("non-TTY attach emitted ANSI escape bytes:\n%q", stdout)
	}
	if got := strings.Count(stdout, "Resume this session:"); got != 1 {
		t.Errorf("conclusion count = %d, want exactly 1:\n%s", got, stdout)
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

func TestAttachNoShellStepsPreservesLegacyReplay(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	prepareTestConfig(t)
	sessionID := "agent-attach-no-shell-steps"
	writeAttachCommandConversation(t, sessionID)
	writeAttachActions(t, sessionID, 1, shellaction.Record{
		Version: 1, SessionID: sessionID, Turn: 1, Sequence: 1,
		Description: "This must be hidden", Command: "echo hidden",
	})

	stdout, stderr := captureOutput(func() {
		if code := handleRunCommand([]string{"--attach", "--resume", sessionID, "--no-shell-steps"}); code != 0 {
			t.Fatalf("handleRunCommand() exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if strings.Contains(stdout, "SHELL STEPS") || strings.Contains(stdout, "echo hidden") {
		t.Fatalf("--no-shell-steps rendered an action:\n%s", stdout)
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
		{name: "deprecated session id", args: []string{"--attach", "--session-id", "agent-attach"}, wantError: "Error: --attach requires --resume/-r; the deprecated --session-id flag cannot be combined with it"},
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

func TestAttachRunningSessionTailsUntilLockReleased(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	prepareTestConfig(t)
	sessionID := "agent-attach-running"
	conv := conversation.New(sessionID, "Attach to a running session")
	conv.Status = "completed"
	addAttachTailTurn(conv, 1)
	path := writeAttachTailConversation(t, conv)

	scratchDir, err := artifacts.SessionScratchDirectory(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(scratchDir, "session.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	})
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("hold running-session lock: %v", err)
	}

	originalRun := sessionRunFn
	t.Cleanup(func() { sessionRunFn = originalRun })
	sessionRunFn = func(context.Context, session.Options) session.Result {
		t.Fatal("sessionRunFn called during running attach")
		return session.Result{}
	}

	publishDone := make(chan error, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		addAttachTailTurn(conv, 2)
		if err := publishAttachTailConversation(path, conv); err != nil {
			publishDone <- err
			return
		}
		time.Sleep(300 * time.Millisecond)
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
			publishDone <- err
			return
		}
		publishDone <- nil
	}()

	stdout, stderr := captureOutput(func() {
		if code := handleRunCommand([]string{"--attach", "--resume", sessionID}); code != 0 {
			t.Fatalf("handleRunCommand() exit = %d, want 0", code)
		}
	})
	if err := <-publishDone; err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{"Attach to a running session", "Live question 1", "Live answer 1", "Live question 2", "Live answer 2"} {
		if got := strings.Count(stdout, want); got != 1 {
			t.Errorf("stdout count for %q = %d, want 1:\n%s", want, got, stdout)
		}
	}
	if got := strings.Count(stdout, "Following session "+sessionID); got != 1 {
		t.Errorf("follow notice count = %d, want 1:\n%s", got, stdout)
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

func TestAttachFocusedSuppressesReplaySections(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	prepareTestConfig(t)
	sessionID := "agent-attach-focused"
	writeAttachCommandConversation(t, sessionID)
	writeAttachActions(t, sessionID, 1, shellaction.Record{
		Version: 1, SessionID: sessionID, Turn: 1, Sequence: 1,
		Description: "This must be hidden", Command: "echo hidden",
	})

	stdout, stderr := captureOutput(func() {
		if code := handleRunCommand([]string{"--attach", "--resume", sessionID, "--focused"}); code != 0 {
			t.Fatalf("handleRunCommand() exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{"Finished successfully.", "Resume this session:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("focused stdout missing %q in:\n%s", want, stdout)
		}
	}
	for _, suppressed := range []string{
		"Following session",
		"──── TURN",
		"── QUESTION ──",
		"── ANSWER ──",
		"── DECISION ──",
		"SHELL STEPS",
		"echo hidden",
	} {
		if strings.Contains(stdout, suppressed) {
			t.Errorf("focused stdout contains suppressed %q in:\n%s", suppressed, stdout)
		}
	}
}

func TestAttachRendersOnlyLastConclusion(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	prepareTestConfig(t)
	sessionID := "agent-attach-stale-final"
	conv := conversation.New(sessionID, "Replay stale conclusions")
	addAttachTailTurn(conv, 1)
	conv.AddMessage("assistant", "Stale capped conclusion", map[string]any{"type": "final", "turns": 1, "capped": true})
	addAttachTailTurn(conv, 2)
	conv.AddMessage("assistant", "Fresh final conclusion", map[string]any{"type": "final", "turns": 2, "capped": false})
	writeAttachTailConversation(t, conv)

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		probe:        func(string) (bool, error) { return false, nil },
		pollInterval: time.Millisecond,
		quietGrace:   3 * time.Millisecond,
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	if strings.Contains(output, "Stale capped conclusion") {
		t.Errorf("stale intermediate conclusion rendered:\n%s", output)
	}
	if got := strings.Count(output, "Resume this session:"); got != 1 {
		t.Errorf("conclusion count = %d, want exactly 1:\n%s", got, output)
	}
	if !strings.Contains(output, "Fresh final conclusion") {
		t.Errorf("final conclusion missing in:\n%s", output)
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
