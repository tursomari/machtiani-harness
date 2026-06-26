package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sort"

	"github.com/BurntSushi/toml"
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

// setNestedConfig reads the TOML file at configPath, traverses the given
// keyPath (creating intermediate map[string]any entries as needed), sets
// the final key to value, and writes the updated map back to the same file.
func setNestedConfig(configPath string, value string, keyPath ...string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("config file not found: %s", configPath)
		}
		return fmt.Errorf("read %s: %w", configPath, err)
	}

	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("decode %s: %w", configPath, err)
	}
	if cfg == nil {
		cfg = make(map[string]any)
	}

	// Traverse the key path, creating sub-maps where necessary.
	cur := cfg
	for i := 0; i < len(keyPath)-1; i++ {
		key := keyPath[i]
		next, ok := cur[key]
		if !ok {
			// Key does not exist - create a new map.
			newMap := make(map[string]any)
			cur[key] = newMap
			cur = newMap
			continue
		}
		childMap, isMap := next.(map[string]any)
		if !isMap {
			// Key exists but is not a map - overwrite with an empty map.
			childMap = make(map[string]any)
			cur[key] = childMap
		}
		cur = childMap
	}

	// Set the final key.
	finalKey := keyPath[len(keyPath)-1]
	cur[finalKey] = value

	f, err := os.Create(configPath)
	if err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}
	defer f.Close()

	encoder := toml.NewEncoder(f)
	if err := encoder.Encode(cfg); err != nil {
		return fmt.Errorf("encode %s: %w", configPath, err)
	}
	return nil
}

// handleConfigURLCommand sets a provider base_url in the config.
// Usage: mct-agent config url [--alias <name>] <base-url>
func handleConfigURLCommand(args []string) int {
	configPath := filepath.Join(".machtiani", "config.toml")

	fs := pflag.NewFlagSet("mct-agent config url", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	alias := fs.String("alias", "default", "provider alias name")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent config url [--alias <name>] <base-url>\n\n")
		fmt.Fprintln(os.Stderr, "Set the provider base URL for the given alias.")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: base-url is required")
		fs.Usage()
		return 2
	}
	baseURL := fs.Arg(0)

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: config file not found at %s. Run 'mct-agent init' first.\n", configPath)
		return 1
	}

	if err := setNestedConfig(configPath, baseURL, "providers", *alias, "base_url"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	fmt.Printf("Set providers.%s.base_url = %s in %s\n", *alias, baseURL, configPath)
	return 0
}

// handleConfigAPIKeyCommand sets a provider api_key in the config.
// Usage: mct-agent config api-key [--alias <name>] <api-key>
func handleConfigAPIKeyCommand(args []string) int {
	configPath := filepath.Join(".machtiani", "config.toml")

	fs := pflag.NewFlagSet("mct-agent config api-key", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	alias := fs.String("alias", "default", "provider alias name")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent config api-key [--alias <name>] <api-key>\n\n")
		fmt.Fprintln(os.Stderr, "Set the provider API key for the given alias.")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: api-key is required")
		fs.Usage()
		return 2
	}
	apiKey := fs.Arg(0)

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: config file not found at %s. Run 'mct-agent init' first.\n", configPath)
		return 1
	}

	if err := setNestedConfig(configPath, apiKey, "providers", *alias, "api_key"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	fmt.Printf("Set providers.%s.api_key in %s\n", *alias, configPath)
	return 0
}

// handleConfigModelCommand sets a model name entry in the config.
// Usage: mct-agent config model [--alias <name>] <model-name>
func handleConfigModelCommand(args []string) int {
	configPath := filepath.Join(".machtiani", "config.toml")

	fs := pflag.NewFlagSet("mct-agent config model", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	alias := fs.String("alias", "default", "model alias name")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent config model [--alias <name>] <model-name>\n\n")
		fmt.Fprintln(os.Stderr, "Set the model identifier for the given alias.")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: model name is required")
		fs.Usage()
		return 2
	}
	modelName := fs.Arg(0)

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: config file not found at %s. Run 'mct-agent init' first.\n", configPath)
		return 1
	}

	if err := setNestedConfig(configPath, modelName, "models", *alias, "model"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	fmt.Printf("Set models.%s.model = %s in %s\n", *alias, modelName, configPath)
	return 0
}

// handleConfigReasoningCommand sets the reasoning effort for a model.
// Usage: mct-agent config reasoning [--alias <name>] <effort>
func handleConfigReasoningCommand(args []string) int {
	configPath := filepath.Join(".machtiani", "config.toml")

	fs := pflag.NewFlagSet("mct-agent config reasoning", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	alias := fs.String("alias", "default", "model alias name")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent config reasoning [--alias <name>] <effort>\n\n")
		fmt.Fprintln(os.Stderr, "Set the reasoning effort level. Valid values: low, medium, high.")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: effort value is required")
		fs.Usage()
		return 2
	}
	effort := fs.Arg(0)

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: config file not found at %s. Run 'mct-agent init' first.\n", configPath)
		return 1
	}

	if err := setNestedConfig(configPath, effort, "models", *alias, "params", "reasoning", "effort"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	fmt.Printf("Set models.%s.params.reasoning.effort = %s in %s\n", *alias, effort, configPath)
	return 0
}

// handleConfigShowCommand displays the effective configuration with source
// annotations.
// Usage: mct-agent config show
func handleConfigShowCommand(args []string) int {
	var showFull bool
	var showKey string
	fs := pflag.NewFlagSet("mct-agent config show", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var showVerbose bool
	fs.BoolVarP(&showFull, "full", "f", false, "Show all configuration settings including obscure ones (trajectory, file paths, etc.)")
	fs.BoolVar(&showVerbose, "verbose", false, "Enable verbose mode (for testing source=flag)")
	fs.StringVarP(&showKey, "key", "k", "", "Show detailed documentation for a specific config key")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent config show [--full]\n\n")
		fmt.Fprintln(os.Stderr, "Print the effective configuration with source annotations showing")
		fmt.Fprintln(os.Stderr, "whether each value comes from defaults or config.toml.")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	defaults := llm.DefaultConfig()
	configPath := filepath.Join(".machtiani", "config.toml")

	var fileConfig llm.Config
	if _, err := toml.DecodeFile(configPath, &fileConfig); err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
			return 1
		}
	}
	flagOverrides := llm.Config{}
	if showVerbose {
		flagOverrides.Verbose = true
		flagOverrides.VerboseSource = llm.SourceFlag
	}
	effective := llm.MergeConfig(defaults, fileConfig, flagOverrides)
	if showKey != "" {
		printKeyDetail(effective, showKey)
		return 0
	}
	printConfigWithSources(effective, defaults, showFull)
	return 0
}

// printConfigWithSources prints each configuration field in the format
//   key = value # source
// where source is either "default" or "config.toml".
// When showFull is false, only common settings are shown.

// configEntry holds a single key-value pair with its source for display.

// fieldDoc holds per-field documentation for config show --key.
type fieldDoc struct {
	summary     string
	explanation string
	example     string
	details     []string
}

// fieldDocs maps config keys to their documentation.
var fieldDocs = map[string]fieldDoc{
	"default_model": {
		summary:     "Default model alias for the planner",
		explanation: "Sets the model used by the planner when no model is specified. Must match an alias defined in the [[models]] section.",
		example:     "default_model = default",
		details: []string{
			"If empty, the planner will use the first available model alias.",
			"Only applies to the planner; shell-agent and file-discovery models are set separately.",
		},
	},
	"planner.max_turns": {
		summary:     "Maximum number of planner turns per session",
		explanation: "Caps how many planning iterations the planner loop can execute before stopping. Each turn is one plan-decide-act cycle.",
		example:     "planner.max_turns = 150",
		details: []string{
			"Default: 150",
			"Set lower for tighter cost control; higher for complex multi-step tasks.",
			"The planner may stop earlier if it decides to finalize before hitting the cap.",
		},
	},
	"planner.turn_timeout": {
		summary:     "Timeout per planner turn",
		explanation: "Maximum duration the planner can spend on a single planning turn. 0 means unlimited.",
		example:     "planner.turn_timeout = 0",
		details: []string{
			"Default: 0 (unlimited)",
			"Useful as a safety net to prevent runaway prompts in production.",
		},
	},
	"planner.max_input_tokens": {
		summary:     "Maximum input tokens for planner prompts",
		explanation: "Caps the token count of the prompt sent to the planner model. 0 disables truncation.",
		example:     "planner.max_input_tokens = 180000",
		details: []string{
			"Default: 180000",
			"Helps avoid hitting model context limits.",
		},
	},
	"shell-agent.max_steps": {
		summary:     "Maximum shell-agent action steps",
		explanation: "Caps the number of observe-think-act cycles the shell-agent can execute before finalizing.",
		example:     "shell-agent.max_steps = 110",
		details: []string{
			"Default: 110",
			"Increase for tasks requiring more exploration.",
		},
	},
	"shell-agent.finalize_remaining_steps": {
		summary:     "Remaining steps before forced finalize",
		explanation: "When the shell-agent has this many steps left, it will be prompted to finalize.",
		example:     "shell-agent.finalize_remaining_steps = 10",
		details: []string{
			"Default: 10",
		},
	},
	"environment.type": {
		summary:     "Execution environment selector",
		explanation: "Controls whether the session runs in local mode or an isolated workspace. Non-local values redirect MACHTIANI_TMP_ROOT.",
		example:     "environment.type = local",
		details: []string{
			"Empty string and 'local' are equivalent \u2014 both use the host workspace.",
			"Any other value enables workspace isolation (tmp directory placement).",
			"Does not provide container-level sandboxing.",
		},
	},
	"environment.command_timeout": {
		summary:     "Shell command timeout",
		explanation: "Maximum duration for a single shell command executed by the shell-agent.",
		example:     "environment.command_timeout = 9999",
		details: []string{
			"Default: 9999 seconds",
			"Long-running commands will be killed after this timeout.",
		},
	},
	"environment.cwd": {
		summary:     "Working directory for shell commands",
		explanation: "Directory in which the shell-agent runs commands. Empty means the project root.",
		example:     "environment.cwd = /tmp/work",
		details: []string{
			"Leave empty to default to project root.",
		},
	},
	"environment.max_command_output_bytes": {
		summary:     "Maximum shell command output bytes",
		explanation: "Truncates captured stdout/stderr from shell commands to this byte limit.",
		example:     "environment.max_command_output_bytes = 65536",
		details: []string{
			"Default: 65536 (64KB)",
		},
	},
	"providers.*.base_url": {
		summary:     "Provider base URL",
		explanation: "The base endpoint URL for the provider API, e.g., https://api.openai.com",
		example:     "providers.default.base_url = https://api.openai.com",
		details: []string{
			"Each provider alias has its own base_url.",
		},
	},
	"providers.*.api_key": {
		summary:     "Provider API key",
		explanation: "API key or token for authenticating with the provider.",
		example:     "providers.default.api_key = sk-...",
		details: []string{
			"Stored in cleartext in config.toml; secure the file permissions.",
		},
	},
	"providers.*.endpoint": {
		summary:     "Provider endpoint override",
		explanation: "Optional path appended to base_url for specific API operations.",
		example:     "providers.default.endpoint = /v1/chat/completions",
		details: []string{
			"Leave empty to use the provider's default endpoint.",
		},
	},
	"models.*.provider": {
		summary:     "Model provider alias",
		explanation: "Which [[providers]] entry this model uses.",
		example:     "models.default.provider = default",
		details: []string{
			"Must match an alias in the [[providers]] section.",
		},
	},
	"models.*.model": {
		summary:     "Model name",
		explanation: "The model identifier string sent to the provider, e.g., gpt-4o, claude-sonnet-4-20250514.",
		example:     "models.default.model = gpt-4o",
		details: []string{
			"The exact name must match the provider's API expectations.",
		},
	},
	"models.*.params.*": {
		summary:     "Model parameter",
		explanation: "Additional key-value parameters passed to the model, e.g., reasoning.effort.",
		example:     `models.default.params.reasoning.effort = "high"`,
		details: []string{
			"Params are forwarded to the provider as-is; supported keys depend on the provider.",
		},
	},
	"verbose": {
		summary:     "Verbose logging",
		explanation: "Enables detailed debug output from the agent.",
		example:     "verbose = false",
		details: []string{
			"Default: false",
		},
	},
	"persist_tmp_data": {
		summary:     "Persist temporary data",
		explanation: "If true, temporary files generated during the session are kept after completion.",
		example:     "persist_tmp_data = false",
		details: []string{
			"Default: false",
		},
	},
	"dry_run": {
		summary:     "Dry-run mode",
		explanation: "If true, the agent will not execute any shell commands; it only shows what it would run.",
		example:     "dry_run = false",
		details: []string{
			"Default: false",
		},
	},
	"shell_agent_enabled": {
		summary:     "Enable shell-agent",
		explanation: "Turns the shell-agent on or off. When disabled, the planner answers directly.",
		example:     "shell_agent_enabled = true",
		details: []string{
			"Default: true",
		},
	},
	"answer_tag": {
		summary:     "Override tag for final answer XML element",
		explanation: "Sets the XML tag name wrapping the agent's final output. Composed with tag to produce the effective answer tag.",
		example:     "answer_tag = now",
		details: []string{
			"When empty, defaults to <answer>.",
			"Distinct from tag, which is a session suffix.",
		},
	},
	"tag": {
		summary:     "Session tag suffix",
		explanation: "Appended to session IDs and used as a component when composing the effective answer tag.",
		example:     "tag = my-session",
		details: []string{
			"Combined with answer_tag to form the final XML tag.",
		},
	},
	"enable_tag_format": {
		summary:     "Enable tag format",
		explanation: "When true, the agent uses tagged formatting for conversation output.",
		example:     "enable_tag_format = false",
		details: []string{
			"Default: false",
		},
	},
	"answer_model": {
		summary:     "Answer model alias",
		explanation: "Model used for generating the final answer. Overrides the default model if set.",
		example:     "answer_model = gpt-4o-mini",
		details: []string{
			"Leave empty to use the default model.",
		},
	},
	"file_discovery_model": {
		summary:     "File-discovery model alias",
		explanation: "Model used by the file-discovery tool. If empty, uses the default model.",
		example:     "file_discovery_model = haiku",
		details: []string{
			"Leave empty to use the default model.",
		},
	},
	"shell_agent_model": {
		summary:     "Shell-agent model alias",
		explanation: "Model used by the shell-agent for its observe-think-act loop.",
		example:     "shell_agent_model = sonnet",
		details: []string{
			"Leave empty to use the default model.",
			"Can be different from the planner model for cost optimization.",
		},
	},
	"final_file": {
		summary:     "Final answer output file",
		explanation: "Path where the final answer is written after the session completes.",
		example:     `final_file = "output/answer.txt"`,
		details: []string{
			"Shown only with --full.",
		},
	},
	"transcript_file": {
		summary:     "Transcript output file",
		explanation: "Path where the full conversation transcript is saved.",
		example:     `transcript_file = "output/transcript.json"`,
		details: []string{
			"Shown only with --full.",
		},
	},
	"file_discovery_trajectory": {
		summary:     "File-discovery trajectory output file",
		explanation: "Path where the file-discovery trajectory is written if trajectory is enabled.",
		example:     `file_discovery_trajectory = "output/fd_trajectory.jsonl"`,
		details: []string{
			"Shown only with --full.",
		},
	},
	"file_discovery_output_dir": {
		summary:     "File-discovery output directory",
		explanation: "Directory where file-discovery writes intermediate results.",
		example:     `file_discovery_output_dir = "tmp/discovery"`,
		details: []string{
			"Shown only with --full.",
		},
	},
	"trajectory.enabled": {
		summary:     "Enable trajectory logging",
		explanation: "If true, detailed trajectory metadata is recorded per turn.",
		example:     "trajectory.enabled = true",
		details: []string{
			"Default: true. Shown only with --full.",
		},
	},
	"trajectory.file": {
		summary:     "Trajectory output file",
		explanation: "Path to the JSONL file where trajectory records are appended.",
		example:     `trajectory.file = "trajectory.jsonl"`,
		details: []string{
			"Shown only with --full.",
		},
	},
	"trajectory.verbose_llm": {
		summary:     "Verbose LLM trajectory",
		explanation: "If true, full LLM request/response details are included in trajectory records.",
		example:     "trajectory.verbose_llm = false",
		details: []string{
			"Default: false. Shown only with --full.",
		},
	},
	"trajectory.stream_tokens": {
		summary:     "Stream tokens in trajectory",
		explanation: "If true, token-by-token streaming is captured in trajectory logs.",
		example:     "trajectory.stream_tokens = false",
		details: []string{
			"Default: false. Shown only with --full.",
		},
	},
	"trajectory.excerpt": {
		summary:     "Trajectory excerpt length",
		explanation: "Maximum character count of shell output excerpts stored in trajectory records.",
		example:     "trajectory.excerpt = 512",
		details: []string{
			"Default: 512. Shown only with --full.",
		},
	},
	"trajectory.omit_repo_root": {
		summary:     "Omit repo root from trajectory",
		explanation: "If true, the repo root path is stripped from file paths in trajectory records.",
		example:     "trajectory.omit_repo_root = false",
		details: []string{
			"Default: false. Shown only with --full.",
		},
	},
}

// getFieldValue returns the string representation of a config key's value and source.
func getFieldValue(effective llm.Config, key string) (value string, source string) {
	switch key {
	case "default_model":
		return effective.DefaultModel, sourceLabel(effective.DefaultModelSource)
	case "planner.max_turns":
		return fmt.Sprintf("%d", effective.Planner.MaxTurns), sourceLabel(effective.Planner.MaxTurnsSource)
	case "planner.turn_timeout":
		return fmt.Sprintf("%d", effective.Planner.TurnTimeout), sourceLabel(effective.Planner.TurnTimeoutSource)
	case "planner.max_input_tokens":
		return fmt.Sprintf("%d", effective.Planner.MaxInputTokens), sourceLabel(effective.Planner.MaxInputTokensSource)
	case "shell-agent.max_steps":
		return fmt.Sprintf("%d", effective.ShellAgent.MaxSteps), sourceLabel(effective.ShellAgent.MaxStepsSource)
	case "shell-agent.finalize_remaining_steps":
		return fmt.Sprintf("%d", effective.ShellAgent.FinalizeRemainingSteps), sourceLabel(effective.ShellAgent.FinalizeRemainingStepsSource)
	case "environment.type":
		return effective.Environment.Type, sourceLabel(effective.Environment.TypeSource)
	case "environment.command_timeout":
		return fmt.Sprintf("%d", effective.Environment.CommandTimeout), sourceLabel(effective.Environment.CommandTimeoutSource)
	case "environment.cwd":
		return effective.Environment.CWD, sourceLabel(effective.Environment.CWDSource)
	case "environment.max_command_output_bytes":
		return fmt.Sprintf("%d", effective.Environment.MaxCommandOutputBytes), sourceLabel(effective.Environment.MaxCommandOutputBytesSource)
	case "verbose":
		return fmt.Sprintf("%v", effective.Verbose), sourceLabel(effective.VerboseSource)
	case "persist_tmp_data":
		return fmt.Sprintf("%v", effective.PersistTmpData), sourceLabel(effective.PersistTmpDataSource)
	case "dry_run":
		return fmt.Sprintf("%v", effective.DryRun), sourceLabel(effective.DryRunSource)
	case "shell_agent_enabled":
		return fmt.Sprintf("%v", effective.ShellAgentEnabled), sourceLabel(effective.ShellAgentEnabledSource)
	case "answer_tag":
		return effective.AnswerTag, sourceLabel(effective.AnswerTagSource)
	case "tag":
		return effective.Tag, sourceLabel(effective.TagSource)
	case "enable_tag_format":
		return fmt.Sprintf("%v", effective.EnableTagFormat), sourceLabel(effective.EnableTagFormatSource)
	case "answer_model":
		return effective.AnswerModel, sourceLabel(effective.AnswerModelSource)
	case "file_discovery_model":
		return effective.FileDiscoveryModel, sourceLabel(effective.FileDiscoveryModelSource)
	case "shell_agent_model":
		return effective.ShellAgentModel, sourceLabel(effective.ShellAgentModelSource)
	case "final_file":
		return effective.FinalFile, sourceLabel(effective.FinalFileSource)
	case "transcript_file":
		return effective.TranscriptFile, sourceLabel(effective.TranscriptFileSource)
	case "file_discovery_trajectory":
		return effective.FileDiscoveryTrajectory, sourceLabel(effective.FileDiscoveryTrajectorySource)
	case "file_discovery_output_dir":
		return effective.FileDiscoveryOutputDir, sourceLabel(effective.FileDiscoveryOutputDirSource)
	case "trajectory.enabled":
		return fmt.Sprintf("%v", effective.Trajectory.Enabled), sourceLabel(effective.Trajectory.EnabledSource)
	case "trajectory.file":
		return effective.Trajectory.File, sourceLabel(effective.Trajectory.FileSource)
	case "trajectory.verbose_llm":
		return fmt.Sprintf("%v", effective.Trajectory.VerboseLLM), sourceLabel(effective.Trajectory.VerboseLLMSource)
	case "trajectory.stream_tokens":
		return fmt.Sprintf("%v", effective.Trajectory.StreamTokens), sourceLabel(effective.Trajectory.StreamTokensSource)
	case "trajectory.excerpt":
		return fmt.Sprintf("%d", effective.Trajectory.Excerpt), sourceLabel(effective.Trajectory.ExcerptSource)
	case "trajectory.omit_repo_root":
		return fmt.Sprintf("%v", effective.Trajectory.OmitRepoRoot), sourceLabel(effective.Trajectory.OmitRepoRootSource)
	default:
		// handle providers.<alias>.<subkey> and models.<alias>.<subkey>
		parts := strings.SplitN(key, ".", 3)
		if len(parts) == 3 && parts[0] == "providers" {
			alias := parts[1]
			sub := parts[2]
			prov, ok := effective.Providers[alias]
			if !ok {
				return "", ""
			}
			switch sub {
			case "base_url":
				return prov.BaseURL, sourceLabel(prov.BaseURLSource)
			case "api_key":
				if prov.APIKey != "" {
					return "********", sourceLabel(prov.APIKeySource)
				}
				return "", sourceLabel(prov.APIKeySource)
			case "endpoint":
				return prov.Endpoint, sourceLabel(prov.EndpointSource)
			default:
				return "", ""
			}
		}
		if len(parts) >= 3 && parts[0] == "models" {
			alias := parts[1]
			sub := parts[2]
			model, ok := effective.Models[alias]
			if !ok {
				return "", ""
			}
			switch sub {
			case "provider":
				return model.Provider, sourceLabel(model.ProviderSource)
			case "model":
				return model.Model, sourceLabel(model.ModelSource)
			default:
				if strings.HasPrefix(sub, "params.") {
					paramKey := strings.TrimPrefix(sub, "params.")
					val, ok := model.Params[paramKey]
					if ok {
						return fmt.Sprintf("%v", val), sourceLabel(effective.ModelSources[alias])
					}
					return "", ""
				}
			}
		}
		return "", ""
	}
}

// printKeyDetail prints detailed documentation for a single config key.
func printKeyDetail(effective llm.Config, key string) {
	doc, ok := fieldDocs[key]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown config key: %s\n\nValid keys:\n", key)
		for k := range fieldDocs {
			fmt.Fprintf(os.Stderr, "  %s\n", k)
		}
		os.Exit(1)
	}
	value, source := getFieldValue(effective, key)
	buf := &strings.Builder{}
	fmt.Fprintf(buf, "%s\n\n", key)
	fmt.Fprintf(buf, "%s\n\n", doc.summary)
	fmt.Fprintf(buf, "%s\n\n", doc.explanation)
	fmt.Fprintf(buf, "Example:\n  %s\n\n", doc.example)
	if len(doc.details) > 0 {
		fmt.Fprintf(buf, "Details:\n")
		for _, d := range doc.details {
			fmt.Fprintf(buf, "  - %s\n", d)
		}
		buf.WriteByte('\n')
	}
	fmt.Fprintf(buf, "Current value:\n  %s = %s  (%s)\n", key, value, source)
	fmt.Print(buf.String())
}


type configEntry struct {
	key    string
	value  string
	source string
}

// aliasEntry holds a named group of config entries (e.g., a provider or model alias).
type aliasEntry struct {
	name   string
	source string
	keys   []configEntry
}


// maskAPIKey masks an API key, showing only "********" if non-empty.
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	return "********"
}

// sourceLabel converts a FieldSource to a human-readable label.
func sourceLabel(src llm.FieldSource) string {
	switch src {
	case llm.SourceFile:
		return "config.toml"
	case llm.SourceFlag:
		return "flag"
	default:
		return "default"
	}
}

// renderScalarSection writes a titled section with aligned key-value pairs and (source) tags.
func renderScalarSection(buf *strings.Builder, title, desc string, entries []configEntry) {
	if len(entries) == 0 {
		return
	}
	maxKeyLen := 0
	for _, e := range entries {
		if len(e.key) > maxKeyLen {
			maxKeyLen = len(e.key)
		}
	}
	buf.WriteString(title + "\n")
	buf.WriteString("  " + desc + "\n")
	for _, e := range entries {
		fmt.Fprintf(buf, "  %-*s = %s  (%s)\n", maxKeyLen, e.key, e.value, e.source)
	}
	buf.WriteByte('\n')
}

// renderAliasSection writes a titled section with per-alias sub-blocks. Each
// alias prints a [name]  (source) header and its keys with the dotted prefix
// stripped.
func renderAliasSection(buf *strings.Builder, title, desc string, aliases []aliasEntry) {
	maxKeyLen := 0
	for _, a := range aliases {
		for _, k := range a.keys {
			if len(k.key) > maxKeyLen {
				maxKeyLen = len(k.key)
			}
		}
	}
	buf.WriteString(title + "\n")
	buf.WriteString("  " + desc + "\n")
	for _, a := range aliases {
		fmt.Fprintf(buf, "  [%s]  (%s)\n", a.name, a.source)
		for _, k := range a.keys {
			fmt.Fprintf(buf, "    %-*s = %s\n", maxKeyLen, k.key, k.value)
		}
	}
	buf.WriteByte('\n')
}

func printConfigWithSources(effective, defaults llm.Config, showFull bool) {
	var buf strings.Builder

	// --- Providers ---
	providerNames := make([]string, 0, len(effective.Providers))
	for n := range effective.Providers {
		providerNames = append(providerNames, n)
	}
	sort.Strings(providerNames)
	var providerAliases []aliasEntry
	for _, name := range providerNames {
		prov := effective.Providers[name]
		label := sourceLabel(effective.ProviderSources[name])
		keys := []configEntry{
			{key: "base_url", value: prov.BaseURL},
			{key: "api_key", value: maskAPIKey(prov.APIKey)},
			{key: "endpoint", value: prov.Endpoint},
		}
		providerAliases = append(providerAliases, aliasEntry{name: name, source: label, keys: keys})
	}
	renderAliasSection(&buf, "Providers", "Base URLs, API keys, and endpoints for each LLM provider", providerAliases)

	// --- Models ---
	modelNames := make([]string, 0, len(effective.Models))
	for n := range effective.Models {
		modelNames = append(modelNames, n)
	}
	sort.Strings(modelNames)
	var modelAliases []aliasEntry
	for _, name := range modelNames {
		model := effective.Models[name]
		label := sourceLabel(effective.ModelSources[name])
		keys := []configEntry{
			{key: "provider", value: model.Provider},
			{key: "model", value: model.Model},
		}
		if len(model.Params) > 0 {
			for k, v := range model.Params {
				keys = append(keys, configEntry{key: "params." + k, value: formatParamValue(v)})
			}
		}
		modelAliases = append(modelAliases, aliasEntry{name: name, source: label, keys: keys})
	}
	renderAliasSection(&buf, "Models", "Model identifiers and provider bindings for each alias", modelAliases)

	// --- Planner ---
	var plannerEntries []configEntry
	if effective.Planner != nil {
		plannerEntries = []configEntry{
			{key: "planner.max_turns", value: fmt.Sprintf("%d", effective.Planner.MaxTurns), source: sourceLabel(effective.Planner.MaxTurnsSource)},
			{key: "planner.turn_timeout", value: fmt.Sprintf("%d", effective.Planner.TurnTimeout), source: sourceLabel(effective.Planner.TurnTimeoutSource)},
			{key: "planner.max_input_tokens", value: fmt.Sprintf("%d", effective.Planner.MaxInputTokens), source: sourceLabel(effective.Planner.MaxInputTokensSource)},
		}
	} else {
		plannerEntries = []configEntry{
			{key: "planner.max_turns", value: "", source: "default"},
			{key: "planner.turn_timeout", value: "", source: "default"},
			{key: "planner.max_input_tokens", value: "", source: "default"},
		}
	}
	renderScalarSection(&buf, "Planner", "Turn budget, timeout, and input token limit", plannerEntries)

	// --- Shell Agent ---
	var saEntries []configEntry
	if effective.ShellAgent != nil {
		saEntries = []configEntry{
			{key: "shell-agent.max_steps", value: fmt.Sprintf("%d", effective.ShellAgent.MaxSteps), source: sourceLabel(effective.ShellAgent.MaxStepsSource)},
			{key: "shell-agent.finalize_remaining_steps", value: fmt.Sprintf("%d", effective.ShellAgent.FinalizeRemainingSteps), source: sourceLabel(effective.ShellAgent.FinalizeRemainingStepsSource)},
		}
	} else {
		saEntries = []configEntry{
			{key: "shell-agent.max_steps", value: "", source: "default"},
			{key: "shell-agent.finalize_remaining_steps", value: "", source: "default"},
		}
	}
	renderScalarSection(&buf, "Shell Agent", "Step budget and finalize window", saEntries)

	// --- Environment ---
	var envEntries []configEntry
	if effective.Environment != nil {
		envEntries = []configEntry{
			{key: "environment.type", value: effective.Environment.Type, source: sourceLabel(effective.Environment.TypeSource)},
			{key: "environment.command_timeout", value: fmt.Sprintf("%d", effective.Environment.CommandTimeout), source: sourceLabel(effective.Environment.CommandTimeoutSource)},
			{key: "environment.cwd", value: effective.Environment.CWD, source: sourceLabel(effective.Environment.CWDSource)},
			{key: "environment.max_command_output_bytes", value: fmt.Sprintf("%d", effective.Environment.MaxCommandOutputBytes), source: sourceLabel(effective.Environment.MaxCommandOutputBytesSource)},
		}
	}
	renderScalarSection(&buf, "Environment", "Execution environment and workspace settings", envEntries)

	// --- General ---
	generalEntries := []configEntry{
		{key: "default_model", value: effective.DefaultModel, source: sourceLabel(effective.DefaultModelSource)},
		{key: "verbose", value: fmt.Sprintf("%t", effective.Verbose), source: sourceLabel(effective.VerboseSource)},
		{key: "persist_tmp_data", value: fmt.Sprintf("%t", effective.PersistTmpData), source: sourceLabel(effective.PersistTmpDataSource)},
		{key: "dry_run", value: fmt.Sprintf("%t", effective.DryRun), source: sourceLabel(effective.DryRunSource)},
		{key: "shell_agent_enabled", value: fmt.Sprintf("%t", effective.ShellAgentEnabled), source: sourceLabel(effective.ShellAgentEnabledSource)},
		{key: "answer_tag", value: effective.AnswerTag, source: sourceLabel(effective.AnswerTagSource)},
		{key: "tag", value: effective.Tag, source: sourceLabel(effective.TagSource)},
		{key: "enable_tag_format", value: fmt.Sprintf("%t", effective.EnableTagFormat), source: sourceLabel(effective.EnableTagFormatSource)},
		{key: "answer_model", value: effective.AnswerModel, source: sourceLabel(effective.AnswerModelSource)},
		{key: "file_discovery_model", value: effective.FileDiscoveryModel, source: sourceLabel(effective.FileDiscoveryModelSource)},
		{key: "shell_agent_model", value: effective.ShellAgentModel, source: sourceLabel(effective.ShellAgentModelSource)},
	}
	renderScalarSection(&buf, "General", "Top-level defaults and flags", generalEntries)

	// --- Files (--full only) ---
	if showFull {
		filesEntries := []configEntry{
			{key: "final_file", value: effective.FinalFile, source: sourceLabel(effective.FinalFileSource)},
			{key: "transcript_file", value: effective.TranscriptFile, source: sourceLabel(effective.TranscriptFileSource)},
			{key: "file_discovery_trajectory", value: effective.FileDiscoveryTrajectory, source: sourceLabel(effective.FileDiscoveryTrajectorySource)},
			{key: "file_discovery_output_dir", value: effective.FileDiscoveryOutputDir, source: sourceLabel(effective.FileDiscoveryOutputDirSource)},
		}
		renderScalarSection(&buf, "Files", "Output paths for final answers and transcripts", filesEntries)

		// --- Trajectory (--full only) ---
		var trajEntries []configEntry
		if effective.Trajectory != nil {
			trajEntries = []configEntry{
				{key: "trajectory.enabled", value: fmt.Sprintf("%t", effective.Trajectory.Enabled), source: sourceLabel(effective.Trajectory.EnabledSource)},
				{key: "trajectory.file", value: effective.Trajectory.File, source: sourceLabel(effective.Trajectory.FileSource)},
				{key: "trajectory.verbose_llm", value: fmt.Sprintf("%t", effective.Trajectory.VerboseLLM), source: sourceLabel(effective.Trajectory.VerboseLLMSource)},
				{key: "trajectory.stream_tokens", value: fmt.Sprintf("%t", effective.Trajectory.StreamTokens), source: sourceLabel(effective.Trajectory.StreamTokensSource)},
				{key: "trajectory.excerpt", value: fmt.Sprintf("%d", effective.Trajectory.Excerpt), source: sourceLabel(effective.Trajectory.ExcerptSource)},
				{key: "trajectory.omit_repo_root", value: fmt.Sprintf("%t", effective.Trajectory.OmitRepoRoot), source: sourceLabel(effective.Trajectory.OmitRepoRootSource)},
			}
		}
		renderScalarSection(&buf, "Trajectory", "Telemetry and debugging trajectory settings", trajEntries)
	}

	fmt.Print(buf.String())
}



// formatParamValue formats a parameter value for display.
func formatParamValue(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int, int64:
		return fmt.Sprintf("%d", val)
	case float64:
		return fmt.Sprintf("%g", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case map[string]any:
		return flattenMap(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// flattenMap converts a single-level map to "key=val key2=val2" format.
func flattenMap(m map[string]any) string {
	var parts []string
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s=%s", k, formatParamValue(v)))
	}
	return strings.Join(parts, " ")
}

