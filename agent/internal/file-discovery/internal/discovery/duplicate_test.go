package discovery

import (
	"strings"
	"testing"
)

func TestShouldNudgeDuplicate(t *testing.T) {
	// identical pattern and last empty -> nudge
	last := lastRGState{kind: "files_pattern", pattern: "foo", outCount: 0}
	cur := rgCommand{kind: "files_pattern", pattern: "foo"}
	if !shouldNudgeDuplicate(last, cur) {
		t.Fatalf("expected nudge for identical pattern with last empty")
	}

	// identical pattern but last non-empty -> no nudge
	last = lastRGState{kind: "files_pattern", pattern: "foo", outCount: 3}
	if shouldNudgeDuplicate(last, cur) {
		t.Fatalf("did not expect nudge when last had results")
	}

	// different pattern with last empty -> no nudge
	last = lastRGState{kind: "files_pattern", pattern: "foo", outCount: 0}
	cur = rgCommand{kind: "files_pattern", pattern: "bar"}
	if shouldNudgeDuplicate(last, cur) {
		t.Fatalf("did not expect nudge for different pattern")
	}

	// unknown last out count (-1) -> no nudge even if identical
	last = lastRGState{kind: "files_pattern", pattern: "foo", outCount: -1}
	cur = rgCommand{kind: "files_pattern", pattern: "foo"}
	if shouldNudgeDuplicate(last, cur) {
		t.Fatalf("did not expect nudge when last outCount unknown")
	}
}

func TestFailedPatternsNudgeAndMessage(t *testing.T) {
	var fp failedRG
	// No entries yet -> no nudge
	if fp.has("foo") {
		t.Fatalf("unexpected has for empty tracker")
	}
	// Add a couple of failed patterns
	fp.add("foo")
	fp.add("bar")
	// Re-adding increments count but not order
	fp.add("foo")
	if !fp.has("foo") || !fp.has("bar") {
		t.Fatalf("expected tracker to contain foo and bar")
	}
	if len(fp.order) != 2 || fp.order[0] != "foo" || fp.order[1] != "bar" {
		t.Fatalf("unexpected order: %v", fp.order)
	}
	// Build message ensures current first and lists others once
	msg := buildDuplicateNudgeMessage("bar", fp.order)
	if !strings.Contains(msg, `"bar"`) || !strings.Contains(msg, `"foo"`) {
		t.Fatalf("nudge message missing expected patterns: %q", msg)
	}
}
