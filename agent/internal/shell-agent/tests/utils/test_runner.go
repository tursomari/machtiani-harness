package utils

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ExecutionOption customises an execution context for a specific test invocation.
type ExecutionOption func(*ExecutionContext)

// WithEnv injects or overrides environment variables for a command run.
func WithEnv(env map[string]string) ExecutionOption {
	return func(ctx *ExecutionContext) {
		if ctx.Env == nil {
			ctx.Env = make(map[string]string, len(env))
		}
		for k, v := range env {
			ctx.Env[k] = v
		}
	}
}

// WithWorkingDir overrides the working directory for a run.
func WithWorkingDir(dir string) ExecutionOption {
	return func(ctx *ExecutionContext) {
		ctx.WorkingDir = dir
	}
}

// WithTimeout overrides the timeout for a single execution.
func WithTimeout(timeout time.Duration) ExecutionOption {
	return func(ctx *ExecutionContext) {
		ctx.Timeout = timeout
	}
}

// ShellTestRunner wires common setup/teardown for shell-based tests.
type ShellTestRunner struct {
	t              *testing.T
	ctx            context.Context
	cancel         context.CancelFunc
	shellPath      string
	repoRoot       string
	fixturesDir    string
	defaultEnv     map[string]string
	defaultTimeout time.Duration
}

// NewShellTestRunner initialises the runner and validates the host shell.
func NewShellTestRunner(t *testing.T) *ShellTestRunner {
	t.Helper()

	shellPath, err := ResolveShellPath()
	if err != nil {
		t.Fatalf("resolve shell: %v", err)
	}

	repoRoot, err := findModuleRoot()
	if err != nil {
		t.Fatalf("determine module root: %v", err)
	}

	baseEnv := map[string]string{
		"SHELL": shellPath,
	}
	if user := os.Getenv("USER"); user != "" {
		baseEnv["USER"] = user
	}

	ctx, cancel := context.WithCancel(context.Background())
	runner := &ShellTestRunner{
		t:              t,
		ctx:            ctx,
		cancel:         cancel,
		shellPath:      shellPath,
		repoRoot:       repoRoot,
		fixturesDir:    filepath.Join(repoRoot, "internal", "shell-agent", "tests", "fixtures"),
		defaultEnv:     baseEnv,
		defaultTimeout: 5 * time.Second,
	}

	t.Cleanup(cancel)
	runner.ensureShellOperational()

	return runner
}

// Context exposes the long-lived root context for the suite.
func (r *ShellTestRunner) Context() context.Context {
	return r.ctx
}

// NewContextWithTimeout derives a context with the requested timeout bound to the runner lifecycle.
func (r *ShellTestRunner) NewContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(r.ctx)
	}
	return context.WithTimeout(r.ctx, timeout)
}

// ShellPath returns the resolved shell binary path.
func (r *ShellTestRunner) ShellPath() string {
	return r.shellPath
}

// RepoRoot returns the module root directory.
func (r *ShellTestRunner) RepoRoot() string {
	return r.repoRoot
}

// FixturesDir returns the directory containing JSON fixtures.
func (r *ShellTestRunner) FixturesDir() string {
	return r.fixturesDir
}

// FixturePath joins the supplied name against the fixtures directory.
func (r *ShellTestRunner) FixturePath(name string) string {
	return filepath.Join(r.fixturesDir, name)
}

// ExecutionContext builds an ExecutionContext using the runner defaults and supplied options.
func (r *ShellTestRunner) ExecutionContext(opts ...ExecutionOption) ExecutionContext {
	execCtx := ExecutionContext{
		ShellPath:  r.shellPath,
		WorkingDir: r.repoRoot,
		Env:        make(map[string]string, len(r.defaultEnv)),
		Timeout:    r.defaultTimeout,
	}
	for k, v := range r.defaultEnv {
		execCtx.Env[k] = v
	}
	for _, opt := range opts {
		opt(&execCtx)
	}
	return execCtx
}

// RunCommand executes a shell command using the runner defaults and provided options.
func (r *ShellTestRunner) RunCommand(command string, opts ...ExecutionOption) (ShellResult, error) {
	return RunShellCommand(r.ctx, r.ExecutionContext(opts...), command)
}

// RunCommandWithContext executes a shell command with a caller-supplied context and options.
func (r *ShellTestRunner) RunCommandWithContext(ctx context.Context, command string, opts ...ExecutionOption) (ShellResult, error) {
	return RunShellCommand(ctx, r.ExecutionContext(opts...), command)
}

// ensureShellOperational executes a trivial probe to confirm the shell is invocable.
func (r *ShellTestRunner) ensureShellOperational() {
	r.t.Helper()

	probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, r.shellPath, "-lc", "echo ok")
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("shell probe failed for %s: %v", r.shellPath, err)
	}
}

func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		candidate := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(candidate); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", dir)
		}
		dir = parent
	}
}
