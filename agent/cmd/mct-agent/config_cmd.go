package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

// init registers cliCommand entries for config subcommands. The existing
// handleConfigCommand in main.go dispatches config subcommands directly;
// no top-level cliCommand entries are added here.
func init() {
}

// handleConfigProviderCommand dispatches config provider subcommands.
func handleConfigProviderCommand(args []string) int {
	if len(args) < 1 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent config provider <subcommand>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  url        Set the provider base URL")
		fmt.Fprintln(os.Stderr, "  api-key    Set the provider API key")
		fmt.Fprintln(os.Stderr, "  model      Set the default model")
		fmt.Fprintln(os.Stderr, "  reasoning  Set the reasoning effort")
		return 2
	}

	switch args[0] {
	case "url":
		return handleConfigProviderURL(args[1:])
	case "api-key":
		return handleConfigProviderAPIKey(args[1:])
	case "model":
		return handleConfigProviderModel(args[1:])
	case "reasoning":
		return handleConfigProviderReasoning(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown config provider subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "Usage: mct-agent config provider <url|api-key|model|reasoning>")
		return 2
	}
}

// handleConfigProviderURL sets the provider base_url in the config file.
func handleConfigProviderURL(args []string) int {
	fs := pflag.NewFlagSet("mct-agent config provider url", pflag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent config provider url <url>\n")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: <url> argument is required")
		fs.Usage()
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "Error: too many arguments")
		fs.Usage()
		return 2
	}

	url := fs.Arg(0)

	// Verify config exists and resolve its path.
	_, _, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: config does not exist: %v\n", err)
		return 1
	}
	configPath, err := llm.ConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: config does not exist: %v\n", err)
		return 1
	}

	// Read the config into a generic map for raw manipulation.
	var config map[string]any
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
		return 1
	}

	// Ensure nested key path exists.
	providers, ok := config["providers"].(map[string]any)
	if !ok {
		providers = make(map[string]any)
		config["providers"] = providers
	}
	defaultProvider, ok := providers["default"].(map[string]any)
	if !ok {
		defaultProvider = make(map[string]any)
		providers["default"] = defaultProvider
	}
	defaultProvider["base_url"] = url

	// Write back.
	f, err := os.Create(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
		return 1
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding config: %v\n", err)
		return 1
	}

	fmt.Printf("Provider base_url set to: %s\n", url)
	return 0
}

// handleConfigProviderAPIKey sets the provider api_key in the config file.
func handleConfigProviderAPIKey(args []string) int {
	fs := pflag.NewFlagSet("mct-agent config provider api-key", pflag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent config provider api-key <key>\n")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: <key> argument is required")
		fs.Usage()
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "Error: too many arguments")
		fs.Usage()
		return 2
	}

	apiKey := fs.Arg(0)

	_, _, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: config does not exist: %v\n", err)
		return 1
	}
	configPath, err := llm.ConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: config does not exist: %v\n", err)
		return 1
	}

	var config map[string]any
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
		return 1
	}

	providers, ok := config["providers"].(map[string]any)
	if !ok {
		providers = make(map[string]any)
		config["providers"] = providers
	}
	defaultProvider, ok := providers["default"].(map[string]any)
	if !ok {
		defaultProvider = make(map[string]any)
		providers["default"] = defaultProvider
	}
	defaultProvider["api_key"] = apiKey

	f, err := os.Create(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
		return 1
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding config: %v\n", err)
		return 1
	}

	fmt.Println("Provider api_key set")
	return 0
}

// handleConfigProviderModel sets the default model in the config file.
func handleConfigProviderModel(args []string) int {
	fs := pflag.NewFlagSet("mct-agent config provider model", pflag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent config provider model <model>\n")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: <model> argument is required")
		fs.Usage()
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "Error: too many arguments")
		fs.Usage()
		return 2
	}

	model := fs.Arg(0)

	_, _, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: config does not exist: %v\n", err)
		return 1
	}
	configPath, err := llm.ConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: config does not exist: %v\n", err)
		return 1
	}

	var config map[string]any
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
		return 1
	}

	models, ok := config["models"].(map[string]any)
	if !ok {
		models = make(map[string]any)
		config["models"] = models
	}
	defaultModel, ok := models["default"].(map[string]any)
	if !ok {
		defaultModel = make(map[string]any)
		models["default"] = defaultModel
	}
	defaultModel["model"] = model

	f, err := os.Create(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
		return 1
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding config: %v\n", err)
		return 1
	}

	fmt.Printf("Model set to: %s\n", model)
	return 0
}

// handleConfigProviderReasoning sets the reasoning effort in the config file.
func handleConfigProviderReasoning(args []string) int {
	fs := pflag.NewFlagSet("mct-agent config provider reasoning", pflag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent config provider reasoning <effort>\n")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: <effort> argument is required")
		fs.Usage()
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "Error: too many arguments")
		fs.Usage()
		return 2
	}

	effort := fs.Arg(0)

	_, _, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: config does not exist: %v\n", err)
		return 1
	}
	configPath, err := llm.ConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: config does not exist: %v\n", err)
		return 1
	}

	var config map[string]any
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
		return 1
	}

	models, ok := config["models"].(map[string]any)
	if !ok {
		models = make(map[string]any)
		config["models"] = models
	}
	defaultModel, ok := models["default"].(map[string]any)
	if !ok {
		defaultModel = make(map[string]any)
		models["default"] = defaultModel
	}
	params, ok := defaultModel["params"].(map[string]any)
	if !ok {
		params = make(map[string]any)
		defaultModel["params"] = params
	}
	reasoning, ok := params["reasoning"].(map[string]any)
	if !ok {
		reasoning = make(map[string]any)
		params["reasoning"] = reasoning
	}
	reasoning["effort"] = effort

	f, err := os.Create(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
		return 1
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(config); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding config: %v\n", err)
		return 1
	}

	fmt.Printf("Reasoning effort set to: %s\n", effort)
	return 0
}
