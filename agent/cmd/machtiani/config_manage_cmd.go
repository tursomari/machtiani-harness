package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/presentation"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
	"golang.org/x/term"
)

type configTargetFlags struct {
	path    string
	global  bool
	project bool
}

type configTarget struct {
	path       string
	overridden string
}

type configDocument struct {
	path string
	raw  map[string]any
}

func addConfigTargetFlags(fs *pflag.FlagSet, flags *configTargetFlags) {
	fs.StringVar(&flags.path, "path", "", "exact configuration file to use")
	fs.BoolVar(&flags.global, "global", false, "use $HOME/.machtiani/config.toml")
	fs.BoolVar(&flags.project, "project", false, "use the current UUID project's config.toml")
}

func resolveConfigTarget(flags configTargetFlags) (configTarget, error) {
	explicit := 0
	if strings.TrimSpace(flags.path) != "" {
		explicit++
	}
	if flags.global {
		explicit++
	}
	if flags.project {
		explicit++
	}
	if explicit > 1 {
		return configTarget{}, errors.New("--path, --global, and --project are mutually exclusive")
	}
	envPath := strings.TrimSpace(os.Getenv("MACHTIANI_CONFIG"))
	var selected string
	var overridden string
	switch {
	case strings.TrimSpace(flags.path) != "":
		selected = strings.TrimSpace(flags.path)
		overridden = envPath
	case flags.global:
		var err error
		selected, err = projectstore.GlobalConfigPath()
		if err != nil {
			return configTarget{}, err
		}
		overridden = envPath
	case flags.project:
		ctx, err := projectstore.Discover("")
		if err != nil {
			return configTarget{}, err
		}
		if ctx.Status != projectstore.StatusInitialized {
			return configTarget{}, errors.New("--project requires an initialized project; run machtiani init")
		}
		selected = ctx.ProjectConfigPath()
		overridden = envPath
	case envPath != "":
		selected = envPath
	default:
		ctx, err := projectstore.Discover("")
		if err != nil {
			return configTarget{}, err
		}
		switch ctx.Status {
		case projectstore.StatusInitialized:
			if ctx.ConfigScope == projectstore.ScopeProject {
				selected = ctx.ProjectConfigPath()
			} else {
				selected, err = projectstore.GlobalConfigPath()
				if err != nil {
					return configTarget{}, err
				}
			}
		case projectstore.StatusLegacy:
			selected = filepath.Join(ctx.ProjectRoot, ".machtiani", "config.toml")
		default:
			selected, err = projectstore.GlobalConfigPath()
			if err != nil {
				return configTarget{}, err
			}
		}
	}
	abs, err := filepath.Abs(selected)
	if err != nil {
		return configTarget{}, fmt.Errorf("resolve config path: %w", err)
	}
	return configTarget{path: filepath.Clean(abs), overridden: overridden}, nil
}

func localConfigRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for dir := filepath.Clean(wd); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return wd, nil
}

func printConfigTarget(target configTarget) {
	fmt.Fprintf(os.Stderr, "Config file: %s\n", target.path)
	if target.overridden != "" {
		fmt.Fprintf(os.Stderr, "Note: ignoring MACHTIANI_CONFIG=%s because an explicit target was supplied.\n", target.overridden)
	}
}

func loadConfigDocument(target configTarget, create bool) (*configDocument, error) {
	data, err := os.ReadFile(target.path)
	if err != nil {
		if !os.IsNotExist(err) || !create {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("config file not found: %s", target.path)
			}
			return nil, fmt.Errorf("read %s: %w", target.path, err)
		}
		return &configDocument{path: target.path, raw: llm.DefaultMinimalConfigMap()}, nil
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode %s: %w", target.path, err)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	return &configDocument{path: target.path, raw: raw}, nil
}

func (d *configDocument) save() error {
	if err := validateConfigDocument(d.raw); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := writeConfigAtomically(d.path, d.raw); err != nil {
		return fmt.Errorf("write %s: %w", d.path, err)
	}
	return nil
}

func (d *configDocument) menuTheme(out io.Writer) (presentation.Theme, error) {
	configured := string(presentation.ProfileTerminal)
	glyphs := string(presentation.GlyphUnicode)
	if ui, ok := d.raw["ui"].(map[string]any); ok {
		if value, ok := ui["theme"].(string); ok && strings.TrimSpace(value) != "" {
			configured = value
		}
		if value, ok := ui["glyphs"].(string); ok && strings.TrimSpace(value) != "" {
			glyphs = value
		}
	}
	theme, err := presentation.ResolveWithGlyphs(configured, glyphs, out)
	if err != nil {
		return presentation.Theme{}, fmt.Errorf("resolve interactive menu theme: %w", err)
	}
	return theme, nil
}

func configTable(raw map[string]any, key string) map[string]any {
	if table, ok := raw[key].(map[string]any); ok {
		return table
	}
	table := map[string]any{}
	raw[key] = table
	return table
}

func validateConfigDocument(raw map[string]any) error {
	if planner, ok := raw["planner"].(map[string]any); ok {
		if _, legacy := planner["max_input_tokens"]; legacy {
			return errors.New("planner.max_input_tokens was removed; delete it and configure models.<alias>.context_length instead")
		}
	}
	var typed llm.Config
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(raw); err != nil {
		return fmt.Errorf("encode proposed configuration: %w", err)
	}
	if _, err := toml.Decode(encoded.String(), &typed); err != nil {
		return fmt.Errorf("validate proposed configuration: %w", err)
	}
	providers := configTable(raw, "providers")
	models := configTable(raw, "models")
	for name, value := range providers {
		if strings.TrimSpace(name) == "" {
			return errors.New("provider name cannot be empty")
		}
		provider, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("providers.%s must be a table", name)
		}
		baseURL, _ := provider["base_url"].(string)
		if strings.TrimSpace(baseURL) == "" {
			return fmt.Errorf("providers.%s.base_url is required", name)
		}
	}
	for alias, value := range models {
		if strings.TrimSpace(alias) == "" {
			return errors.New("model alias cannot be empty")
		}
		model, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("models.%s must be a table", alias)
		}
		provider, _ := model["provider"].(string)
		if strings.TrimSpace(provider) == "" {
			return fmt.Errorf("models.%s.provider is required", alias)
		}
		if _, ok := providers[provider]; !ok {
			return fmt.Errorf("models.%s references unknown provider %q", alias, provider)
		}
		modelID, _ := model["model"].(string)
		if strings.TrimSpace(modelID) == "" {
			return fmt.Errorf("models.%s.model is required", alias)
		}
		if value, exists := model["context_length"]; exists {
			length, ok := configInt(value)
			if !ok || length < 4096 {
				return fmt.Errorf("models.%s.context_length must be an integer of at least 4096", alias)
			}
		}
		if err := validateCacheNumbers("models."+alias, model); err != nil {
			return err
		}
	}
	if len(models) > 0 {
		defaultAlias, _ := raw["default_model"].(string)
		if strings.TrimSpace(defaultAlias) == "" {
			return errors.New("default_model is required when models are configured")
		}
		if _, ok := models[defaultAlias]; !ok {
			return fmt.Errorf("default_model %q not found in [models]", defaultAlias)
		}
	}
	for _, key := range []string{"shell_agent_model", "answer_model", "file_discovery_model"} {
		alias, _ := raw[key].(string)
		if alias != "" {
			if _, ok := models[alias]; !ok {
				return fmt.Errorf("%s %q not found in [models]", key, alias)
			}
		}
	}
	if legacy, ok := raw["model"].(map[string]any); ok {
		if alias, _ := legacy["model_name"].(string); alias != "" {
			if _, ok := models[alias]; !ok {
				return fmt.Errorf("model.model_name %q not found in [models]", alias)
			}
		}
	}
	if defaults, ok := raw["model_defaults"].(map[string]any); ok {
		if value, exists := defaults["context_length"]; exists {
			length, valid := configInt(value)
			if !valid || length < 4096 {
				return errors.New("model_defaults.context_length must be an integer of at least 4096")
			}
		}
		if err := validateCacheNumbers("model_defaults", defaults); err != nil {
			return err
		}
	}
	return nil
}

func validateCacheNumbers(prefix string, table map[string]any) error {
	for _, key := range []string{"cache_trigger_threshold", "cache_lookback_offset", "cache_reanchor_tokens", "cache_reanchor_messages", "cache_reanchor_min_cached_tokens"} {
		value, ok := table[key]
		if !ok {
			continue
		}
		var negative bool
		switch n := value.(type) {
		case int64:
			negative = n < 0
		case int:
			negative = n < 0
		case float64:
			negative = n < 0
		default:
			return fmt.Errorf("%s.%s must be a non-negative number", prefix, key)
		}
		if negative {
			return fmt.Errorf("%s.%s must be non-negative", prefix, key)
		}
	}
	return nil
}

func requireInteractive(noInteractive bool) error {
	if noInteractive {
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("interactive configuration requires a terminal; pass --no-interactive with complete arguments")
	}
	return nil
}

func confirmConfigChange(noInteractive bool, summary string) (bool, error) {
	if noInteractive {
		return true, nil
	}
	fmt.Println(summary)
	return promptYesNo(bufio.NewReader(os.Stdin), os.Stdout, "Apply this change? [y/N]: ", false)
}

func parseAssignments(values []string) (map[string]string, error) {
	result := map[string]string{}
	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("expected key=value, got %q", value)
		}
		result[strings.TrimSpace(key)] = item
	}
	return result, nil
}

func sortedKeys(table map[string]any) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func promptLineDefault(reader *bufio.Reader, label, current string, required bool) (string, error) {
	for {
		if current == "" {
			fmt.Printf("%s: ", label)
		} else {
			fmt.Printf("%s [%s]: ", label, current)
		}
		line, err := readInitLine(reader)
		if err != nil {
			return "", err
		}
		if line == "" {
			line = current
		}
		if line != "" || !required {
			return line, nil
		}
		fmt.Printf("%s is required.\n", label)
	}
}

func promptSecret(label string) (string, error) {
	fmt.Printf("%s: ", label)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}

func confirmProviderReasoning(reader *bufio.Reader, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if canonical, ok := canonicalReasoningValue(value); ok {
		return canonical, nil
	}
	if suggestion, likely := reasoningTypoSuggestion(value); likely {
		useSuggestion, err := promptYesNo(reader, os.Stdout, fmt.Sprintf("Did you mean %q? [Y/n]: ", suggestion), true)
		if err != nil {
			return "", err
		}
		if useSuggestion {
			return suggestion, nil
		}
	}
	confirmed, err := promptYesNo(reader, os.Stdout, fmt.Sprintf("Use provider-specific reasoning value %q? [y/N]: ", value), false)
	if err != nil {
		return "", err
	}
	if !confirmed {
		return "", errors.New("reasoning value was not confirmed")
	}
	return value, nil
}

func apiKeyValue(literal, envName string) (string, error) {
	if literal != "" && envName != "" {
		return "", errors.New("--api-key and --api-key-env are mutually exclusive")
	}
	if envName != "" {
		if !validEnvironmentName(envName) {
			return "", errors.New("--api-key-env expects an environment variable name")
		}
		return "${" + envName + "}", nil
	}
	return literal, nil
}

func validEnvironmentName(value string) bool {
	for index, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (index > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return value != ""
}

func printProvider(name string, provider map[string]any) {
	fmt.Printf("Provider %s\n", name)
	for _, key := range []string{"base_url", "endpoint", "reasoning_format"} {
		if value, ok := provider[key]; ok {
			fmt.Printf("  %s: %v\n", key, value)
		}
	}
	if value, ok := provider["api_key"].(string); ok && value != "" {
		if strings.HasPrefix(value, "${") {
			fmt.Printf("  api_key: %s\n", value)
		} else {
			fmt.Println("  api_key: [redacted]")
		}
	}
}

func printModel(alias string, model map[string]any, inheritedContextLength int) {
	fmt.Printf("Model %s\n", alias)
	fmt.Printf("  provider: %v\n", model["provider"])
	fmt.Printf("  model: %v\n", model["model"])
	if value, ok := model["context_length"]; ok {
		fmt.Printf("  context_length: %v (model)\n", value)
	} else {
		fmt.Printf("  context_length: %d (inherited)\n", inheritedContextLength)
	}
	if params, ok := model["params"].(map[string]any); ok {
		if effort, ok := params["reasoning_effort"]; ok {
			fmt.Printf("  reasoning: %v\n", effort)
		}
		if reasoning, ok := params["reasoning"].(map[string]any); ok {
			if effort, ok := reasoning["effort"]; ok {
				fmt.Printf("  reasoning: %v\n", effort)
			}
		}
	}
	if value, ok := model["params_json"].(string); ok && strings.TrimSpace(value) != "" {
		fmt.Printf("  params_json: %s\n", value)
	}
}

func parseJSONMap(value string) (map[string]any, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	result, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("invalid JSON object: value must be a top-level object")
	}
	return result, nil
}

func parseNonNegativeFlag(name, value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("--%s must be a non-negative integer", name)
	}
	return n, nil
}

func copyMap(source map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range source {
		result[key] = value
	}
	return result
}

func configActionTitle(action string) string {
	if action == "add" {
		return "Add"
	}
	return "Update"
}

func configActionPast(action string) string {
	if action == "add" {
		return "Added"
	}
	return "Updated"
}

func removeAliasReferences(raw map[string]any, old, replacement string) {
	for _, key := range []string{"default_model", "shell_agent_model", "answer_model", "file_discovery_model"} {
		if value, _ := raw[key].(string); value == old {
			raw[key] = replacement
		}
	}
	if legacy, ok := raw["model"].(map[string]any); ok {
		if value, _ := legacy["model_name"].(string); value == old {
			legacy["model_name"] = replacement
		}
	}
}

func aliasIsReferenced(raw map[string]any, alias string) bool {
	for _, key := range []string{"default_model", "shell_agent_model", "answer_model", "file_discovery_model"} {
		if value, _ := raw[key].(string); value == alias {
			return true
		}
	}
	if legacy, ok := raw["model"].(map[string]any); ok {
		if value, _ := legacy["model_name"].(string); value == alias {
			return true
		}
	}
	return false
}

func promptSelection(title string, values []string, themes ...presentation.Theme) (string, error) {
	options := make([]initMenuOption, 0, len(values))
	for _, value := range values {
		options = append(options, initMenuOption{label: value, value: value})
	}
	return promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), title, "Use Up/Down arrows and Enter.", options, themes...)
}

func promptDocumentSelection(doc *configDocument, title string, values []string) (string, error) {
	theme, err := doc.menuTheme(os.Stdout)
	if err != nil {
		return "", err
	}
	return promptSelection(title, values, theme)
}

func printStringMap(out io.Writer, value any, indent string) {
	table, ok := value.(map[string]any)
	if !ok {
		return
	}
	for _, key := range sortedKeys(table) {
		fmt.Fprintf(out, "%s%s=%v\n", indent, key, table[key])
	}
}
