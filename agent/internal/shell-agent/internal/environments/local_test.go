package environments

import (
	"context"
	"errors"
	"os/exec"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func TestExecuteCreatesScriptAndHandlesQuotes(t *testing.T) {
	tempRepo := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = tempRepo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v (%s)", err, output)
	}

	env, err := NewLocalEnvironment(&minisweagent.EnvironmentConfig{CommandTimeout: 5})
	if err != nil {
		t.Fatalf("NewLocalEnvironment() error = %v", err)
	}

	command := `echo "grep 'error message' /var/log/app.log"`

	result, err := env.Execute(context.Background(), command, tempRepo)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.ReturnCode != 0 {
		t.Fatalf("ReturnCode = %d, want 0", result.ReturnCode)
	}

	if got := strings.TrimSpace(result.Output); got != "grep 'error message' /var/log/app.log" {
		t.Fatalf("unexpected command output: %q", result.Output)
	}

	if result.Metadata == "" {
		t.Fatalf("metadata should not be empty")
	}

	lines := strings.SplitN(result.Metadata, "\n", 2)
	if len(lines) < 2 {
		t.Fatalf("unexpected metadata format: %q", result.Metadata)
	}

	const pathPrefix = "[mini-swe] script path: "
	if !strings.HasPrefix(lines[0], pathPrefix) {
		t.Fatalf("missing script path prefix: %q", lines[0])
	}

	scriptPath := strings.TrimPrefix(lines[0], pathPrefix)
	scriptPath = strings.TrimSpace(scriptPath)
	if scriptPath == "" {
		t.Fatal("empty script path")
	}

	tempDir := filepath.Clean(os.TempDir())
	if !strings.HasPrefix(filepath.Clean(scriptPath), tempDir) {
		t.Fatalf("script not created in temp dir: %s", scriptPath)
	}

	rest := lines[1]

	const scriptMarker = "[mini-swe] script contents:\n"
	if !strings.HasPrefix(rest, scriptMarker) {
		t.Fatalf("missing script contents marker: %q", rest)
	}

	scriptContent := strings.TrimPrefix(rest, scriptMarker)

	expectedScript := "#!/bin/bash\nset -euo pipefail\n" + command
	if !strings.HasSuffix(expectedScript, "\n") {
		expectedScript += "\n"
	}

	if scriptContent != expectedScript {
		t.Fatalf("unexpected script content:\n%s\nwant:\n%s", scriptContent, expectedScript)
	}

	if _, err := os.Stat(scriptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("script file should be removed, stat error = %v", err)
	}
}

func TestLocalEnvironmentCapturesLaunchCWD(t *testing.T) {
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet")
	cmd.Dir = repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v (%s)", err, output)
	}

	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	env, err := NewLocalEnvironment(&minisweagent.EnvironmentConfig{CommandTimeout: 5})
	if restoreErr := os.Chdir(original); restoreErr != nil {
		t.Fatalf("restore working directory: %v", restoreErr)
	}
	if err != nil {
		t.Fatalf("NewLocalEnvironment() error = %v", err)
	}

	vars := env.GetTemplateVars()
	if got := vars["CWD"]; got != repo {
		t.Fatalf("CWD template variable = %q, want %q", got, repo)
	}
	if got := vars["cwd"]; got != repo {
		t.Fatalf("cwd template variable = %q, want %q", got, repo)
	}

	result, err := env.Execute(context.Background(), "pwd", "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := strings.TrimSpace(result.Output); got != repo {
		t.Fatalf("pwd output = %q, want %q", got, repo)
	}
}
