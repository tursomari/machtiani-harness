package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	fs := pflag.NewFlagSet("mct-agent config show", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent config show\n\n")
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
	hasFile := true
	if _, err := toml.DecodeFile(configPath, &fileConfig); err != nil {
		if os.IsNotExist(err) {
			hasFile = false
		} else {
			fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
			return 1
		}
	}

	effective := llm.MergeConfig(defaults, fileConfig, llm.Config{})
	printConfigWithSources(effective, defaults, hasFile)
	return 0
}

// printConfigWithSources prints each configuration field in the format
//   key = value # source
// where source is either "default" or "config.toml".
func printConfigWithSources(effective, defaults llm.Config, hasFile bool) {
	// --- Fixed fields ---
	fmt.Printf("default_model = %s # %s\n",
		effective.DefaultModel,
		stringSource(effective.DefaultModel, defaults.DefaultModel, hasFile))

	if effective.Planner != nil {
		defaultStepLimit := 0
		if defaults.Planner != nil {
			defaultStepLimit = defaults.Planner.StepLimit
		}
		fmt.Printf("planner.step_limit = %d # %s\n",
			effective.Planner.StepLimit,
			intSource(effective.Planner.StepLimit, defaultStepLimit, hasFile))
	} else {
		fmt.Printf("planner.step_limit = # default\n")
	}

	if effective.ShellAgent != nil {
		defaultFRS := 0
		if defaults.ShellAgent != nil {
			defaultFRS = defaults.ShellAgent.FinalizeRemainingSteps
		}
		fmt.Printf("shell-agent.finalize_remaining_steps = %d # %s\n",
			effective.ShellAgent.FinalizeRemainingSteps,
			intSource(effective.ShellAgent.FinalizeRemainingSteps, defaultFRS, hasFile))
	} else {
		fmt.Printf("shell-agent.finalize_remaining_steps = # default\n")
	}

	if effective.Environment != nil {
		defaultEnv := defaults.Environment
		if defaultEnv == nil {
			defaultEnv = &llm.EnvironmentConfig{}
		}
		fmt.Printf("environment.type = %s # %s\n",
			effective.Environment.Type,
			stringSource(effective.Environment.Type, defaultEnv.Type, hasFile))
		fmt.Printf("environment.timeout = %d # %s\n",
			effective.Environment.Timeout,
			intSource(effective.Environment.Timeout, defaultEnv.Timeout, hasFile))
		fmt.Printf("environment.cwd = %s # %s\n",
			effective.Environment.CWD,
			stringSource(effective.Environment.CWD, defaultEnv.CWD, hasFile))
		fmt.Printf("environment.max_command_output_bytes = %d # %s\n",
			effective.Environment.MaxCommandOutputBytes,
			intSource(effective.Environment.MaxCommandOutputBytes, defaultEnv.MaxCommandOutputBytes, hasFile))

		defaultInternetAccess := false
		if defaultEnv != nil {
			defaultInternetAccess = defaultEnv.InternetAccess
		}
		fmt.Printf("environment.internet_access = %t # %s\n",
			effective.Environment.InternetAccess,
			internetAccessSource(effective.Environment.InternetAccess, defaultInternetAccess, hasFile))
	}

	// --- Providers ---
	for name, prov := range effective.Providers {
		defaultBaseURL := ""
		defaultAPIKey := ""
		defaultEndpoint := ""
		if defProv, ok := defaults.Providers[name]; ok {
			defaultBaseURL = defProv.BaseURL
			defaultAPIKey = defProv.APIKey
			defaultEndpoint = defProv.Endpoint
		}
		fmt.Printf("providers.%s.base_url = %s # %s\n",
			name, prov.BaseURL,
			stringSource(prov.BaseURL, defaultBaseURL, hasFile))
		fmt.Printf("providers.%s.api_key = %s # %s\n",
			name, prov.APIKey,
			stringSource(prov.APIKey, defaultAPIKey, hasFile))
		fmt.Printf("providers.%s.endpoint = %s # %s\n",
			name, prov.Endpoint,
			stringSource(prov.Endpoint, defaultEndpoint, hasFile))
	}

	// --- Models ---
	for name, model := range effective.Models {
		defaultProvider := ""
		defaultModel := ""
		if defModel, ok := defaults.Models[name]; ok {
			defaultProvider = defModel.Provider
			defaultModel = defModel.Model
		}
		fmt.Printf("models.%s.provider = %s # %s\n",
			name, model.Provider,
			stringSource(model.Provider, defaultProvider, hasFile))
		fmt.Printf("models.%s.model = %s # %s\n",
			name, model.Model,
			stringSource(model.Model, defaultModel, hasFile))

		if len(model.Params) > 0 {
			for k, v := range model.Params {
				fmt.Printf("models.%s.params.%s = %s # %s\n",
					name, k, formatParamValue(v),
					paramSource(name, defaults.Models, hasFile))
			}
		}
	}
}

// stringSource returns "config.toml" when hasFile is true and the effective
// value differs from the default, otherwise "default".
func stringSource(effective, defaults string, hasFile bool) string {
	if hasFile && effective != defaults {
		return "config.toml"
	}
	return "default"
}

// intSource returns "config.toml" when hasFile is true and the effective
// value differs from the default, otherwise "default".
func intSource(effective, defaults int, hasFile bool) string {
	if hasFile && effective != defaults {
		return "config.toml"
	}
	return "default"
}

// internetAccessSource is the same as stringSource but for booleans,
// formatted with %t.
func internetAccessSource(effective, defaults bool, hasFile bool) string {
	if hasFile && effective != defaults {
		return "config.toml"
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
func paramSource(modelName string, defaults map[string]llm.ModelDefinition, hasFile bool) string {
	if !hasFile {
		return "default"
	}
	if _, exists := defaults[modelName]; exists {
		return "default"
	}
	return "config.toml"
}
