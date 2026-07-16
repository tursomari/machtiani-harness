package environments

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	internalgit "github.com/tursomari/machtiani/agent/internal/git"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// LocalEnvironment executes shell commands on the host machine.
type LocalEnvironment struct {
	config       *minisweagent.EnvironmentConfig
	cwd          string
	markerDir    string
	prevFinalDir string
	hadFinalDir  bool
	setFinalDir  bool
}

// NewLocalEnvironment builds a LocalEnvironment from configuration.
func NewLocalEnvironment(cfg *minisweagent.EnvironmentConfig) (*LocalEnvironment, error) {
	if cfg == nil {
		cfg = &minisweagent.EnvironmentConfig{CommandTimeout: 30}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve current working directory: %w", err)
	}
	env := &LocalEnvironment{config: cfg, cwd: filepath.Clean(cwd)}
	prevFinalDir, hadFinalDir := os.LookupEnv("MINISWE_FINAL_DIR")
	if strings.TrimSpace(prevFinalDir) == "" {
		markerDir, err := ensureMarkerDir()
		if err != nil {
			markerDir = os.TempDir()
		}
		if markerDir != "" {
			if err := os.Setenv("MINISWE_FINAL_DIR", markerDir); err == nil {
				env.markerDir = markerDir
				env.setFinalDir = true
			}
		}
	}
	env.prevFinalDir = prevFinalDir
	env.hadFinalDir = hadFinalDir
	return env, nil
}

// Config returns the environment configuration.
func (e *LocalEnvironment) Config() interface{} { return e.config }

// Close restores any environment variable changes made during initialization.
func (e *LocalEnvironment) Close() error {
	if e == nil || !e.setFinalDir {
		return nil
	}
	if e.hadFinalDir {
		return os.Setenv("MINISWE_FINAL_DIR", e.prevFinalDir)
	}
	return os.Unsetenv("MINISWE_FINAL_DIR")
}

// Execute runs a provided command with a context that may enforce a timeout.
func (e *LocalEnvironment) Execute(ctx context.Context, command, cwd string) (minisweagent.ExecuteResult, error) {
	workingDir := cwd
	if workingDir == "" {
		workingDir = e.cwd
	}

	absWorkDir, err := filepath.Abs(workingDir)
	if err != nil {
		return minisweagent.ExecuteResult{}, fmt.Errorf("resolve working directory: %w", err)
	}

	if _, err := internalgit.RepoRoot(absWorkDir); err != nil {
		return minisweagent.ExecuteResult{}, fmt.Errorf("local environment requires a git repository; %q is not a git repository: %w", absWorkDir, err)
	}

	tmpFile, err := os.CreateTemp("", "mini-swe-cmd-*.sh")
	if err != nil {
		return minisweagent.ExecuteResult{}, fmt.Errorf("create temp script: %w", err)
	}
	scriptPath := tmpFile.Name()
	defer os.Remove(scriptPath)
	if err := tmpFile.Chmod(0o700); err != nil {
		tmpFile.Close()
		return minisweagent.ExecuteResult{}, fmt.Errorf("chmod temp script: %w", err)
	}

	scriptBuilder := &strings.Builder{}
	scriptBuilder.WriteString("#!/bin/bash\n")
	scriptBuilder.WriteString("set -euo pipefail\n")
	scriptBuilder.WriteString(command)
	if !strings.HasSuffix(command, "\n") {
		scriptBuilder.WriteString("\n")
	}

	scriptContent := scriptBuilder.String()
	if _, err := tmpFile.WriteString(scriptContent); err != nil {
		tmpFile.Close()
		return minisweagent.ExecuteResult{}, fmt.Errorf("write temp script: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return minisweagent.ExecuteResult{}, fmt.Errorf("close temp script: %w", err)
	}

	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	if absWorkDir != "" {
		cmd.Dir = absWorkDir
	}

	env := os.Environ()
	cmd.Env = env

	maxBytes := 65536

	var buf bytes.Buffer
	cw := &combinedWriter{buf: &buf, limit: maxBytes}
	cmd.Stdout = cw
	cmd.Stderr = cw
	err = cmd.Run()

	output := buf.String()
	exitCode := 0
	if status := cmd.ProcessState; status != nil {
		exitCode = status.ExitCode()
	}

	if cw.overflow > 0 {
		output += fmt.Sprintf("\n[... output truncated to %d bytes]", maxBytes)
	}

	result := minisweagent.ExecuteResult{
		Output:     output,
		ReturnCode: exitCode,
	}

	metaBuilder := &strings.Builder{}
	metaBuilder.WriteString("[mini-swe] script path: ")
	metaBuilder.WriteString(scriptPath)
	metaBuilder.WriteString("\n[mini-swe] script contents:\n")
	metaBuilder.WriteString(scriptContent)
	if !strings.HasSuffix(scriptContent, "\n") {
		metaBuilder.WriteString("\n")
	}
	if cw.overflow > 0 {
		metaBuilder.WriteString(fmt.Sprintf("\n[mini-swe] output truncated: %d bytes captured, %d bytes overflowed (limit %d bytes)", maxBytes, cw.overflow, maxBytes))
	}
	result.Metadata = metaBuilder.String()

	if ctx.Err() == context.DeadlineExceeded {
		result.ReturnCode = -1
		return result, nil
	}

	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			err = nil
		}
	}

	return result, err
}

// combinedWriter is an io.Writer that writes to a buffer up to a limit,
// then silently counts overflow bytes without storing them.
type combinedWriter struct {
	buf      *bytes.Buffer
	limit    int
	overflow int64
}

// Write implements io.Writer. Bytes beyond the configured limit are counted
// but discarded so that the underlying command does not block.
func (w *combinedWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.limit {
		n := len(p)
		w.overflow += int64(n)
		return n, nil
	}
	remaining := w.limit - w.buf.Len()
	if len(p) <= remaining {
		return w.buf.Write(p)
	}
	n, err := w.buf.Write(p[:remaining])
	w.overflow += int64(len(p) - n)
	return len(p), err
}

// compile-time check: combinedWriter implements io.Writer
var _ io.Writer = (*combinedWriter)(nil)

// GetTemplateVars exposes host metadata for template rendering.
func (e *LocalEnvironment) GetTemplateVars() map[string]interface{} {
	envMap := make(map[string]string)
	for _, kv := range os.Environ() {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		envMap[parts[0]] = parts[1]
	}

	return map[string]interface{}{
		"cwd":     e.cwd,
		"CWD":     e.cwd,
		"env":     envMap,
		"Env":     envMap,
		"machine": runtime.GOARCH,
		"Machine": runtime.GOARCH,
		"os":      runtime.GOOS,
		"OS":      runtime.GOOS,
		"timeout": e.config.CommandTimeout,
		"Timeout": e.config.CommandTimeout,
	}
}

// GetSyncProgress returns the workspace sync progress (0.0 to 1.0).
// LocalEnvironment has no sync operations, so it returns 1.0 (complete).
func (e *LocalEnvironment) GetSyncProgress() float64 {
	return 1.0
}

// GetSyncStatus returns a status description of workspace sync.
// LocalEnvironment has no sync operations, so it returns empty string.
func (e *LocalEnvironment) GetSyncStatus() string {
	return ""
}
