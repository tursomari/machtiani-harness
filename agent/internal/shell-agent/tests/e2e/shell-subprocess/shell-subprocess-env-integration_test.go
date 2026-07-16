package shellsubprocess

import (
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/tests/utils"
)

func TestShellPropagatesCustomEnvVar(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	env := map[string]string{"CUSTOM_TEST_VAR": "123"}
	result, err := runner.RunCommand("printf '%s' \"$CUSTOM_TEST_VAR\"", utils.WithEnv(env))
	if err != nil {
		t.Fatalf("run command: %v", err)
	}

	utils.RequireExitCode(t, result, 0)
	utils.RequireStdoutContains(t, result, "123")
}

func TestShellLoadsEnvFixtureVariables(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	type envFixture struct {
		Env map[string]string `json:"env"`
	}

	var fixture envFixture
	utils.LoadJSONFixture(t, runner.FixturesDir(), "env-variables.json", &fixture)

	result, err := runner.RunCommand("printf '%s:%s' \"$TEST_FOO\" \"$TEST_PATH_PREFIX\"", utils.WithEnv(fixture.Env))
	if err != nil {
		t.Fatalf("run command: %v", err)
	}

	utils.RequireExitCode(t, result, 0)
	for key, value := range fixture.Env {
		if key == "TEST_FOO" && !strings.Contains(result.Stdout, value) {
			t.Fatalf("expected stdout to contain %s value %q; got %q", key, value, result.Stdout)
		}
		if key == "TEST_PATH_PREFIX" && !strings.Contains(result.Stdout, value) {
			t.Fatalf("expected stdout to contain %s value %q; got %q", key, value, result.Stdout)
		}
	}
}
