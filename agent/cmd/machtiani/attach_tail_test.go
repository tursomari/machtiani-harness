package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/presentation"
	"github.com/tursomari/machtiani/agent/internal/shellaction"
)

type attachTestTerminal struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	wants  []string
	seen   chan struct{}
	once   sync.Once
}

func newAttachTestTerminal(wants ...string) *attachTestTerminal {
	return &attachTestTerminal{wants: wants, seen: make(chan struct{})}
}

func (w *attachTestTerminal) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buffer.Write(p)
	output := w.buffer.String()
	w.mu.Unlock()
	if err == nil {
		matched := true
		for _, want := range w.wants {
			if !strings.Contains(output, want) {
				matched = false
				break
			}
		}
		if matched {
			w.once.Do(func() { close(w.seen) })
		}
	}
	return n, err
}

func (w *attachTestTerminal) Fd() uintptr { return 1 }

func (w *attachTestTerminal) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func TestRunAttachTailsRunningSessionWithoutDuplicates(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-tail-loop"
	conv := conversation.New(sessionID, "Tail the live session")
	addAttachTailTurn(conv, 1)
	path := writeAttachTailConversation(t, conv)

	var active atomic.Bool
	active.Store(true)
	writerDone := make(chan error, 1)
	go func() {
		time.Sleep(15 * time.Millisecond)
		conv.AddMessage("assistant", "Live question 2", map[string]any{
			"type":     "work_request",
			"turn":     2,
			"decision": "Live decision 2",
		})
		if err := publishAttachTailConversation(path, conv); err != nil {
			writerDone <- err
			return
		}

		time.Sleep(15 * time.Millisecond)
		conv.AddMessage("assistant", "Live answer 2", map[string]any{"type": "work_result", "turn": 2})
		if err := publishAttachTailConversation(path, conv); err != nil {
			writerDone <- err
			return
		}

		time.Sleep(15 * time.Millisecond)
		addAttachTailTurn(conv, 3)
		if err := publishAttachTailConversation(path, conv); err != nil {
			writerDone <- err
			return
		}
		time.Sleep(15 * time.Millisecond)
		active.Store(false)
		writerDone <- nil
	}()

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile: os.ReadFile,
		probe: func(string) (bool, error) {
			return active.Load(), nil
		},
		pollInterval: 5 * time.Millisecond,
		quietGrace:   20 * time.Millisecond,
	})
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("runAttachWithDependencies() exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	output := stdout.String()
	for _, want := range []string{
		"Live question 1",
		"Live answer 1",
		"Live question 2",
		"Live answer 2",
		"Live question 3",
		"Live answer 3",
	} {
		if got := strings.Count(output, want); got != 1 {
			t.Errorf("stdout count for %q = %d, want 1:\n%s", want, got, output)
		}
	}
	for _, framing := range []string{"── ", "──── ", "ARTIFACTS"} {
		if strings.Contains(output, framing) {
			t.Errorf("stdout contains replay-only framing %q:\n%s", framing, output)
		}
	}
}

func TestRunAttachReportsMidLoopFailures(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-tail-errors"
	sessionDirectory, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	initial := conversation.New(sessionID, "Tail errors are reported")
	addAttachTailTurn(initial, 1)
	initialData, err := initial.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	unknown := conversation.New(sessionID, "Tail errors are reported")
	addAttachTailTurn(unknown, 1)
	unknown.AddMessage("assistant", "Cannot render this", map[string]any{"type": "unknown_tail_type"})
	unknownData, err := unknown.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		nextData   []byte
		readErr    error
		wantStderr string
	}{
		{name: "read", readErr: errors.New("tail read failed"), wantStderr: "tail read failed"},
		{name: "unmarshal", nextData: []byte("{invalid json"), wantStderr: "unmarshal conversation"},
		{name: "render", nextData: unknownData, wantStderr: `unhandled message type "unknown_tail_type"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reads := 0
			readFile := func(string) ([]byte, error) {
				reads++
				if reads == 1 {
					return initialData, nil
				}
				return tt.nextData, tt.readErr
			}

			var stdout, stderr strings.Builder
			code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
				readFile:     readFile,
				probe:        func(string) (bool, error) { return true, nil },
				pollInterval: time.Millisecond,
				quietGrace:   5 * time.Millisecond,
			})
			if code != 1 {
				t.Fatalf("runAttachWithDependencies() exit = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stderr = %q, want error containing %q", stderr.String(), tt.wantStderr)
			}
			if got := strings.Count(stdout.String(), "Live question 1"); got != 1 {
				t.Fatalf("initial snapshot count = %d, want 1: %q", got, stdout.String())
			}
		})
	}
}

func TestRunAttachRendersPersistedShellActionsByDefault(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-actions-finished"
	conv := conversation.New(sessionID, "Replay persisted actions")
	addAttachTailTurn(conv, 1)
	writeAttachTailConversation(t, conv)
	writeAttachActions(t, sessionID, 1,
		shellaction.Record{Version: 1, SessionID: sessionID, Turn: 1, Sequence: 2, Description: "Run tests", Command: "go test ./..."},
		shellaction.Record{Version: 1, SessionID: sessionID, Turn: 1, Sequence: 1, Description: "Inspect status", Command: "git status"},
	)

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		readActions:  os.ReadFile,
		probe:        func(string) (bool, error) { return false, nil },
		pollInterval: time.Millisecond,
		quietGrace:   3 * time.Millisecond,
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{"$ git status", "$ go test ./..."} {
		if got := strings.Count(output, want); got != 1 {
			t.Errorf("stdout count for %q = %d, want 1:\n%s", want, got, output)
		}
	}
	if strings.Index(output, "$ git status") >= strings.Index(output, "$ go test ./...") {
		t.Fatalf("actions not rendered in sequence order:\n%s", output)
	}
}

func TestRunAttachNoShellStepsDoesNotReadJournal(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-actions-hidden"
	conv := conversation.New(sessionID, "Hide persisted actions")
	addAttachTailTurn(conv, 1)
	writeAttachTailConversation(t, conv)
	legacy, err := conversation.RenderReplay(conv)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile: os.ReadFile,
		readActions: func(string) ([]byte, error) {
			t.Fatal("actions journal read with --no-shell-steps")
			return nil, nil
		},
		probe:        func(string) (bool, error) { return false, nil },
		pollInterval: time.Millisecond,
		quietGrace:   3 * time.Millisecond,
		noShellSteps: true,
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	want := legacy + "\n\n"
	if got := stdout.String(); got != want {
		t.Fatalf("suppressed output changed legacy replay:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestRunAttachTailsLateShellActionAndExtendsQuietGrace(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-actions-live"
	conv := conversation.New(sessionID, "Tail late actions")
	addAttachTailTurn(conv, 1)
	writeAttachTailConversation(t, conv)

	writerDone := make(chan error, 1)
	go func() {
		time.Sleep(15 * time.Millisecond)
		writerDone <- appendAttachAction(sessionID, 1, shellaction.Record{
			Version: 1, SessionID: sessionID, Turn: 1, Sequence: 1,
			Description: "Late announced action", Command: "printf late",
		})
	}()

	started := time.Now()
	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		readActions:  os.ReadFile,
		probe:        func(string) (bool, error) { return false, nil },
		pollInterval: 2 * time.Millisecond,
		quietGrace:   30 * time.Millisecond,
	})
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	if got := strings.Count(stdout.String(), "$ printf late"); got != 1 {
		t.Fatalf("late action count = %d, want 1:\n%s", got, stdout.String())
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("attach exited after %s; late action did not extend quiet grace", elapsed)
	}
}

func TestRunAttachTTYShowsBannerNoticeAndSingleConclusion(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-tty-finished"
	conv := conversation.New(sessionID, "TTY finished session")
	addAttachTailTurn(conv, 1)
	conv.AddMessage("assistant", "TTY final answer", map[string]any{"type": "final", "turns": 1, "capped": false})
	writeAttachTailConversation(t, conv)
	writeAttachActions(t, sessionID, 1, shellaction.Record{
		Version: 1, SessionID: sessionID, Turn: 1, Sequence: 1,
		Description: "Inspect status", Command: "git status", Step: 1, StepLimit: 2,
		CommandsExecuted: 1, RemainingSteps: 1,
	})

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		probe:        func(string) (bool, error) { return false, nil },
		pollInterval: time.Millisecond,
		quietGrace:   3 * time.Millisecond,
		theme:        presentation.NewForTest(presentation.ProfileTerminal, true, false),
		isTerminal:   func(io.Writer) bool { return true },
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{"machtiani (mct)", "PROMPT", "TTY final answer", "Resume this session:"} {
		if !strings.Contains(output, want) {
			t.Errorf("TTY stdout missing %q in:\n%s", want, output)
		}
	}
	for _, want := range []string{"\x1b[1;36mStep 1 of 2\x1b[0m", "\x1b[1;32m$ \x1b[0m", "git status"} {
		if !strings.Contains(output, want) {
			t.Errorf("TTY stdout missing styled action %q in:\n%q", want, output)
		}
	}
	if strings.Contains(output, "commands executed:") {
		t.Errorf("TTY stdout contains replay-only action counter:\n%s", output)
	}
	if got := strings.Count(output, "Resume this session:"); got != 1 {
		t.Errorf("conclusion count = %d, want exactly 1:\n%s", got, output)
	}
	if strings.Contains(output, "following session") {
		t.Errorf("inactive session drew a status line:\n%q", output)
	}
	if !strings.HasSuffix(output, "\n") {
		t.Errorf("TTY stdout does not end with a newline:\n%q", output)
	}
}

func TestRunAttachTTYStatusLineClearsOnExit(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-tty-live"
	conv := conversation.New(sessionID, "TTY live session")
	addAttachTailTurn(conv, 1)
	path := writeAttachTailConversation(t, conv)

	var active atomic.Bool
	active.Store(true)
	writerDone := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		conv.AddMessage("assistant", "TTY live turn 2", map[string]any{
			"type":     "work_request",
			"turn":     2,
			"decision": "TTY live decision",
		})
		if err := publishAttachTailConversation(path, conv); err != nil {
			writerDone <- err
			return
		}
		time.Sleep(300 * time.Millisecond)
		active.Store(false)
		writerDone <- nil
	}()

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		probe:        func(string) (bool, error) { return active.Load(), nil },
		pollInterval: 3 * time.Millisecond,
		quietGrace:   8 * time.Millisecond,
		isTerminal:   func(io.Writer) bool { return true },
	})
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "following session") {
		t.Errorf("status line label missing in:\n%q", output)
	}
	if strings.Contains(output, "following session "+sessionID) {
		t.Errorf("status line label retained the session id in:\n%q", output)
	}
	if !strings.Contains(output, "\u2814\u2800\u2800") {
		t.Errorf("status line animation frame missing in:\n%q", output)
	}
	if !strings.Contains(output, "\r\x1b[2K") {
		t.Errorf("status line was never cleared with carriage return:\n%q", output)
	}
	if !strings.Contains(output, "\r\x1b[2K0s  session token input 0 (cache 0%)  output 0\n\r\x1b[2Kturn 1  session "+sessionID+"\n") {
		t.Errorf("TTY output does not end with the run-style persisted footer:\n%q", output)
	}
	if !strings.Contains(output, "TTY live turn 2") {
		t.Errorf("delta content missing in:\n%s", output)
	}
}

func TestRunAttachTTYShowsLiveFooterOverlayBeforeQuietGrace(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-live-footer"
	conv := conversation.New(sessionID, "Live persisted footer")
	addAttachTailTurn(conv, 1)
	conv.TurnsCompleted = 2
	conv.RuntimeStats = conversation.NewRuntimeStatsState(9_000, 20_000, 30_000, 4_000)
	conv.RuntimeStats.PlannerActivePromptTokens = 25_000
	conv.Footer = &conversation.FooterState{
		CWD:            filepath.Join(string(filepath.Separator), "workspace", "live-project"),
		MaxInputTokens: 100_000,
		Mode:           "code",
		Models: []conversation.FooterModelState{
			{Role: "planner", Label: "provider:planner-live"},
			{Role: "shell", Label: "provider:shell-live"},
		},
	}
	writeAttachTailConversation(t, conv)

	terminal := newAttachTestTerminal(
		"following session",
		"/workspace/live-project",
		"context remain 75%",
		"session token input 50,000",
		"code turn 3 running",
		"planner provider:planner-live",
		"shell provider:shell-live",
	)
	var active atomic.Bool
	active.Store(true)
	done := make(chan int, 1)
	var stderr strings.Builder
	go func() {
		done <- runAttachWithDependencies(sessionID, terminal, &stderr, attachDependencies{
			readFile:     os.ReadFile,
			probe:        func(string) (bool, error) { return active.Load(), nil },
			pollInterval: 3 * time.Millisecond,
			quietGrace:   12 * time.Millisecond,
			isTerminal:   func(out io.Writer) bool { _, ok := out.(*attachTestTerminal); return ok },
			footerWidth:  func(io.Writer) int { return 240 },
		})
	}()

	select {
	case <-terminal.seen:
		if !active.Load() {
			t.Fatal("live footer was first observed after the session became inactive")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("live footer overlay was not drawn before quiet grace:\n%q", terminal.String())
	}
	active.Store(false)

	select {
	case code := <-done:
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("attach did not finish after the source session became quiet")
	}

	output := terminal.String()
	lastTokenLine := strings.LastIndex(output, "session token input")
	if lastTokenLine < 0 {
		t.Fatalf("stable final footer missing:\n%q", output)
	}
	finalStart := strings.LastIndex(output[:lastTokenLine], "\r\x1b[2K")
	if finalStart < 0 {
		t.Fatalf("stable final footer lacks a cleared first line:\n%q", output)
	}
	stable := output[finalStart:]
	if strings.Count(stable, "\n") != 2 || !strings.HasSuffix(stable, "\n") {
		t.Errorf("stable footer is not exactly two newline-terminated lines:\n%q", stable)
	}
	if strings.Count(stable, "session token input") != 1 || strings.Contains(stable, "following session") || strings.Contains(stable, "\x1b[2A") {
		t.Errorf("stable footer retained or duplicated the live overlay:\n%q", stable)
	}
}

func TestAttachOverlaySpacerAndClearCountFollowActivityLine(t *testing.T) {
	conv := conversation.New("agent-overlay-lines", "Count overlay lines")
	full := &attachOverlay{
		out:     &bytes.Buffer{},
		theme:   presentation.NewForTest(presentation.ProfileTerminal, true, false),
		enabled: true,
	}
	full.draw(conv, time.Now(), 0, 120)
	if full.lines != 4 {
		t.Fatalf("full-motion overlay line count = %d, want 4", full.lines)
	}
	fullOutput := full.out.(*bytes.Buffer)
	if !strings.Contains(fullOutput.String(), "following session\x1b[0m\n\n") {
		t.Fatalf("full-motion overlay lacks activity spacer: %q", fullOutput.String())
	}
	full.clear()
	if !strings.Contains(fullOutput.String(), "\x1b[3A") {
		t.Fatalf("four-line overlay was not cleared as a four-line block: %q", fullOutput.String())
	}

	noneTheme, err := presentation.ResolveWithGlyphsAndMotion("terminal", "unicode", "none", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	noneOutput := &bytes.Buffer{}
	none := &attachOverlay{out: noneOutput, theme: noneTheme, enabled: true}
	none.draw(conv, time.Now(), 0, 120)
	if none.lines != 2 {
		t.Fatalf("motion-none overlay line count = %d, want 2", none.lines)
	}
	if strings.Contains(noneOutput.String(), "\n\n") {
		t.Fatalf("motion-none overlay retained an activity spacer: %q", noneOutput.String())
	}
	none.clear()
	if strings.Contains(noneOutput.String(), "\x1b[3A") || !strings.Contains(noneOutput.String(), "\x1b[1A") {
		t.Fatalf("motion-none overlay clear count did not stay at two lines: %q", noneOutput.String())
	}
}

func TestRunAttachTTYPrintsSingleConclusionWhenSessionCompletes(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-tty-complete"
	conv := conversation.New(sessionID, "TTY completing session")
	addAttachTailTurn(conv, 1)
	path := writeAttachTailConversation(t, conv)

	var active atomic.Bool
	active.Store(true)
	writerDone := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		conv.AddMessage("assistant", "TTY completing answer", map[string]any{"type": "final", "turns": 1, "capped": false})
		if err := publishAttachTailConversation(path, conv); err != nil {
			writerDone <- err
			return
		}
		time.Sleep(300 * time.Millisecond)
		active.Store(false)
		writerDone <- nil
	}()

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		probe:        func(string) (bool, error) { return active.Load(), nil },
		pollInterval: 3 * time.Millisecond,
		quietGrace:   8 * time.Millisecond,
		isTerminal:   func(io.Writer) bool { return true },
	})
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	if got := strings.Count(output, "Resume this session:"); got != 1 {
		t.Errorf("conclusion count = %d, want exactly 1:\n%s", got, output)
	}
	if got := strings.Count(output, "TTY completing answer"); got != 1 {
		t.Errorf("final answer count = %d, want exactly 1:\n%s", got, output)
	}
	if !strings.Contains(output, "\u2814\u2800\u2800") {
		t.Errorf("status line was not drawn while the session was active:\n%q", output)
	}
}

func TestRunAttachStatusHonorsMotionAndGlyphs(t *testing.T) {
	setupSessionArchiveCommandTest(t)

	tests := []struct {
		name        string
		motion      string
		glyphs      string
		wantFrame   string
		wantNoFrame string
	}{
		{
			name:        "full unicode animates",
			motion:      string(presentation.MotionFull),
			glyphs:      string(presentation.GlyphUnicode),
			wantFrame:   "\u2814\u2800\u2800",
			wantNoFrame: "o..",
		},
		{
			name:        "reduced shows static final frame",
			motion:      string(presentation.MotionReduced),
			glyphs:      string(presentation.GlyphUnicode),
			wantFrame:   "\u2800\u2821\u2800",
			wantNoFrame: "\u2814\u2800\u2800",
		},
		{
			name:        "full ascii uses ascii frames",
			motion:      string(presentation.MotionFull),
			glyphs:      string(presentation.GlyphASCII),
			wantFrame:   "o..",
			wantNoFrame: "\u2814\u2800\u2800",
		},
		{
			name:        "none omits the line",
			motion:      string(presentation.MotionNone),
			glyphs:      string(presentation.GlyphUnicode),
			wantFrame:   "",
			wantNoFrame: "\u2814\u2800\u2800",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("MACHTIANI_MOTION", tt.motion)
			t.Setenv("MACHTIANI_GLYPHS", tt.glyphs)
			theme, err := presentation.ResolveWithGlyphsAndMotion("terminal", tt.glyphs, tt.motion, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			sessionID := "agent-attach-motion-" + tt.name
			conv := conversation.New(sessionID, "Motion parity session")
			addAttachTailTurn(conv, 1)
			writeAttachTailConversation(t, conv)

			var active atomic.Bool
			active.Store(true)
			probed := make(chan struct{}, 1)
			writerDone := make(chan error, 1)
			go func() {
				<-probed
				time.Sleep(100 * time.Millisecond)
				active.Store(false)
				writerDone <- nil
			}()

			var stdout, stderr strings.Builder
			code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
				readFile: os.ReadFile,
				probe: func(string) (bool, error) {
					select {
					case probed <- struct{}{}:
					default:
					}
					return active.Load(), nil
				},
				pollInterval: 5 * time.Millisecond,
				quietGrace:   20 * time.Millisecond,
				theme:        theme,
				isTerminal:   func(io.Writer) bool { return true },
			})
			if err := <-writerDone; err != nil {
				t.Fatal(err)
			}
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
			}
			output := stdout.String()
			if tt.wantFrame != "" && !strings.Contains(output, tt.wantFrame) {
				t.Errorf("stdout missing frame %q in:\n%q", tt.wantFrame, output)
			}
			if strings.Contains(output, tt.wantNoFrame) {
				t.Errorf("stdout contains unexpected frame %q in:\n%q", tt.wantNoFrame, output)
			}
			if tt.motion == string(presentation.MotionNone) && !strings.Contains(output, "session token input") {
				t.Errorf("motion-none output lost the final footer:\n%q", output)
			}
		})
	}
}

func TestRunAttachTTYFooterUsesPersistedFooterState(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-persisted-footer"
	conv := conversation.New(sessionID, "Persisted footer")
	addAttachTailTurn(conv, 1)
	conv.TurnsCompleted = 3
	conv.RuntimeStats = conversation.NewRuntimeStatsState(65_000, 1_234, 56_789, 1_000)
	conv.RuntimeStats.PlannerActivePromptTokens = 25_000
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	conv.Footer = &conversation.FooterState{
		CWD:            filepath.Join(home, "project"),
		MaxInputTokens: 120_000,
		Mode:           "code-forge",
		Models: []conversation.FooterModelState{
			{Role: "planner", Label: "deepseek:deepseek-v4-flash"},
			{Role: "shell", Label: "deepseek:deepseek-v4-flash"},
		},
	}
	writeAttachTailConversation(t, conv)

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		probe:        func(string) (bool, error) { return false, nil },
		pollInterval: time.Millisecond,
		quietGrace:   3 * time.Millisecond,
		theme:        presentation.NewForTest(presentation.ProfileTerminal, true, false),
		isTerminal:   func(io.Writer) bool { return true },
		footerWidth:  func(io.Writer) int { return 240 },
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"\x1b[1;36m1:05\x1b[0m", "~/project", "context remain \x1b[33m79%\x1b[0m", "session token input \x1b[33m58,023\x1b[0m (cache \x1b[33m2%\x1b[0m)  output \x1b[33m1,000\x1b[0m", "code-forge", "\x1b[1;36mturn 4\x1b[0m", "session \x1b[33m" + sessionID + "\x1b[0m", "planner \x1b[33mdeepseek:deepseek-v4-flash\x1b[0m", "shell \x1b[33mdeepseek:deepseek-v4-flash\x1b[0m",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("footer missing persisted value %q in:\n%q", want, output)
		}
	}
}

func TestRunAttachTTYFooterToleratesConversationWithoutFooterState(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-old-footer"
	conv := conversation.New(sessionID, "Old footer")
	addAttachTailTurn(conv, 1)
	writeAttachTailConversation(t, conv)

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		probe:        func(string) (bool, error) { return false, nil },
		pollInterval: time.Millisecond,
		quietGrace:   3 * time.Millisecond,
		theme:        presentation.NewForTest(presentation.ProfileTerminal, true, false),
		isTerminal:   func(io.Writer) bool { return true },
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	for _, unavailable := range []string{"context remain", "planner ", "shell ", "~/"} {
		if strings.Contains(output, unavailable) {
			t.Errorf("old footer invented unavailable value %q in:\n%q", unavailable, output)
		}
	}
}

func TestRunAttachTTYStatusUsesActivitySemanticColors(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-status-colors"
	conv := conversation.New(sessionID, "Status colors")
	addAttachTailTurn(conv, 1)
	path := writeAttachTailConversation(t, conv)

	var active atomic.Bool
	active.Store(true)
	writerDone := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		active.Store(false)
		writerDone <- publishAttachTailConversation(path, conv)
	}()

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		probe:        func(string) (bool, error) { return active.Load(), nil },
		pollInterval: 3 * time.Millisecond,
		quietGrace:   8 * time.Millisecond,
		theme:        presentation.NewForTest(presentation.ProfileTerminal, true, false),
		isTerminal:   func(io.Writer) bool { return true },
	})
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "\x1b[1;36m\u2814\u2800\u2800\x1b[0m") {
		t.Errorf("spinner does not use bold Truth styling:\n%q", output)
	}
	if !strings.Contains(output, "\x1b[35mfollowing session\x1b[0m") {
		t.Errorf("following label does not use Beauty styling:\n%q", output)
	}
	if strings.Contains(output, "following session "+sessionID) {
		t.Errorf("following label retained the session id:\n%q", output)
	}
}

func TestRunAttachStatusLineOmittedWhenNotTerminal(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-plain"
	conv := conversation.New(sessionID, "Plain non-TTY output")
	addAttachTailTurn(conv, 1)
	path := writeAttachTailConversation(t, conv)

	var active atomic.Bool
	active.Store(true)
	writerDone := make(chan error, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		addAttachTailTurn(conv, 2)
		if err := publishAttachTailConversation(path, conv); err != nil {
			writerDone <- err
			return
		}
		time.Sleep(10 * time.Millisecond)
		active.Store(false)
		writerDone <- nil
	}()

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		probe:        func(string) (bool, error) { return active.Load(), nil },
		pollInterval: 3 * time.Millisecond,
		quietGrace:   8 * time.Millisecond,
	})
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	if strings.Contains(output, "\x1b") {
		t.Errorf("non-TTY attach emitted ANSI escape bytes:\n%q", output)
	}
	if strings.Contains(output, "following session") {
		t.Errorf("non-TTY attach drew a status line:\n%q", output)
	}
	if !strings.Contains(output, "Live question 2") {
		t.Errorf("non-TTY attach lost delta content:\n%s", output)
	}
	if !strings.HasSuffix(output, "\n") {
		t.Errorf("non-TTY attach output does not end with a newline:\n%q", output)
	}
}

func addAttachTailTurn(conv *conversation.Conversation, turn int) {
	conv.AddMessage("assistant", fmt.Sprintf("Live question %d", turn), map[string]any{
		"type":     "work_request",
		"turn":     turn,
		"decision": fmt.Sprintf("Live decision %d", turn),
	})
	conv.AddMessage("assistant", fmt.Sprintf("Live answer %d", turn), map[string]any{
		"type": "work_result",
		"turn": turn,
	})
}

func writeAttachTailConversation(t *testing.T, conv *conversation.Conversation) string {
	t.Helper()
	path, err := artifacts.SessionConversationFile(conv.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publishAttachTailConversation(path, conv); err != nil {
		t.Fatal(err)
	}
	return path
}

func publishAttachTailConversation(path string, conv *conversation.Conversation) error {
	data, err := conv.Marshal()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".conversation-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func writeAttachActions(t *testing.T, sessionID string, turn int, records ...shellaction.Record) string {
	t.Helper()
	path, err := artifacts.ShellAgentTrajectoryPath(sessionID, turn)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(filepath.Dir(path), "actions.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err := appendAttachAction(sessionID, turn, record); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func appendAttachAction(sessionID string, turn int, record shellaction.Record) error {
	trajectoryPath, err := artifacts.ShellAgentTrajectoryPath(sessionID, turn)
	if err != nil {
		return err
	}
	path := filepath.Join(filepath.Dir(trajectoryPath), "actions.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := shellaction.EncodeRecord(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}
