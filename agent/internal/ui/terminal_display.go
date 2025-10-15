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

const defaultWidth = 80

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
	display *TerminalDisplay
	buffer  strings.Builder
	started bool
	done    bool
	lastLen int
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
		if t.hasPrompt {
			fmt.Fprintln(t.out, "|")
		}
		fmt.Fprintln(t.out, strings.TrimSpace(prompt))
		if opts != nil {
			for _, meta := range opts.Metadata {
				clean := strings.TrimSpace(meta)
				if clean == "" {
					continue
				}
				fmt.Fprintf(t.out, "|  %s\n", clean)
			}
		}
		fmt.Fprintln(t.out, "|")
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
			fmt.Fprintf(t.out, "|  %s\n", clean)
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
		line := firstLine(finalText)
		if line == "" {
			line = "(empty response)"
		}
		s.printLineLocked(line)
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
		s.printLineLocked(text)
		fmt.Fprintln(s.display.out)
		s.done = true
		s.display.current = nil
	})
}

func (s *PromptStream) renderCurrentLocked() {
	current := s.buffer.String()
	preview := firstLine(current)
	if preview == "" {
		preview = sanitizeLine(current)
	}
	if preview == "" {
		preview = "..."
	}
	s.printLineLocked(preview)
}

func (s *PromptStream) printLineLocked(text string) {
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
	if s.lastLen > len(full) {
		diff := s.lastLen - len(full)
		fmt.Fprint(s.display.out, strings.Repeat(" ", diff))
		fmt.Fprintf(s.display.out, "\r%s", full)
	}
	s.lastLen = len(full)
}

func (s *PromptStream) flushLineLocked() {
	if !s.started || s.done {
		return
	}
	fmt.Fprintln(s.display.out)
	s.started = false
	s.lastLen = 0
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
