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
// Test 2: completeSession emits one semantic conclusion event containing the
// rendered answer, saved path, and verbose details.
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

	var conclusion ui.SessionConclusionEvent
	conclusionSeen := false
	for {
		select {
		case e := <-events:
			if value, ok := e.(ui.SessionConclusionEvent); ok {
				conclusion = value
				conclusionSeen = true
			}
		default:
			goto doneLoop
		}
	}
doneLoop:
	if !conclusionSeen {
		t.Fatal("expected SessionConclusionEvent")
	}
	if conclusion.Outcome != ui.SessionConclusionCompleted || strings.TrimSpace(conclusion.RenderedAnswer) == "" {
		t.Fatalf("unexpected conclusion: %#v", conclusion)
	}
	if conclusion.FinalAnswerPath != finalPath || !conclusion.Verbose || conclusion.TurnsCompleted != 1 || conclusion.Goal != "Goal" {
		t.Fatalf("conclusion metadata mismatch: %#v", conclusion)
	}
	if strings.Contains(diagBuf.String(), "Final answer saved:") {
		t.Fatalf("saved path was duplicated outside the conclusion event: %s", diagBuf.String())
	}

	// The final answer file should exist on disk.
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("expected final answer file at %s: %v", finalPath, err)
	}
}

// ---------------------------------------------------------------------------
// Test 3: session conclusion diagWriter output (nil display fallback)
// ---------------------------------------------------------------------------

func TestSessionConclusionDiagWriterFallback(t *testing.T) {
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
	runState.printSessionConclusion(nil, &diagBuf, ui.SessionConclusionEvent{
		Outcome:         ui.SessionConclusionCompleted,
		RenderedAnswer:  "  Final answer",
		FinalAnswerPath: filepath.Join(home, "answer.md"),
		SessionID:       runState.sessionID,
		Verbose:         true,
		TurnsCompleted:  3,
		Goal:            runState.goal,
	})

	output := diagBuf.String()
	for _, want := range []string{
		"Final answer",
		"Answer saved to:",
		"Session ID: resume-hint-test",
		"Turns completed: 3",
		"Test goal for resume hint",
		"mct-agent run \\",
		`-t "<your follow-up prompt>" \`,
		"--session-id resume-hint-test",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in diagWriter output, got:\n%s", want, output)
		}
	}
	if strings.Contains(output, "SESSION COMPLETE") || strings.Contains(output, "<next instruction>") {
		t.Fatalf("legacy conclusion copy remains:\n%s", output)
	}
}

func TestSessionConclusionDisplayPath(t *testing.T) {
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
	runState.printSessionConclusion(bus, io.Discard, ui.SessionConclusionEvent{
		Outcome:         ui.SessionConclusionCompleted,
		RenderedAnswer:  "answer",
		FinalAnswerPath: "/tmp/answer.md",
		SessionID:       runState.sessionID,
		Verbose:         true,
		TurnsCompleted:  5,
		Goal:            runState.goal,
	})

	var conclusion ui.SessionConclusionEvent
	seen := false
	for {
		select {
		case e := <-events:
			if value, ok := e.(ui.SessionConclusionEvent); ok {
				conclusion = value
				seen = true
			}
		default:
			goto donePrintResume
		}
	}
donePrintResume:
	if !seen {
		t.Fatal("expected SessionConclusionEvent to be emitted")
	}
	if conclusion.Outcome != ui.SessionConclusionCompleted {
		t.Fatalf("unexpected conclusion outcome %q", conclusion.Outcome)
	}
	if conclusion.SessionID != "resume-hint-display" || conclusion.TurnsCompleted != 5 || conclusion.Goal != "Test goal for display path" {
		t.Fatalf("conclusion metadata mismatch: %#v", conclusion)
	}
}

func TestSessionConclusionNonVerboseOmitsDetails(t *testing.T) {
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

	finalPath := filepath.Join(home, ".machtiani", "project", "sessions", "resume-hint-quiet", "chat", "agent-final-answer.md")
	event := ui.SessionConclusionEvent{
		Outcome:         ui.SessionConclusionCompleted,
		RenderedAnswer:  "answer",
		FinalAnswerPath: finalPath,
		SessionID:       runState.sessionID,
	}

	var diagBuf bytes.Buffer
	runState.printSessionConclusion(nil, &diagBuf, event)
	if got := diagBuf.String(); !strings.Contains(got, "Answer saved to:") || !strings.Contains(got, "Continue this session:") {
		t.Fatalf("quiet conclusion missing essential information:\n%s", got)
	} else if strings.Contains(got, "Turns completed:") || strings.Contains(got, "Goal so far:") {
		t.Fatalf("quiet conclusion leaked verbose details:\n%s", got)
	}

	bus := ui.NewEventBus(0)
	events := bus.Subscribe()
	runState.printSessionConclusion(bus, io.Discard, event)

	select {
	case event := <-events:
		conclusion, ok := event.(ui.SessionConclusionEvent)
		if !ok {
			t.Fatalf("event type = %T, want ui.SessionConclusionEvent", event)
		}
		if conclusion.Verbose {
			t.Fatalf("quiet conclusion marked verbose: %#v", conclusion)
		}
		if conclusion.FinalAnswerPath != finalPath || conclusion.SessionID != "resume-hint-quiet" {
			t.Fatalf("quiet conclusion metadata mismatch: %#v", conclusion)
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
		hint, ok := event.(ui.SessionConclusionEvent)
		if !ok {
			t.Fatalf("event type = %T, want ui.SessionConclusionEvent", event)
		}
		if hint.Outcome != ui.SessionConclusionShellInterrupted || hint.SessionID != "shell-resume-display" {
			t.Fatalf("interrupted conclusion mismatch: %#v", hint)
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
			case ui.SessionConclusionEvent:
				sawHint = true
				if e.Outcome != ui.SessionConclusionShellInterrupted || e.SessionID != "shell-resume-interrupted" {
					t.Fatalf("interrupted conclusion mismatch: %#v", e)
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
		"USER INPUT NEEDED",
		"Why this needs your input:",
		"Your decision:",
		"Which approach do you prefer?",
		"Option A is safer but slower.",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in diagWriter output, got:\n%s", want, output)
		}
	}
}
