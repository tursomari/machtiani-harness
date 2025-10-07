package ui

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

const defaultWidth = 80

// TerminalDisplay manages the structured streaming output for mct-agent.
type TerminalDisplay struct {
	out       io.Writer
	started   bool
	closed    bool
	hasPrompt bool
	width     int
}

// PromptStream coordinates streaming tokens for a single prompt turn.
type PromptStream struct {
	display *TerminalDisplay
	buffer  strings.Builder
	started bool
	done    bool
	lastLen int
}

// NewTerminalDisplay constructs a TerminalDisplay writing to out (defaults to STDOUT).
func NewTerminalDisplay(out io.Writer) *TerminalDisplay {
	if out == nil {
		out = os.Stdout
	}
	return &TerminalDisplay{out: out, width: detectWidth(out)}
}

// StartSession marks the start of a session. Prompt lines are printed by BeginPrompt.
func (t *TerminalDisplay) StartSession(goal string) {
	if t.started {
		return
	}
	t.started = true
	// The initial prompt is printed when BeginPrompt is called.
}

// BeginPrompt prepares the stream for a prompt/question block.
func (t *TerminalDisplay) BeginPrompt(prompt string) *PromptStream {
	if !t.started {
		t.StartSession(prompt)
	}
	// Link from previous answer to next prompt, if any.
	if t.hasPrompt {
		fmt.Fprintln(t.out, "|")
	}
	// Print the prompt itself (no indentation).
	fmt.Fprintln(t.out, strings.TrimSpace(prompt))
	// Link prompt to its answer preview.
	fmt.Fprintln(t.out, "|")
	stream := &PromptStream{display: t}
	t.hasPrompt = true
	return stream
}

// ShowFinal prints the final rendered answer (expected to be glow-rendered).
func (t *TerminalDisplay) ShowFinal(rendered string) {
	if !t.started {
		t.StartSession("")
	}
	// Do not emit labels or extra leading newlines; present the answer as-is.
	final := strings.Trim(rendered, "\n")
	if strings.TrimSpace(final) == "" {
		return
	}
	// Print a single leading newline before the final answer for separation.
	fmt.Fprintln(t.out)
	fmt.Fprintln(t.out, "===> FINAL RESPONSE <===")
	fmt.Fprintln(t.out, final)
}

// EndSession prints the trailing border once.
func (t *TerminalDisplay) EndSession() {
	if t.closed {
		return
	}
	t.closed = true
}

// OnChunk ingests a header/token chunk while streaming.
func (s *PromptStream) OnChunk(chunk string) {
	if s.done {
		return
	}
	s.buffer.WriteString(chunk)
	s.renderCurrent()
}

// Complete flushes the stream and prints a condensed summary.
func (s *PromptStream) Complete(finalText string) {
	if s.done {
		return
	}
	if strings.TrimSpace(finalText) == "" {
		finalText = s.buffer.String()
	}
	line := firstLine(finalText)
	if line == "" {
		line = "(empty response)"
	}
	s.printLine(line)
	fmt.Fprintln(s.display.out)
	s.done = true
}

// Abort stops the stream and prints an error line.
func (s *PromptStream) Abort(message string) {
	if s.done {
		return
	}
	text := message
	if !strings.HasPrefix(strings.ToLower(text), "error") {
		text = "error: " + text
	}
	s.printLine(text)
	fmt.Fprintln(s.display.out)
	s.done = true
}

func (s *PromptStream) renderCurrent() {
	current := s.buffer.String()
	preview := firstLine(current)
	if preview == "" {
		preview = sanitizeLine(current)
	}
	if preview == "" {
		preview = "..."
	}
	s.printLine(preview)
}

func (s *PromptStream) printLine(text string) {
	prefix := "`-- "
	cleaned := sanitizeLine(text)
	maxWidth := s.display.width - len(prefix)
	if maxWidth <= 0 {
		maxWidth = 1
	}
	cleaned = truncate(cleaned, maxWidth)
	full := prefix + cleaned
	if !s.started {
		fmt.Fprint(s.display.out, full)
		s.started = true
		s.lastLen = len(full)
		return
	}
	fmt.Fprintf(s.display.out, "\r%s", full)
	// Clear leftovers if the new text is shorter than previous.
	if s.lastLen > len(full) {
		diff := s.lastLen - len(full)
		fmt.Fprint(s.display.out, strings.Repeat(" ", diff))
		fmt.Fprintf(s.display.out, "\r%s", full)
	}
	s.lastLen = len(full)
}

func sanitizeLine(text string) string {
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
	text = replacer.Replace(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// Collapse consecutive whitespace
	fields := strings.Fields(text)
	return strings.Join(fields, " ")
}

func detectWidth(out io.Writer) int {
	if w := widthFromWriter(out); w > 0 {
		return w
	}
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if n, err := strconv.Atoi(cols); err == nil && n > 0 {
			return n
		}
	}
	return defaultWidth
}

func widthFromWriter(out io.Writer) int {
	file, ok := out.(*os.File)
	if !ok {
		return 0
	}
	fd := int(file.Fd())
	if !term.IsTerminal(fd) {
		return 0
	}
	w, _, err := term.GetSize(fd)
	if err != nil || w <= 0 {
		return 0
	}
	return w
}

func firstLine(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	idx := strings.IndexByte(text, '\n')
	if idx == -1 {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(text[:idx])
}

func truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}
