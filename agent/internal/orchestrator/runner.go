package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
func invokeMCTAgent(ctx context.Context, metaSessionID string, trajDir string, mctSessionID string, args ...string) (int, error) {
	fullArgs := append([]string{"run"}, args...)
	cmd := exec.CommandContext(ctx, "mct-agent", fullArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "MACHTIANI_SESSION_ID="+mctSessionID)

	err := cmd.Run()
	var exitCode int
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	} else {
		exitCode = 0
	}

	if trajDir != "" {
		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":       "mct_invocation",
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
			"session_id": mctSessionID,
			"args":       fullArgs,
			"exit_code":  exitCode,
		})
	}

	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return exitCode, nil
		}
		return exitCode, err
	}
	return exitCode, nil
}

// writeTrajectoryLine appends a JSON line to the trajectory file.
// It never returns an error so that trajectory issues do not break the loop.
func writeTrajectoryLine(dir string, entry interface{}) {
	f, err := os.OpenFile(filepath.Join(dir, "trajectory.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trajectory: open: %v\n", err)
		return
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trajectory: marshal: %v\n", err)
		return
	}
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "trajectory: write: %v\n", err)
	}
}

// invokeMCTAgentRun invokes mct-agent run with an instruction file (-f).
func invokeMCTAgentRun(
	ctx context.Context,
	metaSessionID string,
	trajDir string,
	mctSessionID string,
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
	args = append(args, "-f", instructionFilePath)
	if persistTmpData {
		args = append(args, "--persist-tmp-data")
	}
	return invokeMCTAgent(ctx, metaSessionID, trajDir, mctSessionID, args...)
}

// RunLoop runs the meta-orchestrator loop: invoke mct-agent, evaluate the
// final answer with a conversational LLM, and continue, finish, or block
// based on the orchestrator's decision.
func RunLoop(
	ctx context.Context,
	metaSessionID string,
	mctSessionID string,
	instructionFilePath string,
	mode string,
	model string,
	shellAgentModel string,
	tag string,
	persistTmpData bool,
) (int, error) {
	trajDir := filepath.Join(".machtiani", "meta-orchestrator", "sessions", metaSessionID)

	if err := os.MkdirAll(trajDir, 0755); err != nil {
		return 1, fmt.Errorf("creating trajectory directory: %w", err)
	}

	exitCode, err := invokeMCTAgentRun(ctx, metaSessionID, trajDir, mctSessionID, instructionFilePath, mode, model, shellAgentModel, tag, persistTmpData)
	if err != nil || exitCode != 0 {
		return exitCode, err
	}
	writeTrajectoryLine(trajDir, map[string]interface{}{
		"type":    "llm_message",
		"role":    "system",
		"content": orchestratorSystemPrompt,
	})

	messages := []llm.Message{
		{
			Role:    "system",
			Content: orchestratorSystemPrompt,
		},
	}

	for {
		data, err := os.ReadFile(filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md"))
		if err != nil {
			return 1, fmt.Errorf("reading final answer: %w", err)
		}

		content := strings.TrimSpace(string(data))
		if content == "" {
			return 1, fmt.Errorf("final answer file is empty for session %s", mctSessionID)
		}

		messages = append(messages, llm.Message{Role: "user", Content: content})

		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":    "llm_message",
			"role":    "user",
			"content": content,
		})

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
			args = append(args, "--session-id", mctSessionID)
			args = append(args, "-t", message)
			if persistTmpData {
				args = append(args, "--persist-tmp-data")
			}

			exitCode, err := invokeMCTAgent(ctx, metaSessionID, trajDir, mctSessionID, args...)
			if err != nil || exitCode != 0 {
				return exitCode, err
			}

			messages = append(messages, llm.Message{Role: "assistant", Content: response})

			writeTrajectoryLine(trajDir, map[string]interface{}{
				"type":    "llm_message",
				"role":    "assistant",
				"content": response,
			})
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
