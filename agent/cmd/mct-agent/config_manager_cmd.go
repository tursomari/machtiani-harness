package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

func handleConfigManager(args []string) int {
	fs := pflag.NewFlagSet("mct-agent config", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags configTargetFlags
	addConfigTargetFlags(fs, &flags)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			printConfigUsage()
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return configUsageError("unexpected arguments")
	}
	target, err := resolveConfigTarget(flags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	if err := requireInteractive(false); err != nil {
		return configError(err)
	}
	if _, err := os.Stat(target.path); os.IsNotExist(err) {
		fmt.Println("No configuration exists at the selected path. Starting setup.")
		return handleConfigAddCommand(configTargetArgs(flags))
	}
	for {
		choice, err := promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "Configuration", "Choose an action. Changes are confirmed before writing.", []initMenuOption{
			{label: "Finish", value: "finish"},
			{label: "Add provider or model", value: "add"},
			{label: "Manage providers", value: "provider"},
			{label: "Manage models", value: "model"},
			{label: "Set default model", value: "default"},
			{label: "Configure caching", value: "cache"},
			{label: "Validate configuration", value: "check"},
		})
		if err != nil {
			return configError(err)
		}
		base := configTargetArgs(flags)
		switch choice {
		case "add":
			handleConfigAddCommand(base)
		case "provider":
			handleInteractiveProviderMenu(flags)
		case "model":
			handleInteractiveModelMenu(flags)
		case "default":
			handleConfigModelCommand(append([]string{"default"}, base...))
		case "cache":
			handleInteractiveCacheMenu(flags)
		case "check":
			handleManagedConfigCheck(base)
		case "finish":
			return 0
		}
	}
}

func handleInteractiveProviderMenu(flags configTargetFlags) {
	base := configTargetArgs(flags)
	for {
		action, err := promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "Providers", "Choose an action.", []initMenuOption{
			{label: "List providers", value: "list"}, {label: "Show provider", value: "show"},
			{label: "Add provider", value: "add"}, {label: "Edit provider", value: "set"},
			{label: "Rename provider", value: "rename"}, {label: "Remove provider", value: "remove"},
			{label: "Back", value: "back"},
		})
		if err != nil || action == "back" {
			return
		}
		if action == "list" {
			handleConfigProviderCommand(append([]string{"list"}, base...))
			continue
		}
		if action == "add" || action == "set" || action == "remove" {
			handleConfigProviderCommand(append([]string{action}, base...))
			continue
		}
		names, loadErr := configuredNames(flags, "providers")
		if loadErr != nil {
			configError(loadErr)
			continue
		}
		name, selectErr := promptSelection("Provider", names)
		if selectErr != nil {
			configError(selectErr)
			continue
		}
		if action == "show" {
			handleConfigProviderCommand(append([]string{"show", name}, base...))
			continue
		}
		newName, promptErr := promptLineDefault(bufio.NewReader(os.Stdin), "New provider name", "", true)
		if promptErr != nil {
			configError(promptErr)
			continue
		}
		handleConfigProviderCommand(append([]string{"rename", name, newName}, base...))
	}
}

func handleInteractiveModelMenu(flags configTargetFlags) {
	base := configTargetArgs(flags)
	for {
		action, err := promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "Models", "Choose an action.", []initMenuOption{
			{label: "List models", value: "list"}, {label: "Show model", value: "show"},
			{label: "Add model", value: "add"}, {label: "Edit model", value: "set"},
			{label: "Rename model", value: "rename"}, {label: "Remove model", value: "remove"},
			{label: "Set default model", value: "default"}, {label: "Back", value: "back"},
		})
		if err != nil || action == "back" {
			return
		}
		if action == "list" {
			handleConfigModelCommand(append([]string{"list"}, base...))
			continue
		}
		if action == "add" || action == "set" || action == "remove" || action == "default" {
			handleConfigModelCommand(append([]string{action}, base...))
			continue
		}
		names, loadErr := configuredNames(flags, "models")
		if loadErr != nil {
			configError(loadErr)
			continue
		}
		name, selectErr := promptSelection("Model", names)
		if selectErr != nil {
			configError(selectErr)
			continue
		}
		if action == "show" {
			handleConfigModelCommand(append([]string{"show", name}, base...))
			continue
		}
		newName, promptErr := promptLineDefault(bufio.NewReader(os.Stdin), "New model alias", "", true)
		if promptErr != nil {
			configError(promptErr)
			continue
		}
		handleConfigModelCommand(append([]string{"rename", name, newName}, base...))
	}
}

func handleInteractiveCacheMenu(flags configTargetFlags) {
	base := configTargetArgs(flags)
	for {
		action, err := promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "Caching", "Choose an action.", []initMenuOption{
			{label: "Show global defaults", value: "show"}, {label: "Enable global caching", value: "enable"},
			{label: "Disable global caching", value: "disable"}, {label: "Edit global cache settings", value: "set"},
			{label: "Show model caching", value: "model-show"}, {label: "Enable model caching", value: "model-enable"},
			{label: "Disable model caching", value: "model-disable"}, {label: "Restore model inheritance", value: "model-inherit"},
			{label: "Back", value: "back"},
		})
		if err != nil || action == "back" {
			return
		}
		if !strings.HasPrefix(action, "model-") {
			handleConfigCacheCommand(append([]string{action}, base...))
			continue
		}
		names, loadErr := configuredNames(flags, "models")
		if loadErr != nil {
			configError(loadErr)
			continue
		}
		name, selectErr := promptSelection("Model", names)
		if selectErr != nil {
			configError(selectErr)
			continue
		}
		operation := strings.TrimPrefix(action, "model-")
		child := []string{operation, "--model", name}
		handleConfigCacheCommand(append(child, base...))
	}
}

func configuredNames(flags configTargetFlags, table string) ([]string, error) {
	target, err := resolveConfigTarget(flags)
	if err != nil {
		return nil, err
	}
	doc, err := loadConfigDocument(target, false)
	if err != nil {
		return nil, err
	}
	return sortedKeys(configTable(doc.raw, table)), nil
}

func configTargetArgs(flags configTargetFlags) []string {
	if strings.TrimSpace(flags.path) != "" {
		return []string{"--path", flags.path}
	}
	if flags.global {
		return []string{"--global"}
	}
	return nil
}

func handleManagedConfigCheck(args []string) int {
	fs := pflag.NewFlagSet("mct-agent config check", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags configTargetFlags
	addConfigTargetFlags(fs, &flags)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return configUsageError("config check takes no positional arguments")
	}
	target, err := resolveConfigTarget(flags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	doc, err := loadConfigDocument(target, false)
	if err != nil {
		return configError(err)
	}
	if err := validateConfigDocument(doc.raw); err != nil {
		return configError(err)
	}
	fmt.Printf("Config OK: %s\n", target.path)
	if value, _ := doc.raw["default_model"].(string); value != "" {
		fmt.Printf("Default model: %s\n", value)
	}
	return 0
}

func handleManagedConfigShow(args []string) int {
	fs := pflag.NewFlagSet("mct-agent config show", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags configTargetFlags
	addConfigTargetFlags(fs, &flags)
	full := fs.BoolP("full", "f", false, "show all configuration settings")
	key := fs.StringP("key", "k", "", "show documentation for a specific key")
	verbose := fs.Bool("verbose", false, "enable verbose source display")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return configUsageError("config show takes no positional arguments")
	}
	target, err := resolveConfigTarget(flags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	old, had := os.LookupEnv("MACHTIANI_CONFIG")
	if err := os.Setenv("MACHTIANI_CONFIG", target.path); err != nil {
		return configError(err)
	}
	defer func() {
		if had {
			_ = os.Setenv("MACHTIANI_CONFIG", old)
		} else {
			_ = os.Unsetenv("MACHTIANI_CONFIG")
		}
	}()
	forward := []string{}
	if *full {
		forward = append(forward, "--full")
	}
	if *key != "" {
		forward = append(forward, "--key", *key)
	}
	if *verbose {
		forward = append(forward, "--verbose")
	}
	return handleConfigShowCommand(forward)
}
