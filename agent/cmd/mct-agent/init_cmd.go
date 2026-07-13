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
	in           io.Reader
	out          io.Writer
	errOut       io.Writer
	stdinFD      int
	isTerminal   func(int) bool
	readPassword func(int) ([]byte, error)
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
		in:           os.Stdin,
		out:          os.Stdout,
		errOut:       os.Stderr,
		stdinFD:      int(os.Stdin.Fd()),
		isTerminal:   term.IsTerminal,
		readPassword: term.ReadPassword,
	})
}

func handleInitCommandWithDeps(args []string, deps initCommandDeps) int {
	fs := pflag.NewFlagSet("mct-agent init", pflag.ContinueOnError)
	fs.SetOutput(deps.errOut)

	providerURL := fs.String("provider-url", "", "LLM provider base URL (required)")
	apiKey := fs.String("api-key", "", "API key for the provider (required)")
	model := fs.String("model", "", "Model name (required)")
	reasoning := fs.String("reasoning", "medium", "Reasoning effort level (low, medium, high)")
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
		if *reasoning, err = promptChoice(reader, deps.out, "Reasoning effort", "How hard the model should think: low | medium | high", "Reasoning [medium]: ", "medium", map[string]bool{"low": true, "medium": true, "high": true}); err != nil {
			return initPromptError(deps.errOut, err)
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
	models[*alias] = map[string]any{
		"provider": *alias,
		"model":    *model,
		"params": map[string]any{
			"reasoning": map[string]any{
				"effort": *reasoning,
			},
		},
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

func promptChoice(reader *bufio.Reader, out io.Writer, heading, explanation, prompt, defaultValue string, allowed map[string]bool) (string, error) {
	fmt.Fprintf(out, "\n%s\n  %s\n", heading, explanation)
	for {
		fmt.Fprint(out, prompt)
		value, err := readInitLine(reader)
		if err != nil {
			return "", err
		}
		value = strings.ToLower(value)
		if value == "" {
			return defaultValue, nil
		}
		if allowed[value] {
			return value, nil
		}
		fmt.Fprintln(out, "Invalid reasoning level. Choose low, medium, or high.")
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
