package orchestrator

import (
	"context"
	"testing"
)

// TestRunLoop_Classification is a compile-time type check that verifies the
// RunLoop function signature compiles and can be called with the expected
// argument types. We skip actual execution because RunLoop spawns real
// subprocesses and makes LLM calls.
func TestRunLoop_Classification(t *testing.T) {
	t.Skip("compile-time type check only — RunLoop calls exec and LLM, not suitable for unit testing")

	ctx := context.Background()
	_, _ = RunLoop(ctx, "test-session", "/tmp/instruction.md", "code", "gpt-4", "", "", false)
}

// TestInvokeMCTAgent_ArgsConstruction is a compile-time type check that
// verifies the invokeMCTAgentRun function signature compiles with the expected
// argument types. We skip actual execution because invokeMCTAgentRun calls
// exec.CommandContext, which would try to execute mct-agent on the host.
func TestInvokeMCTAgent_ArgsConstruction(t *testing.T) {
	t.Skip("compile-time type check only — invokeMCTAgentRun calls exec, not suitable for unit testing")

	ctx := context.Background()
	_, _ = invokeMCTAgentRun(ctx, "test-session", "/tmp/instruction.md", "code", "gpt-4", "haiku", "test-tag", true)
}
