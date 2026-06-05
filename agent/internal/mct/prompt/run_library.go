package prompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
)

// runShellAgentLibrary executes the shell-agent in-process using the
// pre-built message array and library configuration from opts.
//
// It renders the instance prompt, combines it with the pre-built
// messages, and calls shellagent.Run. The result is returned as
// contextBlock (empty), verbatimBlock (the answer), and trajectoryPath.
func runShellAgentLibrary(ctx context.Context, task string, opts RunOptions) (contextBlock string, verbatimBlock string, trajectoryPath string, err error) {
	lib := opts.ShellAgentLibrary
	if lib == nil {
		return "", "", "", fmt.Errorf("shell-agent library config is nil")
	}

	// Build the complete message array.
	messages := make([]llm.Message, 0, len(lib.PrebuiltMessages)+1)
	messages = append(messages, lib.PrebuiltMessages...)

	extraVars := map[string]interface{}{}
	if lib.FewShotVariant == "instance" && lib.TurnIndex < 3 {
		extraVars["ShowFewShot"] = true
	}
	// Render and append the instance prompt.
	instPrompt, err := shellagent.RenderInstancePrompt(lib.Prompts, task, lib.Config, lib.Env, extraVars)
	if err != nil {
		return "", "", "", fmt.Errorf("render instance prompt: %w", err)
	}
	messages = append(messages, llm.Message{Role: "user", Content: instPrompt})


	req := shellagent.Request{
		PreconstructedMessages: messages,
		Config:                 lib.Config,
		Prompts:                lib.Prompts,
		Model:                  lib.Model,
		Env:                    lib.Env,
		Verbose:                opts.Verbose,
		MaxInputTokens:         opts.MaxInputTokens,
		SessionID:              opts.ShellAgentSessionID,
		PlannerTurn:            lib.TurnIndex,
		EnforceEarlyCommands:   lib.EnforceEarlyCommands,
	}

	result, runErr := shellagent.Run(ctx, req)
	if runErr != nil {
		return "", "", "", runErr
	}

	answer := strings.TrimSpace(result.Answer)
	return "", answer, "", nil
}