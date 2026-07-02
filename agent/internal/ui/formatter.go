package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// promptStreamState tracks the rendering state for a single prompt stream.
type promptStreamState struct {
	streamID      string
	buffer        strings.Builder
	actionBuffer  strings.Builder
	started       bool
	done          bool
	linesPrinted  int
	renderedLines []string
}

// FormatterPromptStream implements the PromptStream interface by emitting
// events into the EventBus.
type FormatterPromptStream struct {
	bus      *EventBus
	streamID string
}

func (s *FormatterPromptStream) OnChunk(chunk string) {
	s.bus.Emit(ChunkReceivedEvent{StreamID: s.streamID, Text: chunk})
}

func (s *FormatterPromptStream) Complete(finalText string) {
	s.bus.Emit(PromptCompletedEvent{StreamID: s.streamID, FinalText: finalText})
}

func (s *FormatterPromptStream) Abort(message string) {
	s.bus.Emit(PromptAbortedEvent{StreamID: s.streamID, Message: message})
}

// Formatter replaces TerminalDisplay with event-bus-based rendering. It
// subscribes to an EventBus and processes DisplayEvents in a dedicated
// goroutine, serializing all output through the embedded mutex.
type Formatter struct {
	out             io.Writer
	mu              sync.Mutex
	Bus             *EventBus
	theme           Theme
	started         bool
	closed          bool
	hasPrompt       bool
	width           int
	currentStreamID string
	streams         map[string]*promptStreamState
	streamCounter   atomic.Int64

	timerEnabled bool
	timerStart   time.Time
	timerTicker  *time.Ticker
	timerStop    chan struct{}
	timerVisible bool
	lastTimer    string
	manager      *ProcessTimerManager
	id           string
}

// NewFormatter constructs a Formatter writing to out (defaults to os.Stdout).
// It subscribes to the EventBus and launches an event-processing goroutine. If
// the bus returns nil for subscription (bus already closed), the goroutine is
// skipped.
func NewFormatter(out io.Writer, bus *EventBus, theme Theme, manager *ProcessTimerManager, id string) *Formatter {
	if out == nil {
		out = os.Stdout
	}
	if bus == nil {
		bus = NewEventBus(512)
	}
	if theme == (Theme{}) {
		theme = DefaultTheme()
	}
	f := &Formatter{
		out:          out,
		Bus:          bus,
		theme:        theme,
		width:        detectWidth(out),
		timerEnabled: isTerminalWriter(out),
		streams:      make(map[string]*promptStreamState),
		manager:      manager,
		id:           strings.TrimSpace(id),
	}

	sub := bus.Subscribe()
	if sub != nil {
		go f.eventLoop(sub)
	}

	return f
}

// eventLoop processes DisplayEvent values from the subscription channel.
// Each event is dispatched under the mutex to a typed handler method.
func (f *Formatter) eventLoop(ch <-chan DisplayEvent) {
	for event := range ch {
		f.mu.Lock()
		switch e := event.(type) {
		case SessionStartedEvent:
			f.handleSessionStarted(e)
		case SessionEndedEvent:
			f.handleSessionEnded(e)
		case PromptStartedEvent:
			f.handlePromptStarted(e)
		case ChunkReceivedEvent:
			f.handleChunkReceived(e)
		case ActionExecutedEvent:
			f.handleActionExecuted(e)
		case PromptCompletedEvent:
			f.handlePromptCompleted(e)
		case PromptAbortedEvent:
			f.handlePromptAborted(e)
		case NotificationEvent:
			f.handleNotification(e)
		case FinalAnswerEvent:
			f.handleFinalAnswer(e)
		case ModeTaskPlanDisplayEvent:
			f.handleModeTaskPlanDisplay(e)
		case ModeTaskStatusUpdateEvent:
			f.handleModeTaskStatusUpdate(e)
		case RawStringEvent:
			f.handleRawString(e)
		}
		f.mu.Unlock()
	}
}

// --- handler methods (mu is held by caller) ---------------------------------

func (f *Formatter) handleSessionStarted(e SessionStartedEvent) {
	f.started = true
	f.ensureTimerLocked()
	f.renderTimerLocked()
	if f.manager != nil {
		f.manager.RegisterDisplay(f.id, f)
	}
	_ = e.Goal // preserved but not displayed
}

func (f *Formatter) handleSessionEnded(_ SessionEndedEvent) {
	if f.closed {
		return
	}
	ResetTerminal(f.out)
	f.closed = true
	f.stopTimerLocked()
	if f.manager != nil {
		f.manager.UnregisterDisplay(f.id)
	}
}

func (f *Formatter) handlePromptStarted(e PromptStartedEvent) {
	f.started = true
	f.ensureTimerLocked()

	// flush the current stream if one exists
	if f.currentStreamID != "" {
		if st := f.streams[f.currentStreamID]; st != nil {
			f.flushLineLocked(st)
		}
	}

	fmt.Fprintln(f.out)
	fmt.Fprintf(f.out, "%s%s%s\n", f.theme.GrayColor, strings.TrimSpace(e.Prompt), f.theme.ResetColor)

	if e.Opts != nil {
		if mode := strings.TrimSpace(e.Opts.ModeIndicator); mode != "" {
			fmt.Fprintf(f.out, "%s|   [mct:%s]%s\n", f.theme.GrayColor, mode, f.theme.ResetColor)
		}
		for _, meta := range e.Opts.Metadata {
			clean := strings.TrimSpace(meta)
			if clean == "" {
				continue
			}
			fmt.Fprintf(f.out, "%s|   %s%s\n", f.theme.GrayColor, clean, f.theme.ResetColor)
		}
	}
	fmt.Fprintf(f.out, "%s|%s\n", f.theme.GrayColor, f.theme.ResetColor)

	f.hasPrompt = true
	f.currentStreamID = e.StreamID
	f.streams[e.StreamID] = &promptStreamState{streamID: e.StreamID}
	f.renderTimerLocked()
}

func (f *Formatter) handleChunkReceived(e ChunkReceivedEvent) {
	st := f.streams[e.StreamID]
	if st == nil || st.done {
		return
	}
	st.buffer.WriteString(e.Text)
	f.renderTextLocked(st.buffer.String(), st)
}

func (f *Formatter) handleActionExecuted(e ActionExecutedEvent) {
	if f.currentStreamID == "" {
		f.printNotificationLineLocked(e.Description, "")
		return
	}
	st := f.streams[f.currentStreamID]
	if st == nil || st.done {
		f.printNotificationLineLocked(e.Description, "")
		return
	}
	st.actionBuffer.WriteString(e.Description)
	st.actionBuffer.WriteString("\n")
	combined := st.buffer.String() + st.actionBuffer.String()
	f.renderTextLocked(combined, st)
}

func (f *Formatter) handlePromptCompleted(e PromptCompletedEvent) {
	st := f.streams[e.StreamID]
	if st == nil || st.done {
		return
	}

	finalText := e.FinalText
	if strings.TrimSpace(finalText) == "" {
		finalText = st.buffer.String()
	}

	lines := lastNLines(finalText, f.theme.PromptContentLines)
	if len(lines) == 0 {
		lines = []string{f.theme.EmptyResponseText}
	} else {
		hasContent := false
		for _, line := range lines {
			if sanitizeLine(line) != "" {
				hasContent = true
				break
			}
		}
		if !hasContent {
			lines = []string{f.theme.EmptyResponseText}
		}
	}

	f.printLinesLocked(lines, st)
	fmt.Fprintln(f.out)
	f.renderTimerLocked()
	st.done = true
	if f.currentStreamID == e.StreamID {
		f.currentStreamID = ""
	}
}

func (f *Formatter) handlePromptAborted(e PromptAbortedEvent) {
	st := f.streams[e.StreamID]
	if st == nil || st.done {
		return
	}

	text := e.Message
	if !strings.HasPrefix(strings.ToLower(text), "error") {
		text = f.theme.ErrorPrefix + text
	}

	f.printLinesLocked([]string{text}, st)
	fmt.Fprintln(f.out)
	f.renderTimerLocked()
	st.done = true
	if f.currentStreamID == e.StreamID {
		f.currentStreamID = ""
	}
}

func (f *Formatter) handleNotification(e NotificationEvent) {
	lines := sanitizeLines(e.Message)
	if len(lines) == 0 {
		return
	}

	var color string
	switch e.Level {
	case NotificationError:
		color = f.theme.ErrorColor
	case NotificationWarning:
		color = f.theme.WarningColor
	default:
		color = f.theme.InfoColor
	}

	st := f.streams[f.currentStreamID]
	if st != nil && !st.done && st.started {
		f.interruptWithNotificationLocked(lines, color, st)
		return
	}
	if st != nil {
		f.flushLineLocked(st)
	}
	f.printNotificationLinesLocked(lines, color)
}

func (f *Formatter) handleFinalAnswer(e FinalAnswerEvent) {
	f.ensureTimerLocked()
	final := strings.Trim(e.RenderedText, "\n")
	if strings.TrimSpace(final) == "" {
		return
	}

	if f.currentStreamID != "" {
		if st := f.streams[f.currentStreamID]; st != nil {
			f.flushLineLocked(st)
		}
	}

	fmt.Fprintln(f.out)
	fmt.Fprintln(f.out, f.theme.FinalAnswerHeader)
	fmt.Fprintln(f.out, final)
	f.renderTimerLocked()
}

func (f *Formatter) handleModeTaskPlanDisplay(e ModeTaskPlanDisplayEvent) {
	if len(e.Tasks) == 0 {
		return
	}

	fmt.Fprintln(f.out)
	fmt.Fprintln(f.out, f.theme.ModePlanHeader)
	for _, task := range e.Tasks {
		mode := strings.ToLower(strings.TrimSpace(task.Mode))
		if mode == "" {
			mode = "-"
		}
		status := strings.TrimSpace(task.Status)
		if status == "" {
			status = "pending"
		}
		line := fmt.Sprintf("  %d. [%s] %s -- %s", task.Index, mode, task.Title, status)
		fmt.Fprintln(f.out, line)
	}
}

func (f *Formatter) handleModeTaskStatusUpdate(e ModeTaskStatusUpdateEvent) {
	idx := e.Index + 1
	cleanStatus := strings.TrimSpace(e.Status)
	if cleanStatus == "" {
		cleanStatus = "pending"
	}
	message := fmt.Sprintf("%s task %d (%s): %s", f.theme.ModeTaskPrefix, idx, e.Title, cleanStatus)
	fmt.Fprintln(f.out)
	fmt.Fprintln(f.out, message)
}

func (f *Formatter) handleRawString(e RawStringEvent) {
	fmt.Fprintln(f.out, e.Text)
}

// --- unified render ---------------------------------------------------------

// renderTextLocked replaces the old renderCurrentLocked and
// renderCurrentWithActionsLocked. It takes the combined text and the stream
// state, extracts the last PromptContentLines, and delegates to
// printLinesLocked.
func (f *Formatter) renderTextLocked(text string, st *promptStreamState) {
	lines := lastNLines(text, f.theme.PromptContentLines)
	if len(lines) == 0 {
		placeholder := sanitizeLine(text)
		if placeholder == "" {
			lines = []string{f.theme.EmptyPlaceholder}
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
			lines = []string{f.theme.EmptyPlaceholder}
		}
	}
	f.printLinesLocked(lines, st)
}

// printLinesLocked builds a sliding window of PromptWindowLines, clears the
// previous block, writes the new block, and updates the stream state.
func (f *Formatter) printLinesLocked(lines []string, st *promptStreamState) {
	if len(lines) > f.theme.PromptContentLines {
		lines = lines[len(lines)-f.theme.PromptContentLines:]
	}

	window := make([]string, f.theme.PromptWindowLines)
	window[0] = ""
	copy(window[1:], lines)

	maxWidth := f.width - len(f.theme.PromptFirstLinePrefix)
	if maxWidth <= 0 {
		maxWidth = 1
	}
	sanitized := make([]string, len(window))
	for i, line := range window {
		cleaned := sanitizeLine(line)
		sanitized[i] = truncate(cleaned, maxWidth)
	}

	f.ensureTimerLocked()
	f.clearPreviousLinesLocked(st)
	f.writeSanitizedLinesLocked(sanitized, st)
	f.renderTimerLocked()
}

// clearPreviousLinesLocked clears lines previously printed by this stream
// using the shared ClearLinesAbove helper.
func (f *Formatter) clearPreviousLinesLocked(st *promptStreamState) {
	if !st.started || st.linesPrinted == 0 {
		return
	}
	ClearLinesAbove(st.linesPrinted, f.out)
}

// writeSanitizedLinesLocked writes the windowed lines with appropriate
// prefixes and color, updating the stream's rendering state.
func (f *Formatter) writeSanitizedLinesLocked(window []string, st *promptStreamState) {
	if len(window) == 0 {
		st.started = false
		st.linesPrinted = 0
		st.renderedLines = st.renderedLines[:0]
		return
	}

	for i, line := range window {
		prefix := f.theme.PromptSpacerPrefix
		if i == 0 {
			prefix = f.theme.PromptFirstLinePrefix
		}
		full := prefix + line
		colored := f.theme.GrayColor + full + f.theme.ResetColor
		fmt.Fprint(f.out, colored)
		if i < len(window)-1 {
			fmt.Fprint(f.out, "\n")
		}
	}

	st.started = true
	st.linesPrinted = len(window)
	st.renderedLines = append(st.renderedLines[:0], window...)
}

// flushLineLocked terminates the prompt stream rendering, prints a trailing
// newline, and resets stream state.
func (f *Formatter) flushLineLocked(st *promptStreamState) {
	if !st.started || st.done {
		return
	}
	f.ensureTimerLocked()
	fmt.Fprintln(f.out)
	f.renderTimerLocked()
	st.started = false
	st.linesPrinted = 0
	st.renderedLines = nil
}

// --- notifications ----------------------------------------------------------

func (f *Formatter) printNotificationLinesLocked(lines []string, color string) {
	for _, line := range lines {
		f.printNotificationLineLocked(line, color)
	}
}

func (f *Formatter) printNotificationLineLocked(message string, color string) {
	if color == "" {
		color = f.theme.GrayColor
	}
	text := sanitizeLine(message)
	if text == "" {
		return
	}
	if f.hasPrompt {
		maxWidth := f.width - len(f.theme.NotificationPrefix)
		if maxWidth <= 0 {
			maxWidth = 1
		}
		text = truncate(text, maxWidth)
		fmt.Fprintf(f.out, "%s%s%s%s\n", color, f.theme.NotificationPrefix, text, f.theme.ResetColor)
	}
	if !f.hasPrompt {
		fmt.Fprintln(f.out, text)
	}
	f.renderTimerLocked()
}

func (f *Formatter) interruptWithNotificationLocked(lines []string, color string, st *promptStreamState) bool {
	if st == nil || st.done || !st.started || st.linesPrinted == 0 || len(lines) == 0 {
		return false
	}
	sanitizedSnapshot := append([]string(nil), st.renderedLines...)
	f.clearPreviousLinesLocked(st)
	f.printNotificationLinesLocked(lines, color)
	f.writeSanitizedLinesLocked(sanitizedSnapshot, st)
	return true
}

// --- timer ------------------------------------------------------------------

func (f *Formatter) ensureTimerLocked() {
	if !f.timerEnabled || f.closed {
		return
	}
	if !f.timerStart.IsZero() {
		return
	}
	f.refreshTerminalSizeLocked()
	f.timerStart = time.Now()
	f.timerVisible = false
	f.lastTimer = ""
	stop := make(chan struct{})
	f.timerStop = stop
	f.timerTicker = time.NewTicker(time.Second)
	go f.timerLoop(f.timerTicker, stop)
}

func (f *Formatter) refreshTerminalSizeLocked() {
	if !f.timerEnabled {
		return
	}
	file, ok := f.out.(*os.File)
	if !ok {
		return
	}
	fd := int(file.Fd())
	if !term.IsTerminal(fd) {
		f.timerEnabled = false
		return
	}
	w, _, err := term.GetSize(fd)
	if err != nil {
		return
	}
	if w > 0 {
		f.width = w
	}
}

func (f *Formatter) renderTimerLocked() {
	if !f.timerEnabled || f.timerStart.IsZero() || f.closed {
		return
	}
	f.refreshTerminalSizeLocked()
	elapsed := time.Since(f.timerStart)
	formatted := formatElapsed(elapsed)
	if f.timerVisible && formatted == f.lastTimer {
		return
	}
	if f.manager != nil {
		f.manager.RenderFooter(f.id, formatted)
	} else {
		coloredTime := f.theme.GrayColor + formatted + f.theme.ResetColor
		RenderFooterLine(coloredTime, f.out)
	}
	f.lastTimer = formatted
	f.timerVisible = true
}

func (f *Formatter) stopTimerLocked() {
	if f.timerTicker == nil {
		return
	}
	f.timerTicker.Stop()
	if f.timerStop != nil {
		close(f.timerStop)
	}
	f.timerTicker = nil
	f.timerStop = nil
	f.timerStart = time.Time{}
	f.timerVisible = false
	f.lastTimer = ""
}

func (f *Formatter) timerLoop(ticker *time.Ticker, stop <-chan struct{}) {
	defer func() { f.mu.Lock(); ResetTerminal(f.out); f.mu.Unlock() }()
	for {
		select {
		case <-ticker.C:
			f.mu.Lock()
			if f.closed {
				f.mu.Unlock()
				return
			}
			f.renderTimerLocked()
			f.mu.Unlock()
		case <-stop:
			return
		}
	}
}

// --- SessionDisplay interface methods ---------------------------------------

// StartSession emits a SessionStartedEvent.
func (f *Formatter) StartSession(goal string) {
	f.Bus.Emit(SessionStartedEvent{Goal: goal})
}

// EndSession emits a SessionEndedEvent.
func (f *Formatter) EndSession() {
	f.Bus.Emit(SessionEndedEvent{})
}

// BeginPrompt emits a PromptStartedEvent and returns a FormatterPromptStream
// that emits subsequent events for this prompt turn.
func (f *Formatter) BeginPrompt(prompt string, opts *PromptOptions) PromptStream {
	id := fmt.Sprintf("stream-%d", f.streamCounter.Add(1))
	f.Bus.Emit(PromptStartedEvent{StreamID: id, Prompt: prompt, Opts: opts})
	return &FormatterPromptStream{bus: f.Bus, streamID: id}
}

// ShowFinal emits a FinalAnswerEvent.
func (f *Formatter) ShowFinal(rendered string) {
	f.Bus.Emit(FinalAnswerEvent{RenderedText: rendered})
}

// StreamAction emits an ActionExecutedEvent.
func (f *Formatter) StreamAction(line string) {
	f.Bus.Emit(ActionExecutedEvent{Description: line})
}

// RenderModePlan emits a ModeTaskPlanDisplayEvent.
func (f *Formatter) RenderModePlan(tasks []ModeTaskDisplay) {
	f.Bus.Emit(ModeTaskPlanDisplayEvent{Tasks: tasks})
}

// UpdateModeTaskStatus emits a ModeTaskStatusUpdateEvent.
func (f *Formatter) UpdateModeTaskStatus(index int, title string, status string) {
	f.Bus.Emit(ModeTaskStatusUpdateEvent{Index: index, Title: title, Status: status})
}

// Notify emits a NotificationEvent at the Info level.
func (f *Formatter) Notify(message string) {
	f.Bus.Emit(NotificationEvent{Level: NotificationInfo, Message: message})
}

// WriteString emits a RawStringEvent.
func (f *Formatter) WriteString(s string) {
	f.Bus.Emit(RawStringEvent{Text: s})
}

// --- concrete prompt methods (mu held by caller) ----------------------------

// PromptSelection prints the prompt and numbered options, then reads the
// user's choice from stdin. Returns the trimmed input or an error.
func (f *Formatter) PromptSelection(prompt string, options []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.refreshTerminalSizeLocked()
	prompt = strings.TrimSpace(prompt)
	if prompt != "" {
		fmt.Fprintln(f.out, prompt)
	}
	for _, option := range options {
		clean := strings.TrimSpace(option)
		if clean == "" {
			continue
		}
		fmt.Fprintln(f.out, clean)
	}
	fmt.Fprint(f.out, f.theme.ChoicePrompt)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

// PromptUser prints a yes/no prompt and returns true when the user answers
// "y" or "yes" (case-insensitive).
func (f *Formatter) PromptUser(prompt string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.refreshTerminalSizeLocked()
	fmt.Fprintf(f.out, "%s%s", prompt, f.theme.ConfirmPromptSuffix)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	input = strings.TrimSpace(strings.ToLower(input))
	return input == "y" || input == "yes"
}

// PromptInput prints the prompt and returns the trimmed user input from stdin
// or an error.
func (f *Formatter) PromptInput(prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.refreshTerminalSizeLocked()
	fmt.Fprintf(f.out, "%s%s", prompt, f.theme.InputPromptSuffix)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

// --- compile-time assertions ------------------------------------------------

var _ SessionDisplay = (*Formatter)(nil)
var _ PromptStream = (*FormatterPromptStream)(nil)
