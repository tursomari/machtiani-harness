package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/configcatalog"
)

func handleConfigCatalogCommand(args []string) int {
	if len(args) == 0 {
		return configUsageError("config catalog requires 'list' or 'show <provider>'")
	}
	catalog, err := configcatalog.Load()
	if err != nil {
		return configError(err)
	}
	switch args[0] {
	case "list":
		fs := pflag.NewFlagSet("mct-agent config catalog list", pflag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, pflag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() != 0 {
			return configUsageError("config catalog list takes no arguments")
		}
		for _, provider := range catalog.Providers {
			fmt.Printf("%s\t%s\t%s\n", provider.ID, provider.Name, provider.DefaultModel)
		}
		return 0
	case "show":
		fs := pflag.NewFlagSet("mct-agent config catalog show", pflag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		asJSON := fs.Bool("json", false, "print machine-readable JSON")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, pflag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() != 1 {
			return configUsageError("config catalog show requires one provider id")
		}
		provider, ok := catalog.Provider(strings.TrimSpace(fs.Arg(0)))
		if !ok {
			return configError(fmt.Errorf("provider preset %q not found", fs.Arg(0)))
		}
		if *asJSON {
			encoded, encodeErr := json.MarshalIndent(provider, "", "  ")
			if encodeErr != nil {
				return configError(encodeErr)
			}
			fmt.Println(string(encoded))
			return 0
		}
		fmt.Printf("Provider: %s (%s)\n", provider.Name, provider.ID)
		fmt.Printf("URL: %s%s\n", provider.BaseURL, provider.Endpoint)
		fmt.Printf("API key environment: %s\n", provider.APIKeyEnv)
		fmt.Printf("Default model: %s\n", provider.DefaultModel)
		fmt.Printf("Documentation: %s\n", provider.DocumentationURL)
		fmt.Println("Models:")
		for _, model := range provider.Models {
			reasoning := "provider-defined"
			if len(model.Reasoning) > 0 {
				reasoning = strings.Join(model.Reasoning, ", ")
			}
			fmt.Printf("  %s (alias %s; reasoning: %s)\n", model.ID, model.Alias, reasoning)
		}
		return 0
	default:
		return configUsageError(fmt.Sprintf("unknown config catalog subcommand %q", args[0]))
	}
}
