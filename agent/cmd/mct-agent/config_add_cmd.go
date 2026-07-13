package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

func handleConfigAddCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent config add", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags configTargetFlags
	addConfigTargetFlags(fs, &flags)
	noInteractive := fs.Bool("no-interactive", false, "never prompt; require complete arguments")
	providerName := fs.String("provider", "", "provider name")
	providerURL := fs.String("url", "", "provider base URL (new providers only)")
	apiKey := fs.String("api-key", "", "literal provider API key (new providers only)")
	apiKeyEnv := fs.String("api-key-env", "", "API key environment variable (new providers only)")
	modelID := fs.String("model", "", "provider model identifier")
	alias := fs.String("alias", "", "model alias")
	reasoning := fs.String("reasoning", "", "reasoning effort; omit for provider default")
	makeDefault := fs.Bool("default", false, "make the added model the default")
	noCache := fs.Bool("no-cache", false, "disable global caching when creating a new configuration")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return configUsageError("config add takes flags, not positional arguments")
	}
	target, err := resolveConfigTarget(flags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	if err := requireInteractive(*noInteractive); err != nil {
		return configError(err)
	}
	_, statErr := os.Stat(target.path)
	newConfig := os.IsNotExist(statErr)
	doc, err := loadConfigDocument(target, true)
	if err != nil {
		return configError(err)
	}
	providers := configTable(doc.raw, "providers")
	models := configTable(doc.raw, "models")
	reader := bufio.NewReader(os.Stdin)
	if !*noInteractive {
		if strings.TrimSpace(*providerName) == "" {
			if len(providers) > 0 {
				choices := append(sortedKeys(providers), "Add a new provider...")
				selected, selectErr := promptSelection("Provider", choices)
				if selectErr != nil {
					return configError(selectErr)
				}
				if selected != "Add a new provider..." {
					*providerName = selected
				}
			}
			if *providerName == "" {
				*providerName, err = promptLineDefault(reader, "Provider name", "default", true)
				if err != nil {
					return configError(err)
				}
			}
		}
	}
	*providerName = strings.TrimSpace(*providerName)
	if *providerName == "" {
		return configUsageError("--provider is required")
	}
	_, providerExists := providers[*providerName]
	if providerExists && (fs.Changed("url") || fs.Changed("api-key") || fs.Changed("api-key-env")) {
		return configUsageError("provider definition flags cannot be used for an existing provider; use config provider set")
	}
	if !providerExists {
		if !*noInteractive {
			if strings.TrimSpace(*providerURL) == "" {
				*providerURL, err = promptLineDefault(reader, "Provider URL", "", true)
				if err != nil {
					return configError(err)
				}
			}
			if !fs.Changed("api-key") && !fs.Changed("api-key-env") {
				*apiKey, err = promptSecret("API key")
				if err != nil {
					return configError(err)
				}
			}
		}
		key, keyErr := apiKeyValue(strings.TrimSpace(*apiKey), strings.TrimSpace(*apiKeyEnv))
		if keyErr != nil {
			return configUsageError(keyErr.Error())
		}
		if strings.TrimSpace(*providerURL) == "" || key == "" {
			return configUsageError("new providers require --url and either --api-key or --api-key-env")
		}
		providers[*providerName] = map[string]any{"base_url": strings.TrimSpace(*providerURL), "api_key": key}
	}
	if !*noInteractive {
		if strings.TrimSpace(*modelID) == "" {
			*modelID, err = promptLineDefault(reader, "Model", "", true)
			if err != nil {
				return configError(err)
			}
		}
		if strings.TrimSpace(*alias) == "" {
			suggested := "default"
			if len(models) > 0 {
				suggested = fmt.Sprintf("model-%d", len(models)+1)
			}
			*alias, err = promptLineDefault(reader, "Model alias", suggested, true)
			if err != nil {
				return configError(err)
			}
		}
		if !fs.Changed("reasoning") {
			*reasoning, err = promptLineDefault(reader, "Reasoning (blank for provider default)", "", false)
			if err != nil {
				return configError(err)
			}
		}
		*reasoning, err = confirmProviderReasoning(reader, *reasoning)
		if err != nil {
			return configError(err)
		}
	}
	*modelID, *alias, *reasoning = strings.TrimSpace(*modelID), strings.TrimSpace(*alias), strings.TrimSpace(*reasoning)
	if *modelID == "" || *alias == "" {
		return configUsageError("--model and --alias are required")
	}
	if _, exists := models[*alias]; exists {
		return configError(fmt.Errorf("model %q already exists", *alias))
	}
	if suggestion, likely := reasoningTypoSuggestion(*reasoning); likely {
		return configUsageError(fmt.Sprintf("reasoning value %q looks misspelled; did you mean %q?", *reasoning, suggestion))
	}
	entry := map[string]any{"provider": *providerName, "model": *modelID}
	if *reasoning != "" {
		entry["params"] = map[string]any{"reasoning": map[string]any{"effort": *reasoning}}
	}
	models[*alias] = entry
	currentDefault, _ := doc.raw["default_model"].(string)
	if !*noInteractive && currentDefault != "" && !fs.Changed("default") {
		*makeDefault, err = promptYesNo(reader, os.Stdout, fmt.Sprintf("Make %q the default model? [y/N]: ", *alias), false)
		if err != nil {
			return configError(err)
		}
	}
	if currentDefault == "" || *makeDefault {
		doc.raw["default_model"] = *alias
	}
	if newConfig {
		cacheEnabled := !*noCache
		defaults := map[string]any{"cache_enabled": cacheEnabled}
		if cacheEnabled {
			defaults["cache_key_name"] = defaultCacheKeyName
			defaults["cache_control"] = map[string]any{"type": "ephemeral"}
			defaults["cache_trigger_threshold"] = defaultCacheTriggerThreshold
			defaults["cache_lookback_offset"] = defaultCacheLookbackOffset
		}
		doc.raw["model_defaults"] = defaults
	} else if *noCache {
		return configUsageError("--no-cache is only valid when creating a new configuration")
	}
	confirmed, err := confirmConfigChange(*noInteractive, fmt.Sprintf("Add model %q using provider %q%s.", *alias, *providerName, func() string {
		if providerExists {
			return ""
		}
		return " (new provider)"
	}()))
	if err != nil {
		return configError(err)
	}
	if !confirmed {
		fmt.Println("No changes written.")
		return 0
	}
	if err := doc.save(); err != nil {
		return configError(err)
	}
	fmt.Printf("Added model %s using provider %s.\n", *alias, *providerName)
	if !*noInteractive {
		more, promptErr := promptYesNo(reader, os.Stdout, "Add another provider or model? [y/N]: ", false)
		if promptErr != nil {
			return configError(promptErr)
		}
		if more {
			return handleConfigAddCommand(configTargetArgs(flags))
		}
	}
	return 0
}
