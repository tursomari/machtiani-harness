package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	fs := pflag.NewFlagSet("mct-agent config show", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVarP(&showFull, "full", "f", false, "Show all configuration settings including obscure ones (trajectory, file paths, etc.)")
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
	effective := llm.MergeConfig(defaults, fileConfig, llm.Config{})
	printConfigWithSources(effective, defaults, showFull)
	return 0
}

// printConfigWithSources prints each configuration field in the format
//   key = value # source
// where source is either "default" or "config.toml".
// When showFull is false, only common settings are shown.
func printConfigWithSources(effective, defaults llm.Config, showFull bool) {
	var buf strings.Builder
	// --- Fixed fields ---
	fmt.Fprintf(&buf, "default_model = %s # %s\n",
		effective.DefaultModel,
		stringSource(effective.DefaultModel, defaults.DefaultModel, effective.DefaultModelSource))

	if effective.Planner != nil {
		defaultMaxTurns := 0
		defaultTurnTimeout := 0
		defaultMaxInputTokens := 0
		if defaults.Planner != nil {
			defaultMaxTurns = defaults.Planner.MaxTurns
			defaultTurnTimeout = defaults.Planner.TurnTimeout
			defaultMaxInputTokens = defaults.Planner.MaxInputTokens
		}
		fmt.Fprintf(&buf, "planner.max_turns = %d # %s\n",
			effective.Planner.MaxTurns,
			intSource(effective.Planner.MaxTurns, defaultMaxTurns, effective.Planner.MaxTurnsSource))
		fmt.Fprintf(&buf, "planner.turn_timeout = %d # %s\n",
			effective.Planner.TurnTimeout,
			intSource(effective.Planner.TurnTimeout, defaultTurnTimeout, effective.Planner.TurnTimeoutSource))
		fmt.Fprintf(&buf, "planner.max_input_tokens = %d # %s\n",
			effective.Planner.MaxInputTokens,
			intSource(effective.Planner.MaxInputTokens, defaultMaxInputTokens, effective.Planner.MaxInputTokensSource))
	} else {
		fmt.Fprintf(&buf, "planner.max_turns = # default\n")
		fmt.Fprintf(&buf, "planner.turn_timeout = # default\n")
		fmt.Fprintf(&buf, "planner.max_input_tokens = # default\n")
	}

	if effective.ShellAgent != nil {
		defaultMaxSteps := 0
		defaultFRS := 0
		if defaults.ShellAgent != nil {
			defaultMaxSteps = defaults.ShellAgent.MaxSteps
			defaultFRS = defaults.ShellAgent.FinalizeRemainingSteps
		}
		fmt.Fprintf(&buf, "shell-agent.max_steps = %d # %s\n",
			effective.ShellAgent.MaxSteps,
			intSource(effective.ShellAgent.MaxSteps, defaultMaxSteps, effective.ShellAgent.MaxStepsSource))
		fmt.Fprintf(&buf, "shell-agent.finalize_remaining_steps = %d # %s\n",
			effective.ShellAgent.FinalizeRemainingSteps,
			intSource(effective.ShellAgent.FinalizeRemainingSteps, defaultFRS, effective.ShellAgent.FinalizeRemainingStepsSource))
	} else {
		fmt.Fprintf(&buf, "shell-agent.max_steps = # default\n")
		fmt.Fprintf(&buf, "shell-agent.finalize_remaining_steps = # default\n")
	}

	if effective.Environment != nil {
		defaultEnv := defaults.Environment
		if defaultEnv == nil {
			defaultEnv = &llm.EnvironmentConfig{}
		}
		fmt.Fprintf(&buf, "environment.type = %s # %s\n",
			effective.Environment.Type,
			stringSource(effective.Environment.Type, defaultEnv.Type, effective.Environment.TypeSource))
		fmt.Fprintf(&buf, "environment.command_timeout = %d # %s\n",
			effective.Environment.CommandTimeout,
			intSource(effective.Environment.CommandTimeout, defaultEnv.CommandTimeout, effective.Environment.CommandTimeoutSource))
		fmt.Fprintf(&buf, "environment.cwd = %s # %s\n",
			effective.Environment.CWD,
			stringSource(effective.Environment.CWD, defaultEnv.CWD, effective.Environment.CWDSource))
		fmt.Fprintf(&buf, "environment.max_command_output_bytes = %d # %s\n",
			effective.Environment.MaxCommandOutputBytes,
			intSource(effective.Environment.MaxCommandOutputBytes, defaultEnv.MaxCommandOutputBytes, effective.Environment.MaxCommandOutputBytesSource))
	}

	// --- Providers ---
	providerNames := make([]string, 0, len(effective.Providers))
	for n := range effective.Providers {
		providerNames = append(providerNames, n)
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		prov := effective.Providers[name]
		defaultBaseURL := ""
		defaultAPIKey := ""
		defaultEndpoint := ""
		if defProv, ok := defaults.Providers[name]; ok {
			defaultBaseURL = defProv.BaseURL
			defaultAPIKey = defProv.APIKey
			defaultEndpoint = defProv.Endpoint
		}
		fmt.Fprintf(&buf, "providers.%s.base_url = %s # %s\n",
			name, prov.BaseURL,
			stringSource(prov.BaseURL, defaultBaseURL, prov.BaseURLSource))
		fmt.Fprintf(&buf, "providers.%s.api_key = %s # %s\n",
			name, prov.APIKey,
			stringSource(prov.APIKey, defaultAPIKey, prov.APIKeySource))
		fmt.Fprintf(&buf, "providers.%s.endpoint = %s # %s\n",
			name, prov.Endpoint,
			stringSource(prov.Endpoint, defaultEndpoint, prov.EndpointSource))
	}

	// --- top-level behavioral settings (common) ---
	defaultsVerbose := defaults.Verbose
	defaultsPersistTmp := defaults.PersistTmpData
	defaultsDryRun := defaults.DryRun
	defaultsShellEnabled := defaults.ShellAgentEnabled
	defaultsAnswerTag := defaults.AnswerTag
	defaultsTag := defaults.Tag
	defaultsTagFormat := defaults.EnableTagFormat
	fmt.Fprintf(&buf, "verbose = %t # %s\n", effective.Verbose, boolSource(effective.Verbose, defaultsVerbose, effective.VerboseSource))
	fmt.Fprintf(&buf, "persist_tmp_data = %t # %s\n", effective.PersistTmpData, boolSource(effective.PersistTmpData, defaultsPersistTmp, effective.PersistTmpDataSource))
	fmt.Fprintf(&buf, "dry_run = %t # %s\n", effective.DryRun, boolSource(effective.DryRun, defaultsDryRun, effective.DryRunSource))
	fmt.Fprintf(&buf, "shell_agent_enabled = %t # %s\n", effective.ShellAgentEnabled, boolSource(effective.ShellAgentEnabled, defaultsShellEnabled, effective.ShellAgentEnabledSource))
	fmt.Fprintf(&buf, "answer_tag = %s # %s\n", effective.AnswerTag, stringSource(effective.AnswerTag, defaultsAnswerTag, effective.AnswerTagSource))
	fmt.Fprintf(&buf, "tag = %s # %s\n", effective.Tag, stringSource(effective.Tag, defaultsTag, effective.TagSource))
	fmt.Fprintf(&buf, "enable_tag_format = %t # %s\n", effective.EnableTagFormat, boolSource(effective.EnableTagFormat, defaultsTagFormat, effective.EnableTagFormatSource))
	fmt.Fprintf(&buf, "answer_model = %s # %s\n", effective.AnswerModel, stringSource(effective.AnswerModel, defaults.AnswerModel, effective.AnswerModelSource))
	fmt.Fprintf(&buf, "file_discovery_model = %s # %s\n", effective.FileDiscoveryModel, stringSource(effective.FileDiscoveryModel, defaults.FileDiscoveryModel, effective.FileDiscoveryModelSource))
	fmt.Fprintf(&buf, "shell_agent_model = %s # %s\n", effective.ShellAgentModel, stringSource(effective.ShellAgentModel, defaults.ShellAgentModel, effective.ShellAgentModelSource))

	if showFull {
		fmt.Fprintf(&buf, "final_file = %s # %s\n", effective.FinalFile, stringSource(effective.FinalFile, defaults.FinalFile, effective.FinalFileSource))
		fmt.Fprintf(&buf, "transcript_file = %s # %s\n", effective.TranscriptFile, stringSource(effective.TranscriptFile, defaults.TranscriptFile, effective.TranscriptFileSource))
		fmt.Fprintf(&buf, "file_discovery_trajectory = %s # %s\n", effective.FileDiscoveryTrajectory, stringSource(effective.FileDiscoveryTrajectory, defaults.FileDiscoveryTrajectory, effective.FileDiscoveryTrajectorySource))
		fmt.Fprintf(&buf, "file_discovery_output_dir = %s # %s\n", effective.FileDiscoveryOutputDir, stringSource(effective.FileDiscoveryOutputDir, defaults.FileDiscoveryOutputDir, effective.FileDiscoveryOutputDirSource))
		if effective.Trajectory != nil {
			defaultTraj := defaults.Trajectory
			if defaultTraj == nil {
				defaultTraj = &llm.TrajectoryConfig{}
			}
			fmt.Fprintf(&buf, "trajectory.enabled = %t # %s\n", effective.Trajectory.Enabled, boolSource(effective.Trajectory.Enabled, defaultTraj.Enabled, effective.Trajectory.EnabledSource))
			fmt.Fprintf(&buf, "trajectory.file = %s # %s\n", effective.Trajectory.File, stringSource(effective.Trajectory.File, defaultTraj.File, effective.Trajectory.FileSource))
			fmt.Fprintf(&buf, "trajectory.verbose_llm = %t # %s\n", effective.Trajectory.VerboseLLM, boolSource(effective.Trajectory.VerboseLLM, defaultTraj.VerboseLLM, effective.Trajectory.VerboseLLMSource))
			fmt.Fprintf(&buf, "trajectory.stream_tokens = %t # %s\n", effective.Trajectory.StreamTokens, boolSource(effective.Trajectory.StreamTokens, defaultTraj.StreamTokens, effective.Trajectory.StreamTokensSource))
			fmt.Fprintf(&buf, "trajectory.excerpt = %d # %s\n", effective.Trajectory.Excerpt, intSource(effective.Trajectory.Excerpt, defaultTraj.Excerpt, effective.Trajectory.ExcerptSource))
			fmt.Fprintf(&buf, "trajectory.omit_repo_root = %t # %s\n", effective.Trajectory.OmitRepoRoot, boolSource(effective.Trajectory.OmitRepoRoot, defaultTraj.OmitRepoRoot, effective.Trajectory.OmitRepoRootSource))
		}
	}

	// --- Models ---

	modelNames := make([]string, 0, len(effective.Models))
	for n := range effective.Models {
		modelNames = append(modelNames, n)
	}
	sort.Strings(modelNames)
	for _, name := range modelNames {
		model := effective.Models[name]
		defaultProvider := ""
		defaultModel := ""
		if defModel, ok := defaults.Models[name]; ok {
			defaultProvider = defModel.Provider
			defaultModel = defModel.Model
		}
		fmt.Fprintf(&buf, "models.%s.provider = %s # %s\n",
			name, model.Provider,
			stringSource(model.Provider, defaultProvider, model.ProviderSource))
		fmt.Fprintf(&buf, "models.%s.model = %s # %s\n",
			name, model.Model,
			stringSource(model.Model, defaultModel, model.ModelSource))

		if len(model.Params) > 0 {
			_, aliasInDefaults := defaults.Models[name]
			for k, v := range model.Params {
				defaultVal := any(nil)
				if aliasInDefaults {
					defaultVal = defaults.Models[name].Params[k]
				}
				source := paramValueSource(v, defaultVal, aliasInDefaults, effective.ModelSources[name])
				fmt.Fprintf(&buf, "models.%s.params.%s = %s # %s\n",
					name, k, formatParamValue(v),
					source)
			}
		}
	}

	fmt.Print(buf.String())
}

// stringSource returns "config.toml" when hasFile is true and the effective
// value differs from the default, otherwise "default".
func stringSource(effective, defaults string, src llm.FieldSource) string {
	if src == llm.SourceFile {
		return "config.toml"
	}
	if src == llm.SourceFlag {
		return "flag"
	}
	return "default"
}

// intSource returns "config.toml" when hasFile is true and the effective
// value differs from the default, otherwise "default".
func intSource(effective, defaults int, src llm.FieldSource) string {
	if src == llm.SourceFile {
		return "config.toml"
	}
	if src == llm.SourceFlag {
		return "flag"
	}
	return "default"
}

// boolSource returns "config.toml" when hasFile is true and the effective
// value differs from the default, otherwise "default".
func boolSource(effective, defaults bool, src llm.FieldSource) string {
	if src == llm.SourceFile {
		return "config.toml"
	}
	if src == llm.SourceFlag {
		return "flag"
	}
	return "default"
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

// paramSource returns "config.toml" when hasFile is true and the defaults
// have no entry for the given model (since all params originate from the
// config file), otherwise "default".
func paramValueSource(effective, defaultVal any, aliasInDefaults bool, src llm.FieldSource) string {
	if src == llm.SourceDefault {
		return "default"
	}
	if !aliasInDefaults {
		if src == llm.SourceFile {
			return "config.toml"
		}
		return "flag"
	}
	if reflect.DeepEqual(effective, defaultVal) {
		if src == llm.SourceDefault {
			return "default"
		}
		if src == llm.SourceFile {
			return "config.toml"
		}
		return "flag"
	}
	if src == llm.SourceFile {
		return "config.toml"
	}
	return "flag"
}
