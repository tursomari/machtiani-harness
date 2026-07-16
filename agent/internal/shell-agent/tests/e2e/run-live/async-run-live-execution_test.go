package runlive

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/environments"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/tests/utils"
)

func newLocalEnvironment(t *testing.T, runner *utils.ShellTestRunner) *environments.LocalEnvironment {
	t.Helper()
	cfg := &minisweagent.EnvironmentConfig{
		CommandTimeout: 5,
	}
	env, err := environments.NewLocalEnvironment(cfg)
	if err != nil {
		t.Fatalf("NewLocalEnvironment: %v", err)
	}
	return env
}

func TestAsyncCommandCompletesBackgroundWork(t *testing.T) {
	runner := utils.NewShellTestRunner(t)
	env := newLocalEnvironment(t, runner)

	ctx, cancel := runner.NewContextWithTimeout(3 * time.Second)
	defer cancel()

	command := `
set -euo pipefail
tmp_file=$(mktemp)
( sleep 0.2 && echo "background complete" > "$tmp_file" ) &
wait
cat "$tmp_file"
`

	result, err := env.Execute(ctx, command, runner.RepoRoot())
	if err != nil {
		t.Fatalf("execute command: %v", err)
	}

	shellResult := utils.ShellResult{Stdout: result.Output, ExitCode: result.ReturnCode}
	utils.RequireExitCode(t, shellResult, 0)
	utils.RequireStdoutContains(t, shellResult, "background complete")
	if !strings.Contains(result.Metadata, "script path:") {
		t.Fatalf("expected metadata to include script path, got %q", result.Metadata)
	}
}

func TestAsyncCommandRespectsContextTimeout(t *testing.T) {
	runner := utils.NewShellTestRunner(t)
	env := newLocalEnvironment(t, runner)

	type timeoutFixture struct {
		Command          string `json:"command"`
		TimeoutSeconds   int    `json:"timeoutSeconds"`
		ExpectedExitCode int    `json:"expectedExitCode"`
	}

	var fixture timeoutFixture
	utils.LoadJSONFixture(t, runner.FixturesDir(), "timeout-scenario.json", &fixture)

	ctx, cancel := runner.NewContextWithTimeout(time.Duration(fixture.TimeoutSeconds) * time.Second)
	defer cancel()

	result, err := env.Execute(ctx, fixture.Command, runner.RepoRoot())
	if err != nil {
		t.Fatalf("execute command: %v", err)
	}

	shellResult := utils.ShellResult{Stdout: result.Output, ExitCode: result.ReturnCode}
	utils.RequireExitCode(t, shellResult, fixture.ExpectedExitCode)
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("expected context deadline exceeded, got %v", ctx.Err())
	}
}
