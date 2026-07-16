package utils

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MockCommandSpec describes a mocked shell command outcome.
type MockCommandSpec struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Delay    time.Duration
}

// Script returns a bash snippet implementing the mock behaviour.
func (s MockCommandSpec) Script() string {
	var builder strings.Builder
	if s.Delay > 0 {
		builder.WriteString(fmt.Sprintf("sleep %.3f\n", s.Delay.Seconds()))
	}
	if s.Stdout != "" {
		builder.WriteString("cat <<'STDOUT'\n")
		builder.WriteString(s.Stdout)
		if !strings.HasSuffix(s.Stdout, "\n") {
			builder.WriteString("\n")
		}
		builder.WriteString("STDOUT\n")
	}
	if s.Stderr != "" {
		builder.WriteString("cat <<'STDERR' >&2\n")
		builder.WriteString(s.Stderr)
		if !strings.HasSuffix(s.Stderr, "\n") {
			builder.WriteString("\n")
		}
		builder.WriteString("STDERR\n")
	}
	builder.WriteString(fmt.Sprintf("exit %d\n", s.ExitCode))
	return builder.String()
}

// MockScript represents an executable script placed on disk.
type MockScript struct {
	Path string
}

// NewMockScript writes the mock script to a temporary file.
func NewMockScript(spec MockCommandSpec) (*MockScript, error) {
	scriptPath, err := writeMockScript(spec)
	if err != nil {
		return nil, err
	}
	return &MockScript{Path: scriptPath}, nil
}

// Cleanup removes the underlying script file.
func (m *MockScript) Cleanup() error {
	if m == nil || m.Path == "" {
		return nil
	}
	return os.Remove(m.Path)
}

// ShellCall converts the spec into a ShellCall compatible with the retry helper.
func (s MockCommandSpec) ShellCall(execCtx ExecutionContext) ShellCall {
	return func(ctx context.Context) (ShellResult, error) {
		return RunShellCommand(ctx, execCtx, s.Script())
	}
}

// writeMockScript persists a mock command to disk with an executable bit.
func writeMockScript(spec MockCommandSpec) (string, error) {
	file, err := os.CreateTemp("", "shell-agent-mock-*.sh")
	if err != nil {
		return "", fmt.Errorf("create temp mock script: %w", err)
	}
	defer file.Close()

	if err := os.Chmod(file.Name(), 0o700); err != nil {
		return "", fmt.Errorf("chmod mock script: %w", err)
	}

	script := spec.Script()
	if _, err := file.WriteString("#!/bin/bash\nset -euo pipefail\n"); err != nil {
		return "", fmt.Errorf("write mock prologue: %w", err)
	}
	if _, err := file.WriteString(script); err != nil {
		return "", fmt.Errorf("write mock payload: %w", err)
	}

	return file.Name(), nil
}

// SpawnMockScript creates a script on disk and returns the command invocation string.
func SpawnMockScript(spec MockCommandSpec) (string, func() error, error) {
	script, err := NewMockScript(spec)
	if err != nil {
		return "", nil, err
	}

	cleanup := func() error {
		return script.Cleanup()
	}

	return script.Path, cleanup, nil
}

// EnsureExecutableDirectory creates a temporary directory populated with mock scripts.
func EnsureExecutableDirectory(specs map[string]MockCommandSpec) (string, func() error, error) {
	dir, err := os.MkdirTemp("", "shell-agent-mocks-*")
	if err != nil {
		return "", nil, fmt.Errorf("create mock directory: %w", err)
	}

	cleanup := func() error {
		return os.RemoveAll(dir)
	}

	for name, spec := range specs {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/bash\nset -euo pipefail\n"+spec.Script()), 0o700); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("write mock script %s: %w", name, err)
		}
	}

	return dir, cleanup, nil
}
