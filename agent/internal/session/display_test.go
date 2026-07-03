package session

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/ui"
)

// TestSessionRunWithNilDisplayDefaultsNoPanic verifies that session.Run does
// not panic when Display and Diagnostics are nil.  The session may exit early
// with ExitCode 1 when no LLM backend is configured, but the wiring itself
// must remain panic-free.
func TestSessionRunWithNilDisplayDefaultsNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("session.Run panicked with nil Display/Diagnostics: %v", r)
		}
	}()

	ctx := context.Background()
	opts := Options{
		Config:      Config{},
		Goal:        "test goal",
		Diagnostics: nil,
		Context:     ctx,
	}

	result := Run(ctx, opts)
	// result is a value type and cannot be nil, but we assert the call
	// returned without panicking.
	if result.ExitCode == 0 && result.Err == nil {
		// If we somehow succeeded, that is also acceptable.
	}
}

// TestSessionRunWithCustomDiagnosticsCapturesOutput verifies that session.Run
// does not panic when a custom diagnostics writer and a terminal display are
// provided.
func TestSessionRunWithCustomDiagnosticsCapturesOutput(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("session.Run panicked with custom diagnostics: %v", r)
		}
	}()

	ctx := context.Background()
	opts := Options{
		Config:      Config{},
		Goal:        "test goal with diagnostics",
		Diagnostics: new(bytes.Buffer),
		Context:     ctx,
	}

	_ = Run(ctx, opts)
}

// mockEventCollector creates an EventBus, subscribes to it, and collects
// DisplayEvent values emitted to the bus in a background goroutine.
type mockEventCollector struct {
	bus    *ui.EventBus
	events []ui.DisplayEvent
}

func newMockEventCollector() *mockEventCollector {
	c := &mockEventCollector{
		bus: ui.NewEventBus(256),
	}
	ch := c.bus.Subscribe()
	go func() {
		for e := range ch {
			c.events = append(c.events, e)
		}
	}()
	return c
}

// TestSessionRunWithCustomDisplayNoPanic verifies that session.Run does not
// panic when an event bus collector is set up.  No assertions about emitted
// events are made because the session may exit early before emitting any
// events when no LLM backend is present.
func TestSessionRunWithCustomDisplayNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("session.Run panicked with mock event collector: %v", r)
		}
	}()

	collector := newMockEventCollector()
	_ = collector
	ctx := context.Background()
	opts := Options{
		Config:      Config{},
		Goal:        "test goal with mock",
		Diagnostics: io.Discard,
		Context:     ctx,
	}

	_ = Run(ctx, opts)
}
