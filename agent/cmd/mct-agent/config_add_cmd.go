package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/configcatalog"
)

func handleConfigAddCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent config add", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags configTargetFlags
	addConfigTargetFlags(fs, &flags)
	noInteractive := fs.Bool("no-interactive", false, "never prompt; use supplied or catalogue values")
	presetID := fs.String("preset", "", "provider catalogue preset")
	providerName := fs.String("provider", "", "provider name")
	providerURL := fs.String("url", "", "provider base URL (new providers only)")
	endpoint := fs.String("endpoint", "", "provider endpoint path (new providers only)")
	apiKey := fs.String("api-key", "", "literal provider API key (new providers only)")
	apiKeyEnv := fs.String("api-key-env", "", "API key environment variable (new providers only)")
	headers := fs.StringArray("header", nil, "provider header in key=value form (repeatable)")
	queries := fs.StringArray("query", nil, "provider query parameter in key=value form (repeatable)")
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
	catalog, err := configcatalog.Load()
	if err != nil {
		return configError(err)
	}
	var preset *configcatalog.Provider
	if strings.TrimSpace(*presetID) != "" {
		value, ok := catalog.Provider(strings.TrimSpace(*presetID))
		if !ok {
			return configUsageError(fmt.Sprintf("unknown provider preset %q; use 'mct-agent config catalog list'", *presetID))
		}
		preset = &value
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

	if !*noInteractive && strings.TrimSpace(*providerName) == "" && preset == nil {
		selection, selectErr := promptProviderChoice(providers, catalog)
		if selectErr != nil {
			return configError(selectErr)
		}
		switch {
		case strings.HasPrefix(selection, "existing:"):
			*providerName = strings.TrimPrefix(selection, "existing:")
		case strings.HasPrefix(selection, "catalog:"):
			value, _ := catalog.Provider(strings.TrimPrefix(selection, "catalog:"))
			preset = &value
			*presetID = value.ID
		case selection == "other":
			*providerName, err = promptLineDefault(reader, "Provider name", "default", true)
			if err != nil {
				return configError(err)
			}
		}
	}
	if preset != nil {
		if strings.TrimSpace(*providerName) != "" && strings.TrimSpace(*providerName) != preset.ID {
			return configUsageError(fmt.Sprintf("--provider must be %q when --preset %s is used", preset.ID, preset.ID))
		}
		*providerName = preset.ID
		if !fs.Changed("url") {
			*providerURL = preset.BaseURL
		}
		if !fs.Changed("endpoint") {
			*endpoint = preset.Endpoint
		}
		if !fs.Changed("api-key") && !fs.Changed("api-key-env") {
			if !*noInteractive && strings.TrimSpace(os.Getenv(preset.APIKeyEnv)) == "" {
				*apiKey, err = promptSecret(fmt.Sprintf("API key (%s is not set)", preset.APIKeyEnv))
				if err != nil {
					return configError(err)
				}
			} else {
				*apiKeyEnv = preset.APIKeyEnv
			}
		}
	}
	if !*noInteractive && strings.TrimSpace(*providerName) == "" {
		*providerName, err = promptLineDefault(reader, "Provider name", "default", true)
		if err != nil {
			return configError(err)
		}
	}
	*providerName = strings.TrimSpace(*providerName)
	if *providerName == "" {
		return configUsageError("--provider or --preset is required")
	}

	_, providerExists := providers[*providerName]
	providerDefinitionChanged := fs.Changed("url") || fs.Changed("endpoint") || fs.Changed("api-key") || fs.Changed("api-key-env") || fs.Changed("header") || fs.Changed("query")
	if providerExists && (providerDefinitionChanged || preset != nil) {
		return configUsageError("provider definition flags and --preset cannot be used for an existing provider; use config provider set")
	}
	if !providerExists {
		if !*noInteractive && preset == nil {
			if strings.TrimSpace(*providerURL) == "" {
				*providerURL, err = promptLineDefault(reader, "Provider URL", "", true)
				if err != nil {
					return configError(err)
				}
			}
			if !fs.Changed("endpoint") {
				*endpoint, err = promptLineDefault(reader, "Endpoint (blank to use provider default)", "", false)
				if err != nil {
					return configError(err)
				}
			}
			if !fs.Changed("header") && !fs.Changed("query") {
				advanced, promptErr := promptYesNo(reader, os.Stdout, "Add custom headers or query parameters? [y/N]: ", false)
				if promptErr != nil {
					return configError(promptErr)
				}
				if advanced {
					*headers, err = promptKeyValueList(reader, "Header key=value (blank to finish)")
					if err != nil {
						return configError(err)
					}
					*queries, err = promptKeyValueList(reader, "Query key=value (blank to finish)")
					if err != nil {
						return configError(err)
					}
				}
			}
		}
		if !*noInteractive && !fs.Changed("api-key") && !fs.Changed("api-key-env") && preset == nil {
			*apiKey, err = promptSecret("API key")
			if err != nil {
				return configError(err)
			}
		}
		key, keyErr := apiKeyValue(strings.TrimSpace(*apiKey), strings.TrimSpace(*apiKeyEnv))
		if keyErr != nil {
			return configUsageError(keyErr.Error())
		}
		if strings.TrimSpace(*providerURL) == "" || key == "" {
			return configUsageError("new providers require --url and either --api-key or --api-key-env (or use --preset)")
		}
		headerMap, mapErr := parseKeyValueFlags(*headers, "header")
		if mapErr != nil {
			return configUsageError(mapErr.Error())
		}
		queryMap, mapErr := parseKeyValueFlags(*queries, "query")
		if mapErr != nil {
			return configUsageError(mapErr.Error())
		}
		providerEntry := map[string]any{"base_url": strings.TrimSpace(*providerURL), "api_key": key}
		if value := strings.TrimSpace(*endpoint); value != "" {
			providerEntry["endpoint"] = value
		}
		if len(headerMap) > 0 {
			providerEntry["headers"] = headerMap
		}
		if len(queryMap) > 0 {
			providerEntry["query"] = queryMap
		}
		providers[*providerName] = providerEntry
	}

	modelCatalogProvider := preset
	discoveryCredential := modelDiscoveryCredential(*apiKey, *apiKeyEnv)
	if modelCatalogProvider == nil && providerExists && !*noInteractive {
		if value, ok := catalogProviderForConfigured(*providerName, providers[*providerName], catalog); ok {
			modelCatalogProvider = &value
			discoveryCredential = configuredProviderCredential(providers[*providerName])
		}
	}
	var catalogModel *configcatalog.Model
	if modelCatalogProvider != nil {
		if strings.TrimSpace(*modelID) == "" {
			if *noInteractive {
				*modelID = modelCatalogProvider.DefaultModel
			} else {
				selected, selectErr := promptCatalogModel(reader, *modelCatalogProvider, discoveryCredential)
				if selectErr != nil {
					return configError(selectErr)
				}
				if selected != "other" {
					*modelID = selected
				}
			}
		}
		if value, ok := modelCatalogProvider.Model(strings.TrimSpace(*modelID)); ok {
			catalogModel = &value
			if strings.TrimSpace(*alias) == "" {
				*alias = value.Alias
			}
		}
	}
	if !*noInteractive {
		if strings.TrimSpace(*modelID) == "" {
			*modelID, err = promptLineDefault(reader, "Model", "", true)
			if err != nil {
				return configError(err)
			}
		}
		if strings.TrimSpace(*alias) == "" {
			suggested := uniqueModelAliasSuggestion(*modelID, models)
			*alias, err = promptLineDefault(reader, "Model alias", suggested, true)
			if err != nil {
				return configError(err)
			}
		}
		if !fs.Changed("reasoning") {
			*reasoning, err = promptReasoningChoice(reader, catalogModel)
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
		return configUsageError("--model and --alias are required (catalogue presets supply defaults)")
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
	if catalogModel != nil {
		switch catalogModel.CacheDefault {
		case "enabled":
			entry["cache_enabled"] = true
		case "disabled":
			entry["cache_enabled"] = false
		}
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

func promptProviderChoice(existing map[string]any, catalog configcatalog.Catalog) (string, error) {
	options := make([]initMenuOption, 0, len(existing)+len(catalog.Providers)+1)
	for _, name := range sortedKeys(existing) {
		options = append(options, initMenuOption{label: "Existing: " + name, value: "existing:" + name})
	}
	for _, provider := range catalog.Providers {
		if _, exists := existing[provider.ID]; !exists {
			options = append(options, initMenuOption{label: provider.Name + " — " + provider.Description, value: "catalog:" + provider.ID})
		}
	}
	options = append(options, initMenuOption{label: "Other provider...", value: "other"})
	return promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "Provider", "Choose a configured provider, a catalogue preset, or Other.", options)
}

func promptCatalogModel(reader *bufio.Reader, provider configcatalog.Provider, apiKey string) (string, error) {
	options := make([]initMenuOption, 0, len(provider.Models)+2)
	for _, model := range provider.Models {
		label := model.Name
		if model.ID == provider.DefaultModel {
			label += " (default)"
		}
		options = append(options, initMenuOption{label: label + " — " + model.Description, value: model.ID})
	}
	if provider.ModelsURL != "" {
		options = append(options, initMenuOption{label: "Search current model catalogue...", value: "search"})
	}
	options = append(options, initMenuOption{label: "Other model...", value: "other"})
	selected, err := promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "Model", "Choose a recommended model, search the provider, or enter another model ID.", options)
	if err != nil || selected != "search" {
		return selected, err
	}
	for {
		search, promptErr := promptLineDefault(reader, "Search model names or IDs", "", true)
		if promptErr != nil {
			return "", promptErr
		}
		matches, discoveryErr := discoverProviderModels(modelDiscoveryHTTPClient, provider, apiKey, search)
		if discoveryErr != nil {
			fmt.Fprintf(os.Stdout, "Could not load %s models: %v\nEnter the model ID manually instead.\n", provider.Name, discoveryErr)
			return "other", nil
		}
		if len(matches) == 0 {
			fmt.Fprintf(os.Stdout, "No %s models matched %q. Try another search.\n", provider.Name, search)
			continue
		}
		matchOptions := make([]initMenuOption, 0, len(matches)+2)
		for _, match := range matches {
			label := match.Name
			if label == "" {
				label = match.ID
			} else {
				label += " — " + match.ID
			}
			matchOptions = append(matchOptions, initMenuOption{label: label, value: match.ID})
		}
		matchOptions = append(matchOptions,
			initMenuOption{label: "Search again...", value: "search"},
			initMenuOption{label: "Other model...", value: "other"},
		)
		selected, selectErr := promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "Search results", "Showing up to 25 matches. Choose a model or search again.", matchOptions)
		if selectErr != nil {
			return "", selectErr
		}
		if selected != "search" {
			return selected, nil
		}
	}
}

func modelDiscoveryCredential(apiKey, apiKeyEnv string) string {
	if value := strings.TrimSpace(apiKey); value != "" {
		return value
	}
	if name := strings.TrimSpace(apiKeyEnv); name != "" {
		return os.Getenv(name)
	}
	return ""
}

func catalogProviderForConfigured(name string, entry any, catalog configcatalog.Catalog) (configcatalog.Provider, bool) {
	if provider, ok := catalog.Provider(strings.TrimSpace(name)); ok {
		return provider, true
	}
	table, ok := entry.(map[string]any)
	if !ok {
		return configcatalog.Provider{}, false
	}
	baseURL, _ := table["base_url"].(string)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return configcatalog.Provider{}, false
	}
	for _, provider := range catalog.Providers {
		if strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/") == baseURL {
			return provider, true
		}
	}
	return configcatalog.Provider{}, false
}

func configuredProviderCredential(entry any) string {
	table, ok := entry.(map[string]any)
	if !ok {
		return ""
	}
	value, _ := table["api_key"].(string)
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		return os.Getenv(strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}"))
	}
	return value
}

func promptReasoningChoice(reader *bufio.Reader, model *configcatalog.Model) (string, error) {
	values := []string{"low", "medium", "high"}
	if model != nil && len(model.Reasoning) > 0 {
		values = model.Reasoning
	}
	options := []initMenuOption{{label: "Provider default", value: ""}}
	for _, value := range values {
		options = append(options, initMenuOption{label: value, value: value})
	}
	options = append(options, initMenuOption{label: "Other...", value: "other"})
	selected, err := promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "Reasoning", "Choose an effort or let the provider use its default.", options)
	if err != nil || selected != "other" {
		return selected, err
	}
	return promptLineDefault(reader, "Reasoning value", "", true)
}

func parseKeyValueFlags(values []string, kind string) (map[string]any, error) {
	result := make(map[string]any, len(values))
	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("--%s must use key=value form: %q", kind, value)
		}
		result[key] = item
	}
	return result, nil
}

func promptKeyValueList(reader *bufio.Reader, label string) ([]string, error) {
	var values []string
	for {
		value, err := promptLineDefault(reader, label, "", false)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(value) == "" {
			return values, nil
		}
		values = append(values, value)
	}
}
