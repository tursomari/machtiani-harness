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
	_, _ = RunLoop(ctx, "test-session", "/tmp/instruction.md", "code", "gpt-4", "", "", false)
}
