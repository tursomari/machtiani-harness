package agents

import (
	"strings"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/run"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// DefaultAgent implements the core reasoning loop used by shell-agent-go.
type DefaultAgent struct {
	// RunConfig is the AgentRunConfig holding immutable run configuration.
	RunConfig *AgentRunConfig
	// State is the AgentRunState holding mutable runtime state.
	State *AgentRunState
}

// NewDefaultAgent constructs a DefaultAgent.
func NewDefaultAgent(model minisweagent.Model, env minisweagent.Environment, cfg *minisweagent.ShellAgentConfig, prompts *minisweagent.PromptsConfig, opts ...DefaultAgentOption) *DefaultAgent {
	if cfg == nil {
		cfg = &minisweagent.ShellAgentConfig{}
	}
	if prompts == nil {
		prompts = &minisweagent.PromptsConfig{}
	}
	plannerPrompts := prompts.Planner
	if plannerPrompts == nil {
		plannerPrompts = &minisweagent.PlannerPromptsConfig{}
	}
	if strings.TrimSpace(plannerPrompts.SystemTemplate) == "" {
		plannerPrompts.SystemTemplate, _ = run.TemplateWithFallback(plannerPrompts.SystemTemplate, "shell_agent.system_template")
	}
	if strings.TrimSpace(plannerPrompts.InstanceTemplate) == "" {
		plannerPrompts.InstanceTemplate, _ = run.TemplateWithFallback(plannerPrompts.InstanceTemplate, "planner.instance_template")
	}
	shellPrompts := prompts.ShellAgent
	if shellPrompts == nil {
		shellPrompts = &minisweagent.ShellAgentPromptsConfig{}
	}
	if plannerPrompts.SystemTemplate == "" && shellPrompts.LightweightSystemTemplate != "" {
		plannerPrompts.SystemTemplate = shellPrompts.LightweightSystemTemplate
	}
	if strings.TrimSpace(shellPrompts.SystemTemplate) == "" {
		shellPrompts.SystemTemplate, _ = run.TemplateWithFallback(shellPrompts.SystemTemplate, "shell_agent.system_template")
	}
	if strings.TrimSpace(shellPrompts.InstanceTemplate) == "" {
		shellPrompts.InstanceTemplate, _ = run.TemplateWithFallback(shellPrompts.InstanceTemplate, "shell_agent.instance_template")
	}
	if strings.TrimSpace(shellPrompts.TimeoutTemplate) == "" {
		shellPrompts.TimeoutTemplate, _ = run.TemplateWithFallback(shellPrompts.TimeoutTemplate, "shell_agent.timeout_template")
	}
	if strings.TrimSpace(shellPrompts.FormatErrorTemplate) == "" {
		shellPrompts.FormatErrorTemplate, _ = run.TemplateWithFallback(shellPrompts.FormatErrorTemplate, "shell_agent.format_error_template")
	}
	if strings.TrimSpace(shellPrompts.ActionObservationTemplate) == "" {
		shellPrompts.ActionObservationTemplate, _ = run.TemplateWithFallback(shellPrompts.ActionObservationTemplate, "shell_agent.action_observation_template")
	}
	if strings.TrimSpace(shellPrompts.LightweightSystemTemplate) == "" {
		shellPrompts.LightweightSystemTemplate, _ = run.TemplateWithFallback(shellPrompts.LightweightSystemTemplate, "shell_agent.lightweight_system_template")
	}
	if strings.TrimSpace(shellPrompts.LightweightIntentTemplate) == "" {
		shellPrompts.LightweightIntentTemplate, _ = run.TemplateWithFallback(shellPrompts.LightweightIntentTemplate, "shell_agent.lightweight_intent_template")
	}
	if strings.TrimSpace(shellPrompts.LightweightErrorTemplate) == "" {
		shellPrompts.LightweightErrorTemplate, _ = run.TemplateWithFallback(shellPrompts.LightweightErrorTemplate, "shell_agent.lightweight_error_template")
	}
	effectivePrompts := &minisweagent.PromptsConfig{
		Planner:    plannerPrompts,
		ShellAgent: shellPrompts,
	}

	sessionID := minisweagent.GenerateSessionID()

	agent := &DefaultAgent{}

	agent.RunConfig = &AgentRunConfig{
		Model:                model,
		Env:                  env,
		Task:                 "",
		SessionID:            sessionID,
		MaxInputTokens:       0,
		PlannerTurn:          0,
		EnforceEarlyCommands: false,
		MaxSteps:              cfg.MaxSteps,
		FinalizeRemainingSteps: cfg.FinalizeRemainingSteps,
		AnswerTag:            "answer",
		CommandTag:           "command",
		Verbose:              false,
		SystemPrompt:         "",
		SystemPromptCached:   false,
		NewModel: func() (minisweagent.Model, error) {
			return model, nil
		},
	}

	agent.State = &AgentRunState{
		Messages:  make([]minisweagent.Message, 0),
		Prompts:   effectivePrompts,
		ExtraVars: map[string]interface{}{
			"answer_tag":  "answer",
			"AnswerTag":   "answer",
			"command_tag": "command",
			"CommandTag":  "command",
		},
	}

	agent.RunConfig.State = agent.State
	agent.RunConfig.NormalizeAnswerTag()
	agent.RunConfig.NormalizeCommandTag()

	// Apply functional options after normalization so WithAnswerTag / WithCommandTag
	// can override the normalized defaults. Nil options are skipped so that callers
	// can embed conditional logic without extra nil guards.
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(agent)
	}

	return agent
}

// Model returns the underlying model implementation.
func (a *DefaultAgent) Model() minisweagent.Model { return a.RunConfig.Model }

// Env returns the environment used for command execution.
func (a *DefaultAgent) Env() minisweagent.Environment { return a.RunConfig.Env }

// Messages returns a copy of the conversation history.
func (a *DefaultAgent) Messages() []minisweagent.Message {
	return a.State.Messages
}

// Config returns the agent configuration.
func (a *DefaultAgent) Config() interface{} {
	if a.RunConfig == nil {
		return nil
	}
	return a.RunConfig.ShellAgentConfig()
}

