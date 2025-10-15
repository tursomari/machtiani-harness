package ui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestPromptStreamCompleteShowsOnlyFirstLine(t *testing.T) {
	var buf bytes.Buffer
	display := NewTerminalDisplay(&buf)
	display.width = 120
	display.StartSession("Primary goal")
	stream := display.BeginPrompt("What is concurrency?", nil)

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
	stream := display.BeginPrompt("Explain goroutines", nil)

	answer := "Goroutines are lightweight managed threads in Go that scale."
	stream.Complete(answer)

	raw := buf.String()
	lines := strings.Split(stripANSI(raw), "\n")
	var preview string
	for _, line := range lines {
		if strings.HasPrefix(line, "`-- ") {
			preview = line
		}
	}
	if preview == "" {
		t.Fatalf("did not capture preview line in output: %q", raw)
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

func TestNotifyDuringStream(t *testing.T) {
	var buf bytes.Buffer
	display := NewTerminalDisplay(&buf)
	display.width = 120
	stream := display.BeginPrompt("Prompt", nil)
	stream.OnChunk("partial answer")
	display.Notify("[llm failover] triggered: alias=test from primary")
	stream.OnChunk(" with continuation")
	stream.Complete("partial answer with continuation")

	output := stripANSI(buf.String())
	if !strings.Contains(output, "|   [llm failover] triggered: alias=test from primary") {
		t.Fatalf("expected notify line in output, got %q", output)
	}
	if strings.Count(output, "`-- ") == 0 {
		t.Fatalf("expected prompt preview line, got %q", output)
	}
	if !strings.Contains(output, "partial answer with continuation") {
		t.Fatalf("expected completion text, got %q", output)
	}
}

var ansiCodes = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiCodes.ReplaceAllString(s, "")
}

func TestNotifyBeforePrompt(t *testing.T) {
	var buf bytes.Buffer
	display := NewTerminalDisplay(&buf)
	display.Notify("[llm failover] triggered early")

	output := buf.String()
	if strings.TrimSpace(output) != "[llm failover] triggered early" {
		t.Fatalf("expected notify output before prompt, got %q", output)
	}
}
