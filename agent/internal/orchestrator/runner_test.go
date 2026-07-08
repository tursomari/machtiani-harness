package orchestrator

import (
	"context"
	"strings"
	"testing"
)

func TestParseOrchestratorResponse_MultilineMessage(t *testing.T) {
	response := "ACTION: CONTINUE\nMESSAGE: Phase 2 — The Reset and Re-Execution. You MUST now:\n\n" +
		"a) Re-read /app/instruction.md.\n" +
		"b) Reset the git project completely.\n" +
		"c) Create /app/implementation-plan.md and commit it."

	action, message, err := parseOrchestratorResponse(response)
	if err != nil {
		t.Fatalf("parseOrchestratorResponse returned error: %v", err)
	}
	if action != "CONTINUE" {
		t.Fatalf("action = %q, want CONTINUE", action)
	}
	for _, want := range []string{
		"Phase 2 — The Reset and Re-Execution. You MUST now:",
		"a) Re-read /app/instruction.md.",
		"b) Reset the git project completely.",
		"c) Create /app/implementation-plan.md and commit it.",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
}

// TestRunLoop_StatefulSignature is a compile-time type check that verifies the
// RunLoop function signature compiles and can be called with the expected
// argument types. We skip actual execution because RunLoop spawns real
// subprocesses and makes LLM calls.
func TestRunLoop_StatefulSignature(t *testing.T) {
	t.Skip("compile-time type check only — RunLoop calls exec and LLM, not suitable for unit testing")

	ctx := context.Background()
	_, _ = RunLoop(ctx, "test-meta-session", "test-mct-session", "/tmp/instruction.md", "code", "gpt-4", "", "", false, false)
}

// TestInvokeMCTAgent_Signature is a compile-time type check that verifies the
// invokeMCTAgent function signature compiles and can be called with the new
// metaSessionID parameter. We skip actual execution because invokeMCTAgent
// spawns a real subprocess.
func TestInvokeMCTAgent_Signature(t *testing.T) {
	t.Skip("compile-time type check only — invokeMCTAgent calls exec, not suitable for unit testing")

	ctx := context.Background()
	_, _ = invokeMCTAgent(ctx, "test-meta-session", "/tmp/traj", "test-mct-session", "--mode", "code")
}
