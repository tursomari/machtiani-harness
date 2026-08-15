package prompt

import (
	"context"
	"fmt"
	"os"
	"strings"

	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// ShellAgentOnlyResult captures the shell-agent output when running without the chat model.
type ShellAgentOnlyResult struct {
	Summary            string
	Context            string
	Verbatim           string
	TrajectoryPath     string
	TrajectoryMessages []minisweagent.Message `json:"trajectory_messages,omitempty"`
	Cancelled          bool
}

// RunShellAgentOnly runs the shell agent on the prompt and returns its output
// without invoking the chat model or mutating session history.
func RunShellAgentOnly(ctx context.Context, opts RunOptions, req shellagent.Request) (ShellAgentOnlyResult, error) {
	var res ShellAgentOnlyResult

	if err := validateMCTPromptsConfig(opts.Prompts); err != nil {
		return res, err
	}

	origSession := os.Getenv("MACHTIANI_SESSION_ID")
	restoreSession := false
	if opts.SessionID != "" && origSession != opts.SessionID {
		if err := os.Setenv("MACHTIANI_SESSION_ID", opts.SessionID); err != nil {
			return res, fmt.Errorf("set session id: %w", err)
		}
		restoreSession = true
	}
	if restoreSession {
		defer func() {
			if origSession == "" {
				_ = os.Unsetenv("MACHTIANI_SESSION_ID")
				return
			}
			_ = os.Setenv("MACHTIANI_SESSION_ID", origSession)
		}()
	}

	if strings.TrimSpace(opts.ShellAgentModel) == "" {
		candidate := strings.TrimSpace(opts.Runtime.Alias)
		if candidate == "" {
			candidate = strings.TrimSpace(opts.Runtime.Resolved.Alias)
		}
		if candidate != "" {
			opts.ShellAgentModel = candidate
		}
	}

	if req.PreconstructedMessages == nil || len(req.PreconstructedMessages) == 0 {
		return res, fmt.Errorf("shell-agent request has no preconstructed messages")
	}
	result, err := shellAgentRun(ctx, req)
	if err != nil {
		return res, err
	}
	res.TrajectoryPath = result.TrajectoryPath
	res.TrajectoryMessages = result.Trajectory.Messages
	if result.Error != nil {
		return res, fmt.Errorf("shell-agent failed: %w", result.Error)
	}
	if result.ExitStatus != "Submitted" {
		res.Summary = strings.TrimSpace(result.Answer)
		res.Cancelled = true
		return res, nil
	}
	verbatimBlock := strings.TrimSpace(result.Answer)
	if verbatimBlock == "" {
		return res, fmt.Errorf("shell-agent submitted an empty final answer")
	}
	contextBlock := ""
	res.Context = strings.TrimSpace(contextBlock)
	res.Verbatim = strings.TrimSpace(verbatimBlock)
	res.Summary = strings.TrimSpace(res.Verbatim)
	if res.Summary == "" {
		res.Summary = res.Context
	}
	return res, nil
}
