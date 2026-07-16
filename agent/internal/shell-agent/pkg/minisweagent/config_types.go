package minisweagent

import "github.com/tursomari/machtiani/agent/internal/llm"

// AgentConfig, ModelConfig, and EnvironmentConfig are exposed for compatibility
// with existing shell-agent components while reusing the unified llm config
// structs loaded from .machtiani/config.toml.
type (
	ShellAgentConfig = llm.ShellAgentConfig
	// AgentConfig is a deprecated alias for ShellAgentConfig retained for backwards compatibility.
	AgentConfig             = llm.ShellAgentConfig
	PlannerConfig           = llm.PlannerConfig
	PromptsConfig           = llm.PromptsConfig
	PlannerPromptsConfig    = llm.PlannerPromptsConfig
	ShellAgentPromptsConfig = llm.ShellAgentPromptsConfig
	EnvironmentConfig       = llm.EnvironmentConfig
)

// ModelConfig holds model configuration for shell-agent model adapters.
// It replaces the legacy llm.ModelConfig that was previously loaded from
// the [model] section of the global TOML configuration.
type ModelConfig struct {
	ModelName   string         `toml:"model_name"`
	APIKey      string         `toml:"api_key"`
	ModelKwargs map[string]any `toml:"model_kwargs"`
}


