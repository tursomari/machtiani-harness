package shellsubprocess

import (
	"os"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/tests/utils"
)

func TestShellSeparatesStdoutAndStderr(t *testing.T) {
	runner := utils.NewShellTestRunner(t)
	command := "echo 'primary output'; echo 'warning stream' >&2"

	result, err := runner.RunCommand(command)
	if err != nil {
		t.Fatalf("run command: %v", err)
	}

	utils.RequireExitCode(t, result, 0)
	utils.RequireStdoutContains(t, result, "primary output")
	utils.RequireStderrContains(t, result, "warning stream")
}

func TestShellStdoutMatchesJSONFixture(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	fixturePath := runner.FixturePath("success-output.json")
	content, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	command := "cat <<'EOF'\n" + string(content) + "\nEOF"
	result, runErr := runner.RunCommand(command)
	if runErr != nil {
		t.Fatalf("run command: %v", runErr)
	}

	utils.RequireExitCode(t, result, 0)
	utils.RequireStdoutMatchesJSONFixture(t, result.Stdout, fixturePath)
}
