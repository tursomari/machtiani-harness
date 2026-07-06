package orchestrator

import (
	"context"
	"testing"
)

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
