package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

func configInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), true
	case float64:
		return int(number), true
	default:
		return 0, false
	}
}

func handleConfigProviderCommand(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "Usage: machtiani config provider <list|show|add|set|rename|remove> [flags]")
		return 0
	}
	switch args[0] {
	case "list", "show":
		return handleConfigProviderRead(args[0], args[1:])
	case "add", "set":
		return handleConfigProviderWrite(args[0], args[1:])
	case "rename":
		return handleConfigProviderRename(args[1:])
	case "remove":
		return handleConfigProviderRemove(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown provider subcommand: %s\n", args[0])
		return 2
	}
}

func handleConfigProviderRead(action string, args []string) int {
	fs := pflag.NewFlagSet("machtiani config provider "+action, pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var targetFlags configTargetFlags
	addConfigTargetFlags(fs, &targetFlags)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	target, err := resolveConfigTarget(targetFlags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	doc, err := loadConfigDocument(target, false)
	if err != nil {
		return configError(err)
	}
	providers := configTable(doc.raw, "providers")
	if action == "list" {
		if fs.NArg() != 0 {
			return configUsageError("provider list takes no positional arguments")
		}
		for _, name := range sortedKeys(providers) {
			provider, _ := providers[name].(map[string]any)
			fmt.Printf("%s\t%v\n", name, provider["base_url"])
		}
		return 0
	}
	if fs.NArg() != 1 {
		return configUsageError("provider show requires a provider name")
	}
	provider, ok := providers[fs.Arg(0)].(map[string]any)
	if !ok {
		return configError(fmt.Errorf("provider %q not found", fs.Arg(0)))
	}
	printProvider(fs.Arg(0), provider)
	if headers, ok := provider["headers"]; ok {
		fmt.Println("  headers:")
		printStringMap(os.Stdout, headers, "    ")
	}
	if query, ok := provider["query"]; ok {
		fmt.Println("  query:")
		printStringMap(os.Stdout, query, "    ")
	}
	return 0
}

func handleConfigProviderWrite(action string, args []string) int {
	fs := pflag.NewFlagSet("machtiani config provider "+action, pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var targetFlags configTargetFlags
	addConfigTargetFlags(fs, &targetFlags)
	noInteractive := fs.Bool("no-interactive", false, "never prompt; require complete arguments")
	url := fs.String("url", "", "provider base URL")
	apiKey := fs.String("api-key", "", "literal provider API key")
	apiKeyEnv := fs.String("api-key-env", "", "environment variable name to store by reference")
	clearAPIKey := fs.Bool("clear-api-key", false, "remove the configured API key")
	endpoint := fs.String("endpoint", "", "provider endpoint path")
	clearEndpoint := fs.Bool("clear-endpoint", false, "remove the provider endpoint")
	reasoningFormat := fs.String("reasoning-format", "", "reasoning request format: auto, reasoning_effort, reasoning, or reasoning_explicit")
	var headers, removeHeaders, queries, removeQueries multiString
	fs.Var(&headers, "header", "set provider header key=value (repeatable)")
	fs.Var(&removeHeaders, "remove-header", "remove provider header (repeatable)")
	fs.Var(&queries, "query", "set provider query key=value (repeatable)")
	fs.Var(&removeQueries, "remove-query", "remove provider query key (repeatable)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 {
		return configUsageError("expected at most one provider name")
	}
	target, err := resolveConfigTarget(targetFlags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	if err := requireInteractive(*noInteractive); err != nil {
		return configError(err)
	}
	doc, err := loadConfigDocument(target, false)
	if err != nil {
		return configError(err)
	}
	providers := configTable(doc.raw, "providers")
	name := ""
	if fs.NArg() == 1 {
		name = strings.TrimSpace(fs.Arg(0))
	}
	reader := bufio.NewReader(os.Stdin)
	if name == "" && !*noInteractive {
		if action == "set" {
			name, err = promptDocumentSelection(doc, "Provider", sortedKeys(providers))
		} else {
			name, err = promptLineDefault(reader, "Provider name", "", true)
		}
		if err != nil {
			return configError(err)
		}
	}
	if name == "" {
		return configUsageError("provider name is required")
	}
	existing, exists := providers[name].(map[string]any)
	if action == "add" && exists {
		return configError(fmt.Errorf("provider %q already exists", name))
	}
	if action == "set" && !exists {
		return configError(fmt.Errorf("provider %q not found", name))
	}
	entry := map[string]any{}
	if exists {
		entry = copyMap(existing)
	}
	if !*noInteractive {
		currentURL, _ := entry["base_url"].(string)
		if !fs.Changed("url") {
			*url, err = promptLineDefault(reader, "Provider URL", currentURL, true)
			if err != nil {
				return configError(err)
			}
			if *url != currentURL {
				_ = fs.Set("url", *url)
			}
		}
		if action == "add" && !fs.Changed("api-key") && !fs.Changed("api-key-env") {
			*apiKey, err = promptSecret("API key")
			if err != nil {
				return configError(err)
			}
		}
		if action == "set" && !fs.Changed("api-key") && !fs.Changed("api-key-env") && !*clearAPIKey {
			changeKey, promptErr := promptYesNo(reader, os.Stdout, "Replace the configured API key? [y/N]: ", false)
			if promptErr != nil {
				return configError(promptErr)
			}
			if changeKey {
				*apiKey, err = promptSecret("New API key")
				if err != nil {
					return configError(err)
				}
			}
		}
		if !fs.Changed("endpoint") && !*clearEndpoint {
			currentEndpoint, _ := entry["endpoint"].(string)
			*endpoint, err = promptLineDefault(reader, "Endpoint (optional)", currentEndpoint, false)
			if err != nil {
				return configError(err)
			}
			if *endpoint != currentEndpoint {
				_ = fs.Set("endpoint", *endpoint)
			}
		}
		if !fs.Changed("reasoning-format") {
			currentFormat, _ := entry["reasoning_format"].(string)
			*reasoningFormat, err = promptLineDefault(reader, "Reasoning request format (auto/reasoning_effort/reasoning/reasoning_explicit)", currentFormat, false)
			if err != nil {
				return configError(err)
			}
			if *reasoningFormat != currentFormat {
				_ = fs.Set("reasoning-format", *reasoningFormat)
			}
		}
	}
	if action == "add" && strings.TrimSpace(*url) == "" {
		return configUsageError("--url is required")
	}
	key, err := apiKeyValue(strings.TrimSpace(*apiKey), strings.TrimSpace(*apiKeyEnv))
	if err != nil {
		return configUsageError(err.Error())
	}
	if *clearAPIKey && key != "" {
		return configUsageError("--clear-api-key cannot be combined with API key flags")
	}
	if action == "add" && key == "" {
		return configUsageError("--api-key or --api-key-env is required")
	}
	if fs.Changed("url") || action == "add" {
		entry["base_url"] = strings.TrimSpace(*url)
	}
	if key != "" {
		entry["api_key"] = key
	}
	if *clearAPIKey {
		delete(entry, "api_key")
	}
	if fs.Changed("endpoint") {
		entry["endpoint"] = strings.TrimSpace(*endpoint)
	}
	if *clearEndpoint {
		delete(entry, "endpoint")
	}
	if fs.Changed("reasoning-format") {
		format := strings.ToLower(strings.TrimSpace(*reasoningFormat))
		switch format {
		case "", "auto":
			delete(entry, "reasoning_format")
		case "reasoning_effort", "reasoning", "reasoning_explicit":
			entry["reasoning_format"] = format
		default:
			return configUsageError("--reasoning-format must be auto, reasoning_effort, reasoning, or reasoning_explicit")
		}
	}
	if err := applyStringTableChanges(entry, "headers", headers, removeHeaders); err != nil {
		return configUsageError(err.Error())
	}
	if err := applyStringTableChanges(entry, "query", queries, removeQueries); err != nil {
		return configUsageError(err.Error())
	}
	if action == "set" && *noInteractive && !providerMutationChanged(fs) {
		return configUsageError("provider set requires at least one field flag")
	}
	ok, err := confirmConfigChange(*noInteractive, fmt.Sprintf("%s provider %q.", configActionTitle(action), name))
	if err != nil {
		return configError(err)
	}
	if !ok {
		fmt.Println("No changes written.")
		return 0
	}
	providers[name] = entry
	if err := doc.save(); err != nil {
		return configError(err)
	}
	fmt.Printf("%s provider %s.\n", configActionPast(action), name)
	return 0
}

func providerMutationChanged(fs *pflag.FlagSet) bool {
	for _, name := range []string{"url", "api-key", "api-key-env", "clear-api-key", "endpoint", "clear-endpoint", "reasoning-format", "header", "remove-header", "query", "remove-query"} {
		if fs.Changed(name) {
			return true
		}
	}
	return false
}

func applyStringTableChanges(entry map[string]any, key string, sets, removes []string) error {
	table, _ := entry[key].(map[string]any)
	if table == nil {
		table = map[string]any{}
	}
	assignments, err := parseAssignments(sets)
	if err != nil {
		return err
	}
	for name, value := range assignments {
		table[name] = value
	}
	for _, name := range removes {
		delete(table, name)
	}
	if len(table) == 0 {
		delete(entry, key)
	} else {
		entry[key] = table
	}
	return nil
}

func handleConfigProviderRename(args []string) int {
	fs, targetFlags, noInteractive := newMutationFlagSet("machtiani config provider rename")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	target, err := resolveConfigTarget(*targetFlags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	if err := requireInteractive(*noInteractive); err != nil {
		return configError(err)
	}
	if fs.NArg() != 2 {
		return configUsageError("provider rename requires old and new names")
	}
	doc, err := loadConfigDocument(target, false)
	if err != nil {
		return configError(err)
	}
	providers := configTable(doc.raw, "providers")
	old, next := fs.Arg(0), fs.Arg(1)
	entry, ok := providers[old]
	if !ok {
		return configError(fmt.Errorf("provider %q not found", old))
	}
	if _, exists := providers[next]; exists {
		return configError(fmt.Errorf("provider %q already exists", next))
	}
	confirmed, err := confirmConfigChange(*noInteractive, fmt.Sprintf("Rename provider %q to %q and update model references.", old, next))
	if err != nil {
		return configError(err)
	}
	if !confirmed {
		return 0
	}
	providers[next] = entry
	delete(providers, old)
	for _, value := range configTable(doc.raw, "models") {
		if model, ok := value.(map[string]any); ok {
			if model["provider"] == old {
				model["provider"] = next
			}
		}
	}
	if err := doc.save(); err != nil {
		return configError(err)
	}
	fmt.Printf("Renamed provider %s to %s.\n", old, next)
	return 0
}

func handleConfigProviderRemove(args []string) int {
	fs, targetFlags, noInteractive := newMutationFlagSet("machtiani config provider remove")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	target, err := resolveConfigTarget(*targetFlags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	if err := requireInteractive(*noInteractive); err != nil {
		return configError(err)
	}
	doc, err := loadConfigDocument(target, false)
	if err != nil {
		return configError(err)
	}
	providers := configTable(doc.raw, "providers")
	name := ""
	if fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() > 1 {
		return configUsageError("provider remove expects one name")
	}
	if name == "" && !*noInteractive {
		name, err = promptDocumentSelection(doc, "Provider to remove", sortedKeys(providers))
		if err != nil {
			return configError(err)
		}
	}
	if name == "" {
		return configUsageError("provider name is required")
	}
	if _, ok := providers[name]; !ok {
		return configError(fmt.Errorf("provider %q not found", name))
	}
	var refs []string
	for alias, value := range configTable(doc.raw, "models") {
		if model, ok := value.(map[string]any); ok && model["provider"] == name {
			refs = append(refs, alias)
		}
	}
	if len(refs) > 0 {
		sortStrings(refs)
		return configError(fmt.Errorf("provider %q is used by models: %s; reassign or remove them first", name, strings.Join(refs, ", ")))
	}
	confirmed, err := confirmConfigChange(*noInteractive, fmt.Sprintf("Remove provider %q.", name))
	if err != nil {
		return configError(err)
	}
	if !confirmed {
		return 0
	}
	delete(providers, name)
	if err := doc.save(); err != nil {
		return configError(err)
	}
	fmt.Printf("Removed provider %s.\n", name)
	return 0
}

func handleConfigModelCommand(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "Usage: machtiani config model <list|show|add|set|rename|remove|default|shell-agent> [flags]")
		return 0
	}
	switch args[0] {
	case "list", "show":
		return handleConfigModelRead(args[0], args[1:])
	case "add", "set":
		return handleConfigModelWrite(args[0], args[1:])
	case "rename":
		return handleConfigModelRename(args[1:])
	case "remove":
		return handleConfigModelRemove(args[1:])
	case "default":
		return handleConfigModelDefault(args[1:])
	case "shell-agent":
		return handleConfigModelSelection("shell-agent", "shell_agent_model", "Shell-agent model", args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown model subcommand: %s\n", args[0])
		return 2
	}
}

func handleConfigModelRead(action string, args []string) int {
	fs := pflag.NewFlagSet("machtiani config model "+action, pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags configTargetFlags
	addConfigTargetFlags(fs, &flags)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
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
	models := configTable(doc.raw, "models")
	if action == "list" {
		if fs.NArg() != 0 {
			return configUsageError("model list takes no positional arguments")
		}
		defaultAlias, _ := doc.raw["default_model"].(string)
		for _, alias := range sortedKeys(models) {
			model, _ := models[alias].(map[string]any)
			marker := " "
			if alias == defaultAlias {
				marker = "*"
			}
			fmt.Printf("%s %s\t%v\t%v\n", marker, alias, model["provider"], model["model"])
		}
		return 0
	}
	if fs.NArg() != 1 {
		return configUsageError("model show requires an alias")
	}
	model, ok := models[fs.Arg(0)].(map[string]any)
	if !ok {
		return configError(fmt.Errorf("model %q not found", fs.Arg(0)))
	}
	inheritedContextLength := llm.DefaultContextLength
	if defaults, ok := doc.raw["model_defaults"].(map[string]any); ok {
		switch value := defaults["context_length"].(type) {
		case int64:
			inheritedContextLength = int(value)
		case int:
			inheritedContextLength = value
		}
	}
	printModel(fs.Arg(0), model, inheritedContextLength)
	return 0
}

func handleConfigModelWrite(action string, args []string) int {
	fs := pflag.NewFlagSet("machtiani config model "+action, pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags configTargetFlags
	addConfigTargetFlags(fs, &flags)
	noInteractive := fs.Bool("no-interactive", false, "never prompt; require complete arguments")
	provider := fs.String("provider", "", "provider name")
	modelID := fs.String("model", "", "provider model identifier")
	contextLength := fs.Int("context-length", 0, "total input-plus-output token context")
	clearContextLength := fs.Bool("clear-context-length", false, "inherit the default context length")
	reasoning := fs.String("reasoning", "", "reasoning effort")
	clearReasoning := fs.Bool("clear-reasoning", false, "remove reasoning override")
	var params, removeParams multiString
	fs.Var(&params, "param", "set request parameter key=value (repeatable)")
	paramsJSON := fs.String("param-json", "", "merge a JSON object into request parameters")
	clearParamsJSON := fs.Bool("clear-params-json", false, "remove the inline JSON request parameters")
	fs.Var(&removeParams, "remove-param", "remove request parameter key (repeatable)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 {
		return configUsageError("expected at most one model alias")
	}
	target, err := resolveConfigTarget(flags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	if err := requireInteractive(*noInteractive); err != nil {
		return configError(err)
	}
	doc, err := loadConfigDocument(target, false)
	if err != nil {
		return configError(err)
	}
	models := configTable(doc.raw, "models")
	providers := configTable(doc.raw, "providers")
	alias := ""
	if fs.NArg() == 1 {
		alias = strings.TrimSpace(fs.Arg(0))
	}
	reader := bufio.NewReader(os.Stdin)
	if alias == "" && !*noInteractive {
		if action == "set" {
			alias, err = promptDocumentSelection(doc, "Model", sortedKeys(models))
		} else {
			alias, err = promptLineDefault(reader, "Model alias", "", true)
		}
		if err != nil {
			return configError(err)
		}
	}
	if alias == "" {
		return configUsageError("model alias is required")
	}
	existing, exists := models[alias].(map[string]any)
	if action == "add" && exists {
		return configError(fmt.Errorf("model %q already exists", alias))
	}
	if action == "set" && !exists {
		return configError(fmt.Errorf("model %q not found", alias))
	}
	entry := map[string]any{}
	if exists {
		entry = copyMap(existing)
	}
	if !*noInteractive {
		currentProvider, _ := entry["provider"].(string)
		currentModel, _ := entry["model"].(string)
		if !fs.Changed("provider") {
			*provider, err = promptLineDefault(reader, "Provider", currentProvider, true)
			if err != nil {
				return configError(err)
			}
			if *provider != currentProvider {
				_ = fs.Set("provider", *provider)
			}
		}
		if !fs.Changed("model") {
			*modelID, err = promptLineDefault(reader, "Model", currentModel, true)
			if err != nil {
				return configError(err)
			}
			if *modelID != currentModel {
				_ = fs.Set("model", *modelID)
			}
		}
		if !fs.Changed("context-length") && !*clearContextLength {
			current, _ := configInt(entry["context_length"])
			if current == 0 {
				current = llm.DefaultContextLength
			}
			value, promptErr := promptLineDefault(reader, "Context length (total input + output; 'default' inherits)", strconv.Itoa(current), true)
			if promptErr != nil {
				return configError(promptErr)
			}
			if strings.EqualFold(strings.TrimSpace(value), "default") {
				*clearContextLength = true
			} else {
				parsed, parseErr := strconv.Atoi(strings.TrimSpace(value))
				if parseErr != nil {
					return configUsageError("context length must be an integer or 'default'")
				}
				*contextLength = parsed
				_ = fs.Set("context-length", value)
			}
		}
		if !fs.Changed("reasoning") && !*clearReasoning {
			current := modelReasoning(entry)
			*reasoning, err = promptLineDefault(reader, "Reasoning ('default' clears the override)", current, false)
			if err != nil {
				return configError(err)
			}
			if strings.EqualFold(strings.TrimSpace(*reasoning), "default") {
				*reasoning = ""
				*clearReasoning = true
			}
		}
		if !*clearReasoning {
			*reasoning, err = confirmProviderReasoning(reader, *reasoning)
			if err != nil {
				return configError(err)
			}
		}
	}
	if action == "add" && (strings.TrimSpace(*provider) == "" || strings.TrimSpace(*modelID) == "") {
		return configUsageError("--provider and --model are required")
	}
	if *reasoning != "" && *clearReasoning {
		return configUsageError("--reasoning and --clear-reasoning are mutually exclusive")
	}
	if fs.Changed("param-json") && *clearParamsJSON {
		return configUsageError("--param-json and --clear-params-json are mutually exclusive")
	}
	if fs.Changed("context-length") && *clearContextLength {
		return configUsageError("--context-length and --clear-context-length are mutually exclusive")
	}
	if fs.Changed("context-length") && *contextLength < 4096 {
		return configUsageError("--context-length must be at least 4096")
	}
	if suggestion, likely := reasoningTypoSuggestion(*reasoning); likely {
		return configUsageError(fmt.Sprintf("reasoning value %q looks misspelled; did you mean %q?", *reasoning, suggestion))
	}
	if fs.Changed("provider") || action == "add" {
		entry["provider"] = strings.TrimSpace(*provider)
	}
	if fs.Changed("model") || action == "add" {
		entry["model"] = strings.TrimSpace(*modelID)
	}
	if fs.Changed("context-length") {
		entry["context_length"] = *contextLength
		if *contextLength > 1000000 {
			fmt.Fprintf(os.Stderr, "Warning: context length %d exceeds 1,000,000 tokens; verify provider support.\n", *contextLength)
		}
	}
	if *clearContextLength {
		delete(entry, "context_length")
	}
	paramTable, _ := entry["params"].(map[string]any)
	if paramTable == nil {
		paramTable = map[string]any{}
	}
	if *reasoning != "" {
		entry["params"] = paramTable
		setConfiguredReasoning(entry, providers, *reasoning)
		paramTable, _ = entry["params"].(map[string]any)
	}
	if *clearReasoning {
		entry["params"] = paramTable
		clearConfiguredReasoning(entry)
		paramTable, _ = entry["params"].(map[string]any)
		if paramTable == nil {
			paramTable = map[string]any{}
		}
	}
	assignments, err := parseAssignments(params)
	if err != nil {
		return configUsageError(err.Error())
	}
	for key, value := range assignments {
		paramTable[key] = value
	}
	if fs.Changed("param-json") {
		jsonMap, err := parseJSONMap(*paramsJSON)
		if err != nil {
			return configUsageError(err.Error())
		}
		entry["params_json"] = strings.TrimSpace(*paramsJSON)
		if _, hasEffort := jsonMap["reasoning_effort"]; hasEffort {
			clearConfiguredReasoning(entry)
			paramTable, _ = entry["params"].(map[string]any)
		}
		if _, hasObject := jsonMap["reasoning"]; hasObject {
			clearConfiguredReasoning(entry)
			paramTable, _ = entry["params"].(map[string]any)
		}
		if paramTable == nil {
			paramTable = map[string]any{}
		}
	}
	if *clearParamsJSON {
		delete(entry, "params_json")
	}
	for _, key := range removeParams {
		delete(paramTable, key)
		if raw, ok := entry["params_json"].(string); ok {
			jsonMap, parseErr := parseJSONMap(raw)
			if parseErr != nil {
				return configError(parseErr)
			}
			delete(jsonMap, key)
			if len(jsonMap) == 0 {
				delete(entry, "params_json")
			} else if encoded, marshalErr := json.Marshal(jsonMap); marshalErr == nil {
				entry["params_json"] = string(encoded)
			}
		}
	}
	if len(paramTable) > 0 {
		entry["params"] = paramTable
	} else {
		delete(entry, "params")
	}
	if action == "set" && *noInteractive && !modelMutationChanged(fs) {
		return configUsageError("model set requires at least one field flag")
	}
	if _, ok := configTable(doc.raw, "providers")[fmt.Sprint(entry["provider"])]; !ok {
		return configError(fmt.Errorf("provider %q not found", entry["provider"]))
	}
	confirmed, err := confirmConfigChange(*noInteractive, fmt.Sprintf("%s model %q.", configActionTitle(action), alias))
	if err != nil {
		return configError(err)
	}
	if !confirmed {
		return 0
	}
	models[alias] = entry
	if action == "add" {
		if current, _ := doc.raw["default_model"].(string); current == "" {
			doc.raw["default_model"] = alias
		}
	}
	if err := doc.save(); err != nil {
		return configError(err)
	}
	fmt.Printf("%s model %s.\n", configActionPast(action), alias)
	return 0
}

func modelMutationChanged(fs *pflag.FlagSet) bool {
	for _, name := range []string{"provider", "model", "context-length", "clear-context-length", "reasoning", "clear-reasoning", "param", "param-json", "clear-params-json", "remove-param"} {
		if fs.Changed(name) {
			return true
		}
	}
	return false
}
func modelReasoning(entry map[string]any) string {
	if params, ok := entry["params"].(map[string]any); ok {
		if effort, ok := params["reasoning_effort"].(string); ok {
			return effort
		}
		if r, ok := params["reasoning"].(map[string]any); ok {
			if effort, ok := r["effort"].(string); ok {
				return effort
			}
		}
	}
	return ""
}

func handleConfigModelRename(args []string) int {
	fs, flags, noInteractive := newMutationFlagSet("machtiani config model rename")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	target, err := resolveConfigTarget(*flags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	if err := requireInteractive(*noInteractive); err != nil {
		return configError(err)
	}
	if fs.NArg() != 2 {
		return configUsageError("model rename requires old and new aliases")
	}
	doc, err := loadConfigDocument(target, false)
	if err != nil {
		return configError(err)
	}
	models := configTable(doc.raw, "models")
	old, next := fs.Arg(0), fs.Arg(1)
	entry, ok := models[old]
	if !ok {
		return configError(fmt.Errorf("model %q not found", old))
	}
	if _, ok := models[next]; ok {
		return configError(fmt.Errorf("model %q already exists", next))
	}
	confirmed, err := confirmConfigChange(*noInteractive, fmt.Sprintf("Rename model %q to %q and update selectors.", old, next))
	if err != nil {
		return configError(err)
	}
	if !confirmed {
		return 0
	}
	models[next] = entry
	delete(models, old)
	removeAliasReferences(doc.raw, old, next)
	if err := doc.save(); err != nil {
		return configError(err)
	}
	fmt.Printf("Renamed model %s to %s.\n", old, next)
	return 0
}

func handleConfigModelRemove(args []string) int {
	fs, flags, noInteractive := newMutationFlagSet("machtiani config model remove")
	replacement := fs.String("replacement", "", "replacement for selectors that reference the removed model")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	target, err := resolveConfigTarget(*flags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	if err := requireInteractive(*noInteractive); err != nil {
		return configError(err)
	}
	doc, err := loadConfigDocument(target, false)
	if err != nil {
		return configError(err)
	}
	models := configTable(doc.raw, "models")
	alias := ""
	if fs.NArg() == 1 {
		alias = fs.Arg(0)
	} else if fs.NArg() > 1 {
		return configUsageError("model remove expects one alias")
	}
	if alias == "" && !*noInteractive {
		alias, err = promptDocumentSelection(doc, "Model to remove", sortedKeys(models))
		if err != nil {
			return configError(err)
		}
	}
	if _, ok := models[alias]; !ok {
		return configError(fmt.Errorf("model %q not found", alias))
	}
	if aliasIsReferenced(doc.raw, alias) {
		if *replacement == "" && !*noInteractive {
			choices := []string{}
			for _, candidate := range sortedKeys(models) {
				if candidate != alias {
					choices = append(choices, candidate)
				}
			}
			if len(choices) == 0 {
				return configError(errors.New("cannot remove the only configured model"))
			}
			*replacement, err = promptDocumentSelection(doc, "Replacement model", choices)
			if err != nil {
				return configError(err)
			}
		}
		if *replacement == "" {
			return configUsageError("--replacement is required because the model is referenced")
		}
		if _, ok := models[*replacement]; !ok {
			return configError(fmt.Errorf("replacement model %q not found", *replacement))
		}
	}
	confirmed, err := confirmConfigChange(*noInteractive, fmt.Sprintf("Remove model %q.", alias))
	if err != nil {
		return configError(err)
	}
	if !confirmed {
		return 0
	}
	delete(models, alias)
	if *replacement != "" {
		removeAliasReferences(doc.raw, alias, *replacement)
	}
	if err := doc.save(); err != nil {
		return configError(err)
	}
	fmt.Printf("Removed model %s.\n", alias)
	return 0
}

func handleConfigModelDefault(args []string) int {
	return handleConfigModelSelection("default", "default_model", "Default model", args)
}

func handleConfigModelSelection(action, key, label string, args []string) int {
	fs, flags, noInteractive := newMutationFlagSet("machtiani config model " + action)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: machtiani config model %s [<alias>] [flags]\n\n", action)
		fmt.Fprintf(os.Stderr, "Save the %s using an existing model alias.\n", strings.ToLower(label))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	target, err := resolveConfigTarget(*flags)
	if err != nil {
		return configError(err)
	}
	printConfigTarget(target)
	if err := requireInteractive(*noInteractive); err != nil {
		return configError(err)
	}
	doc, err := loadConfigDocument(target, false)
	if err != nil {
		return configError(err)
	}
	models := configTable(doc.raw, "models")
	alias := ""
	if fs.NArg() == 1 {
		alias = fs.Arg(0)
	} else if fs.NArg() > 1 {
		return configUsageError("model " + action + " expects at most one alias")
	}
	if alias == "" && !*noInteractive {
		alias, err = promptDocumentSelection(doc, label, sortedKeys(models))
		if err != nil {
			return configError(err)
		}
	}
	if alias == "" {
		return configUsageError("model alias is required")
	}
	if _, ok := models[alias]; !ok {
		return configError(fmt.Errorf("model %q not found", alias))
	}
	confirmed, err := confirmConfigChange(*noInteractive, fmt.Sprintf("Set %s to %q.", strings.ToLower(label), alias))
	if err != nil {
		return configError(err)
	}
	if !confirmed {
		return 0
	}
	doc.raw[key] = alias
	if err := doc.save(); err != nil {
		return configError(err)
	}
	fmt.Printf("%s: %s\n", label, alias)
	return 0
}

func newMutationFlagSet(name string) (*pflag.FlagSet, *configTargetFlags, *bool) {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	flags := &configTargetFlags{}
	addConfigTargetFlags(fs, flags)
	noInteractive := fs.Bool("no-interactive", false, "never prompt; require complete arguments")
	return fs, flags, noInteractive
}
func configError(err error) int           { fmt.Fprintf(os.Stderr, "Error: %v\n", err); return 1 }
func configUsageError(message string) int { fmt.Fprintf(os.Stderr, "Error: %s\n", message); return 2 }
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
