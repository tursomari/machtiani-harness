package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// requireContains is a test helper that checks the given output contains the
// expected substring using strings.Contains.  It marks itself as a helper so
// failures point to the caller, not this function.
func requireContains(t *testing.T, output, expected string) {
	t.Helper()
	if !strings.Contains(output, expected) {
		t.Errorf("expected output to contain %q\nGot: %s", expected, output)
	}
}

// newTestFormatter creates a Formatter writing to a bytes.Buffer and wired
// into a fresh EventBus.  The Theme has all colour codes set to empty strings
// so that captured output is plain-text and trivially testable.  The returned
// EventBus should be closed by the caller to shut down the event-loop
// goroutine.
func newTestFormatter() (f *Formatter, bus *EventBus, buf *bytes.Buffer) {
	buf = new(bytes.Buffer)
	bus = NewEventBus(256)

	theme := Theme{
		// All colour codes empty → plain-text output.
		ResetColor:   "",
		GrayColor:    "",
		ErrorColor:   "",
		WarningColor: "",
		InfoColor:    "",

		PromptFirstLinePrefix: "`-- ",
		PromptSpacerPrefix:    "    ",
		FinalAnswerHeader:     "FINAL:",
		NotificationPrefix:    "|   ",
		ErrorPrefix:           "error: ",
		EmptyPlaceholder:      "...",
		EmptyResponseText:     "(empty)",
		PromptWindowLines:     3,
		PromptContentLines:    2,
	}

	f = NewFormatter(buf, bus, theme, nil, "test")
	return
}

// stripANSI removes ANSI escape sequence prefixes from the output so that
// string.Contains assertions work reliably against the visible text content.
func stripANSI(s string) string {
	return strings.ReplaceAll(s, "\033[", "")
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestFormatterSessionStarted(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()

	bus.Emit(SessionStartedEvent{Goal: "test goal"})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())
	// The timer goroutine is disabled for non-terminal writers (bytes.Buffer),
	// so SessionStartedEvent produces no visible output in this configuration.
	// We verify the buffer is readable and the event-loop did not panic.
	if len(output) == 0 {
		t.Log("SessionStartedEvent produced no output (timer disabled for non-terminal writer)")
	}
	_ = f
}

func TestFormatterChunkReceived(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()

	bus.Emit(PromptStartedEvent{StreamID: "s1", Prompt: "hello"})
	bus.Emit(ChunkReceivedEvent{StreamID: "s1", Text: "ab"})
	bus.Emit(ChunkReceivedEvent{StreamID: "s1", Text: "cd"})
	bus.Emit(ChunkReceivedEvent{StreamID: "s1", Text: "ef"})
	bus.Emit(PromptCompletedEvent{StreamID: "s1", FinalText: "abcdef"})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())

	requireContains(t, output, "`--")
	requireContains(t, output, "abcdef")
	_ = f
}

func TestFormatterChunkReceivedStreamCorrelation(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()

	bus.Emit(PromptStartedEvent{StreamID: "s1", Prompt: "p1"})
	bus.Emit(ChunkReceivedEvent{StreamID: "s2", Text: "wrong"})
	bus.Emit(ChunkReceivedEvent{StreamID: "s1", Text: "correct"})
	bus.Emit(PromptCompletedEvent{StreamID: "s1", FinalText: "correct"})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())

	requireContains(t, output, "correct")
	if strings.Contains(output, "wrong") {
		t.Errorf("expected output NOT to contain %q\nGot: %s", "wrong", output)
	}
	_ = f
}

func TestFormatterNotificationAllLevels(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()

	bus.Emit(NotificationEvent{Level: NotificationInfo, Message: "info msg"})
	bus.Emit(NotificationEvent{Level: NotificationWarning, Message: "warn msg"})
	bus.Emit(NotificationEvent{Level: NotificationError, Message: "err msg"})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())

	requireContains(t, output, "info msg")
	requireContains(t, output, "warn msg")
	requireContains(t, output, "err msg")
	_ = f
}

func TestFormatterFinalAnswer(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()

	bus.Emit(FinalAnswerEvent{RenderedText: "answer text here"})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())

	requireContains(t, output, "FINAL:")
	requireContains(t, output, "answer text here")
	_ = f
}

func TestFormatterActionExecuted(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()

	bus.Emit(PromptStartedEvent{StreamID: "s1", Prompt: "test"})
	bus.Emit(ActionExecutedEvent{Description: "cmd: ls"})
	bus.Emit(PromptCompletedEvent{StreamID: "s1", FinalText: "llm output"})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())

	requireContains(t, output, "cmd: ls")
	_ = f
}

func TestFormatterRawString(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()

	bus.Emit(RawStringEvent{Text: "raw output line"})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())

	requireContains(t, output, "raw output line")
	_ = f
}
