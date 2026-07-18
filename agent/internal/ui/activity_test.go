package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/presentation"
)

func TestActivityTrackerEmitsCompleteSnapshots(t *testing.T) {
	bus := NewEventBus(8)
	defer bus.Close()
	sub := bus.Subscribe()
	tracker := NewActivityTracker(bus)

	stopPlanner := tracker.Begin("planner", ActivityPlanning)
	first := (<-sub).(ActivitySnapshotEvent)
	if len(first.Activities) != 1 || first.Activities[0].Kind != ActivityPlanning {
		t.Fatalf("first snapshot = %#v", first)
	}
	stopShell := tracker.Begin("shell", ActivityShell)
	second := (<-sub).(ActivitySnapshotEvent)
	if len(second.Activities) != 2 {
		t.Fatalf("second snapshot = %#v", second)
	}
	stopPlanner()
	third := (<-sub).(ActivitySnapshotEvent)
	if len(third.Activities) != 1 || third.Activities[0].ID != "shell" {
		t.Fatalf("third snapshot = %#v", third)
	}
	stopShell()
	last := (<-sub).(ActivitySnapshotEvent)
	if len(last.Activities) != 0 {
		t.Fatalf("last snapshot = %#v", last)
	}
}

func TestSelectActivityPresentationUsesOverdriveForConcurrentWork(t *testing.T) {
	got := selectActivityPresentation([]Activity{
		{ID: "shell-a", Kind: ActivityShell},
		{ID: "shell-b", Kind: ActivityWaiting},
	})
	if got.Pattern != activityPatternOverdrive || got.Label != "orchestrating" {
		t.Fatalf("presentation = %#v", got)
	}
}

func TestRenderActivityLineHonorsMotionAndGlyphModes(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	full, err := presentation.ResolveWithGlyphsAndMotion("none", "unicode", "full", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	reduced, err := presentation.ResolveWithGlyphsAndMotion("none", "unicode", "reduced", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	ascii, err := presentation.ResolveWithGlyphsAndMotion("none", "ascii", "full", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	activity := activityPresentation{Pattern: activityPatternAgentLoop, Label: "reasoning"}

	if a, b := renderActivityLine(activity, full, 0), renderActivityLine(activity, full, 125*time.Millisecond); a == b {
		t.Fatalf("full motion did not advance: %q", a)
	}
	if a, b := renderActivityLine(activity, reduced, 0), renderActivityLine(activity, reduced, time.Second); a != b {
		t.Fatalf("reduced motion changed: %q then %q", a, b)
	}
	if got := renderActivityLine(activity, ascii, 125*time.Millisecond); strings.ContainsAny(got, "⠔⠡⠊") || !strings.Contains(got, ".o.") {
		t.Fatalf("ASCII activity line = %q", got)
	}
}

func TestFormatterActivityOccupiesThirdFooterRow(t *testing.T) {
	f, bus, _ := newTestFormatter()
	defer bus.Close()
	f.timerEnabled = true
	f.height = minFooterHeight + 2
	f.width = 120
	f.activities = []Activity{{ID: "planner", Kind: ActivityPlanning}}

	if got := f.footerLineCountLocked(); got != 3 {
		t.Fatalf("footer line count = %d, want 3", got)
	}
	lines := f.formatFooterLinesLocked(125*time.Millisecond, 3)
	if len(lines) != 3 || !strings.Contains(lines[0], "reasoning") {
		t.Fatalf("footer lines = %#v", lines)
	}
	if !strings.Contains(lines[1], "session token input") {
		t.Fatalf("token line moved incorrectly: %#v", lines)
	}
}

func TestFormatterActivityHidesHardwareCursorUntilActivityEnds(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()
	f.timerEnabled = true
	f.timerStart = time.Now()
	f.height = minFooterHeight + 2
	f.width = 120

	f.handleActivitySnapshot(ActivitySnapshotEvent{Activities: []Activity{{ID: "planner", Kind: ActivityPlanning}}})
	f.handleActivitySnapshot(ActivitySnapshotEvent{Activities: []Activity{{ID: "planner", Kind: ActivityPlanning}}})
	if got := strings.Count(buf.String(), ansiHideCursor); got != 1 {
		t.Fatalf("hide cursor count = %d, want 1: %q", got, buf.String())
	}
	if strings.Contains(buf.String(), ansiShowCursor) {
		t.Fatalf("cursor restored while activity remained visible: %q", buf.String())
	}

	f.handleActivitySnapshot(ActivitySnapshotEvent{})
	if got := strings.Count(buf.String(), ansiShowCursor); got != 1 {
		t.Fatalf("show cursor count = %d, want 1: %q", got, buf.String())
	}
}

func TestFormatterSessionEndRestoresHardwareCursor(t *testing.T) {
	f, bus, buf := newTestFormatter()
	defer bus.Close()
	f.timerEnabled = true
	f.timerStart = time.Now()
	f.height = minFooterHeight + 2
	f.width = 120
	f.started = true
	f.activities = []Activity{{ID: "shell", Kind: ActivityShell}}
	f.activityCursorHidden = true

	f.handleSessionEnded(SessionEndedEvent{})
	if !strings.Contains(buf.String(), ansiShowCursor) {
		t.Fatalf("session end did not restore hardware cursor: %q", buf.String())
	}
}
