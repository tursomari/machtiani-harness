package agents

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/run"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"github.com/tursomari/machtiani/agent/internal/templates"
)

func (a *DefaultAgent) systemPromptContent() (string, error) {
	if a.RunConfig.SystemPromptCached {
		return a.RunConfig.SystemPrompt, nil
	}

	// When preconstructed messages are provided (via RunWithMessages),
	// preserve the system message from the caller instead of rendering
	// a fresh one from the template. This ensures extra instructions
	// (e.g. per-mode shell-agent guidance) are not discarded.
	if len(a.State.Messages) > 0 && a.State.Messages[0].Role == "system" {
		sysContent := a.State.Messages[0].Content
		a.RunConfig.SystemPrompt = sysContent
		a.RunConfig.SystemPromptCached = true
		return sysContent, nil
	}

	injected := map[string]interface{}{
		"step":        a.State.stepCounter + 1,
		"Step":        a.State.stepCounter + 1,
		"model_calls": a.RunConfig.Model.NCalls() + 1,
		"ModelCalls":  a.RunConfig.Model.NCalls() + 1,
	}
	sysContent, err := a.renderTemplate(a.State.Prompts.Planner.SystemTemplate, injected)
	if err != nil {
		return "", err
	}

	a.RunConfig.SystemPrompt = sysContent
	a.RunConfig.SystemPromptCached = true
	return sysContent, nil
}

func (a *DefaultAgent) renderFormatError(rawErr error) string {
	vars := map[string]interface{}{
		"RawErr":       rawErr.Error(),
		"FailureCount": a.State.consecutiveFormatErrors + 1,
		"FailureLimit": maxConsecutiveFormatErrors,
		"CommandTag":   a.RunConfig.CommandTag,
		"AnswerTag":    a.RunConfig.AnswerTag,
	}
	tmpl := a.State.Prompts.ShellAgent.FormatErrorTemplate
	if tmpl != "" {
		rendered, err := a.renderTemplate(tmpl, vars)
		if err == nil && strings.TrimSpace(rendered) != "" {
			return rendered
		}
	}
	// Render the same embedded template with only known variables, independent
	// of custom templates and environment/model template variables.
	tmpl, err := templates.GetEmbeddedTemplate("shell_agent.format_error_template")
	if err != nil {
		panic(err) // Missing embedded assets are a programming error.
	}
	rendered, err := run.RenderTemplate(tmpl, vars)
	if err != nil {
		panic(err) // Covered by the default/fallback parity tests.
	}
	return rendered
}

func (a *DefaultAgent) renderTemplate(tmpl string, injected map[string]interface{}) (string, error) {
	configVars := run.FlattenConfig(a.RunConfig.ShellAgentConfig(), a.RunConfig.Model.Config(), a.RunConfig.Env.Config())
	envVars := a.RunConfig.Env.GetTemplateVars()
	if envVars == nil {
		envVars = map[string]interface{}{}
	}
	if _, ok := envVars["CWD"]; !ok {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		envVars["cwd"] = filepath.Clean(cwd)
		envVars["CWD"] = filepath.Clean(cwd)
	}
	modelVars := a.RunConfig.Model.GetTemplateVars()

	merged := run.MergeVars(configVars, envVars, modelVars, a.State.ExtraVars, injected)
	// Legacy alias: templates may reference .StepLimit instead of .MaxSteps
	if _, ok := merged["StepLimit"]; !ok {
		if v, ok2 := merged["MaxSteps"]; ok2 {
			merged["StepLimit"] = v
		} else if v, ok2 := merged["max_steps"]; ok2 {
			merged["StepLimit"] = v
		}
	}
	return run.RenderTemplate(tmpl, merged)
}

func (a *DefaultAgent) applyEstimatedTokens(messages []minisweagent.Message) []minisweagent.Message {
	for idx := range messages {
		messages[idx] = a.messageWithEstimatedTokens(messages[idx])
	}
	return messages
}

func (a *DefaultAgent) messageWithEstimatedTokens(msg minisweagent.Message) minisweagent.Message {
	metadata := make(map[string]any, len(msg.Metadata)+1)
	for k, v := range msg.Metadata {
		metadata[k] = v
	}
	metadata["estimated_tokens"] = llm.EstimateTokens(msg.Content)
	msg.Metadata = metadata
	return msg
}
