package utils

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ExecutionContext bundles the shell execution parameters shared across tests.
type ExecutionContext struct {
	ShellPath  string
	WorkingDir string
	Env        map[string]string
	Timeout    time.Duration
}

// ResolveShellPath locates the preferred shell executable, consulting $SHELL first.
func ResolveShellPath() (string, error) {
	if shell := os.Getenv("SHELL"); shell != "" {
		if filepath.IsAbs(shell) {
			if _, err := os.Stat(shell); err == nil {
				return shell, nil
			}
		}
		if path, err := exec.LookPath(shell); err == nil {
			return path, nil
		}
	}

	candidates := []string{"bash", "/bin/bash", "sh"}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("unable to resolve shell executable; set SHELL to a valid absolute path")
}

// BuildExecutionContext constructs a reusable execution configuration for tests.
func BuildExecutionContext(workingDir string, timeout time.Duration, extraEnv map[string]string) (ExecutionContext, error) {
	shellPath, err := ResolveShellPath()
	if err != nil {
		return ExecutionContext{}, err
	}

	if workingDir == "" {
		workingDir, err = os.Getwd()
		if err != nil {
			return ExecutionContext{}, fmt.Errorf("determine working directory: %w", err)
		}
	} else if !filepath.IsAbs(workingDir) {
		cwd, err := os.Getwd()
		if err != nil {
			return ExecutionContext{}, fmt.Errorf("resolve absolute working directory: %w", err)
		}
		workingDir = filepath.Join(cwd, workingDir)
	}

	env := make(map[string]string, len(extraEnv)+2)
	for k, v := range extraEnv {
		env[k] = v
	}
	env["SHELL"] = shellPath
	if user := os.Getenv("USER"); user != "" {
		env["USER"] = user
	}

	return ExecutionContext{
		ShellPath:  shellPath,
		WorkingDir: workingDir,
		Env:        env,
		Timeout:    timeout,
	}, nil
}

// ShellResult captures the output of a shell invocation.
type ShellResult struct {
	Command  []string
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// ShellCall describes a retryable shell invocation.
type ShellCall func(ctx context.Context) (ShellResult, error)

// RetryOptions tunes the retry helper used across tests.
type RetryOptions struct {
	Attempts     int
	InitialDelay time.Duration
	Backoff      float64
	MaxDelay     time.Duration
}

// RetryableError designates errors that are safe to retry.
type RetryableError interface {
	error
	Retryable() bool
}

// RetryShellCall executes the provided shell call with exponential backoff.
func RetryShellCall(ctx context.Context, call ShellCall, opts RetryOptions) (ShellResult, error) {
	attempts := opts.Attempts
	if attempts <= 0 {
		attempts = 3
	}

	delay := opts.InitialDelay
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}

	backoff := opts.Backoff
	if backoff <= 1.0 {
		backoff = 2.0
	}

	maxDelay := opts.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 2 * time.Second
	}

	var lastErr error
	var result ShellResult

	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		start := time.Now()
		res, err := call(ctx)
		res.Duration = time.Since(start)
		result = res

		if err == nil && res.ExitCode == 0 {
			return res, nil
		}

		shouldRetry := false
		if err != nil {
			lastErr = err
			var retryErr RetryableError
			if errors.As(err, &retryErr) && retryErr.Retryable() {
				shouldRetry = true
			} else if errors.Is(err, context.DeadlineExceeded) {
				shouldRetry = true
			}
		} else {
			lastErr = fmt.Errorf("command exited with code %d", res.ExitCode)
			shouldRetry = true
		}

		if !shouldRetry || attempt == attempts {
			if err != nil {
				return res, err
			}
			return res, lastErr
		}

		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(delay):
		}

		delay = time.Duration(float64(delay) * backoff)
		if delay > maxDelay {
			delay = maxDelay
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("retry attempts exhausted")
	}

	return result, lastErr
}

// retryable wraps an error to indicate it may succeed on subsequent attempts.
type retryable struct{ error }

func (e retryable) Retryable() bool { return true }

// nonRetryable marks an error as terminal for the retry helper.
type nonRetryable struct{ error }

func (e nonRetryable) Retryable() bool { return false }

// Retryable flags an error as eligible for retry.
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return retryable{error: err}
}

// NonRetryable flags an error as permanent.
func NonRetryable(err error) error {
	if err == nil {
		return nil
	}
	return nonRetryable{error: err}
}

// RunShellCommand executes the supplied command string using the configured shell.
func RunShellCommand(ctx context.Context, execCtx ExecutionContext, command string) (ShellResult, error) {
	if execCtx.ShellPath == "" {
		return ShellResult{}, fmt.Errorf("shell path is required")
	}

	runCtx := ctx
	cancel := func() {}
	if execCtx.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, execCtx.Timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, execCtx.ShellPath, "-lc", command)
	if execCtx.WorkingDir != "" {
		cmd.Dir = execCtx.WorkingDir
	}

	env := os.Environ()
	for k, v := range execCtx.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	exitCode := 0
	if state := cmd.ProcessState; state != nil {
		exitCode = state.ExitCode()
	}

	result := ShellResult{
		Command:  []string{execCtx.ShellPath, "-lc", command},
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
		Duration: duration,
	}

	if runCtx.Err() == context.DeadlineExceeded {
		result.ExitCode = -1
		return result, Retryable(runCtx.Err())
	}

	if err == nil {
		return result, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return result, nil
	}

	return result, NonRetryable(err)
}
