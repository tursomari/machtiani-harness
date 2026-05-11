package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	defaultWidth           = 80
	ansiReset              = "\033[0m"
	ansiGray               = "\033[37m"
	ansiSaveCursor         = "\033[s"
	ansiRestoreCursor      = "\033[u"
	ansiClearLine          = "\033[2K"
	promptWindowLines      = 9
	promptContentLines     = promptWindowLines - 1
	promptFirstLinePrefix  = "`-- "
	promptSpacerPrefix     = "    "
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

	timerEnabled    bool
	timerStart      time.Time
	timerTicker  *time.Ticker
	timerStop    chan struct{}
	timerVisible bool
	lastTimer    string
	manager      *ProcessTimerManager
	id           string
}

// PromptStream coordinates streaming tokens for a single prompt turn.
type PromptStream struct {
	display        *TerminalDisplay
	buffer         strings.Builder
	actionBuffer   strings.Builder
	started        bool
	done           bool
	linesPrinted   int
	renderedLines  []string
	includeActions bool // if true, action lines are interleaved with LLM tokens
}

// PromptOptions controls how prompts are rendered in the terminal chain.
type PromptOptions struct {
	Metadata      []string
	ModeIndicator string
}

// ModeTaskDisplay captures the metadata required to render mode task progress.
type ModeTaskDisplay struct {
	Index  int
	Title  string
	Mode   string
	Status string
}

// NewTerminalDisplay constructs a TerminalDisplay writing to out (defaults to STDOUT).
func NewTerminalDisplay(out io.Writer, manager *ProcessTimerManager, id string) *TerminalDisplay {
	if out == nil {
		out = os.Stdout
	}
	return &TerminalDisplay{
		out:          out,
		width:        detectWidth(out),
		timerEnabled: isTerminalWriter(out),
		manager:      manager,
		id:           strings.TrimSpace(id),
	}
}

// StartSession marks the start of a session. Prompt lines are printed by BeginPrompt.
func (t *TerminalDisplay) StartSession(goal string) {
	t.withLock(func() {
		if !t.started {
			t.started = true
		}
		t.ensureTimerLocked()
		t.renderTimerLocked()
		if t.manager != nil {
			t.manager.RegisterDisplay(t.id, t)
		}
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
		t.ensureTimerLocked()
		if t.current != nil {
			t.current.flushLineLocked()
		}
		fmt.Fprintln(t.out)
		fmt.Fprintf(t.out, "%s%s%s\n", ansiGray, strings.TrimSpace(prompt), ansiReset)
		printedMeta := false
		if opts != nil {
			mode := strings.TrimSpace(opts.ModeIndicator)
			if mode != "" {
				fmt.Fprintf(t.out, "%s|   [mct:%s]%s\n", ansiGray, mode, ansiReset)
				printedMeta = true
			}
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
		t.renderTimerLocked()
	})
	return stream
}

// ShowFinal prints the final rendered answer (expected to be glow-rendered).
func (t *TerminalDisplay) ShowFinal(rendered string) {
	t.withLock(func() {
		if !t.started {
			t.started = true
		}
		t.ensureTimerLocked()
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
		t.renderTimerLocked()
	})
}

// EndSession prints the trailing border once.
func (t *TerminalDisplay) EndSession() {
	t.withLock(func() {
		if t.closed {
			return
		}
		t.closed = true
		t.stopTimerLocked()
		if t.manager != nil {
			t.manager.UnregisterDisplay(t.id)
		}
	})
}

// Notify prints an informational line within the current session timeline.
func (t *TerminalDisplay) Notify(message string) {
	lines := sanitizeLines(message)
	if len(lines) == 0 {
		return
	}
	t.withLock(func() {
		if !t.started {
			t.started = true
		}
		t.ensureTimerLocked()
		if t.current != nil && t.current.interruptWithNotificationLocked(lines) {
			return
		}
		if t.current != nil {
			t.current.flushLineLocked()
		}
		t.printNotificationLinesLocked(lines)
	})
}

// RenderModePlan prints the initial overview of planned mode tasks.
func (t *TerminalDisplay) RenderModePlan(tasks []ModeTaskDisplay) {
	if len(tasks) == 0 {
		return
	}
	t.withLock(func() {
		fmt.Fprintln(t.out)
		fmt.Fprintln(t.out, "[meta] planned tasks:")
		for _, task := range tasks {
			mode := strings.ToLower(strings.TrimSpace(task.Mode))
			if mode == "" {
				mode = "-"
			}
			status := strings.TrimSpace(task.Status)
			if status == "" {
				status = "pending"
			}
			line := fmt.Sprintf("  %d. [%s] %s — %s", task.Index, mode, task.Title, status)
			fmt.Fprintln(t.out, line)
		}
	})
}

// UpdateModeTaskStatus reports status transitions for a mode task.
func (t *TerminalDisplay) UpdateModeTaskStatus(index int, title, status string) {
	idx := index + 1
	cleanStatus := strings.TrimSpace(status)
	if cleanStatus == "" {
		cleanStatus = "pending"
	}
	message := fmt.Sprintf("[meta] task %d (%s): %s", idx, title, cleanStatus)
	t.withLock(func() {
		fmt.Fprintln(t.out)
		fmt.Fprintln(t.out, message)
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
		s.display.renderTimerLocked()
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
		s.display.renderTimerLocked()
		s.done = true
		s.display.current = nil
	})
}

// OnActionChunk ingests a fully formatted shell-agent action line while streaming.
// This is called when a shell-agent action event should be surfaced in the TUI.
func (s *PromptStream) OnActionChunk(line string) {
	s.display.withLock(func() {
		if s.done {
			return
		}
		actionLine := strings.TrimSpace(line)
		if actionLine == "" {
			return
		}
		s.actionBuffer.WriteString(actionLine)
		s.actionBuffer.WriteString("\n")
		// Re-render with the new action included
		s.renderCurrentWithActionsLocked()
	})
}

// StreamAction surfaces a fully formatted shell-agent action line into the current
// PromptStream if one is active. If no stream is active, it falls back to a plain
// notification line.
func (t *TerminalDisplay) StreamAction(line string) {
	clean := strings.TrimSpace(line)
	if clean == "" {
		return
	}
	t.mu.Lock()
	if !t.started {
		t.started = true
	}
	t.ensureTimerLocked()
	current := t.current
	if current != nil && !current.done {
		t.mu.Unlock()
		current.OnActionChunk(clean)
		return
	}
	// Fall back to notification if no active stream
	t.printNotificationLineLocked(clean)
	t.mu.Unlock()
}

// renderCurrentWithActionsLocked combines LLM output and shell-agent actions,
// maintaining the last promptContentLines worth of content across both sources.
func (s *PromptStream) renderCurrentWithActionsLocked() {
	llmText := s.buffer.String()
	actionText := s.actionBuffer.String()

	// Combine both buffers, with actions interleaved
	combined := llmText + actionText

	lines := lastNLines(combined, promptContentLines)
	if len(lines) == 0 {
		placeholder := sanitizeLine(combined)
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
	s.display.ensureTimerLocked()
	s.clearPreviousLinesLocked()
	s.writeSanitizedLinesLocked(sanitized)
	s.display.renderTimerLocked()
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
	s.display.ensureTimerLocked()
	fmt.Fprintln(s.display.out)
	s.display.renderTimerLocked()
	s.started = false
	s.linesPrinted = 0
	s.renderedLines = s.renderedLines[:0]
}

func (t *TerminalDisplay) printNotificationLinesLocked(lines []string) {
	for _, line := range lines {
		t.printNotificationLineLocked(line)
	}
}

func (t *TerminalDisplay) printNotificationLineLocked(message string) {
	text := sanitizeLine(message)
	if text == "" {
		return
	}
	if t.hasPrompt {
		maxWidth := t.width - len(promptSpacerPrefix)
		if maxWidth <= 0 {
			maxWidth = 1
		}
		text = truncate(text, maxWidth)
		fmt.Fprintf(t.out, "%s|   %s%s\n", ansiGray, text, ansiReset)
	}
	if !t.hasPrompt {
		fmt.Fprintln(t.out, text)
	}
	t.renderTimerLocked()
}

func (s *PromptStream) writeSanitizedLinesLocked(lines []string) {
	if len(lines) == 0 {
		s.started = false
		s.linesPrinted = 0
		s.renderedLines = s.renderedLines[:0]
		return
	}
	for i, line := range lines {
		prefix := promptSpacerPrefix
		if i == 0 {
			prefix = promptFirstLinePrefix
		}
		full := prefix + line
		colored := ansiGray + full + ansiReset
		fmt.Fprint(s.display.out, colored)
		if i < len(lines)-1 {
			fmt.Fprint(s.display.out, "\n")
		}
	}
	s.started = true
	s.linesPrinted = len(lines)
	s.renderedLines = append(s.renderedLines[:0], lines...)
}

func (s *PromptStream) interruptWithNotificationLocked(lines []string) bool {
	if s == nil || s.done || !s.started || s.linesPrinted == 0 || len(lines) == 0 {
		return false
	}
	sanitizedSnapshot := append([]string(nil), s.renderedLines...)
	s.clearPreviousLinesLocked()
	s.display.printNotificationLinesLocked(lines)
	s.writeSanitizedLinesLocked(sanitizedSnapshot)
	return true
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

func sanitizeLines(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	parts := strings.Split(text, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		clean := sanitizeLinePreserveIndent(part)
		if clean == "" {
			continue
		}
		lines = append(lines, clean)
	}
	return lines
}

func sanitizeLinePreserveIndent(text string) string {
	replacer := strings.NewReplacer("\r", " ", "\t", " ")
	text = replacer.Replace(text)
	text = strings.TrimRight(text, " ")
	if text == "" {
		return ""
	}
	leading := len(text) - len(strings.TrimLeft(text, " "))
	core := strings.TrimSpace(text)
	if core == "" {
		return ""
	}
	fields := strings.Fields(core)
	if len(fields) == 0 {
		return ""
	}
	collapsed := strings.Join(fields, " ")
	if leading == 0 {
		return collapsed
	}
	return strings.Repeat(" ", leading) + collapsed
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

func isTerminalWriter(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
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

func (t *TerminalDisplay) ensureTimerLocked() {
	if !t.timerEnabled || t.closed {
		return
	}
	if !t.timerStart.IsZero() {
		return
	}
	t.refreshTerminalSizeLocked()
	t.timerStart = time.Now()
	t.timerVisible = false
	t.lastTimer = ""
	stop := make(chan struct{})
	t.timerStop = stop
	t.timerTicker = time.NewTicker(time.Second)
	go t.timerLoop(t.timerTicker, stop)
}

func (t *TerminalDisplay) refreshTerminalSizeLocked() {
	if !t.timerEnabled {
		return
	}
	file, ok := t.out.(*os.File)
	if !ok {
		return
	}
	fd := int(file.Fd())
	if !term.IsTerminal(fd) {
		t.timerEnabled = false
		return
	}
	w, _, err := term.GetSize(fd)
	if err != nil {
		return
	}
	if w > 0 {
		t.width = w
	}
}
func (t *TerminalDisplay) renderTimerLocked() {
	if !t.timerEnabled || t.timerStart.IsZero() || t.closed {
		return
	}
	t.refreshTerminalSizeLocked()
	elapsed := time.Since(t.timerStart)
	formatted := formatElapsed(elapsed)
	if t.timerVisible && formatted == t.lastTimer {
		return
	}
	if t.manager != nil {
		t.manager.RenderFooter(t.id, formatted)
	}
	t.lastTimer = formatted
	t.timerVisible = true
}

func (t *TerminalDisplay) stopTimerLocked() {
	if t.timerTicker == nil {
		return
	}
	t.timerTicker.Stop()
	if t.timerStop != nil {
		close(t.timerStop)
	}
	t.timerTicker = nil
	t.timerStop = nil
	t.timerStart = time.Time{}
	t.timerVisible = false
	t.lastTimer = ""
}

func (t *TerminalDisplay) timerLoop(ticker *time.Ticker, stop <-chan struct{}) {
	for {
		select {
		case <-ticker.C:
			t.withLock(func() {
				if t.closed {
					return
				}
				t.renderTimerLocked()
			})
		case <-stop:
			return
		}
	}
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	remaining := seconds % 60
	return fmt.Sprintf("%d:%02d", minutes, remaining)
}

func (t *TerminalDisplay) PromptSelection(prompt string, options []string) (string, error) {
	var (
		response string
		readErr  error
	)
	t.withLock(func() {
		t.refreshTerminalSizeLocked()
		prompt = strings.TrimSpace(prompt)
		if prompt != "" {
			fmt.Fprintln(t.out, prompt)
		}
		for _, option := range options {
			clean := strings.TrimSpace(option)
			if clean == "" {
				continue
			}
			fmt.Fprintln(t.out, clean)
		}
		fmt.Fprint(t.out, "Choice: ")
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			readErr = err
			return
		}
		response = strings.TrimSpace(input)
	})
	return response, readErr
}

func (t *TerminalDisplay) PromptUser(prompt string) bool {
	var confirmed bool
	t.withLock(func() {
		t.refreshTerminalSizeLocked()
		fmt.Fprintf(t.out, "%s (y/N): ", prompt)
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			confirmed = false
			return
		}
		input = strings.TrimSpace(strings.ToLower(input))
		confirmed = input == "y" || input == "yes"
	})
	return confirmed
}

func (t *TerminalDisplay) PromptInput(prompt string) (string, error) {
	var (
		response string
		readErr  error
	)
	t.withLock(func() {
		t.refreshTerminalSizeLocked()
		fmt.Fprintf(t.out, "%s: ", prompt)
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			readErr = err
			return
		}
		response = strings.TrimSpace(input)
	})
	return response, readErr
}
