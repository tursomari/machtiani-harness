package shellsubprocess

import (
	"errors"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/tests/utils"
)

func TestShellPropagatesCustomExitCode(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	result, err := runner.RunCommand("exit 7")
	if err != nil {
		t.Fatalf("run command: %v", err)
	}

	utils.RequireExitCode(t, result, 7)
}

func TestShellTimeoutSetsSentinelExitCode(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	result, err := runner.RunCommand("sleep 1", utils.WithTimeout(200*time.Millisecond))
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}

	var retryErr utils.RetryableError
	if !errors.As(err, &retryErr) || !retryErr.Retryable() {
		t.Fatalf("expected retryable timeout error, got %v", err)
	}

	utils.RequireExitCode(t, result, -1)
}
