package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/presentation"
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
type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Len()
}

func newTestFormatter() (f *Formatter, bus *EventBus, buf *lockedBuffer) {
	buf = new(lockedBuffer)
	bus = NewEventBus(256)

	theme := Theme{
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
	s = ansiEscapePattern.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "\r", "")
}

var ansiEscapePattern = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|[A-Za-z])`)

func TestFormatterContinuationHintSemanticRoles(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()
	f.theme.Presentation = presentation.NewForTest(presentation.ProfileTerminal, true, false)

	bus.Emit(ContinuationHintEvent{Command: `mct-agent run -t "<next instruction>" --session-id agent-test`})
	time.Sleep(20 * time.Millisecond)

	output := buf.String()
	for _, want := range []string{
		"\x1b[1;32mContinue with your next instruction:\x1b[0m",
		"\x1b[1;36m--------------------------------",
		"\x1b[1;32m$ \x1b[0m",
		"\x1b[1;33mmct-agent run\x1b[0m",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected semantic output %q in %q", want, output)
		}
	}
	plain := stripANSI(output)
	if strings.Contains(plain, "Session ID:") || strings.Contains(plain, "Turns completed:") {
		t.Fatalf("quiet continuation leaked verbose detail: %q", plain)
	}
}

func TestFormatterContinuationHasOneBlankLineAfterFinalOutput(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()

	f.mu.Lock()
	f.handleFinalAnswer(FinalAnswerEvent{RenderedText: "last stdout line"})
	f.handleContinuationHint(ContinuationHintEvent{Command: `mct-agent run -t "<next instruction>" --session-id agent-test`})
	f.mu.Unlock()

	output := stripANSI(buf.String())
	if !strings.Contains(output, "last stdout line\n\nContinue with your next instruction:") {
		t.Fatalf("expected exactly one blank line before completion output, got %q", output)
	}
	if !strings.Contains(output, "  $ mct-agent run -t \"<next instruction>\" --session-id agent-test") {
		t.Fatalf("expected shell-style continuation command, got %q", output)
	}
	if strings.Count(output, "  --------------------------------") != 2 {
		t.Fatalf("expected continuation command block rules, got %q", output)
	}
	if strings.Contains(output, "last stdout line\n\n\nContinue with your next instruction:") {
		t.Fatalf("found more than one blank line before completion output: %q", output)
	}
}

func TestFormatterContinuationHintCustomInstruction(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()

	f.mu.Lock()
	f.handleContinuationHint(ContinuationHintEvent{
		Instruction: "Resume the interrupted shell-agent work:",
		Command:     `mct-agent run --session-id agent-test`,
	})
	f.mu.Unlock()

	output := stripANSI(buf.String())
	if !strings.Contains(output, "Resume the interrupted shell-agent work:") {
		t.Fatalf("expected custom instruction, got %q", output)
	}
	if strings.Contains(output, "Continue with your next instruction:") {
		t.Fatalf("unexpected default instruction in custom continuation: %q", output)
	}
	if !strings.Contains(output, "  $ mct-agent run --session-id agent-test") {
		t.Fatalf("expected shell-agent resume command block, got %q", output)
	}
}

func TestCoordinatedWriterClearsLiveFooterBeforeDiagnostic(t *testing.T) {
	f, bus, terminal := newTestFormatter()
	defer bus.Close()

	f.mu.Lock()
	f.timerVisible = true
	f.lastFooter = []string{"footer line 1", "footer line 2"}
	f.footerOffset = 1
	f.mu.Unlock()

	var diagnostics bytes.Buffer
	if _, err := f.CoordinateWriter(&diagnostics).Write([]byte("planner diagnostic\n")); err != nil {
		t.Fatal(err)
	}
	if diagnostics.String() != "planner diagnostic\n" {
		t.Fatalf("unexpected diagnostic output %q", diagnostics.String())
	}
	if !strings.Contains(terminal.String(), ansiClearLine) {
		t.Fatalf("expected live footer clear before diagnostic, got %q", terminal.String())
	}
	f.mu.Lock()
	visible := f.timerVisible
	f.mu.Unlock()
	if visible {
		t.Fatal("live footer remained marked visible after external output")
	}
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

func TestFormatterFooterIncludesTokenUsage(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()
	f.timerEnabled = true

	bus.Emit(SessionStartedEvent{
		SessionID: "agent-20260709T125317-5182",
		Goal:      "test goal",
		Elapsed:   65 * time.Second,
		TokenUsage: TokenUsageUpdatedEvent{
			InputHit:  1234,
			InputMiss: 56789,
			Output:    1000,
		},
	})
	bus.Emit(ActionExecutedEvent{Step: 1, StepLimit: 1, Command: "true"})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())
	requireContains(t, output, "1:05  tokens  input hit 1,234  input miss 56,789  output 1,000")
	_ = f
}

func TestFormatterFooterFollowsOutputInsteadOfTerminalBottom(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()
	f.timerEnabled = true
	f.height = 24
	f.width = 120

	f.handleSessionStarted(SessionStartedEvent{
		SessionID: "agent-relative-footer",
		Turn:      1,
	})
	firstRenderEnd := buf.Len()
	f.handleRawString(RawStringEvent{Text: "result one"})

	output := buf.String()
	if strings.Contains(output, "\033[1;22r") || strings.Contains(output, "\033[23;1H") {
		t.Fatalf("footer should not reserve or target terminal-bottom rows\nGot: %q", output)
	}
	if !strings.Contains(output[:firstRenderEnd], "\r\n\r\n\033[2A\033[s\033[1B\r") {
		t.Fatalf("initial footer should allocate one blank row below the output frontier\nGot: %q", output[:firstRenderEnd])
	}
	moved := output[firstRenderEnd:]
	clear := strings.Index(moved, "\033[s\033[1B\r\033[2K")
	result := strings.Index(moved, "result one\n")
	redraw := strings.LastIndex(moved, "\r\n\r\n\033[2A\033[s\033[1B\r")
	if clear < 0 || result < 0 || redraw < 0 || !(clear < result && result < redraw) {
		t.Fatalf("footer should clear, emit output, then redraw below the new frontier\nGot: %q", moved)
	}
}

func TestFormatterFooterIncludesModeAndModelStatus(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()
	f.timerEnabled = true
	f.width = 120

	bus.Emit(ModeTaskPlanDisplayEvent{Tasks: []ModeTaskDisplay{{
		Index:  1,
		Title:  "Code (Forge + Skyvern)",
		Mode:   "code-forge-skyvern",
		Status: "pending",
	}}})
	bus.Emit(ModeTaskStatusUpdateEvent{Index: 0, Title: "Code (Forge + Skyvern)", Status: "running"})
	bus.Emit(SessionStartedEvent{
		Goal:    "test goal",
		Turn:    1,
		Elapsed: 42 * time.Second,
		TokenUsage: TokenUsageUpdatedEvent{
			InputHit:  491392,
			InputMiss: 61688,
			Output:    7007,
		},
		Models: FooterModelMetadata{
			Models: []FooterModelDisplay{
				{Role: "planner", Label: "deepseek-v4-flash", Reasoning: "medium"},
				{Role: "shell", Label: "deepseek-v4-flash", Reasoning: "medium"},
			},
		},
	})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())
	requireContains(t, output, "42s  tokens  input hit 491,392  input miss 61,688  output 7,007")
	requireContains(t, output, "code-forge-skyvern turn 1 running  planner deepseek-v4-flash medium  shell deepseek-v4-flash medium")
	if strings.Contains(output, "[meta] planned tasks") || strings.Contains(output, "[meta] task") {
		t.Fatalf("expected mode metadata to stay out of normal output\nGot: %s", output)
	}
	_ = f
}

func TestFormatterFooterSupportsSyncIdentityAndDynamicRoles(t *testing.T) {
	f, bus, _ := newTestFormatter()
	defer bus.Close()
	f.timerEnabled = true
	f.width = 180

	f.handleSessionStarted(SessionStartedEvent{
		Identity: FooterIdentity{Label: "sync", Value: "abcdef123456"},
	})
	f.handleFooterModelsUpdated(FooterModelsUpdatedEvent{Models: FooterModelMetadata{
		Models: []FooterModelDisplay{
			{Role: "discovery", Label: "provider:discovery-model", Reasoning: "low"},
			{Role: "answer", Label: "provider:answer-model", Reasoning: "high"},
		},
	}})
	f.handleTokenUsageUpdated(TokenUsageUpdatedEvent{InputHit: 40, InputMiss: 60, Output: 25})

	lines := f.finalFooterLinesLocked()
	if len(lines) != 2 {
		t.Fatalf("footer lines = %#v", lines)
	}
	requireContains(t, lines[0], "tokens  input hit 40  input miss 60  output 25")
	requireContains(t, lines[1], "sync abcdef123456  discovery provider:discovery-model low  answer provider:answer-model high")
}

func TestFormatterFooterSyncNoOpStillHasTwoLinesAndSignalsDone(t *testing.T) {
	f, bus, _ := newTestFormatter()
	defer bus.Close()
	f.timerEnabled = true
	f.width = 120
	f.handleSessionStarted(SessionStartedEvent{
		Identity: FooterIdentity{Label: "sync", Value: "abcdef123456"},
	})

	lines := f.finalFooterLinesLocked()
	if len(lines) != 2 || lines[1] != "sync abcdef123456" {
		t.Fatalf("footer lines = %#v", lines)
	}
	f.handleSessionEnded(SessionEndedEvent{})
	select {
	case <-f.Done():
	default:
		t.Fatal("formatter completion was not signaled")
	}
}

func TestFormatterFooterStylesModeAsBoldBeauty(t *testing.T) {
	f, bus, _ := newTestFormatter()
	defer bus.Close()
	f.theme.Presentation = presentation.NewForTest(presentation.ProfileTerminal, true, false)
	f.width = 120
	f.turnNumber = 1
	f.modeTasks = []ModeTaskDisplay{{
		Index:  1,
		Mode:   "code-forge-skyvern",
		Status: "running",
	}}
	f.activeModeTask = 0

	lines := f.formatFooterLinesLocked(42*time.Second, 2)
	styled := f.styleFooterLinesLocked(lines)
	if len(styled) != 2 {
		t.Fatalf("expected two styled footer lines, got %d", len(styled))
	}
	if !strings.Contains(styled[1], "\x1b[1;35mcode-forge-skyvern\x1b[0m") {
		t.Fatalf("expected mode value to use bold Beauty/magenta styling, got %q", styled[1])
	}
	if strings.Contains(styled[1], "\x1b[1;36mcode-forge-skyvern\x1b[0m") {
		t.Fatalf("mode value should not use Truth/cyan styling, got %q", styled[1])
	}
}

func TestFormatterSessionEndedPrintsFinalFooterAfterOutput(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()
	f.timerEnabled = true
	f.width = 180
	f.height = 24

	bus.Emit(ModeTaskPlanDisplayEvent{Tasks: []ModeTaskDisplay{{
		Index:  1,
		Mode:   "code-forge-skyvern",
		Status: "running",
	}}})
	bus.Emit(SessionStartedEvent{
		SessionID: "agent-20260709T125317-5182",
		Goal:      "test goal",
		Turn:      2,
		Elapsed:   65 * time.Second,
		TokenUsage: TokenUsageUpdatedEvent{
			InputHit:  1234,
			InputMiss: 56789,
			Output:    1000,
		},
		Models: FooterModelMetadata{
			Models: []FooterModelDisplay{
				{Role: "planner", Label: "openrouter:z-ai/glm-5.2", Reasoning: "high"},
				{Role: "shell", Label: "deepseek:deepseek-v4-pro", Reasoning: "max"},
			},
		},
	})
	bus.Emit(RawStringEvent{Text: "=== SESSION COMPLETE ==="})
	bus.Emit(SessionEndedEvent{})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())
	complete := strings.LastIndex(output, "=== SESSION COMPLETE ===")
	tokenFooter := strings.LastIndex(output, "tokens  input hit 1,234  input miss 56,789  output 1,000")
	statusFooter := strings.LastIndex(output, "code-forge-skyvern turn 2 running  session agent-20260709T125317-5182  planner openrouter:z-ai/glm-5.2 high  shell deepseek:deepseek-v4-pro max")
	if complete < 0 || tokenFooter < 0 || statusFooter < 0 {
		t.Fatalf("expected session output and final footer\nGot: %s", output)
	}
	if !(complete < tokenFooter && tokenFooter < statusFooter) {
		t.Fatalf("expected final footer after session output\nGot: %s", output)
	}
	requireContains(t, output, "=== SESSION COMPLETE ===\n\n")
	_ = f
}

func TestPrintFinalFooterLinesClearsRows(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()

	f.mu.Lock()
	f.printFinalFooterLinesLocked([]string{"short footer", "status footer"})
	f.mu.Unlock()

	got := buf.String()
	want := "\r\n\r\033[2Kshort footer\n\r\033[2Kstatus footer\n"
	if got != want {
		t.Fatalf("final footer should start on cleared rows\nwant: %q\n got: %q", want, got)
	}
}

func TestFormatterFooterDegradesForNarrowWidth(t *testing.T) {
	f, bus, _ := newTestFormatter()
	defer bus.Close()
	f.width = 50
	f.sessionID = "agent-20260709T125317-5182"
	f.turnNumber = 3
	f.tokenUsage = TokenUsageUpdatedEvent{InputHit: 491392, InputMiss: 61688, Output: 7007}
	f.modeTasks = []ModeTaskDisplay{{
		Index:  1,
		Mode:   "code-forge-skyvern",
		Status: "running",
	}}
	f.activeModeTask = 0
	f.footerModels = FooterModelMetadata{
		Models: []FooterModelDisplay{
			{Role: "planner", Label: "very-long-orchestrator-model-label", Reasoning: "medium"},
			{Role: "shell", Label: "very-long-shell-agent-model-label", Reasoning: "high"},
		},
	}

	lines := f.formatFooterLinesLocked(42*time.Second, 2)
	if len(lines) != 2 {
		t.Fatalf("expected two footer lines, got %d", len(lines))
	}
	for _, line := range lines {
		if len([]rune(line)) > f.width {
			t.Fatalf("footer line exceeds width %d: %q", f.width, line)
		}
	}
	requireContains(t, lines[0], "tokens")
	requireContains(t, lines[1], "session agent-20260709T125317-5182")
	if strings.Contains(lines[1], "medium") || strings.Contains(lines[1], "high") {
		t.Fatalf("expected narrow status line to drop reasoning before task status: %q", lines[1])
	}
}

func TestFormatTokenFooterLineCompactionCandidates(t *testing.T) {
	usage := TokenUsageUpdatedEvent{InputHit: 1234, InputMiss: 56789, Output: 1000}
	tests := []struct {
		name string
		want string
	}{
		{
			name: "widest",
			want: "1:05  ~/projects/mct  active token context 25,000/120,000  tokens  input hit 1,234  input miss 56,789  output 1,000",
		},
		{
			name: "compact labels",
			want: "1:05  ~/projects/mct  act 25,000/120,000  tokens  hit 1,234  miss 56,789  out 1,000",
		},
		{
			name: "short cwd",
			want: "1:05  ~/p/m  act 25,000/120,000  hit 1,234  miss 56,789  out 1,000",
		},
		{
			name: "without cwd",
			want: "1:05  act 25,000/120,000  hit 1,234  miss 56,789  out 1,000",
		},
		{
			name: "compact totals",
			want: "1:05  act 25,000/120,000  in 1,234/56,789  out 1,000",
		},
		{
			name: "fallback",
			want: "1:05  in 1,234/56,789  out 1,000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTokenFooterLine(65*time.Second, "~/projects/mct", 25000, 120000, usage, runeLen(tt.want))
			if got != tt.want {
				t.Fatalf("footer = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatTokenFooterLineOmitsUnavailableOptionalSegments(t *testing.T) {
	usage := TokenUsageUpdatedEvent{InputHit: 40, InputMiss: 60, Output: 25}
	tests := []struct {
		name               string
		cwd                string
		activePromptTokens int
		maxInputTokens     int
		want               string
	}{
		{
			name: "no optional values",
			want: "42s  tokens  input hit 40  input miss 60  output 25",
		},
		{
			name:               "active prompt without budget",
			activePromptTokens: 25000,
			want:               "42s  tokens  input hit 40  input miss 60  output 25",
		},
		{
			name:           "budget without active prompt",
			maxInputTokens: 120000,
			want:           "42s  tokens  input hit 40  input miss 60  output 25",
		},
		{
			name: "cwd only",
			cwd:  "~/projects/mct",
			want: "42s  ~/projects/mct  tokens  input hit 40  input miss 60  output 25",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTokenFooterLine(42*time.Second, tt.cwd, tt.activePromptTokens, tt.maxInputTokens, usage, 200)
			if got != tt.want {
				t.Fatalf("footer = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatFooterCWDAndShortening(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir: %v", err)
	}
	underHome := filepath.Join(home, "projects", "machtiani")
	if got, want := formatFooterCWD(underHome), "~/projects/machtiani"; got != want {
		t.Fatalf("formatFooterCWD(%q) = %q, want %q", underHome, got, want)
	}
	if got := formatFooterCWD(home); got != "~" {
		t.Fatalf("formatFooterCWD(home) = %q, want ~", got)
	}
	sibling := filepath.Join(home+"-other", "project")
	if got := formatFooterCWD(sibling); got != sibling {
		t.Fatalf("formatFooterCWD(%q) = %q, want unchanged path", sibling, got)
	}
	if got, want := shortenFooterPath("~/projects/to/name"), "~/p/t/n"; got != want {
		t.Fatalf("shortenFooterPath = %q, want %q", got, want)
	}
}

func TestFormatterFinalFooterKeepsCWDAndClearsActiveContext(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()
	f.timerEnabled = true
	f.width = 180
	f.handleSessionStarted(SessionStartedEvent{
		CWD:            "~/projects/mct",
		MaxInputTokens: 120000,
	})
	f.handleTokenUsageUpdated(TokenUsageUpdatedEvent{
		InputHit:           1234,
		InputMiss:          56789,
		Output:             1000,
		ActivePromptTokens: 25000,
	})
	buf.Reset()

	f.handleSessionEnded(SessionEndedEvent{})
	output := stripANSI(buf.String())
	requireContains(t, output, "~/projects/mct")
	if strings.Contains(output, "active token context") || strings.Contains(output, "act 25,000/120,000") {
		t.Fatalf("final footer retained active context: %q", output)
	}
	if f.activePromptTokens != 0 || f.maxInputTokens != 0 {
		t.Fatalf("active context was not cleared: active=%d max=%d", f.activePromptTokens, f.maxInputTokens)
	}
}

func TestFormatterFooterUsesAvailableWidthForModelLabels(t *testing.T) {
	f, bus, _ := newTestFormatter()
	defer bus.Close()
	f.width = 180
	f.sessionID = "agent-20260709T125317-5182"
	f.turnNumber = 4
	f.modeTasks = []ModeTaskDisplay{{
		Index:  1,
		Mode:   "code-forge-skyvern",
		Status: "running",
	}}
	f.activeModeTask = 0
	f.footerModels = FooterModelMetadata{
		Models: []FooterModelDisplay{
			{Role: "planner", Label: "very-long-orchestrator-model-label", Reasoning: "medium"},
			{Role: "shell", Label: "very-long-shell-agent-model-label", Reasoning: "high"},
		},
	}

	lines := f.formatFooterLinesLocked(42*time.Second, 2)
	if len(lines) != 2 {
		t.Fatalf("expected two footer lines, got %d", len(lines))
	}
	requireContains(t, lines[1], "code-forge-skyvern turn 4 running")
	requireContains(t, lines[1], "session agent-20260709T125317-5182")
	requireContains(t, lines[1], "planner very-long-orchestrator-model-label medium")
	requireContains(t, lines[1], "shell very-long-shell-agent-model-label high")
}

func TestFormatterFooterShowsTaskFractionForMultiTaskMode(t *testing.T) {
	f, bus, _ := newTestFormatter()
	defer bus.Close()
	f.width = 120
	f.turnNumber = 7
	f.modeTasks = []ModeTaskDisplay{
		{Index: 1, Mode: "code-forge-skyvern", Status: "complete"},
		{Index: 2, Mode: "code-forge-skyvern", Status: "running"},
	}
	f.activeModeTask = 1

	lines := f.formatFooterLinesLocked(42*time.Second, 2)
	if len(lines) != 2 {
		t.Fatalf("expected two footer lines, got %d", len(lines))
	}
	requireContains(t, lines[1], "code-forge-skyvern task 2/2 turn 7 running")
}

func TestFormatterFooterUpdatesTurnStatus(t *testing.T) {
	f, bus, _ := newTestFormatter()
	defer bus.Close()
	f.width = 120
	f.modeTasks = []ModeTaskDisplay{{
		Index:  1,
		Mode:   "code-forge-skyvern",
		Status: "running",
	}}
	f.activeModeTask = 0

	f.handleTurnStatusUpdated(TurnStatusUpdatedEvent{Turn: 3})

	lines := f.formatFooterLinesLocked(42*time.Second, 2)
	if len(lines) != 2 {
		t.Fatalf("expected two footer lines, got %d", len(lines))
	}
	requireContains(t, lines[1], "code-forge-skyvern turn 3 running")
}

func TestFooterLineCountHeightFallbacks(t *testing.T) {
	f, bus, _ := newTestFormatter()
	defer bus.Close()
	f.timerEnabled = true

	f.height = minFooterHeight + 1
	if got := f.footerLineCountLocked(); got != 2 {
		t.Fatalf("footerLineCount tall = %d, want 2", got)
	}
	f.height = minFooterHeight
	if got := f.footerLineCountLocked(); got != 1 {
		t.Fatalf("footerLineCount one-line fallback = %d, want 1", got)
	}
	f.height = minFooterHeight - 1
	if got := f.footerLineCountLocked(); got != 0 {
		t.Fatalf("footerLineCount tiny = %d, want 0", got)
	}
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
	bus.Emit(ActionExecutedEvent{
		Step:        2,
		StepLimit:   5,
		Command:     "ls -la",
		Description: "I'll list the directory.\n\n<command>ls -la</command>",
	})
	bus.Emit(PromptCompletedEvent{StreamID: "s1", FinalText: "llm output"})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())

	requireContains(t, output, "Step 2 of 5")
	requireContains(t, output, "I'll list the directory.")
	requireContains(t, output, "$ ls -la")
	if strings.Contains(output, "<command>") {
		t.Errorf("expected output not to contain raw command tags\nGot: %s", output)
	}
	_ = f
}

func TestFormatterActionExecutedCommandOnly(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()

	bus.Emit(PromptStartedEvent{StreamID: "s1", Prompt: "test"})
	bus.Emit(ActionExecutedEvent{
		Step:        3,
		StepLimit:   110,
		Command:     "cat agent/internal/shell-agent/internal/agents/loop.go",
		Description: "cat agent/internal/shell-agent/internal/agents/loop.go",
	})
	bus.Emit(ActionExecutedEvent{
		Step:        4,
		StepLimit:   110,
		Command:     "cat agent/internal/shell-agent/internal/agents/cache_anchor.go",
		Description: "cat agent/internal/shell-agent/internal/agents/cache_anchor.go",
	})
	time.Sleep(50 * time.Millisecond)

	output := stripANSI(buf.String())

	requireContains(t, output, "Step 3 of 110")
	requireContains(t, output, "$ cat agent/internal/shell-agent/internal/agents/loop.go")
	requireContains(t, output, "$ cat agent/internal/shell-agent/internal/agents/loop.go\n\nStep 4 of 110")
	if strings.Count(output, "cat agent/internal/shell-agent/internal/agents/loop.go") != 1 {
		t.Errorf("expected command to be rendered once\nGot: %s", output)
	}
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
