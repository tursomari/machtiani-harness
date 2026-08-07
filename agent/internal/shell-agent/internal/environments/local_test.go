package environments

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func TestStartExposesRunningCommandLifecycle(t *testing.T) {
	repo := initTestRepo(t)
	env, err := NewLocalEnvironment(&minisweagent.EnvironmentConfig{CommandTimeout: 5})
	if err != nil {
		t.Fatalf("NewLocalEnvironment: %v", err)
	}

	running, err := env.Start(context.Background(), "printf 'ready\\n'; sleep 30", repo)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if running.PID() <= 0 {
		t.Fatalf("PID = %d, want positive", running.PID())
	}
	if running.ProcessGroupID() <= 0 {
		t.Fatalf("PGID = %d, want positive", running.ProcessGroupID())
	}
	if running.StartedAt().IsZero() {
		t.Fatal("StartedAt is zero")
	}

	waitForOutput(t, running, "ready")
	snapshot := running.Snapshot()
	if snapshot.CapturedBytes == 0 || snapshot.TotalBytes < snapshot.CapturedBytes {
		t.Fatalf("unexpected output progress: %+v", snapshot)
	}
	if snapshot.UpdatedAt.IsZero() {
		t.Fatal("output UpdatedAt is zero after output")
	}

	if err := running.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	result, err := running.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.ReturnCode != -1 {
		t.Fatalf("ReturnCode = %d, want -1 after kill", result.ReturnCode)
	}
	select {
	case <-running.Done():
	default:
		t.Fatal("Done channel is not closed after Wait")
	}
}

func TestRunningCommandKillStopsEntireProcessGroup(t *testing.T) {
	repo := initTestRepo(t)
	env, err := NewLocalEnvironment(&minisweagent.EnvironmentConfig{CommandTimeout: 5})
	if err != nil {
		t.Fatalf("NewLocalEnvironment: %v", err)
	}

	running, err := env.Start(context.Background(), "sleep 30 & child=$!; printf '%s\\n' \"$child\"; wait", repo)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	output := waitForOutput(t, running, "\n")
	childPID, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		t.Fatalf("parse child PID from %q: %v", output, err)
	}
	if childPGID, err := syscall.Getpgid(childPID); err != nil || childPGID != running.ProcessGroupID() {
		t.Fatalf("child PGID = %d, %v; want %d", childPGID, err, running.ProcessGroupID())
	}

	if err := running.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, err := running.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processRunning(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processRunning(childPID) {
		t.Fatalf("child process %d still running after process-group kill", childPID)
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet")
	cmd.Dir = repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v (%s)", err, output)
	}
	return repo
}

func waitForOutput(t *testing.T, running minisweagent.RunningCommand, needle string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		output := running.Snapshot().Output
		if strings.Contains(output, needle) {
			return output
		}
		select {
		case <-running.Done():
			t.Fatalf("command finished before producing %q; output %q", needle, output)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for output %q; got %q", needle, running.Snapshot().Output)
	return ""
}

func processRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	return len(fields) > 2 && fields[2] != "Z"
}

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
