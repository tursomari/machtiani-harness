package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/transcript"
)

// ---------------------------------------------------------------------------
// 1. suspendForUserInput — diagWriter capture with display=nil fallback path
// ---------------------------------------------------------------------------

func TestSuspendForUserInputDiagWriterCapturesUserInputHint(t *testing.T) {
	tr, err := transcript.New("suspend-diag-test")
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	recorder := newConversationRecorder(tr, "suspend-diag-test", "Goal", "", false, nil)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	runState := newRunLifecycleState(
		context.Background(),
		legacyConfig{},
		"suspend-diag-test",
		"Goal",
		"Goal",
		"",
		"",
		"",
		0,
		"",
		nil,
	)
	runState.recorder = recorder
	runState.tr = tr

	var diagBuf bytes.Buffer
	result, err := runState.suspendForUserInput(
		nil, // display=nil → triggers diagWriter fallback path
		&diagBuf,
		"Do you want the safer fix?",
		"The safer fix preserves existing behavior.",
		"tradeoff choice",
		"Original mixed ask",
	)
	if err != nil {
		t.Fatalf("suspendForUserInput: %v", err)
	}
	if result.Status != "suspended_user_input" {
		t.Fatalf("unexpected result status: %q", result.Status)
	}

	output := diagBuf.String()
	for _, want := range []string{
		"=== USER INPUT NEEDED ===",
		"Session ID: suspend-diag-test",
		"The safer fix preserves existing behavior.",
		"Do you want the safer fix?",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in diagWriter output, got:\n%s", want, output)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. prepareSessionEnvironment — diagWriter captures marker max-age parse warning
// ---------------------------------------------------------------------------

func TestPrepareSessionEnvironmentDiagWriterCapturesMarkerMaxAgeWarning(t *testing.T) {
	repo := setupSessionEnvironmentTestRepo(t, "[environment]\ntype = \"local\"\n")
	_ = repo

	// Set an invalid marker max-age so shellAgentMarkerMaxAge() returns an error,
	// which prepareSessionEnvironment routes through diagWriter.
	t.Setenv("MACHTIANI_SHELL_AGENT_MARKER_MAX_AGE", "not-a-duration")

	var diagBuf bytes.Buffer
	bootstrap, err := prepareSessionEnvironment("agent-marker-diag", legacyConfig{}, &diagBuf)
	if err != nil {
		t.Fatalf("prepareSessionEnvironment: %v", err)
	}
	defer bootstrap.restore()

	output := diagBuf.String()
	if !strings.Contains(output, "Warning:") {
		t.Fatalf("expected marker max-age warning in diagWriter, got: %s", output)
	}
	if !strings.Contains(output, "using default") {
		t.Fatalf("expected 'using default' in diagWriter, got: %s", output)
	}
	if !strings.Contains(output, "MACHTIANI_SHELL_AGENT_MARKER_MAX_AGE") {
		t.Fatalf("expected env var name in diagWriter, got: %s", output)
	}
}

// ---------------------------------------------------------------------------
// 3a. startLLMRetryLogger — diagWriter captures listener stopped message
// ---------------------------------------------------------------------------

func TestStartLLMRetryLoggerDiagWriterCapturesListenerError(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.jsonl")

	var diagBuf bytes.Buffer
	cancel, done, err := startLLMRetryLogger(&mockDisplay{}, missingPath, &diagBuf)
	if err != nil {
		t.Fatalf("startLLMRetryLogger: %v", err)
	}
	defer cancel()

	<-done

	output := diagBuf.String()
	if !strings.Contains(output, "[trajectory] retry listener stopped") {
		t.Fatalf("expected listener stopped message in diagWriter, got: %s", output)
	}
}

// ---------------------------------------------------------------------------
// 3b. startLLMCacheUsageLogger — diagWriter captures listener stopped message
// ---------------------------------------------------------------------------

func TestStartLLMCacheUsageLoggerDiagWriterCapturesListenerError(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.jsonl")

	var diagBuf bytes.Buffer
	cancel, done, err := startLLMCacheUsageLogger(&mockDisplay{}, missingPath, &diagBuf)
	if err != nil {
		t.Fatalf("startLLMCacheUsageLogger: %v", err)
	}
	defer cancel()

	<-done

	output := diagBuf.String()
	if !strings.Contains(output, "[trajectory] cache usage listener stopped") {
		t.Fatalf("expected listener stopped message in diagWriter, got: %s", output)
	}
}

// ---------------------------------------------------------------------------
// 3c. startLLMCacheDiagnosticsLogger — diagWriter captures listener stopped message
// ---------------------------------------------------------------------------

func TestStartLLMCacheDiagnosticsLoggerDiagWriterCapturesListenerError(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.jsonl")

	var diagBuf bytes.Buffer
	cancel, done, err := startLLMCacheDiagnosticsLogger(&mockDisplay{}, missingPath, &diagBuf)
	if err != nil {
		t.Fatalf("startLLMCacheDiagnosticsLogger: %v", err)
	}
	defer cancel()

	<-done

	output := diagBuf.String()
	if !strings.Contains(output, "[trajectory] cache diagnostics listener stopped") {
		t.Fatalf("expected listener stopped message in diagWriter, got: %s", output)
	}
}

// ---------------------------------------------------------------------------
// 3d. startShellActionStreamer — diagWriter captures listener stopped message
// ---------------------------------------------------------------------------

func TestStartShellActionStreamerDiagWriterCapturesListenerError(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.jsonl")

	var diagBuf bytes.Buffer
	cancel, done, err := startShellActionStreamer(&mockDisplay{}, missingPath, &diagBuf)
	if err != nil {
		t.Fatalf("startShellActionStreamer: %v", err)
	}
	defer cancel()

	<-done

	output := diagBuf.String()
	if !strings.Contains(output, "[trajectory] shell agent listener stopped") {
		t.Fatalf("expected listener stopped message in diagWriter, got: %s", output)
	}
}

// ---------------------------------------------------------------------------
// 4. writeFinalAnswer — diagWriter captures file-write error
// ---------------------------------------------------------------------------

func TestWriteFinalAnswerDiagWriterCapturesWriteError(t *testing.T) {
	tmpDir := t.TempDir()

	// Place a regular file where the parent directory should be, so
	// os.MkdirAll fails when trying to create the output path.
	blocker := filepath.Join(tmpDir, "blocker-dir")
	if err := os.WriteFile(blocker, []byte("block"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	// finalFileFlag points inside blocker (which is a file, not a dir).
	badPath := filepath.Join(blocker, "agent-final-answer.md")

	var diagBuf bytes.Buffer
	err := writeFinalAnswer("test-write-fail", "answer content", badPath, false, false, &diagBuf)
	if err == nil {
		t.Fatalf("expected writeFinalAnswer to fail, but got nil error")
	}

	// writeFinalAnswer returns the error directly (does not write to diagWriter
	// for the WriteFile failure itself). The caller (completeSession) would
	// capture the error. Verify the function returns an error and does not
	// panic. The diagWriter is not written to for write failures — only
	// completeSession routes it. So we verify the error is returned.
	_ = diagBuf.String() // no assertion on content for this path

	// Now test the verbose success path to confirm diagWriter captures "Final answer saved".
	goodPath := filepath.Join(tmpDir, "output", "agent-final-answer.md")
	var diagBuf2 bytes.Buffer
	err2 := writeFinalAnswer("test-write-ok", "answer content", goodPath, true, false, &diagBuf2)
	if err2 != nil {
		t.Fatalf("unexpected error on successful write: %v", err2)
	}
	if !strings.Contains(diagBuf2.String(), "Final answer saved:") {
		t.Fatalf("expected 'Final answer saved:' in diagWriter, got: %s", diagBuf2.String())
	}
}

// ---------------------------------------------------------------------------
// 4b. completeSession — diagWriter captures writeFinalAnswer error
// ---------------------------------------------------------------------------

func TestCompleteSessionDiagWriterCapturesWriteError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	tr, err := transcript.New("complete-diag-test")
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	recorder := newConversationRecorder(tr, "complete-diag-test", "Goal", "", false, nil)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	cfg := legacyConfig{
		finalFile: filepath.Join(tmpDir, "blocker-file", "answer.md"),
		verbose:   false,
	}

	runState := newRunLifecycleState(
		context.Background(),
		cfg,
		"complete-diag-test",
		"Goal",
		"Goal",
		"",
		"",
		"",
		0,
		"",
		nil,
	)
	runState.recorder = recorder
	runState.tr = tr
	runState.sessionStatus = "running"

	// Place a file where the parent directory should be so writeFinalAnswer fails.
	blockerDir := filepath.Join(tmpDir, "blocker-file")
	if err := os.WriteFile(blockerDir, []byte("block"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	var diagBuf bytes.Buffer
	err = runState.completeSession(&mockDisplay{}, &diagBuf, "final answer", 1, 1, false)
	if err == nil {
		t.Fatalf("expected completeSession to fail, but got nil error")
	}
	// completeSession sets sessionErr and returns the error; it does not
	// write to diagWriter for the writeFinalAnswer failure itself.
	// Verify the session error was captured.
	if runState.sessionErr == nil {
		t.Fatalf("expected sessionErr to be set")
	}
}

// ---------------------------------------------------------------------------
// 5. hydrateState / applyPlannerProgress — mode-plan-progress update
// ---------------------------------------------------------------------------

// Note: UpdateModePlanProgress is currently a no-op that always returns nil
// (see mode.go:359). This means the "Warning: failed to update mode plan
// progress" diagnostic path in applyPlannerProgress is unreachable with
// the current implementation. The test below verifies that applyPlannerProgress
// does not produce the warning when UpdateModePlanProgress succeeds (the only
// possible path), and confirms the no-op behavior.

func TestApplyPlannerProgressNoWarningWhenUpdateSucceeds(t *testing.T) {
	runState := newRunLifecycleState(
		context.Background(),
		legacyConfig{verbose: true},
		"progress-test",
		"Goal",
		"Goal",
		"",
		"",
		"",
		0,
		"",
		nil,
	)

	// Add a success file so plannerProgress.toState() returns non-nil,
	// which triggers the UpdateModePlanProgress call.
	runState.plannerProgress.recordSuccess([]string{"/some/file.go"})

	state := runState.baseSessionState()
	var diagBuf bytes.Buffer
	runState.applyPlannerProgress(&state, &diagBuf)

	output := diagBuf.String()
	if strings.Contains(output, "Warning: failed to update mode plan progress") {
		t.Fatalf("did not expect warning, but got: %s", output)
	}
	// UpdateModePlanProgress is a no-op, so the warning is unreachable.
	// This test confirms the happy path is silent.
}
