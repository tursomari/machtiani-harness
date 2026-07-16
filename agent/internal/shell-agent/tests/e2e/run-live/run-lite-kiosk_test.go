package runlive

import (
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/environments"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/tests/utils"
)

func TestLiteCommandEchoMatchesFixture(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	result, err := runner.RunCommand("echo 'integration success'")
	if err != nil {
		t.Fatalf("run command: %v", err)
	}

	utils.RequireExitCode(t, result, 0)
	utils.RequireStdoutContains(t, result, "integration success")

	type fixture struct {
		Stdout string `json:"stdout"`
	}

	var fx fixture
	utils.LoadJSONFixture(t, runner.FixturesDir(), "success-output.json", &fx)
	if !strings.Contains(result.Stdout, strings.TrimSpace(fx.Stdout)) {
		t.Fatalf("expected stdout to contain fixture payload %q; got %q", fx.Stdout, result.Stdout)
	}
}

func TestLiteEnvironmentMetadataIncludesScriptPath(t *testing.T) {
	runner := utils.NewShellTestRunner(t)
	cfg := &minisweagent.EnvironmentConfig{CommandTimeout: 5}
	env, err := environments.NewLocalEnvironment(cfg)
	if err != nil {
		t.Fatalf("NewLocalEnvironment: %v", err)
	}

	result, err := env.Execute(runner.Context(), "pwd", runner.RepoRoot())
	if err != nil {
		t.Fatalf("execute command: %v", err)
	}
	if result.ReturnCode != 0 {
		t.Fatalf("expected zero exit code, got %d", result.ReturnCode)
	}
	if !strings.Contains(result.Metadata, "script contents") {
		t.Fatalf("expected metadata to include script contents, got %q", result.Metadata)
	}
	if !strings.Contains(result.Output, runner.RepoRoot()) {
		t.Fatalf("expected output to mention repo root %q; got %q", runner.RepoRoot(), result.Output)
	}
}
