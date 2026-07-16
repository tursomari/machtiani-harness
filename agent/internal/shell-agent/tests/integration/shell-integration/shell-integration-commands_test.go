package shellintegration

import (
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/tests/utils"
)

func TestMockScriptDirectoryExecution(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	specs := map[string]utils.MockCommandSpec{
		"hello": {Stdout: "hello integration", ExitCode: 0},
	}

	dir, cleanup, err := utils.EnsureExecutableDirectory(specs)
	if err != nil {
		t.Fatalf("ensure executable directory: %v", err)
	}
	t.Cleanup(func() {
		_ = cleanup()
	})

	result, runErr := runner.RunCommand("./hello", utils.WithWorkingDir(dir))
	if runErr != nil {
		t.Fatalf("run command: %v", runErr)
	}

	utils.RequireExitCode(t, result, 0)
	utils.RequireStdoutContains(t, result, "hello integration")
}

func TestSpawnMockScriptProducesExpectedOutput(t *testing.T) {
	runner := utils.NewShellTestRunner(t)

	scriptPath, cleanup, err := utils.SpawnMockScript(utils.MockCommandSpec{Stdout: "mocked output", ExitCode: 0})
	if err != nil {
		t.Fatalf("spawn mock script: %v", err)
	}
	t.Cleanup(func() {
		_ = cleanup()
	})

	absPath, err := filepath.Abs(scriptPath)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	result, runErr := runner.RunCommand(absPath)
	if runErr != nil {
		t.Fatalf("run command: %v", runErr)
	}

	utils.RequireExitCode(t, result, 0)
	utils.RequireStdoutContains(t, result, "mocked output")
}
