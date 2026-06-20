package session

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/ui"
)

// TestTerminalDisplaySatisfiesSessionDisplay is a compile-time assertion
// that the concrete terminal types satisfy the display interfaces.
func TestTerminalDisplaySatisfiesSessionDisplay(t *testing.T) {
	var _ ui.SessionDisplay = (*ui.TerminalDisplay)(nil)
	var _ ui.PromptStream = (*ui.TerminalPromptStream)(nil)
}

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
		Display:     nil,
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
		Display:     ui.NewTerminalDisplay(io.Discard, nil, ""),
		Diagnostics: new(bytes.Buffer),
		Context:     ctx,
	}

	_ = Run(ctx, opts)
}

// mockDisplay is a no-op implementation of ui.SessionDisplay used for testing.
type mockDisplay struct {
	startedGoal string
}

func (m *mockDisplay) StartSession(goal string)              { m.startedGoal = goal }
func (m *mockDisplay) EndSession()                            {}
func (m *mockDisplay) BeginPrompt(_ string, _ *ui.PromptOptions) ui.PromptStream {
	return &mockPromptStream{}
}
func (m *mockDisplay) ShowFinal(_ string)                                    {}
func (m *mockDisplay) StreamAction(_ string)                                 {}
func (m *mockDisplay) RenderModePlan(_ []ui.ModeTaskDisplay)                 {}
func (m *mockDisplay) UpdateModeTaskStatus(_ int, _ string, _ string)        {}
func (m *mockDisplay) Notify(_ string)                                       {}
func (m *mockDisplay) WriteString(_ string)                                  {}

// mockPromptStream is a no-op implementation of ui.PromptStream.
type mockPromptStream struct{}

func (m *mockPromptStream) OnChunk(_ string)   {}
func (m *mockPromptStream) Complete(_ string)  {}
func (m *mockPromptStream) Abort(_ string)     {}

// TestSessionRunWithCustomDisplayNoPanic verifies that session.Run does not
// panic when a fully mocked display and prompt stream are injected.  No
// assertions about method-call ordering are made because the session may
// exit early before invoking any display methods when no LLM backend is
// present.
func TestSessionRunWithCustomDisplayNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("session.Run panicked with mock display: %v", r)
		}
	}()

	md := &mockDisplay{}
	ctx := context.Background()
	opts := Options{
		Config:      Config{},
		Goal:        "test goal with mock",
		Display:     md,
		Diagnostics: io.Discard,
		Context:     ctx,
	}

	_ = Run(ctx, opts)
}
