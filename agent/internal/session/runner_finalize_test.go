package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

// TestCompleteSessionPopulatesRawAnswer drives completeSession with a known
// final answer and verifies the emitted conclusion carries the un-styled raw
// answer next to the styled rendering, while the --final-file write stays
// verbatim (answer + "\n").
func TestCompleteSessionPopulatesRawAnswer(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	tr, err := transcript.New("complete-raw-answer")
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	recorder := newConversationRecorder(tr, "complete-raw-answer", "Goal", "", false, nil, false, false)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	finalPath := filepath.Join(tmpDir, "output", "agent-final-answer.md")
	cfg := legacyConfig{
		finalFile: finalPath,
		verbose:   false,
	}

	runState := newRunLifecycleState(
		context.Background(),
		cfg,
		"complete-raw-answer",
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

	answer := "# Final Answer\n\nBody"
	var diagBuf bytes.Buffer
	bus := ui.NewEventBus(0)
	events := bus.Subscribe()
	if err := runState.completeSession(bus, &diagBuf, answer, 1, 1, false); err != nil {
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
	if conclusion.RawAnswer != answer {
		t.Errorf("RawAnswer = %q, want the raw final answer %q", conclusion.RawAnswer, answer)
	}
	if conclusion.RenderedAnswer == answer {
		t.Errorf("RenderedAnswer = %q, want it to differ from the raw answer (styled rendering)", conclusion.RenderedAnswer)
	}
	if conclusion.FinalAnswerPath != finalPath {
		t.Errorf("FinalAnswerPath = %q, want %q", conclusion.FinalAnswerPath, finalPath)
	}

	data, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read final answer file: %v", err)
	}
	if got := string(data); got != answer+"\n" {
		t.Errorf("final answer file content = %q, want %q (verbatim answer + newline)", got, answer+"\n")
	}
}
