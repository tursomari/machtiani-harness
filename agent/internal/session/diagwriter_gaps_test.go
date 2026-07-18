package session

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

// ---------------------------------------------------------------------------
// Test 1: completeSession diagWriter captures CompleteModePlanTask failure warning
// ---------------------------------------------------------------------------

func TestCompleteSessionDiagWriterCapturesModePlanTaskFailure(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	tr, err := transcript.New("complete-modeplan-fail")
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	recorder := newConversationRecorder(tr, "complete-modeplan-fail", "Goal", "", false, nil, false)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Set up a valid finalFile so writeFinalAnswer succeeds and we reach the
	// CompleteModePlanTask call.
	cfg := legacyConfig{
		finalFile: filepath.Join(tmpDir, "output", "agent-final-answer.md"),
		verbose:   false,
	}

	runState := newRunLifecycleState(
		context.Background(),
		cfg,
		"complete-modeplan-fail",
		"Goal",
		"Goal",
		"",
		"",
		"",
		0, "",
		nil,
	)
	runState.recorder = recorder
	runState.tr = tr
	runState.sessionStatus = "error"

	// Write a corrupt mode-plan.json so that CompleteModePlanTask returns an
	// error from json.Unmarshal, which completeSession routes to diagWriter.
	planPath, err := modePlanPath("complete-modeplan-fail")
	if err != nil {
		t.Fatalf("modePlanPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatalf("mkdir mode plan dir: %v", err)
	}
	if err := os.WriteFile(planPath, []byte("{invalid json"), 0o644); err != nil {
		t.Fatalf("write corrupt mode plan: %v", err)
	}

	var diagBuf bytes.Buffer
	err = runState.completeSession(nil, &diagBuf, "final answer", 1, 1, false)
	if err != nil {
		t.Fatalf("completeSession returned unexpected error: %v", err)
	}

	output := diagBuf.String()
	if !strings.Contains(output, "Warning: failed to update mode plan task status") {
		t.Fatalf("expected 'Warning: failed to update mode plan task status' in diagWriter, got:\n%s", output)
	}
}

// ---------------------------------------------------------------------------
// Test 2: completeSession verbose success path captures "Final answer saved:"
// and presentFinalAnswer renders to the injected display
// ---------------------------------------------------------------------------

func TestCompleteSessionVerboseSuccessCapturesDiagAndDisplay(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	tr, err := transcript.New("complete-verbose-success")
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	recorder := newConversationRecorder(tr, "complete-verbose-success", "Goal", "", false, nil, false)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	finalPath := filepath.Join(tmpDir, "output", "agent-final-answer.md")

	cfg := legacyConfig{
		finalFile: finalPath,
		verbose:   true,
	}

	runState := newRunLifecycleState(
		context.Background(),
		cfg,
		"complete-verbose-success",
		"Goal",
		"Goal",
		"",
		"",
		"",
		0, "",
		nil,
	)
	runState.recorder = recorder
	runState.tr = tr
	runState.sessionStatus = "error"

	var diagBuf bytes.Buffer
	bus := ui.NewEventBus(0)
	events := bus.Subscribe()
	err = runState.completeSession(bus, &diagBuf, "# Final Answer\nDone.", 1, 1, false)
	if err != nil {
		t.Fatalf("completeSession returned unexpected error: %v", err)
	}

	// diagWriter should contain "Final answer saved:" because verbose=true.
	diagOutput := diagBuf.String()
	if !strings.Contains(diagOutput, "Final answer saved:") {
		t.Fatalf("expected 'Final answer saved:' in diagWriter, got:\n%s", diagOutput)
	}

	// presentFinalAnswer should emit a FinalAnswerEvent with the rendered answer.
	finalSeen := false
	for {
		select {
		case e := <-events:
			if e.Type() == "FinalAnswer" {
				if fa, ok := e.(ui.FinalAnswerEvent); ok && strings.TrimSpace(fa.RenderedText) != "" {
					finalSeen = true
				}
			}
		default:
			goto doneLoop
		}
	}
doneLoop:
	if !finalSeen {
		t.Fatal("expected FinalAnswerEvent emitted by presentFinalAnswer")
	}

	// The final answer file should exist on disk.
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("expected final answer file at %s: %v", finalPath, err)
	}
}

// ---------------------------------------------------------------------------
// Test 3: printResumeHint diagWriter output (nil display → diagWriter fallback)
// ---------------------------------------------------------------------------

func TestPrintResumeHintDiagWriterFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runState := newRunLifecycleState(
		context.Background(),
		legacyConfig{verbose: true},
		"resume-hint-test",
		"Test goal for resume hint",
		"Test goal for resume hint",
		"",
		"",
		"",
		0, "",
		nil,
	)

	var diagBuf bytes.Buffer
	runState.printResumeHint(nil, &diagBuf, "=== SESSION COMPLETE ===", 3, filepath.Join(home, "answer.md"))

	output := diagBuf.String()
	if !strings.HasPrefix(output, "\n=== SESSION COMPLETE ===\n") {
		t.Fatalf("expected one blank line before verbose completion output, got:\n%q", output)
	}
	for _, want := range []string{
		"=== SESSION COMPLETE ===",
		"Session ID: resume-hint-test",
		"Turns completed: 3",
		"Test goal for resume hint",
		"mct-agent run",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in diagWriter output, got:\n%s", want, output)
		}
	}
	if strings.Contains(output, "This answer is also available at:") {
		t.Fatalf("verbose completion duplicated final-answer path block:\n%s", output)
	}
}

func TestPrintResumeHintDisplayPath(t *testing.T) {
	runState := newRunLifecycleState(
		context.Background(),
		legacyConfig{verbose: true},
		"resume-hint-display",
		"Test goal for display path",
		"Test goal for display path",
		"",
		"",
		"",
		0, "",
		nil,
	)

	bus := ui.NewEventBus(0)
	events := bus.Subscribe()
	runState.printResumeHint(bus, io.Discard, "=== SESSION INTERRUPTED ===", 5, "/tmp/answer.md")

	var hint ui.ContinuationHintEvent
	seen := false
	for {
		select {
		case e := <-events:
			if value, ok := e.(ui.ContinuationHintEvent); ok {
				hint = value
				seen = true
			}
		default:
			goto donePrintResume
		}
	}
donePrintResume:
	if !seen {
		t.Fatal("expected ContinuationHintEvent to be emitted")
	}
	if hint.Header != "=== SESSION INTERRUPTED ===" {
		t.Fatalf("unexpected continuation header %q", hint.Header)
	}

	joined := hint.Header + "\n" + strings.Join(hint.DetailLines, "\n") + "\n" + hint.Command
	for _, want := range []string{
		"=== SESSION INTERRUPTED ===",
		"Session ID: resume-hint-display",
		"Turns completed: 5",
		"Test goal for display path",
		"mct-agent run",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected %q in display output, got:\n%s", want, joined)
		}
	}
}

func TestPrintResumeHintNonVerboseOnlyPrintsContinuation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runState := newRunLifecycleState(
		context.Background(),
		legacyConfig{verbose: false},
		"resume-hint-quiet",
		"Goal that should stay hidden",
		"Goal that should stay hidden",
		"",
		"",
		"",
		0, "",
		nil,
	)

	command := `mct-agent run -t "<next instruction>" --session-id resume-hint-quiet`
	rule := strings.Repeat("-", len("  $ "+command))
	finalPath := filepath.Join(home, ".machtiani", "project", "sessions", "resume-hint-quiet", "chat", "agent-final-answer.md")
	want := "This answer is also available at:\n" +
		"  ~/.machtiani/project/sessions/resume-hint-quiet/chat/agent-final-answer.md\n\n" +
		"Continue with your next instruction:\n  " + rule + "\n  $ " + command + "\n  " + rule

	var diagBuf bytes.Buffer
	runState.printResumeHint(nil, &diagBuf, "=== SESSION COMPLETE ===", 4, finalPath)
	if got := diagBuf.String(); got != "\n"+want+"\n" {
		t.Fatalf("quiet diag output mismatch\nwant: %q\n got: %q", "\n"+want+"\n", got)
	}

	bus := ui.NewEventBus(0)
	events := bus.Subscribe()
	runState.printResumeHint(bus, io.Discard, "=== SESSION COMPLETE ===", 4, finalPath)

	select {
	case event := <-events:
		hint, ok := event.(ui.ContinuationHintEvent)
		if !ok {
			t.Fatalf("event type = %T, want ui.ContinuationHintEvent", event)
		}
		if hint.Header != "" || len(hint.DetailLines) != 0 {
			t.Fatalf("quiet continuation leaked verbose detail: %#v", hint)
		}
		if hint.FinalAnswerPath != finalPath {
			t.Fatalf("quiet final-answer path = %q, want %q", hint.FinalAnswerPath, finalPath)
		}
		if hint.Command != `mct-agent run -t "<next instruction>" --session-id resume-hint-quiet` {
			t.Fatalf("quiet display command mismatch: %q", hint.Command)
		}
	default:
		t.Fatal("expected quiet continuation event")
	}

	select {
	case event := <-events:
		t.Fatalf("unexpected extra quiet output event: %#v", event)
	default:
	}
}

func TestShellAgentResumeHintDisplayPath(t *testing.T) {
	runState := newRunLifecycleState(
		context.Background(),
		legacyConfig{verbose: false},
		"shell-resume-display",
		"Goal with interrupted shell-agent work",
		"Goal with interrupted shell-agent work",
		"",
		"",
		"",
		0, "",
		nil,
	)

	bus := ui.NewEventBus(0)
	events := bus.Subscribe()
	runState.printShellAgentResumeHint(bus, io.Discard)

	select {
	case event := <-events:
		hint, ok := event.(ui.ContinuationHintEvent)
		if !ok {
			t.Fatalf("event type = %T, want ui.ContinuationHintEvent", event)
		}
		if hint.Header != "=== SHELL-AGENT INTERRUPTED ===" {
			t.Fatalf("header = %q", hint.Header)
		}
		if hint.Instruction != "Resume the interrupted shell-agent work:" {
			t.Fatalf("instruction = %q", hint.Instruction)
		}
		if hint.Command != "mct-agent run --session-id shell-resume-display" {
			t.Fatalf("command = %q", hint.Command)
		}
		if got := strings.Join(hint.DetailLines, "\n"); !strings.Contains(got, "Shell-agent work is resumable.") {
			t.Fatalf("detail lines missing resumable note: %#v", hint.DetailLines)
		}
	default:
		t.Fatal("expected shell-agent resume hint event")
	}
}

func TestInterruptedResultWithHintEmitsShellAgentResumeInstruction(t *testing.T) {
	runState := newRunLifecycleState(
		context.Background(),
		legacyConfig{verbose: false},
		"shell-resume-interrupted",
		"Goal with interrupted shell-agent work",
		"Goal with interrupted shell-agent work",
		"",
		"",
		"",
		0, "",
		nil,
	)
	runState.recorder = &conversationRecorder{shellAgentResumable: true}

	bus := ui.NewEventBus(0)
	events := bus.Subscribe()
	result := runState.interruptedResultWithHint(bus, io.Discard, context.Canceled)
	if result.ExitCode != 130 {
		t.Fatalf("ExitCode = %d, want 130", result.ExitCode)
	}

	var sawHint, sawEnded bool
	for {
		select {
		case event := <-events:
			switch e := event.(type) {
			case ui.ContinuationHintEvent:
				sawHint = true
				if e.Command != "mct-agent run --session-id shell-resume-interrupted" {
					t.Fatalf("command = %q", e.Command)
				}
			case ui.SessionEndedEvent:
				sawEnded = true
			}
		default:
			if !sawHint {
				t.Fatal("expected shell-agent resume hint")
			}
			if !sawEnded {
				t.Fatal("expected session ended event after shell-agent resume hint")
			}
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Test 4: resume-context diagWriter capture — suspendForUserInput with
// loaded state (resume scenario)
// ---------------------------------------------------------------------------

func TestSuspendForUserInputResumeContextCapturesDiag(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	tr, err := transcript.New("resume-suspend-diag")
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	// Simulate a resume scenario: create a conversation.json on disk that
	// the recorder will load.
	convPath := filepath.Join(tmpDir, "conversation.json")
	initialConv := conversation.New("resume-suspend-diag", "Test resume suspend goal")
	if data, err := initialConv.Marshal(); err != nil {
		t.Fatalf("marshal conversation: %v", err)
	} else {
		if err := os.WriteFile(convPath, data, 0o644); err != nil {
			t.Fatalf("write conversation.json: %v", err)
		}
	}

	loadedState := &SessionState{
		SessionID:      "resume-suspend-diag",
		Goal:           "Test resume suspend goal",
		TurnsCompleted: 2,
	}

	recorder := newConversationRecorder(tr, "resume-suspend-diag", "Test resume suspend goal", convPath, true, loadedState, false)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	runState := newRunLifecycleState(
		context.Background(),
		legacyConfig{verbose: true},
		"resume-suspend-diag",
		"Test resume suspend goal",
		"Test resume suspend goal",
		"",
		"",
		"",
		0, "",
		loadedState,
	)
	runState.recorder = recorder
	runState.tr = tr
	runState.sessionStatus = "error"

	var diagBuf bytes.Buffer
	result, err := runState.suspendForUserInput(
		nil, // display=nil → triggers diagWriter fallback path
		&diagBuf,
		"Which approach do you prefer?",
		"Option A is safer but slower.",
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
		"Session ID: resume-suspend-diag",
		"Which approach do you prefer?",
		"Option A is safer but slower.",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in diagWriter output, got:\n%s", want, output)
		}
	}
}
