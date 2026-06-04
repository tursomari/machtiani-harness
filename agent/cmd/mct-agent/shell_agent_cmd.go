package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
)

var (
	shellAgentRunFn      = shellagent.Run
	shellAgentBuildLibFn = shellagent.BuildLibrary
)

func handleShellAgentCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent shell-agent", pflag.ContinueOnError)
	var apiKeyFlags multiString
	verbose := fs.BoolP("verbose", "v", false, "verbose agent logging")
	maxInputTokens := fs.Int("max-input-tokens", 0, "maximum number of tokens allowed in constructed prompts (0 disables truncation)")
	maxCommandOutputBytes := fs.Int("max-command-output-bytes", 65536, "maximum bytes of shell command output captured per step (default 64KB)")
	modelFlag := fs.String("model", "", "Model alias defined in .machtiani/config.toml")
	promptFile := fs.StringP("file", "f", "", "Read task from file (mutually exclusive with --text)")
	var promptText string
	fs.StringVarP(&promptText, "text", "t", "", "Task text (mutually exclusive with --file)")
	fs.Var(&apiKeyFlags, "api-key", "Provider-specific API key override in provider:key format (repeatable)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent shell-agent --text \"<task>\" | --file <path> [flags]\n\n")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	// Validate task input: exactly one of --text or --file is required.
	hasText := strings.TrimSpace(promptText) != ""
	hasFile := strings.TrimSpace(*promptFile) != ""
	if hasText && hasFile {
		fmt.Fprintln(os.Stderr, "Error: --text and --file are mutually exclusive")
		return 2
	}
	if !hasText && !hasFile {
		fmt.Fprintln(os.Stderr, "Error: one of --text or --file is required")
		return 2
	}

	var task string
	if hasFile {
		data, err := os.ReadFile(strings.TrimSpace(*promptFile))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			return 2
		}
		task = strings.TrimSpace(string(data))
	} else {
		task = strings.TrimSpace(promptText)
	}
	if task == "" {
		fmt.Fprintln(os.Stderr, "Error: task is empty. Provide non-empty content via -t or --file.")
		return 2
	}

	parsedArgs := fs.Args()
	if len(parsedArgs) > 0 {
		fmt.Fprintln(os.Stderr, "Error: unexpected positional arguments for 'shell-agent' command. Use -t or --file to specify the task.")
		return 2
	}

	if *maxInputTokens < 0 {
		fmt.Fprintln(os.Stderr, "Error: --max-input-tokens must be zero or positive")
		return 2
	}

	// Load global configuration.
	globalCfg, _, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}

	// Override model alias if --model flag provided.
	if *modelFlag != "" {
		if globalCfg.Model == nil {
			globalCfg.Model = &llm.ModelConfig{}
		}
		globalCfg.Model.ModelName = *modelFlag
	}

	apiOverrides, err := llm.ParseAPIKeyOverrides(apiKeyFlags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	// Apply max-command-output-bytes flag to override config file value.
	if *maxCommandOutputBytes > 0 {
		if globalCfg.Environment == nil {
			globalCfg.Environment = &llm.EnvironmentConfig{}
		}
		globalCfg.Environment.MaxCommandOutputBytes = *maxCommandOutputBytes
	}

	// Build the shell-agent library (model + environment) once.
	lib, err := shellAgentBuildLibFn(&globalCfg, apiOverrides, false, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building shell-agent library: %v\n", err)
		return 1
	}

	// Render prompts.
	sysPrompt, err := shellagent.RenderSystemPrompt(lib.Prompts, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error rendering system prompt: %v\n", err)
		return 1
	}

	instPrompt, err := shellagent.RenderInstancePrompt(lib.Prompts, task, lib.Config, lib.Env, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error rendering instance prompt: %v\n", err)
		return 1
	}

	// Build the message array: system prompt + instance prompt.
	messages := []llm.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: instPrompt},
	}

	// Build the request.
	req := shellagent.Request{
		PreconstructedMessages: messages,
		Model:                  lib.Model,
		Env:                    lib.Env,
		Config:                 lib.Config,
		Prompts:                lib.Prompts,
		Verbose:                *verbose,
		MaxInputTokens:         *maxInputTokens,
	}

	// Run the shell-agent loop.
	ctx := context.Background()
	result, err := shellAgentRunFn(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running shell-agent: %v\n", err)
		return 1
	}
	if result.Error != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", result.Error)
		return 1
	}

	fmt.Println(result.Answer)
	return 0
}