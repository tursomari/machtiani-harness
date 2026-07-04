package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"

)

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

// RunLoop runs the meta-orchestrator loop: invoke mct-agent, classify the
// result, and continue if the agent asked for permission instead of providing
// a real solution.
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

	for {
		result, continuationPrompt, err := ClassifyFinalAnswer(ctx, sessionID, model)
		if err != nil {
			return 1, fmt.Errorf("classification error: %w", err)
		}

		switch result {
		case ClassificationRealSolution:
			return 0, nil
		case ClassificationHardBlocker:
			return 1, nil
		case ClassificationNotRealSolution:
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
			args = append(args, "-t", continuationPrompt)
			if persistTmpData {
				args = append(args, "--persist-tmp-data")
			}

			exitCode, err := invokeMCTAgent(ctx, sessionID, args...)
			if err != nil || exitCode != 0 {
				return exitCode, err
			}
		default:
			return 1, fmt.Errorf("unexpected classification result: %s", string(result))
		}
	}
}
