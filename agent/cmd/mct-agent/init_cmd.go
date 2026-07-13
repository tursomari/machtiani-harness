package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
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
	alias     string
	provider  string
	model     string
	reasoning string
}

type initMenuOption struct {
	label string
	value string
}

func init() {
	cliCommands = append(cliCommands, cliCommand{
		name:        "init",
		description: "Initialize .machtiani/config.toml",
		handler:     handleInitCommand,
	})
}

func handleInitCommand(args []string) int {
	for _, arg := range args {
		name := strings.SplitN(arg, "=", 2)[0]
		switch name {
		case "--provider-url", "--api-key", "--model", "--reasoning", "--alias", "--force":
			fmt.Fprintln(os.Stderr, "Legacy mct-agent init flags were removed. Use 'mct-agent init' for interactive setup or 'mct-agent config add --no-interactive' for automation.")
			return 2
		}
	}

	fs := pflag.NewFlagSet("mct-agent init", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var flags configTargetFlags
	addConfigTargetFlags(fs, &flags)
	noCache := fs.Bool("no-cache", false, "disable global prompt caching in the new configuration")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent init [--global | --path <file>] [--no-cache]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Start first-time interactive setup. Use 'mct-agent config' to modify an existing configuration.")
		fmt.Fprintln(os.Stderr, "For automation, use 'mct-agent config add --no-interactive'.")
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
		return configUsageError("mct-agent init takes flags, not positional arguments")
	}
	target, err := resolveConfigTarget(flags)
	if err != nil {
		return configError(err)
	}
	if _, err := os.Stat(target.path); err == nil {
		fmt.Fprintf(os.Stderr, "Configuration already exists at %s. Run 'mct-agent config' to modify it.\n", target.path)
		return 1
	} else if !os.IsNotExist(err) {
		return configError(fmt.Errorf("inspect %s: %w", target.path, err))
	}

	forwarded := configTargetArgs(flags)
	if *noCache {
		forwarded = append(forwarded, "--no-cache")
	}
	return handleConfigAddCommand(forwarded)
}

func handleInitCommandWithDeps(args []string, deps initCommandDeps) int {
	fs := pflag.NewFlagSet("mct-agent init", pflag.ContinueOnError)
	fs.SetOutput(deps.errOut)

	providerURL := fs.String("provider-url", "", "LLM provider base URL (required)")
	apiKey := fs.String("api-key", "", "API key for the provider (required)")
	model := fs.String("model", "", "Model name (required)")
	reasoning := fs.String("reasoning", "", "Reasoning effort level (omit for provider default)")
	alias := fs.String("alias", "default", "Model alias name")
	force := fs.Bool("force", false, "Overwrite existing configuration")
	noCache := fs.Bool("no-cache", false, "Disable global prompt-caching defaults")

	fs.Usage = func() {
		fmt.Fprintln(deps.errOut, "Usage:")
		fmt.Fprintln(deps.errOut, "  mct-agent init [--force] [--no-cache]")
		fmt.Fprintln(deps.errOut, "  mct-agent init --provider-url <url> --api-key <key> --model <name> [flags]")
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
	for _, name := range []string{"provider-url", "api-key", "model", "reasoning", "alias"} {
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
		fmt.Fprintln(deps.out, "Welcome to mct-agent setup.")
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
		modelsToWrite = []initModel{{alias: strings.TrimSpace(*alias), provider: strings.TrimSpace(*alias), model: strings.TrimSpace(*model), reasoning: strings.TrimSpace(*reasoning)}}
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
		providers[providerEntry.name] = map[string]any{
			"base_url": providerEntry.baseURL,
			"api_key":  providerEntry.apiKey,
		}
	}

	// Create or get models map
	models, ok := cfg["models"].(map[string]any)
	if !ok {
		models = make(map[string]any)
		cfg["models"] = models
	}
	for _, configuredModel := range modelsToWrite {
		modelEntry := map[string]any{
			"provider": configuredModel.provider,
			"model":    configuredModel.model,
		}
		if configuredModel.reasoning != "" {
			modelEntry["params"] = map[string]any{
				"reasoning": map[string]any{
					"effort": configuredModel.reasoning,
				},
			}
		}
		models[configuredModel.alias] = modelEntry
	}
	modelDefaults := map[string]any{"cache_enabled": cacheEnabled}
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
		"Local name used by mct-agent commands.",
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
	return initModel{alias: alias, provider: provider, model: modelName, reasoning: strings.TrimSpace(reasoning)}, nil
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

func promptInitMenu(in io.Reader, out io.Writer, fd int, title, help string, options []initMenuOption) (string, error) {
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", fmt.Errorf("enable %s selection: %w", strings.ToLower(title), err)
	}
	defer func() { _ = term.Restore(fd, state) }()
	return runInitMenu(in, out, title, help, options)
}

func runInitMenu(in io.Reader, out io.Writer, title, help string, options []initMenuOption) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("%s has no options", title)
	}
	fmt.Fprintf(out, "\r\n%s\r\n", title)
	if help != "" {
		fmt.Fprintf(out, "  %s\r\n", help)
	}
	selected := 0
	renderInitMenuOptions(out, options, selected, false)
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
			renderInitMenuOptions(out, options, selected, true)
		case 'j':
			selected = (selected + 1) % len(options)
			renderInitMenuOptions(out, options, selected, true)
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
				renderInitMenuOptions(out, options, selected, true)
			case 'B':
				selected = (selected + 1) % len(options)
				renderInitMenuOptions(out, options, selected, true)
			}
		}
	}
}

func clearInitMenuScreen(out io.Writer) {
	// Replace the previous menu/action view while preserving terminal
	// scrollback. The selected action can then print its result above the next
	// freshly rendered menu.
	fmt.Fprint(out, "\x1b[2J\x1b[H")
}

func renderInitMenuOptions(out io.Writer, options []initMenuOption, selected int, redraw bool) {
	if redraw {
		fmt.Fprintf(out, "\x1b[%dA", len(options))
	}
	for i, option := range options {
		marker := "  "
		if i == selected {
			marker = "> "
		}
		fmt.Fprintf(out, "\r\x1b[2K%s%s\r\n", marker, option.label)
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
