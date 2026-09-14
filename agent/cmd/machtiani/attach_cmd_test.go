package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
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
	for _, want := range []string{"What was completed?", "The replay path was implemented.", "Show the persisted result.", "Finished successfully."} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q in:\n%s", want, stdout)
		}
	}
	for _, unwanted := range []string{"# User", "# Assistant", "# System", "Following session ", "── ", "──── ", "ARTIFACTS"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("stdout contains attach-only framing %q in:\n%s", unwanted, stdout)
		}
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

func TestAttachCapturesRenderedOutput(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	prepareTestConfig(t)
	sessionID := "agent-attach-capture"
	writeAttachCommandConversation(t, sessionID)
	capturePath := filepath.Join(t.TempDir(), "attach.tui.txt")
	t.Setenv("MACHTIANI_TUI_CAPTURE", capturePath)

	stdout, stderr := captureOutput(func() {
		if code := handleRunCommand([]string{"--attach", "--resume", sessionID}); code != 0 {
			t.Fatalf("handleRunCommand() exit = %d, want 0", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(captured); got != stdout {
		t.Fatalf("capture differs from stdout:\n--- capture ---\n%s\n--- stdout ---\n%s", got, stdout)
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
	for _, want := range []string{"Live question 1", "Live answer 1", "Live question 2", "Live answer 2"} {
		if got := strings.Count(stdout, want); got != 1 {
			t.Errorf("stdout count for %q = %d, want 1:\n%s", want, got, stdout)
		}
	}
	if strings.Contains(stdout, "Following session ") {
		t.Errorf("stdout contains attach-only follow notice:\n%s", stdout)
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

func TestAttachFallsBackToGlobalSessionAndThreadsSelectedScope(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-global-fallback-123456"
	globalSessionsRoot, globalScratchRoot, err := attachGlobalRoots()
	if err != nil {
		t.Fatal(err)
	}
	target := newAttachSessionTarget(sessionID, globalSessionsRoot, globalScratchRoot)
	conv := conversation.New(sessionID, "Attach to the global fallback")
	addAttachTailTurn(conv, 1)
	writeAttachConversationAt(t, target.conversationPath(), conv)
	writeAttachActionAt(t, attachShellAgentActionsPath(target.sessionDirectory, 1), shellaction.Record{
		Version: 1, SessionID: sessionID, Turn: 1, Sequence: 1,
		Description: "Read global action", Command: "printf global-scope",
	})

	var probedPath string
	var stdout, stderr strings.Builder
	code := runAttachWithDependencies("agent-global-fallback-12", &stdout, &stderr, attachDependencies{
		readFile:    os.ReadFile,
		readActions: os.ReadFile,
		probe: func(path string) (bool, error) {
			probedPath = path
			return false, nil
		},
		pollInterval: time.Millisecond,
		quietGrace:   3 * time.Millisecond,
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	if probedPath != target.scratchDirectory {
		t.Fatalf("activity probe path = %q, want selected global scratch %q", probedPath, target.scratchDirectory)
	}
	for _, want := range []string{"Live question 1", "$ printf global-scope"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("global attach output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestAttachDefaultScopeShadowsGlobalDuplicate(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-shadow"
	defaultConv := conversation.New(sessionID, "Prefer the default scope")
	defaultConv.AddMessage("assistant", "Default scope answer", map[string]any{"type": "work_result", "turn": 1})
	writeAttachTailConversation(t, defaultConv)

	globalSessionsRoot, globalScratchRoot, err := attachGlobalRoots()
	if err != nil {
		t.Fatal(err)
	}
	globalTarget := newAttachSessionTarget(sessionID, globalSessionsRoot, globalScratchRoot)
	globalConv := conversation.New(sessionID, "Do not select the global duplicate")
	globalConv.AddMessage("assistant", "Global scope answer", map[string]any{"type": "work_result", "turn": 1})
	writeAttachConversationAt(t, globalTarget.conversationPath(), globalConv)

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		probe:        func(string) (bool, error) { return false, nil },
		pollInterval: time.Millisecond,
		quietGrace:   3 * time.Millisecond,
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Default scope answer") || strings.Contains(stdout.String(), "Global scope answer") {
		t.Fatalf("attach did not preserve default-scope shadowing:\n%s", stdout.String())
	}
}

func TestAttachDefaultScopeAmbiguityDoesNotFallBack(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	defaultSessionsRoot, err := artifacts.SessionsRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{"agent-attach-ambiguous-111", "agent-attach-ambiguous-112"} {
		if err := os.MkdirAll(filepath.Join(defaultSessionsRoot, sessionID), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	globalSessionsRoot, _, err := attachGlobalRoots()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(globalSessionsRoot, "agent-attach-ambiguous-11"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err = resolveAttachSession("agent-attach-ambiguous-11")
	if err == nil || !strings.Contains(err.Error(), "ambiguous session id") {
		t.Fatalf("resolveAttachSession() error = %v, want default-scope ambiguity", err)
	}
}

func TestResolveAttachSessionFindsGlobalUUIDStoreOutsideProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	plainDirectory := t.TempDir()
	originalDirectory := mustChdir(t, plainDirectory)
	t.Cleanup(func() { mustChdir(t, originalDirectory) })

	storeRoot := filepath.Join(home, projectstore.RootDirName, "7e6be546-9043-42d6-90c9-13cf67c2421f")
	if err := projectstore.WriteConfigScope(storeRoot, projectstore.ScopeGlobal); err != nil {
		t.Fatal(err)
	}
	sessionID := "agent-host-test-123456"
	want := newAttachSessionTarget(
		sessionID,
		filepath.Join(storeRoot, projectstore.SessionsDirName),
		filepath.Join(storeRoot, projectstore.ScratchDirName),
	)
	conv := conversation.New(sessionID, "Resolve a real UUID-backed global session")
	writeAttachConversationAt(t, want.conversationPath(), conv)

	got, err := resolveAttachSession("agent-host-test-123")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolveAttachSession() = %#v, want %#v", got, want)
	}
}

func TestResolveAttachSessionReportsCrossStorePrefixAmbiguity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	plainDirectory := t.TempDir()
	originalDirectory := mustChdir(t, plainDirectory)
	t.Cleanup(func() { mustChdir(t, originalDirectory) })

	storeSessions := []struct {
		storeName string
		sessionID string
	}{
		{storeName: "11111111-1111-4111-8111-111111111111", sessionID: "agent-host-test-ambiguous-111"},
		{storeName: "22222222-2222-4222-8222-222222222222", sessionID: "agent-host-test-ambiguous-112"},
	}
	var wantCandidates []string
	for _, fixture := range storeSessions {
		storeRoot := filepath.Join(home, projectstore.RootDirName, fixture.storeName)
		if err := projectstore.WriteConfigScope(storeRoot, projectstore.ScopeGlobal); err != nil {
			t.Fatal(err)
		}
		target := newAttachSessionTarget(
			fixture.sessionID,
			filepath.Join(storeRoot, projectstore.SessionsDirName),
			filepath.Join(storeRoot, projectstore.ScratchDirName),
		)
		writeAttachConversationAt(t, target.conversationPath(), conversation.New(fixture.sessionID, "Ambiguous global session"))
		wantCandidates = append(wantCandidates, target.sessionDirectory)
	}

	_, err := resolveAttachSession("agent-host-test-ambiguous-11")
	if err == nil {
		t.Fatal("resolveAttachSession() succeeded, want ambiguity error")
	}
	want := fmt.Sprintf("ambiguous session id %q: 2 candidates: %s", "agent-host-test-ambiguous-11", strings.Join(wantCandidates, ", "))
	if err.Error() != want {
		t.Fatalf("resolveAttachSession() error = %q, want %q", err, want)
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
		"── ",
		"──── ",
		"ARTIFACTS",
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

func writeAttachConversationAt(t *testing.T, path string, conv *conversation.Conversation) {
	t.Helper()
	data, err := conv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAttachActionAt(t *testing.T, path string, record shellaction.Record) {
	t.Helper()
	data, err := shellaction.EncodeRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
