package ui

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestPromptStreamCompleteShowsLastLinesWithBlankHeader(t *testing.T) {
	var buf bytes.Buffer
	display := newTestDisplay(&buf)
	display.width = 120
	display.StartSession("Primary goal")
	stream := display.BeginPrompt("What is concurrency?", nil)

	answer := strings.Join([]string{
		"Line1",
		"Line2",
		"Line3",
		"Line4",
		"Line5",
		"Line6",
	}, "\n")
	stream.Complete(answer)

	expected := []string{"", "Line1", "Line2", "Line3", "Line4", "Line5", "Line6", "", ""}
	if !slicesEqual(stream.renderedLines, expected) {
		t.Fatalf("expected last five lines %v, got %v", expected, stream.renderedLines)
	}
}

func TestPromptStreamCompleteTruncatesLongLine(t *testing.T) {
	var buf bytes.Buffer
	display := newTestDisplay(&buf)
	display.width = 20
	display.StartSession("Primary goal")
	stream := display.BeginPrompt("Explain goroutines", nil)

	answer := "Goroutines are lightweight managed threads in Go that scale."
	stream.Complete(answer)

	if len(stream.renderedLines) != promptWindowLines {
		t.Fatalf("expected %d preview lines, got %v", promptWindowLines, stream.renderedLines)
	}
	var preview string
	for _, line := range stream.renderedLines {
		if strings.TrimSpace(line) != "" {
			preview = line
			break
		}
	}
	if preview == "" {
		t.Fatalf("did not capture preview line in output: %v", stream.renderedLines)
	}
	if len([]rune(promptFirstLinePrefix+preview)) > display.width {
		t.Fatalf("preview exceeds width: got %q (len=%d) width=%d", preview, len([]rune(promptFirstLinePrefix+preview)), display.width)
	}
	if !strings.HasSuffix(preview, "...") {
		t.Fatalf("expected truncated preview to end with ellipsis: %q", preview)
	}
}

func TestPromptStreamCompleteShowsAllLinesWhenFewerThanWindow(t *testing.T) {
	var buf bytes.Buffer
	display := newTestDisplay(&buf)
	display.width = 120
	display.StartSession("Primary goal")
	stream := display.BeginPrompt("Explain channels", nil)

	answer := strings.Join([]string{"Line1", "Line2", "Line3"}, "\n")
	stream.Complete(answer)

	expected := []string{"", "Line1", "Line2", "Line3", "", "", "", "", ""}
	if !slicesEqual(stream.renderedLines, expected) {
		t.Fatalf("expected all lines %v, got %v", expected, stream.renderedLines)
	}
}

func TestPromptStreamOnChunkSlidingWindow(t *testing.T) {
	var buf bytes.Buffer
	display := newTestDisplay(&buf)
	display.width = 120
	display.StartSession("Primary goal")
	stream := display.BeginPrompt("Explain select", nil)

	chunks := []string{
		"Line1\nLine2\nLine3\nLine4\nLine5\n",
		"Line6\n",
	}
	for _, chunk := range chunks {
		stream.OnChunk(chunk)
	}

	expected := []string{"", "Line1", "Line2", "Line3", "Line4", "Line5", "Line6", "", ""}
	if !slicesEqual(stream.renderedLines, expected) {
		t.Fatalf("expected sliding window %v, got %v", expected, stream.renderedLines)
	}
}

func TestShowFinalWithoutMarker(t *testing.T) {
	var buf bytes.Buffer
	display := newTestDisplay(&buf)
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
	display := newTestDisplay(&buf)
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
	replaced := ansiCodes.ReplaceAllString(s, "")
	replaced = strings.ReplaceAll(replaced, "\r", "")
	return replaced
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNotifyBeforePrompt(t *testing.T) {
	var buf bytes.Buffer
	display := newTestDisplay(&buf)
	display.Notify("[llm failover] triggered early")

	output := buf.String()
	if strings.TrimSpace(output) != "[llm failover] triggered early" {
		t.Fatalf("expected notify output before prompt, got %q", output)
	}
}

func TestPromptUserReadsYesResponse(t *testing.T) {
	var buf bytes.Buffer
	display := newTestDisplay(&buf)
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdin = r
	defer func() {
		os.Stdin = oldStdin
		_ = r.Close()
	}()

	go func() {
		_, _ = w.Write([]byte("y\n"))
		_ = w.Close()
	}()

	if ok := display.PromptUser("Mark task complete"); !ok {
		t.Fatalf("expected prompt to accept 'y' as confirmation")
	}
	output := buf.String()
	if !strings.Contains(output, "Mark task complete (y/N):") {
		t.Fatalf("expected prompt text in output, got %q", output)
	}
}

func TestPromptInputReturnsTrimmedValue(t *testing.T) {
	var buf bytes.Buffer
	display := newTestDisplay(&buf)
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdin = r
	defer func() {
		os.Stdin = oldStdin
		_ = r.Close()
	}()

	go func() {
		_, _ = w.Write([]byte("  extra guidance  \n"))
		_ = w.Close()
	}()

	value, err := display.PromptInput("Provide guidance")
	if err != nil {
		t.Fatalf("prompt input error: %v", err)
	}
	if value != "extra guidance" {
		t.Fatalf("expected trimmed guidance, got %q", value)
	}
	output := buf.String()
	if !strings.Contains(output, "Provide guidance:") {
		t.Fatalf("expected prompt output, got %q", output)
	}
}

func newTestDisplay(buf *bytes.Buffer) *TerminalDisplay {
	return NewTerminalDisplay(buf, nil, "", "")
}
