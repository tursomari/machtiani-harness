package ui

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tursomari/machtiani/agent/internal/presentation"
)

type ActivityTracker struct {
	mu     sync.Mutex
	bus    *EventBus
	active map[string]ActivityKind
}

func NewActivityTracker(bus *EventBus) *ActivityTracker {
	return &ActivityTracker{bus: bus, active: make(map[string]ActivityKind)}
}

// Begin starts or replaces one producer-owned activity and returns an
// idempotent completion function.
func (t *ActivityTracker) Begin(id string, kind ActivityKind) func() {
	if t == nil {
		return func() {}
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return func() {}
	}
	t.mu.Lock()
	t.active[id] = kind
	t.emitLocked()
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.active, id)
			t.emitLocked()
			t.mu.Unlock()
		})
	}
}

func (t *ActivityTracker) Update(id string, kind ActivityKind) {
	if t == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	t.mu.Lock()
	if _, exists := t.active[id]; exists {
		t.active[id] = kind
		t.emitLocked()
	}
	t.mu.Unlock()
}

func (t *ActivityTracker) emitLocked() {
	if t.bus == nil {
		return
	}
	activities := make([]Activity, 0, len(t.active))
	for id, kind := range t.active {
		activities = append(activities, Activity{ID: id, Kind: kind})
	}
	sort.Slice(activities, func(i, j int) bool { return activities[i].ID < activities[j].ID })
	t.bus.Emit(ActivitySnapshotEvent{Activities: activities})
}

type activityPattern string

const (
	activityPatternDotTrace    activityPattern = "dot-trace"
	activityPatternSignalScan  activityPattern = "signal-scan"
	activityPatternContextLoad activityPattern = "context-load"
	activityPatternAgentLoop   activityPattern = "agent-loop"
	activityPatternDotBuild    activityPattern = "dot-build"
	activityPatternGitSync     activityPattern = "git-sync"
	activityPatternOverdrive   activityPattern = "overdrive"
)

type activityPresentation struct {
	Pattern activityPattern
	Label   string
}

func selectActivityPresentation(activities []Activity) activityPresentation {
	shellWorkers := 0
	for _, activity := range activities {
		if activity.Kind == ActivityShell || activity.Kind == ActivityWaiting {
			shellWorkers++
		}
	}
	if shellWorkers > 1 {
		return activityPresentation{Pattern: activityPatternOverdrive, Label: "orchestrating"}
	}
	if len(activities) == 0 {
		return activityPresentation{}
	}
	priority := []struct {
		kind         ActivityKind
		presentation activityPresentation
	}{
		{ActivityFinalizing, activityPresentation{Pattern: activityPatternDotBuild, Label: "composing answer"}},
		{ActivityShell, activityPresentation{Pattern: activityPatternSignalScan, Label: "working"}},
		{ActivityWaiting, activityPresentation{Pattern: activityPatternDotTrace, Label: "preparing worker"}},
		{ActivityContext, activityPresentation{Pattern: activityPatternContextLoad, Label: "gathering context"}},
		{ActivityPlanning, activityPresentation{Pattern: activityPatternAgentLoop, Label: "reasoning"}},
		{ActivitySync, activityPresentation{Pattern: activityPatternGitSync, Label: "synchronizing"}},
	}
	for _, candidate := range priority {
		for _, activity := range activities {
			if activity.Kind == candidate.kind {
				return candidate.presentation
			}
		}
	}
	return activityPresentation{Pattern: activityPatternSignalScan, Label: "working"}
}

var unicodeActivityFrames = map[activityPattern][]string{
	activityPatternDotTrace:    {"⠄⠀⠀", "⠐⠀⠀", "⠀⠁⠀", "⠀⠠⠀", "⠀⠀⠂", "⠀⠀⠈"},
	activityPatternSignalScan:  {"⠔⠀⠀", "⠀⠡⠀", "⠀⠀⠊", "⠀⠡⠀"},
	activityPatternContextLoad: {"⠄⠀⠀", "⠔⠁⠀", "⠔⠡⠂", "⠔⠡⠊"},
	activityPatternAgentLoop:   {"⠔⠀⠀", "⠀⠡⠀", "⠀⠀⠊", "⠀⠡⠀"},
	activityPatternDotBuild:    {"⠄⠀⠀", "⠔⠀⠀", "⠔⠁⠀", "⠔⠡⠀", "⠔⠡⠂", "⠔⠡⠊"},
	activityPatternGitSync:     {"⠔⠀⠀", "⠀⠡⠀", "⠀⠀⠊", "⠀⠡⠀"},
	activityPatternOverdrive:   {"⠔⠀⠀", "⠔⠡⠀", "⠔⠡⠊", "⠀⠡⠊", "⠀⠀⠊", "⠀⠡⠀"},
}

var asciiActivityFrames = map[activityPattern][]string{
	activityPatternDotTrace:    {"o..", ".o.", "..o"},
	activityPatternSignalScan:  {"o..", ".o.", "..o", ".o."},
	activityPatternContextLoad: {"o..", "oo.", "ooo"},
	activityPatternAgentLoop:   {"o..", ".o.", "..o", ".o."},
	activityPatternDotBuild:    {"o..", "oo.", "ooo"},
	activityPatternGitSync:     {">..", ".>.", "..>", ".<.", "<.."},
	activityPatternOverdrive:   {"o..", "oo.", "ooo", ".oo", "..o", ".o."},
}

func renderActivityLine(activity activityPresentation, theme presentation.Theme, elapsed time.Duration) string {
	if activity.Pattern == "" || theme.MotionMode() == presentation.MotionNone {
		return ""
	}
	frames := unicodeActivityFrames[activity.Pattern]
	if theme.GlyphMode() == presentation.GlyphASCII {
		frames = asciiActivityFrames[activity.Pattern]
	}
	if len(frames) == 0 {
		return activity.Label
	}
	index := 0
	if theme.MotionMode() == presentation.MotionFull {
		index = int(elapsed/(125*time.Millisecond)) % len(frames)
	} else {
		index = len(frames) - 1
	}
	return frames[index] + "  " + activity.Label
}

// AttachSpinnerFrame returns the animation frame for the attach status line.
// Full motion advances with elapsed time; reduced motion returns the static
// final frame; none and unknown themes return an empty frame.
func AttachSpinnerFrame(theme presentation.Theme, elapsed time.Duration) string {
	if theme.MotionMode() == presentation.MotionNone {
		return ""
	}
	const pattern = activityPatternSignalScan
	frames := unicodeActivityFrames[pattern]
	if theme.GlyphMode() == presentation.GlyphASCII {
		frames = asciiActivityFrames[pattern]
	}
	if len(frames) == 0 {
		return ""
	}
	index := len(frames) - 1
	if theme.MotionMode() == presentation.MotionFull {
		index = int(elapsed/(125*time.Millisecond)) % len(frames)
	}
	return frames[index]
}

// RenderAttachStatusLine renders attach's transient following indicator using
// the same semantic roles as the live activity line: the spinner is Truth and
// its label is Beauty. Keeping this here makes attach share the presentation
// palette selected for run.
func RenderAttachStatusLine(theme presentation.Theme, elapsed time.Duration, label string) string {
	frame := AttachSpinnerFrame(theme, elapsed)
	if frame == "" {
		return ""
	}
	return theme.RenderLine(presentation.StyledLine{
		presentation.Bold(presentation.RoleTruth, frame),
		presentation.Text("  "),
		presentation.RoleText(presentation.RoleBeauty, label),
	})
}
