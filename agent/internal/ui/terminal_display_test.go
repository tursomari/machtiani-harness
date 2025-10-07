package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestPromptStreamCompleteShowsOnlyFirstLine(t *testing.T) {
	var buf bytes.Buffer
	display := NewTerminalDisplay(&buf)
	display.width = 120
	display.StartSession("Primary goal")
	stream := display.BeginPrompt("What is concurrency?")

	answer := "Concurrency lets multiple tasks make progress.\nIt does not guarantee parallel execution."
	stream.Complete(answer)

	output := buf.String()
	if !strings.Contains(output, "Concurrency lets multiple tasks make progress.") {
		t.Fatalf("expected first line preview, got %q", output)
	}
	if strings.Contains(output, "It does not guarantee parallel execution.") {
		t.Fatalf("unexpected multi-line preview in output: %q", output)
	}
}

func TestPromptStreamCompleteTruncatesLongLine(t *testing.T) {
	var buf bytes.Buffer
	display := NewTerminalDisplay(&buf)
	display.width = 20
	display.StartSession("Primary goal")
	stream := display.BeginPrompt("Explain goroutines")

	answer := "Goroutines are lightweight managed threads in Go that scale."
	stream.Complete(answer)

	lines := strings.Split(buf.String(), "\n")
	var preview string
	for _, line := range lines {
		if strings.HasPrefix(line, "`-- ") {
			preview = line
		}
	}
	if preview == "" {
		t.Fatalf("did not capture preview line in output: %q", buf.String())
	}
	if len([]rune(preview)) > display.width {
		t.Fatalf("preview exceeds width: got %q (len=%d) width=%d", preview, len([]rune(preview)), display.width)
	}
	if !strings.HasSuffix(preview, "...") {
		t.Fatalf("expected truncated preview to end with ellipsis: %q", preview)
	}
}

func TestShowFinalWithoutMarker(t *testing.T) {
	var buf bytes.Buffer
	display := NewTerminalDisplay(&buf)
	display.StartSession("Primary goal")
	display.ShowFinal("Hello world")

	output := buf.String()
	expectedPrefix := "\n===> FINAL RESPONSE <===\n"
	if !strings.HasPrefix(output, expectedPrefix) {
		t.Fatalf("final output should begin with separator, got %q", output)
	}
	if strings.Contains(output, "[full answer]") {
		t.Fatalf("final answer marker should be absent, got %q", output)
	}
	if strings.Contains(output, "glow") {
		t.Fatalf("final output should not mention glow, got %q", output)
	}
	if !strings.Contains(output, "Hello world") {
		t.Fatalf("final output should include answer text, got %q", output)
	}
}
