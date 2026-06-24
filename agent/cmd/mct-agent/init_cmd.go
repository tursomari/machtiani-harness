package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

var _ = context.Background

func init() {
	cliCommands = append(cliCommands, cliCommand{
		name:        "init",
		description: "Initialize a minimal .machtiani/config.toml",
		handler:     handleInitCommand,
	})
}

func handleInitCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent init", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	providerURL := fs.String("provider-url", "", "LLM provider base URL (required)")
	apiKey := fs.String("api-key", "", "API key for the provider (required)")
	model := fs.String("model", "", "Model name (required)")
	reasoning := fs.String("reasoning", "medium", "Reasoning effort level")
	alias := fs.String("alias", "default", "Model alias name")
	force := fs.Bool("force", false, "Overwrite existing configuration")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent init --provider-url <url> --api-key <key> --model <name> [flags]\n\n")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 2
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	if strings.TrimSpace(*providerURL) == "" || strings.TrimSpace(*apiKey) == "" || strings.TrimSpace(*model) == "" {
		fmt.Fprintln(os.Stderr, "Error: --provider-url, --api-key, and --model are required")
		return 1
	}

	configPath := filepath.Join(".machtiani", "config.toml")

	if _, err := os.Stat(configPath); err == nil {
		if !*force {
			fmt.Fprintf(os.Stderr, "Error: config already exists at %s (use --force to overwrite)\n", configPath)
			return 1
		}
	}

	if err := os.MkdirAll(".machtiani", 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	cfg := llm.DefaultMinimalConfig()
	cfg.DefaultModel = *alias
	cfg.Providers = map[string]llm.ProviderConfig{
		*alias: {
			BaseURL: *providerURL,
			APIKey:  *apiKey,
		},
	}
	cfg.Models = map[string]llm.ModelDefinition{
		*alias: {
			Provider: *alias,
			Model:    *model,
			Params: map[string]interface{}{
				"reasoning": map[string]interface{}{
					"effort": *reasoning,
				},
			},
		},
	}

	f, err := os.Create(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer f.Close()

	encoder := toml.NewEncoder(f)
	if err := encoder.Encode(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	fmt.Printf("Configuration initialized at %s\n", configPath)
	return 0
}
