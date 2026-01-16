package session

import (
	"errors"
	"strings"
	"testing"
)

func TestAppendPatchPlanToFinalAnswer_NoPlan(t *testing.T) {
	origLoad := loadPatchPlanFn
	origFormat := formatPatchPlanForDisplayFn
	t.Cleanup(func() {
		loadPatchPlanFn = origLoad
		formatPatchPlanForDisplayFn = origFormat
	})

	loadPatchPlanFn = func(sessionID string) (*PatchPlan, error) {
		return nil, errors.New("missing")
	}
	formatPatchPlanForDisplayFn = func(plan *PatchPlan) string {
		t.Fatalf("formatPatchPlanForDisplayFn should not be called when plan load fails")
		return ""
	}

	answer := "hello"
	got := appendPatchPlanToFinalAnswer(answer, "session")
	if got != answer {
		t.Fatalf("expected answer unchanged; got %q", got)
	}
}

func TestAppendPatchPlanToFinalAnswer_WithPlan_AppendsRendered(t *testing.T) {
	origLoad := loadPatchPlanFn
	origFormat := formatPatchPlanForDisplayFn
	t.Cleanup(func() {
		loadPatchPlanFn = origLoad
		formatPatchPlanForDisplayFn = origFormat
	})

	loadPatchPlanFn = func(sessionID string) (*PatchPlan, error) {
		return &PatchPlan{Goal: "Patch"}, nil
	}
	formatPatchPlanForDisplayFn = func(plan *PatchPlan) string {
		if plan == nil {
			t.Fatalf("expected non-nil plan")
		}
		return "[x] step 1"
	}

	answer := "final"
	got := appendPatchPlanToFinalAnswer(answer, "session")
	expected := "final\n\n[x] step 1"
	if got != expected {
		t.Fatalf("expected %q; got %q", expected, got)
	}
}

func TestAppendPatchPlanToFinalAnswer_EmptyRendered_NoAppend(t *testing.T) {
	origLoad := loadPatchPlanFn
	origFormat := formatPatchPlanForDisplayFn
	t.Cleanup(func() {
		loadPatchPlanFn = origLoad
		formatPatchPlanForDisplayFn = origFormat
	})

	loadPatchPlanFn = func(sessionID string) (*PatchPlan, error) {
		return &PatchPlan{Goal: "Patch"}, nil
	}
	formatPatchPlanForDisplayFn = func(plan *PatchPlan) string {
		return ""
	}

	answer := "ok"
	got := appendPatchPlanToFinalAnswer(answer, "session")
	if got != answer {
		t.Fatalf("expected answer unchanged; got %q", got)
	}
}

func TestAppendPatchPlanToFinalAnswer_NilPlan_NoAppend(t *testing.T) {
	origLoad := loadPatchPlanFn
	origFormat := formatPatchPlanForDisplayFn
	t.Cleanup(func() {
		loadPatchPlanFn = origLoad
		formatPatchPlanForDisplayFn = origFormat
	})

	loadPatchPlanFn = func(sessionID string) (*PatchPlan, error) {
		return nil, nil
	}
	formatPatchPlanForDisplayFn = func(plan *PatchPlan) string {
		t.Fatalf("formatPatchPlanForDisplayFn should not be called for nil plan")
		return ""
	}

	answer := "yo"
	got := appendPatchPlanToFinalAnswer(answer, "session")
	if got != answer {
		t.Fatalf("expected answer unchanged; got %q", got)
	}
}

func TestAppendPatchPlanToFinalAnswer_RenderedHasContent_PreservesOriginalPrefix(t *testing.T) {
	origLoad := loadPatchPlanFn
	origFormat := formatPatchPlanForDisplayFn
	t.Cleanup(func() {
		loadPatchPlanFn = origLoad
		formatPatchPlanForDisplayFn = origFormat
	})

	loadPatchPlanFn = func(sessionID string) (*PatchPlan, error) {
		return &PatchPlan{Goal: "Patch"}, nil
	}
	formatPatchPlanForDisplayFn = func(plan *PatchPlan) string {
		return "plan" // minimal
	}

	answer := "answer text"
	got := appendPatchPlanToFinalAnswer(answer, "session")
	if !strings.HasPrefix(got, answer+"\n\n") {
		t.Fatalf("expected appended output to keep original answer prefix; got %q", got)
	}
}
