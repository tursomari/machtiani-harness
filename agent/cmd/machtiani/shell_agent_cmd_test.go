package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// stubLibrary returns a minimal ShellAgentLibrary suitable for testing.
func stubLibrary() *shellagent.ShellAgentLibrary {
	return &shellagent.ShellAgentLibrary{
		Model:  nil,
		Env:    nil,
		Config: &minisweagent.ShellAgentConfig{},
		Prompts: &minisweagent.PromptsConfig{
			Planner: &minisweagent.PlannerPromptsConfig{},
			ShellAgent: &minisweagent.ShellAgentPromptsConfig{
				InstanceTemplate: "--- BEGIN TASK ---\nTask: {{.Task}}\n\n--- END TASK ---\n",
			},
		},
	}
}

func TestShellAgentPromptFlag(t *testing.T) {
	prepareTestConfig(t)

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		return stubLibrary(), nil
	}

	var capturedReq shellagent.Request
	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		capturedReq = req
		return shellagent.Result{Answer: "done", ExitStatus: "Submitted"}, nil
	}

	var exitCode int
	stdout := captureStdout(t, func() {
		exitCode = handleShellAgentCommand([]string{"--prompt", "list files"})
	})

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(stdout, "done") {
		t.Fatalf("expected 'done' in stdout, got %q", stdout)
	}
	// Verify the instance prompt contains the task.
	if len(capturedReq.PreconstructedMessages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(capturedReq.PreconstructedMessages))
	}
	if capturedReq.PreconstructedMessages[0].Role != "system" {
		t.Errorf("expected system role, got %s", capturedReq.PreconstructedMessages[0].Role)
	}
	if capturedReq.PreconstructedMessages[1].Role != "user" {
		t.Errorf("expected user role, got %s", capturedReq.PreconstructedMessages[1].Role)
	}
	if !strings.Contains(capturedReq.PreconstructedMessages[1].Content, "list files") {
		t.Errorf("expected 'list files' in instance prompt, got %q", capturedReq.PreconstructedMessages[1].Content)
	}
}

func TestShellAgentFileFlag(t *testing.T) {
	prepareTestConfig(t)

	tmpDir := t.TempDir()
	taskPath := filepath.Join(tmpDir, "task.txt")
	if err := os.WriteFile(taskPath, []byte("find auth module\n"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		return stubLibrary(), nil
	}

	var capturedReq shellagent.Request
	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		capturedReq = req
		return shellagent.Result{Answer: "result", ExitStatus: "Submitted"}, nil
	}

	exitCode := handleShellAgentCommand([]string{"--file", taskPath})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(capturedReq.PreconstructedMessages[1].Content, "find auth module") {
		t.Errorf("expected 'find auth module' in instance prompt, got %q", capturedReq.PreconstructedMessages[1].Content)
	}
}

func TestShellAgentFileShorthand(t *testing.T) {
	prepareTestConfig(t)

	tmpDir := t.TempDir()
	taskPath := filepath.Join(tmpDir, "task.txt")
	if err := os.WriteFile(taskPath, []byte("check logs\n"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		return stubLibrary(), nil
	}

	var capturedReq shellagent.Request
	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		capturedReq = req
		return shellagent.Result{Answer: "ok", ExitStatus: "Submitted"}, nil
	}

	exitCode := handleShellAgentCommand([]string{"-f", taskPath})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(capturedReq.PreconstructedMessages[1].Content, "check logs") {
		t.Errorf("expected 'check logs' in instance prompt, got %q", capturedReq.PreconstructedMessages[1].Content)
	}
}

func TestShellAgentTextAndFileMutuallyExclusive(t *testing.T) {
	prepareTestConfig(t)

	tmpDir := t.TempDir()
	taskPath := filepath.Join(tmpDir, "task.txt")
	if err := os.WriteFile(taskPath, []byte("from file\n"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleShellAgentCommand([]string{"--prompt", "inline", "--file", taskPath})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("expected 'mutually exclusive' in stderr, got %q", stderr)
	}
}

func TestShellAgentNeitherTextNorFile(t *testing.T) {
	prepareTestConfig(t)

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleShellAgentCommand([]string{})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "one of --prompt or --file is required") {
		t.Fatalf("expected 'one of --prompt or --file is required' in stderr, got %q", stderr)
	}
}

func TestShellAgentEmptyFile(t *testing.T) {
	prepareTestConfig(t)

	tmpDir := t.TempDir()
	taskPath := filepath.Join(tmpDir, "empty.txt")
	if err := os.WriteFile(taskPath, []byte(""), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleShellAgentCommand([]string{"--file", taskPath})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "Provide non-empty content via -p or --file") {
		t.Fatalf("expected 'Provide non-empty content via -p or --file' in stderr, got %q", stderr)
	}
}

func TestShellAgentNonexistentFile(t *testing.T) {
	prepareTestConfig(t)

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleShellAgentCommand([]string{"--file", "/no/such/path/task.txt"})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "Error reading file") {
		t.Fatalf("expected 'Error reading file' in stderr, got %q", stderr)
	}
}

func TestShellAgentPositionalArgumentsError(t *testing.T) {
	prepareTestConfig(t)

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleShellAgentCommand([]string{"--prompt", "task", "extra-arg"})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "Use -p or --file to specify the task") {
		t.Fatalf("expected 'Use -p or --file to specify the task' in stderr, got %q", stderr)
	}
}

func TestShellAgentModelFlagPropagation(t *testing.T) {
	prepareTestConfig(t)

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	var receivedModelAlias string
	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		receivedModelAlias = modelAlias
		return stubLibrary(), nil
	}

	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		return shellagent.Result{Answer: "ok", ExitStatus: "Submitted"}, nil
	}

	exitCode := handleShellAgentCommand([]string{"--model", "gpt4", "--prompt", "test"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if receivedModelAlias != "gpt4" {
		t.Fatalf("expected modelAlias 'gpt4', got %q", receivedModelAlias)
	}
}

func TestShellAgentVerboseFlagPropagation(t *testing.T) {
	prepareTestConfig(t)

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		return stubLibrary(), nil
	}

	var capturedReq shellagent.Request
	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		capturedReq = req
		return shellagent.Result{Answer: "ok", ExitStatus: "Submitted"}, nil
	}

	exitCode := handleShellAgentCommand([]string{"--verbose", "--prompt", "test"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !capturedReq.Verbose {
		t.Fatal("expected Verbose to be true")
	}
}

func TestShellAgentContextLengthPropagation(t *testing.T) {
	prepareTestConfig(t)

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		return stubLibrary(), nil
	}

	var capturedReq shellagent.Request
	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		capturedReq = req
		return shellagent.Result{Answer: "ok", ExitStatus: "Submitted"}, nil
	}

	exitCode := handleShellAgentCommand([]string{"--context-length", "64000", "--prompt", "test"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	want, _ := llm.BudgetForContextLength(64000, llm.ContextSourceSessionFlag)
	if capturedReq.MaxInputTokens != want.MaxInputTokens {
		t.Fatalf("expected MaxInputTokens %d, got %d", want.MaxInputTokens, capturedReq.MaxInputTokens)
	}
}

func TestShellAgentRemovedMaxInputTokens(t *testing.T) {
	prepareTestConfig(t)

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleShellAgentCommand([]string{"--max-input-tokens", "4096", "--prompt", "test"})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "was removed; use --context-length") {
		t.Fatalf("expected migration guidance in stderr, got %q", stderr)
	}
}

func TestShellAgentBuildLibraryError(t *testing.T) {
	prepareTestConfig(t)

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		return nil, errors.New("build library failed")
	}

	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		t.Fatal("Run should not be called when BuildLibrary fails")
		return shellagent.Result{}, nil
	}

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleShellAgentCommand([]string{"--prompt", "test"})
	})
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(stderr, "Error building shell-agent library") {
		t.Fatalf("expected 'Error building shell-agent library' in stderr, got %q", stderr)
	}
}

func TestShellAgentRunError(t *testing.T) {
	prepareTestConfig(t)

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		return stubLibrary(), nil
	}

	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		return shellagent.Result{}, errors.New("run failed")
	}

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleShellAgentCommand([]string{"--prompt", "test"})
	})
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(stderr, "Error running shell-agent") {
		t.Fatalf("expected 'Error running shell-agent' in stderr, got %q", stderr)
	}
}

func TestShellAgentRunResultError(t *testing.T) {
	prepareTestConfig(t)

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		return stubLibrary(), nil
	}

	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		return shellagent.Result{Error: errors.New("agent error")}, nil
	}

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleShellAgentCommand([]string{"--prompt", "test"})
	})
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(stderr, "agent error") {
		t.Fatalf("expected 'agent error' in stderr, got %q", stderr)
	}
}

func TestShellAgentHelpExitsZero(t *testing.T) {
	prepareTestConfig(t)

	exitCode := handleShellAgentCommand([]string{"--help"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
}

func TestShellAgentUsageLine(t *testing.T) {
	fs := pflag.NewFlagSet("machtiani shell-agent", pflag.ContinueOnError)
	var apiKeyFlags multiString
	fs.BoolP("verbose", "v", false, "verbose agent logging")
	fs.Int("context-length", 0, "total input-plus-output token context")
	fs.String("model", "", "Model alias defined in .machtiani/config.toml")
	fs.StringP("file", "f", "", "Read task from file (mutually exclusive with --prompt)")
	var promptText string
	fs.StringVarP(&promptText, "prompt", "p", "", "Inline task prompt (mutually exclusive with --file)")
	fs.Var(&apiKeyFlags, "api-key", "Provider-specific API key override in provider:key format (repeatable)")

	var buf strings.Builder
	fs.SetOutput(&buf)
	fs.Usage = func() {
		fmt.Fprintf(&buf, "Usage: machtiani shell-agent --prompt \"<task>\" | --file <path> [flags]\n\n")
		fmt.Fprintln(&buf, "Flags:")
		fs.PrintDefaults()
	}
	fs.Usage()

	output := buf.String()
	for _, flag := range []string{"--prompt", "--file", "--model", "--verbose", "--context-length"} {
		if !strings.Contains(output, flag) {
			t.Errorf("expected %q in usage output, got %q", flag, output)
		}
	}
}

func TestShellAgentLegacyPromptFlagsRejected(t *testing.T) {
	for _, flag := range []string{"--" + "text", "-" + "t"} {
		var exitCode int
		stderr := captureStderr(t, func() {
			exitCode = handleShellAgentCommand([]string{flag, "task"})
		})
		if exitCode != 2 {
			t.Fatalf("%s exit code = %d, want 2", flag, exitCode)
		}
		if !strings.Contains(stderr, "unknown") {
			t.Fatalf("%s stderr = %q, want unknown-flag error", flag, stderr)
		}
	}
}

func TestPrintUsageIncludesShellAgent(t *testing.T) {
	// Temporarily add shell-agent to cliCommands for this test.
	origCommands := cliCommands
	cliCommands = append(cliCommands, cliCommand{
		name:        "shell-agent",
		description: "Run a shell-agent task directly (no planner)",
		handler:     handleShellAgentCommand,
	})
	t.Cleanup(func() {
		cliCommands = origCommands
	})

	stderr := captureStderr(t, func() {
		printUsage()
	})
	if !strings.Contains(stderr, "shell-agent") {
		t.Fatalf("expected 'shell-agent' in usage output, got %q", stderr)
	}
}

func TestShellAgentTagFlagSetsBothTags(t *testing.T) {
	prepareTestConfig(t)

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		return stubLibrary(), nil
	}

	var capturedReq shellagent.Request
	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		capturedReq = req
		return shellagent.Result{Answer: "done", ExitStatus: "Submitted"}, nil
	}

	var exitCode int
	stdout := captureStdout(t, func() {
		exitCode = handleShellAgentCommand([]string{"--prompt", "test task", "--tag", "foo"})
	})

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if capturedReq.AnswerTag != "answer-foo" {
		t.Fatalf("expected AnswerTag 'answer-foo', got %q", capturedReq.AnswerTag)
	}
	if capturedReq.CommandTag != "command-foo" {
		t.Fatalf("expected CommandTag 'command-foo', got %q", capturedReq.CommandTag)
	}
	_ = stdout
}

func TestShellAgentTagAndAnswerTagMutuallyExclusive(t *testing.T) {
	prepareTestConfig(t)

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleShellAgentCommand([]string{"--prompt", "test task", "--tag", "foo", "--answer-tag", "bar"})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "mutually exclusive") && !strings.Contains(stderr, "conflict") && !strings.Contains(stderr, "Error") {
		t.Fatalf("expected error about mutually exclusive flags in stderr, got %q", stderr)
	}
}

func TestShellAgentDefaultCommandTag(t *testing.T) {
	prepareTestConfig(t)

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		return stubLibrary(), nil
	}

	var capturedReq shellagent.Request
	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		capturedReq = req
		return shellagent.Result{Answer: "done", ExitStatus: "Submitted"}, nil
	}

	var exitCode int
	stdout := captureStdout(t, func() {
		exitCode = handleShellAgentCommand([]string{"--prompt", "test task"})
	})

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if capturedReq.AnswerTag != "answer" {
		t.Fatalf("expected AnswerTag 'answer', got %q", capturedReq.AnswerTag)
	}
	if capturedReq.CommandTag != "command" {
		t.Fatalf("expected CommandTag 'command', got %q", capturedReq.CommandTag)
	}
	_ = stdout
}

func TestShellAgentAnswerTagAlone(t *testing.T) {
	prepareTestConfig(t)

	origBuild := shellAgentBuildLibFn
	origRun := shellAgentRunFn
	t.Cleanup(func() {
		shellAgentBuildLibFn = origBuild
		shellAgentRunFn = origRun
	})

	shellAgentBuildLibFn = func(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*shellagent.ShellAgentLibrary, error) {
		return stubLibrary(), nil
	}

	var capturedReq shellagent.Request
	shellAgentRunFn = func(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
		capturedReq = req
		return shellagent.Result{Answer: "done", ExitStatus: "Submitted"}, nil
	}

	var exitCode int
	stdout := captureStdout(t, func() {
		exitCode = handleShellAgentCommand([]string{"--prompt", "test task", "--answer-tag", "bar"})
	})

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if capturedReq.AnswerTag != "bar" {
		t.Fatalf("expected AnswerTag 'bar', got %q", capturedReq.AnswerTag)
	}
	if capturedReq.CommandTag != "command" {
		t.Fatalf("expected CommandTag 'command', got %q", capturedReq.CommandTag)
	}
	_ = stdout
}
