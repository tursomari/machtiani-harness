package shellintegration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/tests/utils"
)

func TestMockScriptNonZeroExitIsReported(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	result, err := runner.RunCommand("echo 'failure' >&2; exit 3")
	if err != nil {
		t.Fatalf("run command: %v", err)
	}

	utils.RequireExitCode(t, result, 3)
	utils.RequireStderrContains(t, result, "failure")
}

func TestRetryShellCallTimesOutOnSlowMock(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	execCtx := runner.ExecutionContext(utils.WithTimeout(200 * time.Millisecond))

	slowSpec := utils.MockCommandSpec{Delay: time.Second, ExitCode: 0}

	ctx, cancel := context.WithTimeout(runner.Context(), 400*time.Millisecond)
	defer cancel()

	_, err := utils.RetryShellCall(ctx, slowSpec.ShellCall(execCtx), utils.RetryOptions{Attempts: 2, InitialDelay: 50 * time.Millisecond})
	if err == nil {
		t.Fatalf("expected retry error due to timeout")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}
