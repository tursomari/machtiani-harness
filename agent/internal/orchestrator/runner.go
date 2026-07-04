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

const orchestratorSystemPrompt = `You are a meta-orchestrator agent. Your job is to instruct mct-agent to completion through a rigorous, multi-stage validation process. You will receive the contents of mct-agents agent-final-answer.md after each invocation. Evaluate the current state of the work and guide the agent accordingly.

Respond with exactly one of these three formats:

ACTION: CONTINUE
MESSAGE: <authoritative instruction telling mct-agent what to do next>

ACTION: DONE
MESSAGE: <empty or a brief acknowledgment>

ACTION: BLOCKED
MESSAGE: <description of the hard blocker>

Rules and Phases of Operation:

1. **Active Implementation:**
   - If the final answer is a permission ask (e.g. "Shall I proceed?"), a status report, a design discussion, a recommendation without action, or anything short of an actual implemented solution, respond with ACTION: CONTINUE. Provide a firm, authoritative instruction telling mct-agent to implement the solution fully and not ask for permission again.

2. **Phase 1 - The First Sanity Check (First Completion Claim):**
   - When the agent FIRST reports that the implementation work is finished and committed, DO NOT accept it as done.
   - Respond with ACTION: CONTINUE and explicitly instruct the agent to:
     a) Rigorously sanity-check its implementation to ensure it meets ALL original criteria of the issue.
     b) Find and run any relevant existing tests in the repository. If no relevant tests exist, create temporary test files to validate the implementation against the original criteria. Run the tests and fix the implementation until all pass. Then delete any temporary test files you created so they are not included in the final commit.
     c) Base this check strictly on the actual code and requirements—do not rely on its past answers, investigative work, or assumptions.
     d) Update and fix the implementation as needed.

3. **Phase 2 - The Reset and Re-Execution (Second Completion Claim):**
   - When the agent returns claiming the first sanity check and subsequent fixes are complete, DO NOT accept it as done.
   - Respond with ACTION: CONTINUE and instruct the agent to:
     a) Re-read the original instruction file (/app/instruction.md) to ground the re-implementation in the original requirements.
     b) Reset the git project completely and discard all current changes.
     c) Taking everything it learned from the first implementation attempt, create a brand new plan from scratch with an actionable checklist.
     d) Execute that plan fully and commit the new code.

4. **Phase 3 - The Final Sanity Check (Third Completion Claim):**
   - When the agent reports that the second implementation is finished, DO NOT accept it as done.
   - Respond with ACTION: CONTINUE and explicitly instruct the agent to:
     a) Perform a final, rigorous sanity check on this new implementation to ensure it perfectly matches the original criteria.
     b) Find and run any relevant existing tests in the repository. If no relevant tests exist, create temporary test files to validate the implementation against the original criteria. Run the tests and fix the implementation until all pass. Then delete any temporary test files you created so they are not included in the final commit.
     c) Again, base this check strictly on the code and requirements, not assumptions.
     d) Fix any final issues and commit the changes.

5. **Phase 4 - Final Verification:**
   - Only AFTER the agent has completed the Phase 3 "Final Sanity Check" and returns with the polished implementation, evaluate it for true completion.
   - a) Confirm no stray test files remain in the working tree. Delete any temporary test files that were created during validation.
   - b) If the deliverables are fully executed and complete, respond with ACTION: DONE.

Global Rules:
- If the final answer describes an irrecoverable hard blocker (e.g. no API credits available, critical missing dependency that cannot be resolved), respond with ACTION: BLOCKED.
- Maintain context across rounds. Use the chat history to determine which phase the agent is currently in. Escalate the firmness of your instructions if mct-agent stalls or attempts to bypass a phase.`

// extractPrompt scans the args slice for "-f" or "-t" and returns the
// associated prompt text. If "-f" is found, the next element is treated as a
// file path and its contents are read and returned. If "-t" is found, the
// next element is returned directly. Returns an empty string if neither flag
// is present.
func extractPrompt(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f":
			if i+1 < len(args) {
				data, err := os.ReadFile(args[i+1])
				if err == nil {
					return strings.TrimSpace(string(data))
				}
			}
		case "-t":
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	return ""
}

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
			"prompt":     extractPrompt(fullArgs),
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
