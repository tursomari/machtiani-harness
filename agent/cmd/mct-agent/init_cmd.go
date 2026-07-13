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
}

func init() {
	cliCommands = append(cliCommands, cliCommand{
		name:        "init",
		description: "Initialize a minimal .machtiani/config.toml",
		handler:     handleInitCommand,
	})
}

func handleInitCommand(args []string) int {
	return handleInitCommandWithDeps(args, initCommandDeps{
		in:              os.Stdin,
		out:             os.Stdout,
		errOut:          os.Stderr,
		stdinFD:         int(os.Stdin.Fd()),
		isTerminal:      term.IsTerminal,
		readPassword:    term.ReadPassword,
		selectReasoning: promptReasoningMenu,
	})
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
	if interactive {
		if deps.isTerminal == nil || !deps.isTerminal(deps.stdinFD) {
			fmt.Fprintln(deps.errOut, "Error: interactive init requires a terminal; provide --provider-url, --api-key, and --model for non-interactive use")
			return 1
		}
		reader := bufio.NewReader(deps.in)
		fmt.Fprintln(deps.out, "Welcome to mct-agent setup.")
		fmt.Fprintln(deps.out, "This creates .machtiani/config.toml for local LLM access.")
		fmt.Fprintln(deps.out)

		var err error
		if *providerURL, err = promptRequired(reader, deps.out, "Provider base URL", "The OpenAI-compatible API base endpoint.", "Example: https://api.openai.com/v1"); err != nil {
			return initPromptError(deps.errOut, err)
		}
		fmt.Fprintln(deps.out, "\nAPI key")
		fmt.Fprintln(deps.out, "  Stored in the local configuration file.")
		for {
			fmt.Fprint(deps.out, "API key: ")
			password, passwordErr := deps.readPassword(deps.stdinFD)
			fmt.Fprintln(deps.out)
			if passwordErr != nil {
				return initPromptError(deps.errOut, fmt.Errorf("read API key: %w", passwordErr))
			}
			*apiKey = strings.TrimSpace(string(password))
			if *apiKey != "" {
				break
			}
			fmt.Fprintln(deps.out, "API key cannot be empty.")
		}
		if *model, err = promptRequired(reader, deps.out, "Model", "Exact model name your provider expects.", "Example: gpt-4.1"); err != nil {
			return initPromptError(deps.errOut, err)
		}
		if deps.selectReasoning == nil {
			return initPromptError(deps.errOut, fmt.Errorf("reasoning selector is unavailable"))
		}
		if *reasoning, err = deps.selectReasoning(deps.in, deps.out, deps.stdinFD); err != nil {
			return initPromptError(deps.errOut, err)
		}
		if *reasoning == "other" {
			if *reasoning, err = promptOtherReasoning(reader, deps.out); err != nil {
				return initPromptError(deps.errOut, err)
			}
		}
		if *alias, err = promptDefault(reader, deps.out, "Model alias", "Local name used by mct-agent commands.", "Alias [default]: ", "default"); err != nil {
			return initPromptError(deps.errOut, err)
		}
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

	if err := os.MkdirAll(".machtiani", 0755); err != nil {
		fmt.Fprintf(deps.errOut, "Error: %v\n", err)
		return 1
	}

	cfg := llm.DefaultMinimalConfigMap()

	// Set default_model
	cfg["default_model"] = *alias

	// Create or get providers map
	providers, ok := cfg["providers"].(map[string]any)
	if !ok {
		providers = make(map[string]any)
		cfg["providers"] = providers
	}
	providers[*alias] = map[string]any{
		"base_url": *providerURL,
		"api_key":  *apiKey,
	}

	// Create or get models map
	models, ok := cfg["models"].(map[string]any)
	if !ok {
		models = make(map[string]any)
		cfg["models"] = models
	}
	modelEntry := map[string]any{
		"provider": *alias,
		"model":    *model,
	}
	if strings.TrimSpace(*reasoning) != "" {
		modelEntry["params"] = map[string]any{
			"reasoning": map[string]any{
				"effort": strings.TrimSpace(*reasoning),
			},
		}
	}
	models[*alias] = modelEntry
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
	options := []struct {
		label string
		value string
	}{
		{label: "Provider default", value: ""},
		{label: "Low", value: "low"},
		{label: "Medium", value: "medium"},
		{label: "High", value: "high"},
		{label: "Other...", value: "other"},
	}
	fmt.Fprint(out, "\r\nReasoning effort\r\n")
	fmt.Fprint(out, "  Use Up/Down arrows and Enter. Provider default omits the setting.\r\n")
	selected := 0
	renderReasoningOptions(out, options, selected, false)
	for {
		key, err := readInitByte(in)
		if err != nil {
			return "", err
		}
		switch key {
		case '\r', '\n':
			return options[selected].value, nil
		case 3:
			return "", fmt.Errorf("setup interrupted")
		case 4:
			return "", io.EOF
		case 'k':
			selected = (selected - 1 + len(options)) % len(options)
			renderReasoningOptions(out, options, selected, true)
		case 'j':
			selected = (selected + 1) % len(options)
			renderReasoningOptions(out, options, selected, true)
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
				renderReasoningOptions(out, options, selected, true)
			case 'B':
				selected = (selected + 1) % len(options)
				renderReasoningOptions(out, options, selected, true)
			}
		}
	}
}

func readInitByte(in io.Reader) (byte, error) {
	var value [1]byte
	_, err := io.ReadFull(in, value[:])
	return value[0], err
}

func renderReasoningOptions(out io.Writer, options []struct {
	label string
	value string
}, selected int, redraw bool) {
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
