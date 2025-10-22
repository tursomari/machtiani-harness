package ui

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/term"
)

const (
	defaultWidth          = 80
	ansiReset             = "\033[0m"
	ansiGray              = "\033[37m"
	promptWindowLines     = 9
	promptContentLines    = promptWindowLines - 1
	promptFirstLinePrefix = "`-- "
	promptSpacerPrefix    = "    "
)

// TerminalDisplay manages the structured streaming output for mct-agent.
type TerminalDisplay struct {
	out       io.Writer
	mu        sync.Mutex
	started   bool
	closed    bool
	hasPrompt bool
	width     int
	current   *PromptStream
}

// PromptStream coordinates streaming tokens for a single prompt turn.
type PromptStream struct {
	display       *TerminalDisplay
	buffer        strings.Builder
	started       bool
	done          bool
	linesPrinted  int
	renderedLines []string
}

// PromptOptions controls how prompts are rendered in the terminal chain.
type PromptOptions struct {
	Metadata []string
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
	t.withLock(func() {
		if t.started {
			return
		}
		t.started = true
	})
	_ = goal // goal only used for symmetry with BeginPrompt
}

// BeginPrompt prepares the stream for a prompt/question block.
func (t *TerminalDisplay) BeginPrompt(prompt string, opts *PromptOptions) *PromptStream {
	stream := &PromptStream{display: t}
	t.withLock(func() {
		if !t.started {
			t.started = true
		}
		if t.current != nil {
			t.current.flushLineLocked()
		}
		fmt.Fprintln(t.out)
		fmt.Fprintln(t.out, strings.TrimSpace(prompt))
		printedMeta := false
		if opts != nil {
			for _, meta := range opts.Metadata {
				clean := strings.TrimSpace(meta)
				if clean == "" {
					continue
				}
				if !printedMeta {
					fmt.Fprintf(t.out, "%s|%s\n", ansiGray, ansiReset)
					printedMeta = true
				}
				fmt.Fprintf(t.out, "%s|   %s%s\n", ansiGray, clean, ansiReset)
			}
		}
		fmt.Fprintf(t.out, "%s|%s\n", ansiGray, ansiReset)
		t.hasPrompt = true
		t.current = stream
	})
	return stream
}

// ShowFinal prints the final rendered answer (expected to be glow-rendered).
func (t *TerminalDisplay) ShowFinal(rendered string) {
	t.withLock(func() {
		if !t.started {
			t.started = true
		}
		final := strings.Trim(rendered, "\n")
		if strings.TrimSpace(final) == "" {
			return
		}
		if t.current != nil {
			t.current.flushLineLocked()
		}
		fmt.Fprintln(t.out)
		fmt.Fprintln(t.out, "===> FINAL RESPONSE <===")
		fmt.Fprintln(t.out, final)
	})
}

// EndSession prints the trailing border once.
func (t *TerminalDisplay) EndSession() {
	t.withLock(func() {
		if t.closed {
			return
		}
		t.closed = true
	})
}

// Notify prints an informational line within the current session timeline.
func (t *TerminalDisplay) Notify(message string) {
	clean := strings.TrimSpace(message)
	if clean == "" {
		return
	}
	t.withLock(func() {
		if !t.started {
			t.started = true
		}
		if t.current != nil {
			t.current.flushLineLocked()
		}
		if t.hasPrompt {
			fmt.Fprintf(t.out, "%s|   %s%s\n", ansiGray, clean, ansiReset)
			return
		}
		fmt.Fprintln(t.out, clean)
	})
}

// OnChunk ingests a header/token chunk while streaming.
func (s *PromptStream) OnChunk(chunk string) {
	s.display.withLock(func() {
		if s.done {
			return
		}
		s.buffer.WriteString(chunk)
		s.renderCurrentLocked()
	})
}

// Complete flushes the stream and prints a condensed summary.
func (s *PromptStream) Complete(finalText string) {
	s.display.withLock(func() {
		if s.done {
			return
		}
		if strings.TrimSpace(finalText) == "" {
			finalText = s.buffer.String()
		}
		lines := lastNLines(finalText, promptContentLines)
		if len(lines) == 0 {
			lines = []string{"(empty response)"}
		} else {
			hasContent := false
			for _, line := range lines {
				if sanitizeLine(line) != "" {
					hasContent = true
					break
				}
			}
			if !hasContent {
				lines = []string{"(empty response)"}
			}
		}
		s.printLinesLocked(lines)
		fmt.Fprintln(s.display.out)
		s.done = true
		s.display.current = nil
	})
}

// Abort stops the stream and prints an error line.
func (s *PromptStream) Abort(message string) {
	s.display.withLock(func() {
		if s.done {
			return
		}
		text := message
		if !strings.HasPrefix(strings.ToLower(text), "error") {
			text = "error: " + text
		}
		s.printLinesLocked([]string{text})
		fmt.Fprintln(s.display.out)
		s.done = true
		s.display.current = nil
	})
}

func (s *PromptStream) renderCurrentLocked() {
	current := s.buffer.String()
	lines := lastNLines(current, promptContentLines)
	if len(lines) == 0 {
		placeholder := sanitizeLine(current)
		if placeholder == "" {
			lines = []string{"..."}
		} else {
			lines = []string{placeholder}
		}
	} else {
		hasContent := false
		for _, line := range lines {
			if sanitizeLine(line) != "" {
				hasContent = true
				break
			}
		}
		if !hasContent {
			lines = []string{"..."}
		}
	}
	s.printLinesLocked(lines)
}

func (s *PromptStream) printLinesLocked(lines []string) {
	if len(lines) > promptContentLines {
		lines = lines[len(lines)-promptContentLines:]
	}
	window := make([]string, promptWindowLines)
	window[0] = ""
	copy(window[1:], lines)
	maxWidth := s.display.width - len(promptFirstLinePrefix)
	if maxWidth <= 0 {
		maxWidth = 1
	}
	sanitized := make([]string, len(window))
	for i, line := range window {
		cleaned := sanitizeLine(line)
		sanitized[i] = truncate(cleaned, maxWidth)
	}
	s.clearPreviousLinesLocked()
	for i, line := range sanitized {
		prefix := promptSpacerPrefix
		if i == 0 {
			prefix = promptFirstLinePrefix
		}
		full := prefix + line
		colored := ansiGray + full + ansiReset
		fmt.Fprint(s.display.out, colored)
		if i < len(sanitized)-1 {
			fmt.Fprint(s.display.out, "\n")
		}
	}
	s.started = true
	s.linesPrinted = len(sanitized)
	s.renderedLines = append(s.renderedLines[:0], sanitized...)
}

// clearPreviousLinesLocked rewinds the terminal cursor and removes the prior
// preview block so we can redraw the sliding window in place.
func (s *PromptStream) clearPreviousLinesLocked() {
	if !s.started || s.linesPrinted == 0 {
		return
	}
	fmt.Fprint(s.display.out, "\r")
	if s.linesPrinted > 1 {
		fmt.Fprintf(s.display.out, "\033[%dA", s.linesPrinted-1)
	}
	for i := 0; i < s.linesPrinted; i++ {
		fmt.Fprint(s.display.out, "\033[2K")
		if i < s.linesPrinted-1 {
			fmt.Fprint(s.display.out, "\n")
		}
	}
	if s.linesPrinted > 1 {
		fmt.Fprintf(s.display.out, "\033[%dA", s.linesPrinted-1)
	}
	fmt.Fprint(s.display.out, "\r")
}

func (s *PromptStream) flushLineLocked() {
	if !s.started || s.done {
		return
	}
	fmt.Fprintln(s.display.out)
	s.started = false
	s.linesPrinted = 0
	s.renderedLines = s.renderedLines[:0]
}

func (t *TerminalDisplay) withLock(fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn()
}

func sanitizeLine(text string) string {
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
	text = replacer.Replace(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
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

func lastNLines(text string, n int) []string {
	if n <= 0 {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	start := len(lines) - n
	if start < 0 {
		start = 0
	}
	result := make([]string, len(lines)-start)
	copy(result, lines[start:])
	return result
}
