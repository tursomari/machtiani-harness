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
	"sync"
	"sync/atomic"
	"time"

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
	running, err := e.Start(ctx, command, cwd)
	if err != nil {
		return minisweagent.ExecuteResult{}, err
	}
	return running.Wait()
}

// Start launches a command in its own process group and returns its observable
// lifecycle without waiting for completion.
func (e *LocalEnvironment) Start(ctx context.Context, command, cwd string) (minisweagent.RunningCommand, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workingDir := cwd
	if workingDir == "" {
		workingDir = e.cwd
	}

	absWorkDir, err := filepath.Abs(workingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}

	if _, err := internalgit.RepoRoot(absWorkDir); err != nil {
		return nil, fmt.Errorf("local environment requires a git repository; %q is not a git repository: %w", absWorkDir, err)
	}

	tmpFile, err := os.CreateTemp("", "mini-swe-cmd-*.sh")
	if err != nil {
		return nil, fmt.Errorf("create temp script: %w", err)
	}
	scriptPath := tmpFile.Name()
	if err := tmpFile.Chmod(0o700); err != nil {
		tmpFile.Close()
		_ = os.Remove(scriptPath)
		return nil, fmt.Errorf("chmod temp script: %w", err)
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
		_ = os.Remove(scriptPath)
		return nil, fmt.Errorf("write temp script: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return nil, fmt.Errorf("close temp script: %w", err)
	}

	cmd := exec.Command("bash", scriptPath)
	configureProcessGroup(cmd)
	if absWorkDir != "" {
		cmd.Dir = absWorkDir
	}

	env := os.Environ()
	cmd.Env = env

	maxBytes := e.config.MaxCommandOutputBytes
	if maxBytes <= 0 {
		maxBytes = 65536
	}

	cw := &combinedWriter{limit: maxBytes}
	cmd.Stdout = cw
	cmd.Stderr = cw
	pgid, err := startProcessGroup(cmd)
	if err != nil {
		_ = os.Remove(scriptPath)
		return nil, fmt.Errorf("start command: %w", err)
	}

	running := &localRunningCommand{
		cmd:           cmd,
		ctx:           ctx,
		pid:           cmd.Process.Pid,
		pgid:          pgid,
		startedAt:     time.Now(),
		output:        cw,
		scriptPath:    scriptPath,
		scriptContent: scriptContent,
		maxBytes:      maxBytes,
		done:          make(chan struct{}),
	}
	go running.reap()
	go running.killOnContextDone()
	return running, nil
}

// combinedWriter is an io.Writer that writes to a buffer up to a limit,
// then silently counts overflow bytes without storing them.
type combinedWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int
	overflow int64
	total    int64
	updated  time.Time
}

// Write implements io.Writer. Bytes beyond the configured limit are counted
// but discarded so that the underlying command does not block.
func (w *combinedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += int64(len(p))
	w.updated = time.Now()
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

func (w *combinedWriter) snapshot() minisweagent.CommandOutputSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return minisweagent.CommandOutputSnapshot{
		Output:        w.buf.String(),
		CapturedBytes: int64(w.buf.Len()),
		TotalBytes:    w.total,
		OverflowBytes: w.overflow,
		UpdatedAt:     w.updated,
	}
}

type localRunningCommand struct {
	cmd           *exec.Cmd
	ctx           context.Context
	pid           int
	pgid          int
	startedAt     time.Time
	output        *combinedWriter
	scriptPath    string
	scriptContent string
	maxBytes      int
	done          chan struct{}
	killed        atomic.Bool
	mu            sync.Mutex
	result        minisweagent.ExecuteResult
	err           error
}

func (c *localRunningCommand) PID() int              { return c.pid }
func (c *localRunningCommand) ProcessGroupID() int   { return c.pgid }
func (c *localRunningCommand) StartedAt() time.Time  { return c.startedAt }
func (c *localRunningCommand) Done() <-chan struct{} { return c.done }
func (c *localRunningCommand) Snapshot() minisweagent.CommandOutputSnapshot {
	return c.output.snapshot()
}

func (c *localRunningCommand) Wait() (minisweagent.ExecuteResult, error) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result, c.err
}

func (c *localRunningCommand) Kill() error {
	c.killed.Store(true)
	return killProcessGroup(c.cmd, c.pgid)
}

func (c *localRunningCommand) killOnContextDone() {
	select {
	case <-c.ctx.Done():
		_ = c.Kill()
	case <-c.done:
	}
}

func (c *localRunningCommand) reap() {
	err := c.cmd.Wait()
	releaseProcessGroup(c.cmd, c.pgid)
	snapshot := c.output.snapshot()
	result := minisweagent.ExecuteResult{Output: snapshot.Output, ReturnCode: 0}
	if status := c.cmd.ProcessState; status != nil {
		result.ReturnCode = status.ExitCode()
	}
	if c.killed.Load() || c.ctx.Err() != nil {
		result.ReturnCode = -1
	}
	if snapshot.OverflowBytes > 0 {
		result.Output += fmt.Sprintf("\n[... output truncated to %d bytes]", c.maxBytes)
	}
	result.Metadata = commandMetadata(c.scriptPath, c.scriptContent, snapshot, c.maxBytes)
	if _, ok := err.(*exec.ExitError); ok {
		err = nil
	}
	_ = os.Remove(c.scriptPath)

	c.mu.Lock()
	c.result = result
	c.err = err
	c.mu.Unlock()
	close(c.done)
}

func commandMetadata(scriptPath, scriptContent string, snapshot minisweagent.CommandOutputSnapshot, maxBytes int) string {
	metaBuilder := &strings.Builder{}
	metaBuilder.WriteString("[mini-swe] script path: ")
	metaBuilder.WriteString(scriptPath)
	metaBuilder.WriteString("\n[mini-swe] script contents:\n")
	metaBuilder.WriteString(scriptContent)
	if !strings.HasSuffix(scriptContent, "\n") {
		metaBuilder.WriteString("\n")
	}
	if snapshot.OverflowBytes > 0 {
		metaBuilder.WriteString(fmt.Sprintf("\n[mini-swe] output truncated: %d bytes captured, %d bytes overflowed (limit %d bytes)", snapshot.CapturedBytes, snapshot.OverflowBytes, maxBytes))
	}
	return metaBuilder.String()
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
