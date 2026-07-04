package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

const orchestratorSystemPrompt = `You are a meta-orchestrator agent. Your job is to instruct mct-agent to completion. You will receive the contents of mct-agents agent-final-answer.md after each invocation. Evaluate whether the work is genuinely complete.

Respond with exactly one of these three formats:

ACTION: CONTINUE
MESSAGE: <authoritative instruction telling mct-agent what to do next>

ACTION: DONE
MESSAGE: <empty or a brief acknowledgment>

ACTION: BLOCKED
MESSAGE: <description of the hard blocker>

Rules:
- If the final answer shows a genuinely completed solution with concrete code changes, implemented work, or fully executed deliverables, respond with ACTION: DONE.
- If the final answer is a permission ask (e.g. "Shall I proceed?"), a status report, a design discussion, a recommendation without action, or anything short of an actual implemented solution, respond with ACTION: CONTINUE and provide a firm, authoritative instruction telling mct-agent to continue working, implement the solution fully, and not ask for permission again. The instruction must be a direct command, not a suggestion. Tell mct-agent exactly what to implement.
- If the final answer describes an irrecoverable hard blocker (e.g. no API credits available, critical missing dependency that cannot be resolved), respond with ACTION: BLOCKED.
- Maintain context across rounds. If mct-agent continues to stall or ask for permission, escalate the firmness of your instructions.`

// invokeMCTAgent builds and executes an mct-agent command with the given
// arguments. It prepends "run" as the subcommand and returns the exit code
// of the subprocess and any error.
func invokeMCTAgent(ctx context.Context, sessionID string, args ...string) (int, error) {
	fullArgs := append([]string{"run"}, args...)
	cmd := exec.CommandContext(ctx, "mct-agent", fullArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "MACHTIANI_SESSION_ID="+sessionID)

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

// invokeMCTAgentRun invokes mct-agent run with an instruction file (-f).
func invokeMCTAgentRun(
	ctx context.Context,
	sessionID string,
	instructionFilePath string,
	mode string,
	model string,
	shellAgentModel string,
	tag string,
	persistTmpData bool,
) (int, error) {
	var args []string
	if mode != "" {
		args = append(args, "--mode", mode)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if shellAgentModel != "" {
		args = append(args, "--shell-agent-model", shellAgentModel)
	}
	if tag != "" {
		args = append(args, "--tag", tag)
	}
	if sessionID != "" {
		args = append(args, "--session-id", sessionID)
	}
	args = append(args, "-f", instructionFilePath)
	if persistTmpData {
		args = append(args, "--persist-tmp-data")
	}
	return invokeMCTAgent(ctx, sessionID, args...)
}

// RunLoop runs the meta-orchestrator loop: invoke mct-agent, evaluate the
// final answer with a conversational LLM, and continue, finish, or block
// based on the orchestrator's decision.
func RunLoop(
	ctx context.Context,
	sessionID string,
	instructionFilePath string,
	mode string,
	model string,
	shellAgentModel string,
	tag string,
	persistTmpData bool,
) (int, error) {
	exitCode, err := invokeMCTAgentRun(ctx, sessionID, instructionFilePath, mode, model, shellAgentModel, tag, persistTmpData)
	if err != nil || exitCode != 0 {
		return exitCode, err
	}

	messages := []llm.Message{
		{
			Role:    "system",
			Content: orchestratorSystemPrompt,
		},
	}

	for {
		data, err := os.ReadFile(filepath.Join(".machtiani", "sessions", sessionID, "chat", "agent-final-answer.md"))
		if err != nil {
			return 1, fmt.Errorf("reading final answer: %w", err)
		}

		content := strings.TrimSpace(string(data))
		if content == "" {
			return 1, fmt.Errorf("final answer file is empty for session %s", sessionID)
		}

		messages = append(messages, llm.Message{Role: "user", Content: content})

		response, err := llm.Chat(ctx, model, nil, messages)
		if err != nil {
			return 1, fmt.Errorf("orchestrator LLM call failed: %w", err)
		}

		var actionLine string
		var messageLine string
		for _, line := range strings.Split(response, "\n") {
			trimmed := strings.TrimSpace(line)
			if actionLine == "" && strings.HasPrefix(strings.ToUpper(trimmed), "ACTION:") {
				actionLine = trimmed
			}
			if messageLine == "" && strings.HasPrefix(strings.ToUpper(trimmed), "MESSAGE:") {
				messageLine = trimmed
			}
		}

		if actionLine == "" {
			return 1, fmt.Errorf("orchestrator response missing ACTION line; raw: %s", response)
		}

		action := strings.TrimSpace(strings.TrimPrefix(actionLine, "ACTION:"))

		switch action {
		case "CONTINUE":
			message := strings.TrimSpace(strings.TrimPrefix(messageLine, "MESSAGE:"))
			if message == "" {
				return 1, fmt.Errorf("CONTINUE action requires a MESSAGE; raw: %s", response)
			}

			var args []string
			if mode != "" {
				args = append(args, "--mode", mode)
			}
			if model != "" {
				args = append(args, "--model", model)
			}
			if shellAgentModel != "" {
				args = append(args, "--shell-agent-model", shellAgentModel)
			}
			if tag != "" {
				args = append(args, "--tag", tag)
			}
			args = append(args, "--session-id", sessionID)
			args = append(args, "-t", message)
			if persistTmpData {
				args = append(args, "--persist-tmp-data")
			}

			exitCode, err := invokeMCTAgent(ctx, sessionID, args...)
			if err != nil || exitCode != 0 {
				return exitCode, err
			}

			messages = append(messages, llm.Message{Role: "assistant", Content: response})
		case "DONE":
			return 0, nil
		case "BLOCKED":
			message := strings.TrimSpace(strings.TrimPrefix(messageLine, "MESSAGE:"))
			return 1, fmt.Errorf("hard blocker: %s", message)
		default:
			return 1, fmt.Errorf("unexpected orchestrator ACTION %q; raw: %s", action, response)
		}
	}
}
