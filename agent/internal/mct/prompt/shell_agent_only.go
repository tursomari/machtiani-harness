package prompt

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/mct/internal/contextbuilder"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/session"
)

// ShellAgentOnlyResult captures the shell-agent output when running without the chat model.
type ShellAgentOnlyResult struct {
	Summary        string
	Context        string
	Verbatim       string
	TrajectoryPath string
}

// RunShellAgentOnly runs the shell agent on the prompt and returns its output
// without invoking the chat model or mutating session history.
func RunShellAgentOnly(ctx context.Context, opts RunOptions) (ShellAgentOnlyResult, error) {
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

	hist, err := session.LoadHistory()
	if err != nil {
		hist = []contextbuilder.Message{}
	}

	if opts.SessionID != "" {
		historyNote := ""
		if len(hist) > 0 {
			historyNote = "Review the session history above and "
		}
		opts.Prompt = fmt.Sprintf(
			"Continue this session. %sAnalyze the conversation history and decide what action to take or how to respond: %s",
			historyNote, opts.Prompt)
	}

	historyTemplate := ""
	if opts.Prompts != nil {
		historyTemplate = opts.Prompts.ConversationHistoryTemplate
	}
	combined, _, err := contextbuilder.Build(
		opts.Prompt,
		nil,
		hist,
		contextbuilder.Options{
			IncludeHistory:  opts.IncludeHistory,
			MaxInputTokens:  opts.MaxInputTokens,
			PreludeTemplate: historyTemplate,
		},
	)
	if err != nil {
		return res, err
	}

	directiveBlock := formatResponseDirectives(opts.ResponseDirectives)
	if directiveBlock != "" {
		if strings.TrimSpace(combined) != "" {
			combined = combined + "\n\n" + directiveBlock
		} else {
			combined = directiveBlock
		}
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

	contextBlock, verbatimBlock, trajectoryPath, err := invokeShellAgent(ctx, combined, opts)
	if err != nil {
		return res, err
	}

	res.Context = strings.TrimSpace(contextBlock)
	res.Verbatim = strings.TrimSpace(verbatimBlock)
	res.TrajectoryPath = strings.TrimSpace(trajectoryPath)
	res.Summary = strings.TrimSpace(res.Verbatim)
	if res.Summary == "" {
		res.Summary = res.Context
	}
	return res, nil
}
