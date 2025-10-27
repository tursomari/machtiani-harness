package prompt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/mct/internal/discoveryrunner"
	"github.com/tursomari/machtiani/agent/internal/mct/llm"
)

func TestShellAgentModeInvokesShellAgentAndSkipsFileDiscovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	sessionID := "shell-agent-session"
	t.Setenv("MACHTIANI_SESSION_ID", sessionID)

	origDiscovery := discoveryRunnerRun
	discoveryRunnerRun = func(context.Context, string, discoveryrunner.ModelSettings, string, bool) (discoveryrunner.Result, error) {
		t.Fatalf("file discovery should be skipped when shell agent is enabled")
		return discoveryrunner.Result{}, nil
	}
	t.Cleanup(func() { discoveryRunnerRun = origDiscovery })

	origChat := chatStreamWithRuntime
	var capturedPrompt string
	chatStreamWithRuntime = func(ctx context.Context, resolved llm.ResolvedModel, aliases []string, fallbacks []llm.ResolvedModel, extras map[string]any, msgs []llm.Message, onToken func(string)) (string, error) {
		if len(msgs) != 1 {
			t.Fatalf("expected single message, got %d", len(msgs))
		}
		capturedPrompt = msgs[0].Content
		return "assistant", nil
	}
	t.Cleanup(func() { chatStreamWithRuntime = origChat })

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "shell-agent.log")
	trajPath := filepath.Join(binDir, "trajectory-20241010-123000.json")
	scriptPath := filepath.Join(binDir, "shell-agent")
	script := "#!/bin/sh\n" +
		"set -e\n" +
		"printf '%s\\n' \"$@\" > \"$SHELL_AGENT_TEST_LOG\"\n" +
		"printf 'shell agent stdout for %s\\n' \"$*\"\n" +
		"printf 'Trajectory: %s\\n' \"$SHELL_AGENT_TEST_TRAJ\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write shell-agent stub: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("SHELL_AGENT_TEST_LOG", logPath)
	t.Setenv("SHELL_AGENT_TEST_TRAJ", trajPath)

	opts := RunOptions{
		Prompt:       "Collect deployment diagnostics",
		Mode:         "default",
		SessionID:    sessionID,
		Runtime:      ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model", APIKey: "key", BaseURL: "https://example.com"}},
		ExplicitName: "integration-test",
		ShellAgent:   true,
	}

	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.ShellAgentUsed {
		t.Fatalf("expected ShellAgentUsed to be true")
	}
	if res.FileDiscoveryRan {
		t.Fatalf("expected FileDiscoveryRan to be false")
	}
	if res.TrajectoryPath != trajPath {
		t.Fatalf("unexpected trajectory path: want %q got %q", trajPath, res.TrajectoryPath)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read shell-agent log: %v", err)
	}
	logLine := strings.TrimSpace(string(data))
	if !strings.Contains(logLine, "-output-format=json") {
		t.Fatalf("shell-agent invocation missing -output-format flag: %q", logLine)
	}
	if !strings.HasSuffix(logLine, "Collect deployment diagnostics") {
		t.Fatalf("shell-agent invocation missing prompt: %q", logLine)
	}
	if capturedPrompt == "" {
		t.Fatalf("expected prompt to reach llm runner")
	}
	if !strings.Contains(capturedPrompt, shellAgentContextPrefix) {
		t.Fatalf("expected shell-agent context in prompt: %q", capturedPrompt)
	}
}

func TestShellAgentModePropagatesError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	sessionID := "shell-agent-error"
	t.Setenv("MACHTIANI_SESSION_ID", sessionID)

	origDiscovery := discoveryRunnerRun
	discoveryRunnerRun = func(context.Context, string, discoveryrunner.ModelSettings, string, bool) (discoveryrunner.Result, error) {
		t.Fatalf("file discovery should not run when shell agent is enabled")
		return discoveryrunner.Result{}, nil
	}
	t.Cleanup(func() { discoveryRunnerRun = origDiscovery })

	origChat := chatStreamWithRuntime
	chatStreamWithRuntime = func(context.Context, llm.ResolvedModel, []string, []llm.ResolvedModel, map[string]any, []llm.Message, func(string)) (string, error) {
		t.Fatalf("chat stream should not run when shell agent fails")
		return "", nil
	}
	t.Cleanup(func() { chatStreamWithRuntime = origChat })

	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "shell-agent")
	script := "#!/bin/sh\n" +
		"printf 'failure details\\n' >&2\n" +
		"exit 7\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write shell-agent stub: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	opts := RunOptions{
		Prompt:       "Gather pipeline logs",
		Mode:         "default",
		SessionID:    sessionID,
		Runtime:      ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}},
		ExplicitName: "integration-error",
		ShellAgent:   true,
	}

	_, err = Run(context.Background(), opts)
	if err == nil {
		t.Fatalf("expected error from shell-agent failure")
	}
	if !strings.Contains(err.Error(), "shell-agent exited with code 7") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFileDiscoveryRunsWhenShellAgentDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	sessionID := "file-discovery-default"
	t.Setenv("MACHTIANI_SESSION_ID", sessionID)

	origDiscovery := discoveryRunnerRun
	var discoveryCalls int
	discoveryRunnerRun = func(ctx context.Context, prompt string, model discoveryrunner.ModelSettings, sid string, verbose bool) (discoveryrunner.Result, error) {
		discoveryCalls++
		return discoveryrunner.Result{Paths: []string{"src/main.go", "README.md"}}, nil
	}
	t.Cleanup(func() { discoveryRunnerRun = origDiscovery })

	origChat := chatStreamWithRuntime
	var capturedPrompt string
	chatStreamWithRuntime = func(ctx context.Context, resolved llm.ResolvedModel, aliases []string, fallbacks []llm.ResolvedModel, extras map[string]any, msgs []llm.Message, onToken func(string)) (string, error) {
		if len(msgs) != 1 {
			t.Fatalf("expected single message, got %d", len(msgs))
		}
		capturedPrompt = msgs[0].Content
		return "ok", nil
	}
	t.Cleanup(func() { chatStreamWithRuntime = origChat })

	opts := RunOptions{
		Prompt:       "Summarize the project state",
		Mode:         "default",
		SessionID:    sessionID,
		Runtime:      ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model"}},
		ShellAgent:   false,
		ExplicitName: "integration-default",
	}

	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if discoveryCalls != 1 {
		t.Fatalf("expected file discovery to run once, got %d", discoveryCalls)
	}
	if !res.FileDiscoveryRan {
		t.Fatalf("expected FileDiscoveryRan to be true")
	}
	if res.ShellAgentUsed {
		t.Fatalf("expected ShellAgentUsed to be false")
	}
	if len(res.RetrievedFiles) != 2 {
		t.Fatalf("unexpected retrieved files: %+v", res.RetrievedFiles)
	}
	if !strings.Contains(capturedPrompt, "README.md") {
		t.Fatalf("expected retrieved files in prompt: %q", capturedPrompt)
	}
}
