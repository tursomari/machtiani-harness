package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

var cacheNumberFlags = map[string]string{
	"trigger-threshold": "cache_trigger_threshold",
	"lookback-offset":   "cache_lookback_offset",
	"reanchor-tokens":   "cache_reanchor_tokens",
	"reanchor-messages": "cache_reanchor_messages",
	"min-cached-tokens": "cache_reanchor_min_cached_tokens",
}

func handleConfigCacheCommand(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent config cache <show|enable|disable|inherit|set> [flags]")
		return 0
	}
	action := args[0]
	if action != "show" && action != "enable" && action != "disable" && action != "inherit" && action != "set" {
		fmt.Fprintf(os.Stderr, "Unknown cache subcommand: %s\n", action)
		return 2
	}
	fs := pflag.NewFlagSet("mct-agent config cache "+action, pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags configTargetFlags
	addConfigTargetFlags(fs, &flags)
	model := fs.String("model", "", "model alias; omit for global defaults")
	var noInteractive *bool
	if action != "show" {
		noInteractive = fs.Bool("no-interactive", false, "never prompt; require complete arguments")
	}
	keyName := fs.String("key-name", "", "cache marker key name")
	controlJSON := fs.String("control-json", "", "cache control JSON object")
	numberValues := map[string]*string{}
	for flagName := range cacheNumberFlags {
		numberValues[flagName] = fs.String(flagName, "", "non-negative cache tuning value")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return configUsageError("cache commands take no positional arguments")
	}
	target, err := resolveConfigTarget(flags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	if action != "show" {
		if err := requireInteractive(*noInteractive); err != nil {
			return configError(err)
		}
	}
	doc, err := loadConfigDocument(target, false)
	if err != nil {
		return configError(err)
	}
	table, err := cacheTargetTable(doc.raw, strings.TrimSpace(*model))
	if err != nil {
		return configError(err)
	}
	if action == "show" {
		printCacheConfiguration(doc.raw, strings.TrimSpace(*model), table)
		return 0
	}
	if action == "inherit" {
		if strings.TrimSpace(*model) == "" {
			return configUsageError("cache inherit requires --model")
		}
		confirmed, err := confirmConfigChange(*noInteractive, fmt.Sprintf("Remove cache overrides from model %q.", *model))
		if err != nil {
			return configError(err)
		}
		if !confirmed {
			return 0
		}
		for _, key := range cacheConfigKeys() {
			delete(table, key)
		}
	} else {
		if action == "set" && !*noInteractive {
			reader := bufio.NewReader(os.Stdin)
			currentKey, _ := table["cache_key_name"].(string)
			*keyName, err = promptLineDefault(reader, "Cache key name", currentKey, false)
			if err != nil {
				return configError(err)
			}
			_ = fs.Set("key-name", *keyName)
			currentControl := ""
			if control, ok := table["cache_control"].(map[string]any); ok {
				if encoded, marshalErr := json.Marshal(control); marshalErr == nil {
					currentControl = string(encoded)
				}
			}
			*controlJSON, err = promptLineDefault(reader, "Cache control JSON", currentControl, false)
			if err != nil {
				return configError(err)
			}
			if *controlJSON != "" {
				_ = fs.Set("control-json", *controlJSON)
			}
			for flagName, key := range cacheNumberFlags {
				current := ""
				if value, ok := table[key]; ok {
					current = fmt.Sprint(value)
				}
				value, promptErr := promptLineDefault(reader, flagName, current, false)
				if promptErr != nil {
					return configError(promptErr)
				}
				if value != "" {
					_ = fs.Set(flagName, value)
				}
			}
		}
		switch action {
		case "enable":
			table["cache_enabled"] = true
			if strings.TrimSpace(*model) == "" {
				if _, ok := table["cache_key_name"]; !ok {
					table["cache_key_name"] = defaultCacheKeyName
				}
				if _, ok := table["cache_control"]; !ok {
					table["cache_control"] = map[string]any{"type": "ephemeral"}
				}
				if _, ok := table["cache_trigger_threshold"]; !ok {
					table["cache_trigger_threshold"] = defaultCacheTriggerThreshold
				}
				if _, ok := table["cache_lookback_offset"]; !ok {
					table["cache_lookback_offset"] = defaultCacheLookbackOffset
				}
			}
		case "disable":
			table["cache_enabled"] = false
		case "set":
			if !cacheSetChanged(fs) && *noInteractive {
				return configUsageError("cache set requires at least one setting flag")
			}
			if fs.Changed("key-name") {
				table["cache_key_name"] = strings.TrimSpace(*keyName)
			}
			if fs.Changed("control-json") {
				control, err := parseJSONMap(*controlJSON)
				if err != nil {
					return configUsageError(err.Error())
				}
				table["cache_control"] = control
			}
			for flagName, key := range cacheNumberFlags {
				if fs.Changed(flagName) {
					value, err := parseNonNegativeFlag(flagName, *numberValues[flagName])
					if err != nil {
						return configUsageError(err.Error())
					}
					table[key] = value
				}
			}
		}
		confirmed, err := confirmConfigChange(*noInteractive, cacheChangeSummary(action, *model))
		if err != nil {
			return configError(err)
		}
		if !confirmed {
			return 0
		}
	}
	if err := doc.save(); err != nil {
		return configError(err)
	}
	fmt.Printf("Updated cache configuration in %s.\n", target.path)
	return 0
}

func cacheTargetTable(raw map[string]any, alias string) (map[string]any, error) {
	if alias == "" {
		return configTable(raw, "model_defaults"), nil
	}
	model, ok := configTable(raw, "models")[alias].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("model %q not found", alias)
	}
	return model, nil
}

func cacheConfigKeys() []string {
	return []string{"cache_enabled", "cache_key_name", "cache_control", "cache_trigger_threshold", "cache_lookback_offset", "cache_reanchor_tokens", "cache_reanchor_messages", "cache_reanchor_min_cached_tokens"}
}

func cacheSetChanged(fs *pflag.FlagSet) bool {
	if fs.Changed("key-name") || fs.Changed("control-json") {
		return true
	}
	for name := range cacheNumberFlags {
		if fs.Changed(name) {
			return true
		}
	}
	return false
}

func cacheChangeSummary(action, alias string) string {
	target := "global model defaults"
	if alias != "" {
		target = fmt.Sprintf("model %q", alias)
	}
	return fmt.Sprintf("%s caching for %s.", configActionTitle(action), target)
}

func printCacheConfiguration(raw map[string]any, alias string, table map[string]any) {
	if alias == "" {
		fmt.Println("Global cache defaults")
		for _, key := range cacheConfigKeys() {
			if value, ok := table[key]; ok {
				fmt.Printf("  %s: %v\n", key, value)
			}
		}
		return
	}
	fmt.Printf("Cache for model %s\n", alias)
	defaults, _ := raw["model_defaults"].(map[string]any)
	for _, key := range cacheConfigKeys() {
		if value, ok := table[key]; ok {
			fmt.Printf("  %s: %v (model override)\n", key, value)
		} else if value, ok := defaults[key]; ok {
			fmt.Printf("  %s: %v (inherited)\n", key, value)
		}
	}
}
