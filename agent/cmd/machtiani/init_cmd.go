package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/google/uuid"
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/modes"
	"github.com/tursomari/machtiani/agent/internal/presentation"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
	"golang.org/x/term"
)

const (
	defaultCacheKeyName          = "cache_control"
	defaultCacheTriggerThreshold = int64(4096)
	defaultCacheLookbackOffset   = int64(1)
)

type initCommandDeps struct {
	in              io.Reader
	out             io.Writer
	errOut          io.Writer
	stdinFD         int
	isTerminal      func(int) bool
	readPassword    func(int) ([]byte, error)
	selectReasoning func(io.Reader, io.Writer, int) (string, error)
	selectNextStep  func(io.Reader, io.Writer, int, string) (string, error)
	selectDefault   func(io.Reader, io.Writer, int, []initModel) (string, error)
}

type initProvider struct {
	name    string
	baseURL string
	apiKey  string
}

type initModel struct {
	alias         string
	provider      string
	model         string
	reasoning     string
	contextLength int
}

type initMenuOption struct {
	label string
	value string
}

func init() {
	cliCommands = append(cliCommands, cliCommand{
		name:        "init",
		description: "Initialize project identity, modes, and configuration",
		handler:     handleInitCommand,
	})
}

func handleInitCommand(args []string) int {
	fs := pflag.NewFlagSet("machtiani init", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	noInteractive := fs.Bool("no-interactive", false, "never prompt; use existing or complete supplied configuration")
	configScope := fs.String("config-scope", "", "configuration scope: global or project")
	jsonOutput := fs.Bool("json", false, "print initialized project details as JSON")
	preset := fs.String("preset", "", "provider catalogue preset")
	provider := fs.String("provider", "", "provider name")
	url := fs.String("url", "", "provider base URL")
	endpoint := fs.String("endpoint", "", "provider endpoint path")
	apiKey := fs.String("api-key", "", "literal provider API key")
	apiKeyEnv := fs.String("api-key-env", "", "API key environment variable")
	model := fs.String("model", "", "provider model identifier")
	alias := fs.String("alias", "", "model alias")
	reasoning := fs.String("reasoning", "", "reasoning effort")
	headers := fs.StringArray("header", nil, "provider header key=value (repeatable)")
	queries := fs.StringArray("query", nil, "provider query key=value (repeatable)")
	noCache := fs.Bool("no-cache", false, "disable global prompt caching in the new configuration")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: machtiani init [--no-interactive] [--config-scope global|project] [configuration flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Initialize a UUID-backed project store and synchronize canonical modes.")
		fmt.Fprintln(os.Stderr, "Global configuration is used by default; select project scope to keep a complete project-specific config.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return configUsageError("machtiani init takes flags, not positional arguments")
	}
	ctx, err := projectstore.Discover("")
	if err != nil {
		return configError(err)
	}
	if ctx.Status == projectstore.StatusLegacy {
		if err := modes.SyncCanonical(); err != nil {
			return configError(err)
		}
		fmt.Fprintln(os.Stdout, "Legacy project configuration preserved; canonical modes synchronized. Run machtiani migrate to adopt the UUID home store.")
		return 0
	}

	scope := projectstore.ScopeGlobal
	if ctx.Status == projectstore.StatusInitialized {
		scope = ctx.ConfigScope
	}
	if value := strings.TrimSpace(*configScope); value != "" {
		scope = projectstore.ConfigScope(value)
		if err := scope.Validate(); err != nil {
			return configUsageError(err.Error())
		}
	} else if !*noInteractive && term.IsTerminal(int(os.Stdin.Fd())) {
		useGlobal, err := promptInitConfigScope(bufio.NewReader(os.Stdin), os.Stdout)
		if err != nil {
			return configError(err)
		}
		if !useGlobal {
			scope = projectstore.ScopeProject
		}
	}

	if ctx.Status != projectstore.StatusInitialized {
		ctx.ID = uuid.New()
		ctx.StoreRoot = filepath.Join(ctx.HomeRoot, ctx.ID.String())
		ctx.Status = projectstore.StatusInitialized
	}
	target, err := projectstore.GlobalConfigPath()
	if err != nil {
		return configError(err)
	}
	if scope == projectstore.ScopeProject {
		target = ctx.ProjectConfigPath()
	}
	if _, err := os.Stat(target); os.IsNotExist(err) {
		hasCreationFlags := strings.TrimSpace(*preset+*provider+*url+*model+*alias) != ""
		if scope == projectstore.ScopeProject && !hasCreationFlags {
			global, globalErr := projectstore.GlobalConfigPath()
			if globalErr == nil {
				if data, readErr := os.ReadFile(global); readErr == nil {
					if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o700); mkdirErr != nil {
						return configError(mkdirErr)
					}
					if writeErr := os.WriteFile(target, data, 0o600); writeErr != nil {
						return configError(writeErr)
					}
				}
			}
		}
		if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
			forwarded := []string{"--path", target}
			appendFlag := func(name, value string) {
				if strings.TrimSpace(value) != "" {
					forwarded = append(forwarded, name, value)
				}
			}
			appendFlag("--preset", *preset)
			appendFlag("--provider", *provider)
			appendFlag("--url", *url)
			appendFlag("--endpoint", *endpoint)
			appendFlag("--api-key", *apiKey)
			appendFlag("--api-key-env", *apiKeyEnv)
			appendFlag("--model", *model)
			appendFlag("--alias", *alias)
			appendFlag("--reasoning", *reasoning)
			for _, value := range *headers {
				forwarded = append(forwarded, "--header", value)
			}
			for _, value := range *queries {
				forwarded = append(forwarded, "--query", value)
			}
			if *noCache {
				forwarded = append(forwarded, "--no-cache")
			}
			if *noInteractive {
				forwarded = append(forwarded, "--no-interactive")
			}
			if code := handleConfigAddCommand(forwarded); code != 0 {
				return code
			}
		}
	} else if err != nil {
		return configError(fmt.Errorf("inspect %s: %w", target, err))
	}
	if err := modes.SyncCanonical(); err != nil {
		return configError(err)
	}
	if err := projectstore.EnsureLayout(ctx.StoreRoot); err != nil {
		return configError(err)
	}
	if err := projectstore.WriteConfigScope(ctx.StoreRoot, scope); err != nil {
		return configError(err)
	}
	if _, ok, err := projectstore.ReadProjectUUID(ctx.ProjectRoot); err != nil {
		return configError(err)
	} else if !ok {
		if err := projectstore.WriteProjectUUID(ctx.ProjectRoot, ctx.ID); err != nil {
			return configError(err)
		}
	}
	result := map[string]string{"uuid": ctx.ID.String(), "marker": ctx.MarkerPath, "store": ctx.StoreRoot, "config_scope": string(scope), "config": target}
	if *jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(os.Stdout, string(data))
	} else {
		fmt.Fprintf(os.Stdout, "Project initialized: %s\nProject store: %s\nConfiguration (%s): %s\n", ctx.ID, ctx.StoreRoot, scope, target)
	}
	return 0
}

func promptInitConfigScope(reader *bufio.Reader, out io.Writer) (bool, error) {
	fmt.Fprintln(out, "Configuration scope")
	fmt.Fprintln(out, "  Global shares providers and models across projects. Sessions remain project-specific.")
	return promptYesNo(reader, out, "Use global config? [Y/n] (recommended): ", true)
}

func handleInitCommandWithDeps(args []string, deps initCommandDeps) int {
	fs := pflag.NewFlagSet("machtiani init", pflag.ContinueOnError)
	fs.SetOutput(deps.errOut)

	providerURL := fs.String("provider-url", "", "LLM provider base URL (required)")
	apiKey := fs.String("api-key", "", "API key for the provider (required)")
	model := fs.String("model", "", "Model name (required)")
	contextLength := fs.Int("context-length", llm.DefaultContextLength, "total input-plus-output token context")
	reasoning := fs.String("reasoning", "", "Reasoning effort level (omit for provider default)")
	alias := fs.String("alias", "default", "Model alias name")
	force := fs.Bool("force", false, "Overwrite existing configuration")
	noCache := fs.Bool("no-cache", false, "Disable global prompt-caching defaults")

	fs.Usage = func() {
		fmt.Fprintln(deps.errOut, "Usage:")
		fmt.Fprintln(deps.errOut, "  machtiani init [--force] [--no-cache]")
		fmt.Fprintln(deps.errOut, "  machtiani init --provider-url <url> --api-key <key> --model <name> [flags]")
		fmt.Fprintln(deps.errOut, "\nFlags:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 2
		}
		fmt.Fprintln(deps.errOut, err)
		return 2
	}

	configPath := filepath.Join(".machtiani", "config.toml")
	if _, err := os.Stat(configPath); err == nil {
		if !*force {
			fmt.Fprintf(deps.errOut, "Error: config already exists at %s (use --force to overwrite)\n", configPath)
			return 1
		}
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(deps.errOut, "Error: inspect %s: %v\n", configPath, err)
		return 1
	}

	interactive := true
	for _, name := range []string{"provider-url", "api-key", "model", "context-length", "reasoning", "alias"} {
		if fs.Changed(name) {
			interactive = false
			break
		}
	}

	cacheEnabled := !*noCache
	var providersToWrite []initProvider
	var modelsToWrite []initModel
	defaultModel := strings.TrimSpace(*alias)
	if interactive {
		if deps.isTerminal == nil || !deps.isTerminal(deps.stdinFD) {
			fmt.Fprintln(deps.errOut, "Error: interactive init requires a terminal; provide --provider-url, --api-key, and --model for non-interactive use")
			return 1
		}
		reader := bufio.NewReader(deps.in)
		fmt.Fprintln(deps.out, "Welcome to machtiani setup.")
		fmt.Fprintln(deps.out, "This creates .machtiani/config.toml for local LLM access.")
		fmt.Fprintln(deps.out)

		if deps.selectReasoning == nil || deps.selectNextStep == nil || deps.selectDefault == nil {
			return initPromptError(deps.errOut, fmt.Errorf("interactive selector is unavailable"))
		}

		providerEntry, err := promptInitProvider(reader, deps, "default", nil)
		if err != nil {
			return initPromptError(deps.errOut, err)
		}
		providersToWrite = append(providersToWrite, providerEntry)
		modelEntry, err := promptInitModel(reader, deps, providerEntry.name, "default", nil)
		if err != nil {
			return initPromptError(deps.errOut, err)
		}
		modelsToWrite = append(modelsToWrite, modelEntry)

		finished := false
		for !finished {
			printInitModels(deps.out, modelsToWrite)
			next, err := deps.selectNextStep(reader, deps.out, deps.stdinFD, providerEntry.name)
			if err != nil {
				return initPromptError(deps.errOut, err)
			}
			switch next {
			case "finish":
				finished = true
			case "model":
				entry, err := promptInitModel(reader, deps, providerEntry.name, fmt.Sprintf("model-%d", len(modelsToWrite)+1), modelsToWrite)
				if err != nil {
					return initPromptError(deps.errOut, err)
				}
				modelsToWrite = append(modelsToWrite, entry)
			case "provider":
				entry, err := promptInitProvider(reader, deps, fmt.Sprintf("provider-%d", len(providersToWrite)+1), providersToWrite)
				if err != nil {
					return initPromptError(deps.errOut, err)
				}
				providersToWrite = append(providersToWrite, entry)
				providerEntry = entry
				newModel, err := promptInitModel(reader, deps, providerEntry.name, fmt.Sprintf("model-%d", len(modelsToWrite)+1), modelsToWrite)
				if err != nil {
					return initPromptError(deps.errOut, err)
				}
				modelsToWrite = append(modelsToWrite, newModel)
			default:
				return initPromptError(deps.errOut, fmt.Errorf("unknown setup action %q", next))
			}
		}

		defaultModel = modelsToWrite[0].alias
		if len(modelsToWrite) > 1 {
			selected, err := deps.selectDefault(reader, deps.out, deps.stdinFD, modelsToWrite)
			if err != nil {
				return initPromptError(deps.errOut, err)
			}
			defaultModel = selected
		}
		printInitSummary(deps.out, providersToWrite, modelsToWrite, defaultModel)
		if *noCache {
			fmt.Fprintln(deps.out, "\nPrompt caching disabled by --no-cache.")
		} else {
			fmt.Fprintln(deps.out, "\nPrompt caching")
			fmt.Fprintln(deps.out, "  Enabled by default for all models; individual models can override it.")
			cacheEnabled, err = promptYesNo(reader, deps.out, "Enable caching? [Y/n]: ", true)
			if err != nil {
				return initPromptError(deps.errOut, err)
			}
		}
	} else if strings.TrimSpace(*providerURL) == "" || strings.TrimSpace(*apiKey) == "" || strings.TrimSpace(*model) == "" {
		fmt.Fprintln(deps.errOut, "Error: --provider-url, --api-key, and --model are required for non-interactive init")
		return 1
	} else if suggestion, likely := reasoningTypoSuggestion(*reasoning); likely {
		fmt.Fprintf(deps.errOut, "Error: reasoning value %q looks misspelled; did you mean %q?\n", *reasoning, suggestion)
		return 1
	}
	if !interactive {
		providersToWrite = []initProvider{{name: strings.TrimSpace(*alias), baseURL: strings.TrimSpace(*providerURL), apiKey: strings.TrimSpace(*apiKey)}}
		if *contextLength < 4096 {
			fmt.Fprintln(deps.errOut, "Error: --context-length must be at least 4096")
			return 1
		}
		modelsToWrite = []initModel{{alias: strings.TrimSpace(*alias), provider: strings.TrimSpace(*alias), model: strings.TrimSpace(*model), reasoning: strings.TrimSpace(*reasoning), contextLength: *contextLength}}
	}

	if err := os.MkdirAll(".machtiani", 0755); err != nil {
		fmt.Fprintf(deps.errOut, "Error: %v\n", err)
		return 1
	}

	cfg := llm.DefaultMinimalConfigMap()

	// Set default_model
	cfg["default_model"] = defaultModel

	// Create or get providers map
	providers, ok := cfg["providers"].(map[string]any)
	if !ok {
		providers = make(map[string]any)
		cfg["providers"] = providers
	}
	for _, providerEntry := range providersToWrite {
		entry := map[string]any{
			"base_url": providerEntry.baseURL,
			"api_key":  providerEntry.apiKey,
		}
		if format := configuredReasoningFormat(providerEntry.name, map[string]any{providerEntry.name: entry}); format != "" {
			entry["reasoning_format"] = format
		}
		providers[providerEntry.name] = entry
	}

	// Create or get models map
	models, ok := cfg["models"].(map[string]any)
	if !ok {
		models = make(map[string]any)
		cfg["models"] = models
	}
	for _, configuredModel := range modelsToWrite {
		modelEntry := map[string]any{
			"provider":       configuredModel.provider,
			"model":          configuredModel.model,
			"context_length": configuredModel.contextLength,
		}
		if configuredModel.reasoning != "" {
			setConfiguredReasoning(modelEntry, providers, configuredModel.reasoning)
		}
		models[configuredModel.alias] = modelEntry
	}
	modelDefaults := map[string]any{"cache_enabled": cacheEnabled, "context_length": llm.DefaultContextLength}
	if cacheEnabled {
		modelDefaults["cache_key_name"] = defaultCacheKeyName
		modelDefaults["cache_control"] = map[string]any{"type": "ephemeral"}
		modelDefaults["cache_trigger_threshold"] = defaultCacheTriggerThreshold
		modelDefaults["cache_lookback_offset"] = defaultCacheLookbackOffset
	}
	cfg["model_defaults"] = modelDefaults

	if err := writeConfigAtomically(configPath, cfg); err != nil {
		fmt.Fprintf(deps.errOut, "Error: %v\n", err)
		return 1
	}

	fmt.Fprintf(deps.out, "Configuration initialized at %s\n", configPath)
	return 0
}

func promptInitProvider(reader *bufio.Reader, deps initCommandDeps, defaultName string, existing []initProvider) (initProvider, error) {
	name, err := promptUniqueDefault(
		reader,
		deps.out,
		"Provider name",
		"Local identifier shared by models that use these credentials.",
		fmt.Sprintf("Provider name [%s]: ", defaultName),
		defaultName,
		func(value string) bool {
			for _, provider := range existing {
				if provider.name == value {
					return true
				}
			}
			return false
		},
	)
	if err != nil {
		return initProvider{}, err
	}
	baseURL, err := promptRequired(reader, deps.out, "Provider base URL", "The OpenAI-compatible API base endpoint.", "Example: https://api.openai.com/v1")
	if err != nil {
		return initProvider{}, err
	}
	if deps.readPassword == nil {
		return initProvider{}, fmt.Errorf("API key reader is unavailable")
	}
	fmt.Fprintln(deps.out, "\nAPI key")
	fmt.Fprintln(deps.out, "  Stored in the local configuration file.")
	for {
		fmt.Fprint(deps.out, "API key: ")
		password, passwordErr := deps.readPassword(deps.stdinFD)
		fmt.Fprintln(deps.out)
		if passwordErr != nil {
			return initProvider{}, fmt.Errorf("read API key: %w", passwordErr)
		}
		apiKey := strings.TrimSpace(string(password))
		if apiKey != "" {
			return initProvider{name: name, baseURL: baseURL, apiKey: apiKey}, nil
		}
		fmt.Fprintln(deps.out, "API key cannot be empty.")
	}
}

func promptInitModel(reader *bufio.Reader, deps initCommandDeps, provider, defaultAlias string, existing []initModel) (initModel, error) {
	modelName, err := promptRequired(reader, deps.out, "Model", "Exact model name your provider expects.", "Example: gpt-4.1")
	if err != nil {
		return initModel{}, err
	}
	reasoning, err := deps.selectReasoning(reader, deps.out, deps.stdinFD)
	if err != nil {
		return initModel{}, err
	}
	if reasoning == "other" {
		reasoning, err = promptOtherReasoning(reader, deps.out)
		if err != nil {
			return initModel{}, err
		}
	}
	alias, err := promptUniqueDefault(
		reader,
		deps.out,
		"Model alias",
		"Local name used by machtiani commands.",
		fmt.Sprintf("Alias [%s]: ", defaultAlias),
		defaultAlias,
		func(value string) bool {
			for _, model := range existing {
				if model.alias == value {
					return true
				}
			}
			return false
		},
	)
	if err != nil {
		return initModel{}, err
	}
	return initModel{alias: alias, provider: provider, model: modelName, reasoning: strings.TrimSpace(reasoning), contextLength: llm.DefaultContextLength}, nil
}

func promptUniqueDefault(reader *bufio.Reader, out io.Writer, heading, explanation, prompt, defaultValue string, exists func(string) bool) (string, error) {
	for {
		value, err := promptDefault(reader, out, heading, explanation, prompt, defaultValue)
		if err != nil {
			return "", err
		}
		if !exists(value) {
			return value, nil
		}
		fmt.Fprintf(out, "%s %q is already configured. Choose another name.\n", heading, value)
	}
}

func promptInitNextStepMenu(in io.Reader, out io.Writer, fd int, provider string) (string, error) {
	return promptInitMenu(in, out, fd, "What would you like to do?", "Use Up/Down arrows and Enter.", []initMenuOption{
		{label: "Finish setup", value: "finish"},
		{label: fmt.Sprintf("Add another model to %s", provider), value: "model"},
		{label: "Add another provider", value: "provider"},
	})
}

func promptInitDefaultModelMenu(in io.Reader, out io.Writer, fd int, models []initModel) (string, error) {
	options := make([]initMenuOption, 0, len(models))
	for _, model := range models {
		options = append(options, initMenuOption{
			label: fmt.Sprintf("%s  (%s / %s)", model.alias, model.provider, model.model),
			value: model.alias,
		})
	}
	return promptInitMenu(in, out, fd, "Default model", "Used when a command does not specify a model alias.", options)
}

func promptInitMenu(in io.Reader, out io.Writer, fd int, title, help string, options []initMenuOption, themes ...presentation.Theme) (string, error) {
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", fmt.Errorf("enable %s selection: %w", strings.ToLower(title), err)
	}
	defer func() { _ = term.Restore(fd, state) }()
	return runInitMenu(in, out, title, help, options, themes...)
}

func runInitMenu(in io.Reader, out io.Writer, title, help string, options []initMenuOption, themes ...presentation.Theme) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("%s has no options", title)
	}
	theme, err := resolveInitMenuTheme(out, themes)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(out, "\r\n%s\r\n", theme.RenderSpan(presentation.Bold(presentation.RoleTruth, title)))
	if help != "" {
		fmt.Fprintf(out, "  %s\r\n", help)
	}
	selected := 0
	renderInitMenuOptions(out, options, selected, false, theme)
	for {
		key, err := readInitByte(in)
		if err != nil {
			return "", err
		}
		switch key {
		case '\r', '\n':
			clearInitMenuScreen(out)
			return options[selected].value, nil
		case 3:
			clearInitMenuScreen(out)
			return "", fmt.Errorf("setup interrupted")
		case 4:
			clearInitMenuScreen(out)
			return "", io.EOF
		case 'k':
			selected = (selected - 1 + len(options)) % len(options)
			renderInitMenuOptions(out, options, selected, true, theme)
		case 'j':
			selected = (selected + 1) % len(options)
			renderInitMenuOptions(out, options, selected, true, theme)
		case 0x1b:
			second, err := readInitByte(in)
			if err != nil {
				return "", err
			}
			third, err := readInitByte(in)
			if err != nil {
				return "", err
			}
			if second != '[' {
				continue
			}
			switch third {
			case 'A':
				selected = (selected - 1 + len(options)) % len(options)
				renderInitMenuOptions(out, options, selected, true, theme)
			case 'B':
				selected = (selected + 1) % len(options)
				renderInitMenuOptions(out, options, selected, true, theme)
			}
		}
	}
}

func resolveInitMenuTheme(out io.Writer, themes []presentation.Theme) (presentation.Theme, error) {
	if len(themes) > 0 {
		return themes[0], nil
	}
	theme, err := presentation.Resolve(string(presentation.ProfileTerminal), out)
	if err != nil {
		return presentation.Theme{}, fmt.Errorf("resolve interactive menu theme: %w", err)
	}
	return theme, nil
}

func clearInitMenuScreen(out io.Writer) {
	// Replace the previous menu/action view while preserving terminal
	// scrollback. The selected action can then print its result above the next
	// freshly rendered menu.
	fmt.Fprint(out, "\x1b[2J\x1b[H")
}

func renderInitMenuOptions(out io.Writer, options []initMenuOption, selected int, redraw bool, theme presentation.Theme) {
	if redraw {
		fmt.Fprintf(out, "\x1b[%dA", len(options))
	}
	for i, option := range options {
		marker := "  "
		if i == selected {
			marker = theme.Glyphs().Arrow + " "
		}
		line := marker + option.label
		switch {
		case i == selected:
			line = theme.RenderSpan(presentation.Bold(presentation.RoleTruth, line))
		case option.value == "finish":
			line = marker + theme.RenderSpan(presentation.RoleText(presentation.RoleGoodness, option.label))
		}
		fmt.Fprintf(out, "\r\x1b[2K%s\r\n", line)
	}
}

func printInitModels(out io.Writer, models []initModel) {
	fmt.Fprintln(out, "\nConfigured models:")
	for _, model := range models {
		reasoning := model.reasoning
		if reasoning == "" {
			reasoning = "provider default"
		}
		fmt.Fprintf(out, "  %s -> %s / %s (reasoning: %s)\n", model.alias, model.provider, model.model, reasoning)
	}
}

func printInitSummary(out io.Writer, providers []initProvider, models []initModel, defaultModel string) {
	fmt.Fprintln(out, "\nSetup summary")
	fmt.Fprintf(out, "  Providers: %d\n", len(providers))
	fmt.Fprintf(out, "  Models: %d\n", len(models))
	fmt.Fprintf(out, "  Default model: %s\n", defaultModel)
}

func promptRequired(reader *bufio.Reader, out io.Writer, heading, explanation, example string) (string, error) {
	for {
		fmt.Fprintf(out, "\n%s\n  %s\n  %s\n%s: ", heading, explanation, example, heading)
		value, err := readInitLine(reader)
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		fmt.Fprintf(out, "%s cannot be empty.\n", heading)
	}
}

func promptDefault(reader *bufio.Reader, out io.Writer, heading, explanation, prompt, defaultValue string) (string, error) {
	fmt.Fprintf(out, "\n%s\n  %s\n%s", heading, explanation, prompt)
	value, err := readInitLine(reader)
	if err != nil {
		return "", err
	}
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func promptReasoningMenu(in io.Reader, out io.Writer, fd int) (string, error) {
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", fmt.Errorf("enable reasoning selection: %w", err)
	}
	defer func() { _ = term.Restore(fd, state) }()
	return runReasoningMenu(in, out)
}

func runReasoningMenu(in io.Reader, out io.Writer) (string, error) {
	return runInitMenu(in, out, "Reasoning effort", "Use Up/Down arrows and Enter. Provider default omits the setting.", []initMenuOption{
		{label: "Provider default", value: ""},
		{label: "Low", value: "low"},
		{label: "Medium", value: "medium"},
		{label: "High", value: "high"},
		{label: "Other...", value: "other"},
	})
}

func readInitByte(in io.Reader) (byte, error) {
	var value [1]byte
	_, err := io.ReadFull(in, value[:])
	return value[0], err
}

func promptOtherReasoning(reader *bufio.Reader, out io.Writer) (string, error) {
	fmt.Fprintln(out, "\nOther reasoning effort")
	fmt.Fprintln(out, "  Enter a provider-specific value such as xhigh or max.")
	fmt.Fprintln(out, "  Leave blank to use the provider default.")
	for {
		fmt.Fprint(out, "Reasoning value: ")
		value, err := readInitLine(reader)
		if err != nil {
			return "", err
		}
		if value == "" {
			return "", nil
		}
		if canonical, ok := canonicalReasoningValue(value); ok {
			return canonical, nil
		}
		if suggestion, likely := reasoningTypoSuggestion(value); likely {
			useSuggestion, err := promptYesNo(reader, out, fmt.Sprintf("Did you mean %q? [Y/n]: ", suggestion), true)
			if err != nil {
				return "", err
			}
			if useSuggestion {
				return suggestion, nil
			}
		}
		confirmed, err := promptYesNo(reader, out, fmt.Sprintf("Use provider-specific value %q anyway? [y/N]: ", value), false)
		if err != nil {
			return "", err
		}
		if confirmed {
			return value, nil
		}
	}
}

func canonicalReasoningValue(value string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
	switch normalized {
	case "low", "medium", "high", "xhigh", "max":
		return normalized, true
	default:
		return "", false
	}
}

func reasoningTypoSuggestion(value string) (string, bool) {
	if _, ok := canonicalReasoningValue(value); ok || strings.TrimSpace(value) == "" {
		return "", false
	}
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
	for _, candidate := range []string{"xhigh", "max"} {
		if damerauLevenshtein(normalized, candidate) <= 1 {
			return candidate, true
		}
	}
	return "", false
}

func damerauLevenshtein(left, right string) int {
	a, b := []rune(left), []rune(right)
	distance := make([][]int, len(a)+1)
	for i := range distance {
		distance[i] = make([]int, len(b)+1)
		distance[i][0] = i
	}
	for j := range distance[0] {
		distance[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			distance[i][j] = min(distance[i-1][j]+1, distance[i][j-1]+1, distance[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				distance[i][j] = min(distance[i][j], distance[i-2][j-2]+1)
			}
		}
	}
	return distance[len(a)][len(b)]
}

func promptYesNo(reader *bufio.Reader, out io.Writer, prompt string, defaultValue bool) (bool, error) {
	for {
		fmt.Fprint(out, prompt)
		value, err := readInitLine(reader)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(value) {
		case "":
			return defaultValue, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(out, "Please answer yes or no.")
		}
	}
}

func readInitLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func initPromptError(errOut io.Writer, err error) int {
	if err == io.EOF {
		fmt.Fprintln(errOut, "Error: interactive setup ended before configuration was complete")
	} else {
		fmt.Fprintf(errOut, "Error: %v\n", err)
	}
	return 1
}

func writeConfigAtomically(configPath string, cfg map[string]any) error {
	dir := filepath.Dir(configPath)
	tmp, err := os.CreateTemp(dir, ".config.toml-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := toml.NewEncoder(tmp).Encode(cfg); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		return err
	}
	keep = true
	return nil
}
