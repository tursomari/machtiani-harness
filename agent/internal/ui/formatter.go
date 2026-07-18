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

	"github.com/tursomari/machtiani/agent/internal/presentation"
	"golang.org/x/term"
)

// promptStreamState tracks the rendering state for a single prompt stream.
type promptStreamState struct {
	streamID      string
	buffer        strings.Builder
	started       bool
	done          bool
	linesPrinted  int
	renderedLines []string
	role          presentation.Role
	bold          bool
}

// FormatterPromptStream implements the PromptStream interface by emitting
// events into the EventBus.
type FormatterPromptStream struct {
	Bus      *EventBus
	StreamID string
}

func (s *FormatterPromptStream) OnChunk(chunk string) {
	s.Bus.Emit(ChunkReceivedEvent{StreamID: s.StreamID, Text: chunk})
}

func (s *FormatterPromptStream) Complete(finalText string) {
	s.Bus.Emit(PromptCompletedEvent{StreamID: s.StreamID, FinalText: finalText})
}

func (s *FormatterPromptStream) Abort(message string) {
	s.Bus.Emit(PromptAbortedEvent{StreamID: s.StreamID, Message: message})
}

// NewFormatterPromptStream creates a new FormatterPromptStream that emits
// events into the given EventBus using the given streamID for correlation.
func NewFormatterPromptStream(bus *EventBus, streamID string) *FormatterPromptStream {
	return &FormatterPromptStream{Bus: bus, StreamID: streamID}
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
	height          int
	currentStreamID string
	streams         map[string]*promptStreamState
	streamCounter   atomic.Int64

	timerEnabled         bool
	timerStart           time.Time
	elapsedOffset        time.Duration
	timerTicker          *time.Ticker
	timerStop            chan struct{}
	timerVisible         bool
	footerOffset         int
	lastFooter           []string
	tokenUsage           TokenUsageUpdatedEvent
	cwd                  string
	activePromptTokens   int
	maxInputTokens       int
	footerModels         FooterModelMetadata
	footerIdentity       FooterIdentity
	sessionID            string
	turnNumber           int
	activities           []Activity
	activityCursorHidden bool
	modeTasks            []ModeTaskDisplay
	activeModeTask       int
	manager              *ProcessTimerManager
	id                   string
	done                 chan struct{}
}

type coordinatedWriter struct {
	formatter *Formatter
	writer    io.Writer
}

func (w coordinatedWriter) Write(p []byte) (int, error) {
	if w.formatter == nil {
		return w.writer.Write(p)
	}
	w.formatter.mu.Lock()
	defer w.formatter.mu.Unlock()
	w.formatter.clearLiveFooterLocked()
	return w.writer.Write(p)
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
		out:            out,
		Bus:            bus,
		theme:          theme,
		width:          detectWidth(out),
		height:         detectHeight(out),
		timerEnabled:   isTerminalWriter(out),
		streams:        make(map[string]*promptStreamState),
		activeModeTask: -1,
		manager:        manager,
		id:             strings.TrimSpace(id),
		done:           make(chan struct{}),
	}

	sub := bus.Subscribe()
	if sub != nil {
		go f.eventLoop(sub)
	}

	return f
}

// CoordinateWriter returns a writer that clears this formatter's live footer
// before external output advances the shared terminal cursor. This is used for
// diagnostics, which otherwise bypass the event bus and can overwrite the
// first footer row during verbose runs.
func (f *Formatter) CoordinateWriter(writer io.Writer) io.Writer {
	if writer == nil {
		writer = io.Discard
	}
	return coordinatedWriter{formatter: f, writer: writer}
}

// Done is closed after the final footer has been processed and printed.
func (f *Formatter) Done() <-chan struct{} {
	return f.done
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
		case TokenUsageUpdatedEvent:
			f.handleTokenUsageUpdated(e)
		case FooterModelsUpdatedEvent:
			f.handleFooterModelsUpdated(e)
		case TurnStatusUpdatedEvent:
			f.handleTurnStatusUpdated(e)
		case ActivitySnapshotEvent:
			f.handleActivitySnapshot(e)
		case SessionConclusionEvent:
			f.handleSessionConclusion(e)
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
	if !f.started && e.ShowBanner && f.timerEnabled {
		fmt.Fprint(f.out, RenderSessionHeader(e, f.theme, f.width))
	}
	f.started = true
	if e.Elapsed > 0 {
		f.elapsedOffset = e.Elapsed
	}
	f.tokenUsage = e.TokenUsage
	f.cwd = e.CWD
	f.activePromptTokens = e.TokenUsage.ActivePromptTokens
	f.maxInputTokens = e.MaxInputTokens
	f.footerModels = e.Models
	f.footerIdentity = FooterIdentity{Label: sanitizeLine(e.Identity.Label), Value: sanitizeLine(e.Identity.Value)}
	f.sessionID = sanitizeLine(e.SessionID)
	if f.footerIdentity.Label == "" && f.sessionID != "" {
		f.footerIdentity = FooterIdentity{Label: "session", Value: f.sessionID}
	}
	if e.Turn > 0 {
		f.turnNumber = e.Turn
	}
	if f.manager != nil {
		f.manager.RegisterDisplay(f.id, f)
	}
	f.ensureTimerLocked()
	f.renderTimerLocked()
	_ = e.Goal // preserved but not displayed
}

func (f *Formatter) handleSessionEnded(_ SessionEndedEvent) {
	if f.closed {
		return
	}
	f.activePromptTokens = 0
	f.maxInputTokens = 0
	f.activities = nil
	finalFooter := f.finalFooterLinesLocked()
	f.closed = true
	f.stopTimerLocked()
	ResetTerminal(f.out)
	if f.manager != nil {
		f.manager.UnregisterDisplay(f.id)
	}
	f.printFinalFooterLinesLocked(finalFooter)
	close(f.done)
}

func (f *Formatter) handleActivitySnapshot(e ActivitySnapshotEvent) {
	f.activities = append(f.activities[:0], e.Activities...)
	f.renderTimerLocked()
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

	f.clearLiveFooterLocked()
	fmt.Fprintln(f.out)
	fmt.Fprintln(f.out, f.theme.render(presentation.StyledLine{presentation.Text(strings.TrimSpace(e.Prompt))}))

	if e.Opts != nil {
		if mode := strings.TrimSpace(e.Opts.ModeIndicator); mode != "" {
			fmt.Fprintln(f.out, f.theme.render(presentation.StyledLine{
				presentation.Text("|   "),
				presentation.Bold(presentation.RoleTruth, "[mct:"+mode+"]"),
			}))
		}
		for _, meta := range e.Opts.Metadata {
			clean := strings.TrimSpace(meta)
			if clean == "" {
				continue
			}
			fmt.Fprintln(f.out, f.theme.render(styleMetadataLine(clean)))
		}
	}
	fmt.Fprintln(f.out, f.theme.render(presentation.StyledLine{presentation.RoleText(presentation.RoleTruth, "|")}))

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
	if e.Command == "" && e.Step == 0 && e.StepLimit == 0 {
		f.printNotificationLineLocked(e.Description, presentation.RoleNormal, false)
		return
	}
	st := f.streams[f.currentStreamID]
	if st != nil && !st.done {
		f.flushLineLocked(st)
	}
	f.printActionBlockLocked(e)
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
	f.clearLiveFooterLocked()
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
	st.role = presentation.RoleRupture
	st.bold = true

	f.printLinesLocked([]string{text}, st)
	f.clearLiveFooterLocked()
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

	role := presentation.RoleTruth
	bold := false
	switch e.Level {
	case NotificationError:
		role = presentation.RoleRupture
		bold = true
	case NotificationWarning:
		role = presentation.RoleProvenance
		bold = true
	}

	st := f.streams[f.currentStreamID]
	if st != nil && !st.done && st.started {
		f.interruptWithNotificationLocked(lines, role, bold, st)
		return
	}
	if st != nil {
		f.flushLineLocked(st)
	}
	f.printNotificationLinesLocked(lines, role, bold)
}

func (f *Formatter) handleTokenUsageUpdated(e TokenUsageUpdatedEvent) {
	f.tokenUsage = e
	f.activePromptTokens = e.ActivePromptTokens
	f.ensureTimerLocked()
	f.renderTimerLocked()
}

func (f *Formatter) handleFooterModelsUpdated(e FooterModelsUpdatedEvent) {
	f.footerModels = cloneFooterModelMetadata(e.Models)
	f.ensureTimerLocked()
	f.renderTimerLocked()
}

func (f *Formatter) handleTurnStatusUpdated(e TurnStatusUpdatedEvent) {
	if e.Turn > 0 {
		f.turnNumber = e.Turn
	}
	f.ensureTimerLocked()
	f.renderTimerLocked()
}

func (f *Formatter) handleSessionConclusion(e SessionConclusionEvent) {
	if f.currentStreamID != "" {
		if st := f.streams[f.currentStreamID]; st != nil {
			f.flushLineLocked(st)
		}
	}
	f.clearLiveFooterLocked()
	f.refreshTerminalSizeLocked()
	fmt.Fprint(f.out, RenderSessionConclusion(e, f.theme, f.width))
}

func (f *Formatter) handleModeTaskPlanDisplay(e ModeTaskPlanDisplayEvent) {
	if len(e.Tasks) == 0 {
		return
	}
	f.modeTasks = cloneModeTaskDisplays(e.Tasks)
	f.activeModeTask = activeModeTaskIndex(f.modeTasks)
	f.renderTimerLocked()
}

func (f *Formatter) handleModeTaskStatusUpdate(e ModeTaskStatusUpdateEvent) {
	cleanStatus := strings.TrimSpace(e.Status)
	if cleanStatus == "" {
		cleanStatus = "pending"
	}
	if e.Index >= 0 {
		for len(f.modeTasks) <= e.Index {
			f.modeTasks = append(f.modeTasks, ModeTaskDisplay{Index: len(f.modeTasks) + 1})
		}
		task := &f.modeTasks[e.Index]
		if task.Index <= 0 {
			task.Index = e.Index + 1
		}
		if title := strings.TrimSpace(e.Title); title != "" {
			task.Title = title
		}
		task.Status = cleanStatus
		f.activeModeTask = e.Index
	}
	f.renderTimerLocked()
}

func (f *Formatter) handleRawString(e RawStringEvent) {
	f.clearLiveFooterLocked()
	fmt.Fprintln(f.out, e.Text)
	f.renderTimerLocked()
}

func (f *Formatter) printActionBlockLocked(e ActionExecutedEvent) {
	command := sanitizeLine(e.Command)
	description := CleanActionDescription(e.Description)
	if description == "" {
		description = command
	}
	if command == "" {
		command = sanitizeLine(description)
	}
	if strings.EqualFold(sanitizeLine(description), command) {
		description = ""
	}
	if command == "" && description == "" && e.Step <= 0 {
		return
	}
	f.clearLiveFooterLocked()
	if e.Step > 0 {
		label := fmt.Sprintf("Step %d", e.Step)
		if e.StepLimit > 0 {
			label = fmt.Sprintf("Step %d of %d", e.Step, e.StepLimit)
		}
		fmt.Fprintln(f.out, f.theme.render(presentation.StyledLine{presentation.Bold(presentation.RoleTruth, label)}))
		fmt.Fprintln(f.out)
	}
	for _, line := range sanitizeLines(description) {
		fmt.Fprintln(f.out, line)
	}
	if description != "" {
		fmt.Fprintln(f.out)
	}
	if command != "" {
		fmt.Fprintln(f.out, f.theme.render(presentation.StyledLine{
			presentation.Bold(presentation.RoleGoodness, "$ "),
			presentation.Text(command),
		}))
		fmt.Fprintln(f.out)
	}
	f.renderTimerLocked()
}

func actionBlockLines(e ActionExecutedEvent) []string {
	command := sanitizeLine(e.Command)
	description := CleanActionDescription(e.Description)
	if description == "" {
		description = command
	}
	if command == "" {
		command = sanitizeLine(description)
	}
	if strings.EqualFold(sanitizeLine(description), command) {
		description = ""
	}

	var lines []string
	switch {
	case e.Step > 0 && e.StepLimit > 0:
		lines = append(lines, fmt.Sprintf("Step %d of %d", e.Step, e.StepLimit), "")
	case e.Step > 0:
		lines = append(lines, fmt.Sprintf("Step %d", e.Step), "")
	}
	if description != "" {
		lines = append(lines, sanitizeLines(description)...)
		lines = append(lines, "")
	}
	if command != "" {
		lines = append(lines, "$ "+command)
		lines = append(lines, "")
	}
	return lines
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
	f.clearLiveFooterLocked()
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

	f.clearLiveFooterLocked()
	for i, line := range window {
		prefix := f.theme.PromptSpacerPrefix
		if i == 0 {
			prefix = f.theme.PromptFirstLinePrefix
		}
		content := presentation.RoleText(st.role, line)
		content.Bold = st.bold
		styled := presentation.StyledLine{presentation.Text(prefix), content}
		if i == 0 {
			styled[0] = presentation.RoleText(presentation.RoleTruth, prefix)
		}
		fmt.Fprint(f.out, f.theme.render(styled))
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
	f.clearLiveFooterLocked()
	fmt.Fprintln(f.out)
	f.renderTimerLocked()
	st.started = false
	st.linesPrinted = 0
	st.renderedLines = nil
}

// --- notifications ----------------------------------------------------------

func (f *Formatter) printNotificationLinesLocked(lines []string, role presentation.Role, bold bool) {
	for _, line := range lines {
		f.printNotificationLineLocked(line, role, bold)
	}
}

func (f *Formatter) printNotificationLineLocked(message string, role presentation.Role, bold bool) {
	text := sanitizeLine(message)
	if text == "" {
		return
	}
	f.clearLiveFooterLocked()
	if f.hasPrompt {
		maxWidth := f.width - len(f.theme.NotificationPrefix)
		if maxWidth <= 0 {
			maxWidth = 1
		}
		text = truncate(text, maxWidth)
		span := presentation.RoleText(role, f.theme.NotificationPrefix+text)
		span.Bold = bold
		fmt.Fprintln(f.out, f.theme.render(presentation.StyledLine{span}))
	}
	if !f.hasPrompt {
		fmt.Fprintln(f.out, text)
	}
	f.renderTimerLocked()
}

func (f *Formatter) interruptWithNotificationLocked(lines []string, role presentation.Role, bold bool, st *promptStreamState) bool {
	if st == nil || st.done || !st.started || st.linesPrinted == 0 || len(lines) == 0 {
		return false
	}
	sanitizedSnapshot := append([]string(nil), st.renderedLines...)
	f.clearPreviousLinesLocked(st)
	f.printNotificationLinesLocked(lines, role, bold)
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
	if f.footerLineCountLocked() == 0 {
		f.clearLiveFooterLocked()
		return
	}
	f.timerStart = time.Now()
	f.timerVisible = false
	f.lastFooter = nil
	stop := make(chan struct{})
	f.timerStop = stop
	interval := time.Second
	if f.theme.Presentation.MotionMode() == presentation.MotionFull {
		interval = 125 * time.Millisecond
	}
	f.timerTicker = time.NewTicker(interval)
	go f.timerLoop(f.timerTicker, stop)
}

func (f *Formatter) refreshTerminalSizeLocked() {
	if !f.timerEnabled {
		return
	}
	fd, ok := writerFD(f.out)
	if !ok {
		return
	}
	if !term.IsTerminal(int(fd)) {
		f.timerEnabled = false
		return
	}
	w, h, err := term.GetSize(int(fd))
	if err != nil || h <= 0 {
		h = detectHeight(f.out)
	}
	if w > 0 {
		f.width = w
	}
	if h > 0 {
		f.height = h
	}
}

func (f *Formatter) clearLiveFooterLocked() {
	if !f.timerVisible || len(f.lastFooter) == 0 {
		return
	}
	ClearFooterLinesBelow(len(f.lastFooter), f.footerOffset, f.out)
	f.timerVisible = false
	f.footerOffset = 0
	f.lastFooter = nil
}

// liveFooterOffsetLocked returns the first footer row relative to the current
// cursor. A streaming prompt leaves the cursor on its final rendered line, so
// it needs one additional row to preserve the blank separator.
func (f *Formatter) liveFooterOffsetLocked() int {
	if f.currentStreamID != "" {
		if st := f.streams[f.currentStreamID]; st != nil && st.started && !st.done {
			return 2
		}
	}
	return 1
}

func (f *Formatter) footerTooSmallLocked() bool {
	return f.height > 0 && f.height < minFooterHeight
}

func (f *Formatter) footerLineCountLocked() int {
	if f.footerTooSmallLocked() {
		return 0
	}
	if f.height == 0 {
		if f.hasVisibleActivityLocked() {
			return 3
		}
		return 2
	}
	if f.height >= minFooterHeight+2 && f.hasVisibleActivityLocked() {
		return 3
	}
	if f.height >= minFooterHeight+1 {
		return 2
	}
	return 1
}

func (f *Formatter) renderTimerLocked() {
	if !f.timerEnabled || f.timerStart.IsZero() || f.closed {
		if f.activityCursorHidden && (!f.timerEnabled || f.closed) {
			f.setActivityCursorHiddenLocked(false)
		}
		return
	}
	f.refreshTerminalSizeLocked()
	if !f.timerEnabled {
		f.setActivityCursorHiddenLocked(false)
		return
	}
	lineCount := f.footerLineCountLocked()
	f.setActivityCursorHiddenLocked(lineCount >= 3 && f.hasVisibleActivityLocked())
	if lineCount == 0 {
		f.clearLiveFooterLocked()
		return
	}
	elapsed := f.elapsedOffset + time.Since(f.timerStart)
	lines := f.formatFooterLinesLocked(elapsed, lineCount)
	offset := f.liveFooterOffsetLocked()
	if f.timerVisible && f.footerOffset == offset && footerLinesEqual(lines, f.lastFooter) {
		return
	}
	if f.timerVisible && (f.footerOffset != offset || len(f.lastFooter) != len(lines)) {
		f.clearLiveFooterLocked()
	}
	allocate := !f.timerVisible
	rendered := f.styleFooterLinesLocked(lines)
	if f.manager != nil {
		f.manager.RenderFooterLinesBelow(f.id, rendered, offset, allocate)
	} else {
		RenderFooterLinesBelow(rendered, offset, allocate, f.out)
	}
	f.lastFooter = append(f.lastFooter[:0], lines...)
	f.footerOffset = offset
	f.timerVisible = true
}

func (f *Formatter) setActivityCursorHiddenLocked(hidden bool) {
	if f.activityCursorHidden == hidden {
		return
	}
	f.activityCursorHidden = hidden
	if hidden {
		HideCursor(f.out)
		return
	}
	ShowCursor(f.out)
}

func (f *Formatter) formatFooterLinesLocked(elapsed time.Duration, lineCount int) []string {
	width := f.width
	if width <= 0 {
		width = defaultWidth
	}
	tokenLine := formatTokenFooterLine(elapsed, f.cwd, f.activePromptTokens, f.maxInputTokens, f.tokenUsage, width)
	if lineCount <= 1 {
		return []string{tokenLine}
	}
	identity := f.footerIdentity
	if identity.Label == "" && f.sessionID != "" {
		identity = FooterIdentity{Label: "session", Value: f.sessionID}
	}
	statusLine := formatStatusFooterLine(f.modeTasks, f.activeModeTask, f.turnNumber, identity, f.footerModels, width)
	lines := []string{tokenLine, statusLine}
	if lineCount >= 3 && f.hasVisibleActivityLocked() {
		activityLine := renderActivityLine(selectActivityPresentation(f.activities), f.theme.Presentation, elapsed)
		lines = append([]string{activityLine}, lines...)
	}
	return lines
}

func (f *Formatter) hasVisibleActivityLocked() bool {
	return len(f.activities) > 0 && f.theme.Presentation.MotionMode() != presentation.MotionNone
}

func (f *Formatter) finalFooterLinesLocked() []string {
	if !f.timerEnabled || !f.started {
		return nil
	}
	elapsed := f.elapsedOffset
	if !f.timerStart.IsZero() {
		elapsed += time.Since(f.timerStart)
	}
	lines := f.formatFooterLinesLocked(elapsed, 2)
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return append([]string(nil), lines...)
}

func (f *Formatter) printFinalFooterLinesLocked(lines []string) {
	if len(lines) == 0 {
		return
	}
	for i := 0; i < f.liveFooterOffsetLocked(); i++ {
		fmt.Fprint(f.out, "\r\n")
	}
	for _, line := range f.styleFooterLinesLocked(lines) {
		fmt.Fprintf(f.out, "\r%s%s\n", ansiClearLine, line)
	}
}

type footerHighlight struct {
	text string
	role presentation.Role
	bold bool
}

func (f *Formatter) styleFooterLinesLocked(lines []string) []string {
	styled := make([]string, 0, len(lines))
	for i, line := range lines {
		var highlights []footerHighlight
		if len(lines) == 3 && i == 0 {
			mark, label, _ := strings.Cut(line, "  ")
			styled = append(styled, f.theme.render(presentation.StyledLine{
				presentation.Bold(presentation.RoleTruth, mark),
				presentation.Text("  "),
				presentation.RoleText(presentation.RoleBeauty, label),
			}))
			continue
		}
		tokenIndex := 0
		if len(lines) == 3 {
			tokenIndex = 1
		}
		if i == tokenIndex {
			separator := strings.Index(line, "  ")
			if separator < 0 {
				styled = append(styled, f.theme.render(presentation.StyledLine{presentation.Bold(presentation.RoleTruth, line)}))
				continue
			}
			if f.activePromptTokens > 0 && f.maxInputTokens > 0 {
				highlights = append(highlights, footerHighlight{text: formatContextRemainingPercent(f.activePromptTokens, f.maxInputTokens), role: presentation.RoleProvenance})
			}
			inputTokens := sessionInputTokens(f.tokenUsage)
			highlights = append(highlights, footerHighlight{text: formatFooterPercent(f.tokenUsage.InputHit, inputTokens), role: presentation.RoleProvenance})
			for _, value := range []int{inputTokens, f.tokenUsage.Output} {
				highlights = append(highlights, footerHighlight{text: formatTokenCount(value), role: presentation.RoleProvenance})
			}
			tokenLine := highlightFooterText(line[separator:], highlights)
			styled = append(styled, f.theme.render(append(presentation.StyledLine{
				presentation.Bold(presentation.RoleTruth, line[:separator]),
			}, tokenLine...)))
			continue
		} else {
			if f.turnNumber > 0 {
				highlights = append(highlights, footerHighlight{text: formatTurnFooterSegment(f.turnNumber), role: presentation.RoleTruth, bold: true})
			}
			if f.footerIdentity.Value != "" {
				highlights = append(highlights, footerHighlight{text: f.footerIdentity.Value, role: presentation.RoleProvenance})
			} else if f.sessionID != "" {
				highlights = append(highlights, footerHighlight{text: f.sessionID, role: presentation.RoleProvenance})
			}
			for _, model := range f.footerModels.Models {
				if model.Label != "" {
					highlights = append(highlights, footerHighlight{text: model.Label, role: presentation.RoleProvenance})
				}
			}
			if task := f.activeFooterTaskLocked(); task != nil {
				label := strings.ToLower(strings.TrimSpace(task.Mode))
				if label == "" {
					label = sanitizeLine(task.Title)
				}
				if label != "" {
					highlights = append(highlights, footerHighlight{text: label, role: presentation.RoleBeauty, bold: true})
				}
				status := strings.ToLower(strings.TrimSpace(task.Status))
				role := presentation.RoleTruth
				switch status {
				case "complete", "completed", "success":
					role = presentation.RoleGoodness
				case "pending", "waiting":
					role = presentation.RoleProvenance
				case "failed", "error":
					role = presentation.RoleRupture
				}
				if status != "" {
					highlights = append(highlights, footerHighlight{text: status, role: role, bold: true})
				}
			}
		}
		styled = append(styled, f.theme.render(highlightFooterText(line, highlights)))
	}
	return styled
}

func (f *Formatter) activeFooterTaskLocked() *ModeTaskDisplay {
	if len(f.modeTasks) == 0 {
		return nil
	}
	idx := f.activeModeTask
	if idx < 0 || idx >= len(f.modeTasks) {
		idx = activeModeTaskIndex(f.modeTasks)
	}
	if idx < 0 || idx >= len(f.modeTasks) {
		return nil
	}
	return &f.modeTasks[idx]
}

func highlightFooterText(text string, highlights []footerHighlight) presentation.StyledLine {
	line := presentation.StyledLine{}
	remaining := text
	for remaining != "" {
		bestIndex := -1
		best := footerHighlight{}
		for _, candidate := range highlights {
			if candidate.text == "" {
				continue
			}
			idx := strings.Index(remaining, candidate.text)
			if idx >= 0 && (bestIndex < 0 || idx < bestIndex || (idx == bestIndex && len(candidate.text) > len(best.text))) {
				bestIndex = idx
				best = candidate
			}
		}
		if bestIndex < 0 {
			line = append(line, presentation.Text(remaining))
			break
		}
		if bestIndex > 0 {
			line = append(line, presentation.Text(remaining[:bestIndex]))
		}
		span := presentation.RoleText(best.role, best.text)
		span.Bold = best.bold
		line = append(line, span)
		remaining = remaining[bestIndex+len(best.text):]
	}
	return line
}

func styleMetadataLine(text string) presentation.StyledLine {
	valueRole := presentation.RoleNormal
	if idx := strings.Index(text, ":"); idx >= 0 && strings.Contains(strings.ToLower(text[:idx]), "model") {
		valueRole = presentation.RoleProvenance
	}
	return append(presentation.StyledLine{presentation.Text("|   ")}, styleLabelValue(text, valueRole, false)...)
}

func styleLabelValue(text string, valueRole presentation.Role, bold bool) presentation.StyledLine {
	line := presentation.StyledLine{}
	if idx := strings.Index(text, ":"); idx >= 0 {
		value := presentation.RoleText(valueRole, text[idx+1:])
		value.Bold = bold
		line = append(line,
			presentation.RoleText(presentation.RoleTruth, text[:idx+1]),
			value,
		)
		return line
	}
	return append(line, presentation.Text(text))
}

func formatTokenFooterLine(elapsed time.Duration, cwd string, activePromptTokens, maxInputTokens int, usage TokenUsageUpdatedEvent, width int) string {
	elapsedText := formatElapsed(elapsed)
	cwd = FormatHomePath(cwd)
	shortCWD := shortenFooterPath(cwd)
	activeContext := ""
	activeContextShort := ""
	if activePromptTokens > 0 && maxInputTokens > 0 {
		activeContext = fmt.Sprintf("context remain %s", formatContextRemainingPercent(activePromptTokens, maxInputTokens))
		activeContextShort = activeContext
	}
	inputTokens := sessionInputTokens(usage)
	input := formatTokenCount(inputTokens)
	cache := formatFooterPercent(usage.InputHit, inputTokens)
	out := formatTokenCount(usage.Output)
	candidates := []string{
		joinTokenFooterSegments(elapsedText, cwd, activeContext, fmt.Sprintf("session token input %s (cache %s)  output %s", input, cache, out)),
		joinTokenFooterSegments(elapsedText, cwd, activeContextShort, fmt.Sprintf("input %s (cache %s)  output %s", input, cache, out)),
		joinTokenFooterSegments(elapsedText, shortCWD, activeContextShort, fmt.Sprintf("input %s (cache %s)  out %s", input, cache, out)),
		joinTokenFooterSegments(elapsedText, activeContextShort, fmt.Sprintf("input %s (cache %s)  out %s", input, cache, out)),
		joinTokenFooterSegments(elapsedText, activeContextShort, fmt.Sprintf("in %s (cache %s)  out %s", input, cache, out)),
		joinTokenFooterSegments(elapsedText, fmt.Sprintf("in %s (cache %s)  out %s", input, cache, out)),
		joinTokenFooterSegments(elapsedText, fmt.Sprintf("in %s  out %s", input, out)),
	}
	for _, candidate := range candidates {
		if fitsWidth(candidate, width) {
			return candidate
		}
	}
	return truncate(candidates[len(candidates)-1], width)
}

func sessionInputTokens(usage TokenUsageUpdatedEvent) int {
	hit := usage.InputHit
	if hit < 0 {
		hit = 0
	}
	miss := usage.InputMiss
	if miss < 0 {
		miss = 0
	}
	return hit + miss
}

func formatFooterPercent(value, total int) string {
	if value < 0 {
		value = 0
	}
	if total <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", float64(value)*100/float64(total))
}

func formatContextRemainingPercent(active, total int) string {
	if active < 0 {
		active = 0
	}
	remaining := total - active
	if remaining < 0 {
		remaining = 0
	}
	return formatFooterPercent(remaining, total)
}

func joinTokenFooterSegments(segments ...string) string {
	nonEmpty := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment != "" {
			nonEmpty = append(nonEmpty, segment)
		}
	}
	return strings.Join(nonEmpty, "  ")
}

func formatStatusFooterLine(tasks []ModeTaskDisplay, active int, turn int, identity FooterIdentity, models FooterModelMetadata, width int) string {
	task := formatModeTaskFooterSegment(tasks, active, turn, width)
	identitySegment := formatFooterIdentitySegment(identity)
	modelWidth := availableModelFooterWidth(width, task, identitySegment)
	modelsWithReasoning := formatModelFooterSegment(models, true, 0)
	modelsWithoutReasoning := formatModelFooterSegment(models, false, 0)
	modelsTruncated := fitModelFooterSegment(models, false, modelWidth)
	candidates := compactFooterCandidates(task, identitySegment, modelsWithReasoning, modelsWithoutReasoning, modelsTruncated)
	for _, candidate := range candidates {
		if fitsWidth(candidate, width) {
			return candidate
		}
	}
	if identitySegment != "" {
		return truncate(identitySegment, width)
	}
	if task != "" {
		return truncate(task, width)
	}
	if modelsWithoutReasoning != "" {
		return truncate(modelsWithoutReasoning, width)
	}
	return ""
}

func availableModelFooterWidth(width int, leadingSegments ...string) int {
	if width <= 0 {
		return 0
	}
	used := 0
	for _, segment := range leadingSegments {
		if segment == "" {
			continue
		}
		if used > 0 {
			used += 2
		}
		used += runeLen(segment)
	}
	if used == 0 {
		return width
	}
	return width - used - 2
}

func fitModelFooterSegment(models FooterModelMetadata, includeReasoning bool, width int) string {
	if width <= 0 {
		return ""
	}
	full := formatModelFooterSegment(models, includeReasoning, 0)
	if fitsWidth(full, width) {
		return full
	}
	maxLabelWidth := maxFooterModelLabelWidth(models)
	for labelWidth := maxLabelWidth - 1; labelWidth >= 1; labelWidth-- {
		segment := formatModelFooterSegment(models, includeReasoning, labelWidth)
		if segment != "" && fitsWidth(segment, width) {
			return segment
		}
	}
	return ""
}

func maxFooterModelLabelWidth(models FooterModelMetadata) int {
	maxWidth := 0
	for _, model := range models.Models {
		if n := runeLen(sanitizeLine(model.Label)); n > maxWidth {
			maxWidth = n
		}
	}
	return maxWidth
}

func compactFooterCandidates(task string, session string, modelSegments ...string) []string {
	var candidates []string
	for _, model := range modelSegments {
		switch {
		case task != "" && session != "" && model != "":
			candidates = append(candidates, task+"  "+session+"  "+model)
		case task != "" && session != "":
			candidates = append(candidates, task+"  "+session)
		case session != "" && model != "":
			candidates = append(candidates, session+"  "+model)
		case task != "" && model != "":
			candidates = append(candidates, task+"  "+model)
		case session != "":
			candidates = append(candidates, session)
		case task != "":
			candidates = append(candidates, task)
		case model != "":
			candidates = append(candidates, model)
		}
	}
	if task != "" && session != "" {
		candidates = append(candidates, task+"  "+session)
	}
	if session != "" {
		candidates = append(candidates, session)
	}
	if task != "" {
		candidates = append(candidates, task)
	}
	return dedupeStrings(candidates)
}

func formatFooterIdentitySegment(identity FooterIdentity) string {
	label := sanitizeLine(identity.Label)
	value := sanitizeLine(identity.Value)
	if label == "" || value == "" {
		return ""
	}
	return label + " " + value
}

func formatModeTaskFooterSegment(tasks []ModeTaskDisplay, active int, turn int, width int) string {
	if len(tasks) == 0 {
		return formatTurnFooterSegment(turn)
	}
	if active < 0 || active >= len(tasks) {
		active = activeModeTaskIndex(tasks)
	}
	if active < 0 || active >= len(tasks) {
		active = 0
	}
	task := tasks[active]
	label := strings.ToLower(strings.TrimSpace(task.Mode))
	if label == "" {
		label = sanitizeLine(task.Title)
	}
	if label == "" {
		label = "task"
	}
	status := strings.ToLower(strings.TrimSpace(task.Status))
	if status == "" {
		status = "pending"
	}
	index := task.Index
	if index <= 0 {
		index = active + 1
	}
	total := len(tasks)
	if total <= 1 {
		turnSegment := formatTurnFooterSegment(turn)
		candidate := joinFooterSegments(label, turnSegment, status)
		if fitsWidth(candidate, width) {
			return candidate
		}
		if turnSegment != "" {
			withoutTurn := joinFooterSegments(label, status)
			if fitsWidth(withoutTurn, width) {
				return withoutTurn
			}
		}
		reserve := len(" " + status)
		return fmt.Sprintf("%s %s", truncate(label, width-reserve), status)
	}
	taskSegment := fmt.Sprintf("task %d/%d", index, total)
	turnSegment := formatTurnFooterSegment(turn)
	candidate := joinFooterSegments(label, taskSegment, turnSegment, status)
	if fitsWidth(candidate, width) {
		return candidate
	}
	withoutTurn := joinFooterSegments(label, taskSegment, status)
	if fitsWidth(withoutTurn, width) {
		return withoutTurn
	}
	reserve := len(fmt.Sprintf(" %s %s", taskSegment, status))
	return fmt.Sprintf("%s %s %s", truncate(label, width-reserve), taskSegment, status)
}

func formatTurnFooterSegment(turn int) string {
	if turn <= 0 {
		return ""
	}
	return fmt.Sprintf("turn %d", turn)
}

func joinFooterSegments(segments ...string) string {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment != "" {
			parts = append(parts, segment)
		}
	}
	return strings.Join(parts, " ")
}

func formatModelFooterSegment(models FooterModelMetadata, includeReasoning bool, labelWidth int) string {
	var parts []string
	for _, model := range models.Models {
		if part := formatOneModelFooterSegment(model.Role, model.Label, model.Reasoning, includeReasoning, labelWidth); part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "  ")
}

func cloneFooterModelMetadata(metadata FooterModelMetadata) FooterModelMetadata {
	return FooterModelMetadata{Models: append([]FooterModelDisplay(nil), metadata.Models...)}
}

func formatOneModelFooterSegment(prefix, label, reasoning string, includeReasoning bool, labelWidth int) string {
	prefix = sanitizeLine(prefix)
	label = sanitizeLine(label)
	if prefix == "" || label == "" {
		return ""
	}
	if labelWidth > 0 {
		label = truncate(label, labelWidth)
	}
	if includeReasoning {
		reasoning = sanitizeLine(reasoning)
		if reasoning != "" {
			return prefix + " " + label + " " + reasoning
		}
	}
	return prefix + " " + label
}

func cloneModeTaskDisplays(tasks []ModeTaskDisplay) []ModeTaskDisplay {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]ModeTaskDisplay, 0, len(tasks))
	for _, task := range tasks {
		task.Title = sanitizeLine(task.Title)
		task.Mode = sanitizeLine(task.Mode)
		task.Status = sanitizeLine(task.Status)
		out = append(out, task)
	}
	return out
}

func activeModeTaskIndex(tasks []ModeTaskDisplay) int {
	for i, task := range tasks {
		if strings.EqualFold(strings.TrimSpace(task.Status), "running") {
			return i
		}
	}
	for i, task := range tasks {
		if !strings.EqualFold(strings.TrimSpace(task.Status), "complete") {
			return i
		}
	}
	if len(tasks) > 0 {
		return len(tasks) - 1
	}
	return -1
}

func footerLinesEqual(a, b []string) bool {
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

func fitsWidth(text string, width int) bool {
	if width <= 0 {
		return true
	}
	return runeLen(text) <= width
}

func runeLen(text string) int {
	return len([]rune(text))
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func (f *Formatter) stopTimerLocked() {
	f.clearLiveFooterLocked()
	f.setActivityCursorHiddenLocked(false)
	if f.timerTicker != nil {
		f.timerTicker.Stop()
	}
	if f.timerStop != nil {
		close(f.timerStop)
	}
	f.timerTicker = nil
	f.timerStop = nil
	f.timerStart = time.Time{}
	f.timerVisible = false
	f.footerOffset = 0
	f.lastFooter = nil
}

func (f *Formatter) timerLoop(ticker *time.Ticker, stop <-chan struct{}) {
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

// PromptSelection prints the prompt and numbered options, then reads the
// user's choice from stdin. Returns the trimmed input or an error.
func (f *Formatter) PromptSelection(prompt string, options []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.refreshTerminalSizeLocked()
	f.clearLiveFooterLocked()
	f.setActivityCursorHiddenLocked(false)
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
	f.renderTimerLocked()
	return strings.TrimSpace(input), nil
}

// PromptUser prints a yes/no prompt and returns true when the user answers
// "y" or "yes" (case-insensitive).
func (f *Formatter) PromptUser(prompt string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.refreshTerminalSizeLocked()
	f.clearLiveFooterLocked()
	f.setActivityCursorHiddenLocked(false)
	fmt.Fprintf(f.out, "%s%s", prompt, f.theme.ConfirmPromptSuffix)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	f.renderTimerLocked()
	input = strings.TrimSpace(strings.ToLower(input))
	return input == "y" || input == "yes"
}

// PromptInput prints the prompt and returns the trimmed user input from stdin
// or an error.
func (f *Formatter) PromptInput(prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.refreshTerminalSizeLocked()
	f.clearLiveFooterLocked()
	f.setActivityCursorHiddenLocked(false)
	fmt.Fprintf(f.out, "%s%s", prompt, f.theme.InputPromptSuffix)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	f.renderTimerLocked()
	return strings.TrimSpace(input), nil
}

// --- compile-time assertions ------------------------------------------------

var _ PromptStream = (*FormatterPromptStream)(nil)
