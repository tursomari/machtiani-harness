package runlive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/tests/utils"
)

func TestSyncCommandPropagatesNonZeroExit(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	type errorFixture struct {
		Command  string `json:"command"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exitCode"`
	}

	var fixture errorFixture
	utils.LoadJSONFixture(t, runner.FixturesDir(), "error-output.json", &fixture)

	result, err := runner.RunCommand(fixture.Command)
	if err != nil {
		t.Fatalf("run command: %v", err)
	}

	utils.RequireExitCode(t, result, fixture.ExitCode)
	utils.RequireStderrContains(t, result, "No such file or directory")
}

func TestRetryShellCallRecoversAfterTransientFailure(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	marker := filepath.Join(t.TempDir(), "retry-marker")
	command := fmt.Sprintf(`
set -euo pipefail
if [ ! -f %[1]q ]; then
	echo "first attempt" >&2
	touch %[1]q
	exit 1
fi
echo "recovered"
`, marker)

	execCtx := runner.ExecutionContext()

	result, err := utils.RetryShellCall(runner.Context(), func(ctx context.Context) (utils.ShellResult, error) {
		return utils.RunShellCommand(ctx, execCtx, command)
	}, utils.RetryOptions{Attempts: 3, InitialDelay: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("retry shell call: %v", err)
	}

	utils.RequireExitCode(t, result, 0)
	utils.RequireStdoutContains(t, result, "recovered")
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("expected marker file to exist: %v", statErr)
	}
}
