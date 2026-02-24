package prompt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestRunShellAgentOnlySkipsChatAndHistory(t *testing.T) {
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

	sessionID := "shell-agent-only"

	origChat := chatStreamWithRuntime
	chatStreamWithRuntime = func(ctx context.Context, resolved llm.ResolvedModel, aliases []string, fallbacks []llm.ResolvedModel, extras map[string]any, msgs []llm.Message, onToken func(string)) (string, error) {
		t.Fatalf("chat stream should not run for shell-only execution")
		return "", nil
	}
	t.Cleanup(func() { chatStreamWithRuntime = origChat })

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "shell-agent.log")
	scriptPath := filepath.Join(binDir, "shell-agent")
	script := "#!/bin/sh\n" +
		"set -e\n" +
		"printf '%s\\n' \"$@\" > \"$SHELL_AGENT_TEST_LOG\"\n" +
		"printf '%s\\n' \"BEGIN_SHELL_AGENT_RESULT\"\n" +
		"printf '%s\\n' \"shell agent output\"\n" +
		"printf '%s\\n' \"END_SHELL_AGENT_RESULT\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write shell-agent stub: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("SHELL_AGENT_TEST_LOG", logPath)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	sessionPath := filepath.Join(home, ".machtiani", "sessions", "session-"+sessionID+".json")
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("expected no session file before run, got %v", err)
	}

	opts := RunOptions{
		Prompt:         "Collect deployment diagnostics",
		Mode:           "answer-only",
		SessionID:      sessionID,
		IncludeHistory: true,
		Runtime:        ModelRuntime{Resolved: llm.ResolvedModel{Model: "test-model", APIKey: "key", BaseURL: "https://example.com"}},
		ShellAgent:     true,
		Prompts:        testPromptsConfig(),
	}
	res, err := RunShellAgentOnly(context.Background(), opts)
	if err != nil {
		t.Fatalf("RunShellAgentOnly returned error: %v", err)
	}
	if res.Summary != "shell agent output" {
		t.Fatalf("expected shell agent output, got %q", res.Summary)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read shell-agent log: %v", err)
	}
	logLine := strings.TrimSpace(string(data))
	if !strings.Contains(logLine, "Collect deployment diagnostics") {
		t.Fatalf("shell-agent invocation missing prompt: %q", logLine)
	}
	if !strings.Contains(logLine, shellAgentPromptNoticeText(opts.Prompts)) {
		t.Fatalf("shell-agent invocation missing notice: %q", logLine)
	}

	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("expected no session file after run, got %v", err)
	}
}
