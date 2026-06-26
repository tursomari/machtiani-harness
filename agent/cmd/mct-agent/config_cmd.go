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
	fs := pflag.NewFlagSet("mct-agent config show", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var showVerbose bool
	fs.BoolVarP(&showFull, "full", "f", false, "Show all configuration settings including obscure ones (trajectory, file paths, etc.)")
	fs.BoolVar(&showVerbose, "verbose", false, "Enable verbose mode (for testing source=flag)")
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
	printConfigWithSources(effective, defaults, showFull)
	return 0
}

// printConfigWithSources prints each configuration field in the format
//   key = value # source
// where source is either "default" or "config.toml".
// When showFull is false, only common settings are shown.

// configEntry holds a single key-value pair with its source for display.
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

