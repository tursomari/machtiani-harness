package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestConfigAddNonInteractiveCreatesResourceConfig(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	code := handleConfigAddCommand([]string{
		"--provider", "openai", "--url", "https://api.example/v1",
		"--api-key-env", "OPENAI_API_KEY", "--model", "gpt-x",
		"--alias", "coder", "--reasoning", "xhigh", "--no-interactive",
	})
	if code != 0 {
		t.Fatalf("config add exit = %d", code)
	}
	raw := readTOML(t, filepath.Join(".machtiani", "config.toml"))
	if raw["default_model"] != "coder" {
		t.Fatalf("default_model = %#v", raw["default_model"])
	}
	providers := raw["providers"].(map[string]any)
	provider := providers["openai"].(map[string]any)
	if provider["api_key"] != "${OPENAI_API_KEY}" {
		t.Fatalf("api_key = %#v", provider["api_key"])
	}
	models := raw["models"].(map[string]any)
	model := models["coder"].(map[string]any)
	if model["provider"] != "openai" || model["model"] != "gpt-x" {
		t.Fatalf("model = %#v", model)
	}
	info, err := os.Stat(filepath.Join(".machtiani", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestConfigContextLengthLifecycle(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	if code := handleConfigAddCommand([]string{
		"--provider", "custom", "--url", "https://api.example/v1", "--api-key-env", "TEST_KEY",
		"--model", "demo", "--alias", "demo", "--context-length", "64000", "--no-interactive",
	}); code != 0 {
		t.Fatalf("config add exit = %d", code)
	}
	path := filepath.Join(".machtiani", "config.toml")
	raw := readTOML(t, path)
	if got := raw["model_defaults"].(map[string]any)["context_length"]; got != int64(llm.DefaultContextLength) {
		t.Fatalf("default context_length = %#v", got)
	}
	if got := raw["models"].(map[string]any)["demo"].(map[string]any)["context_length"]; got != int64(64000) {
		t.Fatalf("model context_length = %#v", got)
	}
	if code := handleConfigModelCommand([]string{"set", "demo", "--clear-context-length", "--no-interactive"}); code != 0 {
		t.Fatalf("clear context exit = %d", code)
	}
	raw = readTOML(t, path)
	if _, exists := raw["models"].(map[string]any)["demo"].(map[string]any)["context_length"]; exists {
		t.Fatal("model context_length remained after clear")
	}
}

func TestManagedConfigCheckUsesExplicitPath(t *testing.T) {
	dir := t.TempDir()
	explicitPath := filepath.Join(dir, "explicit.toml")
	otherPath := filepath.Join(dir, "other.toml")
	valid := `default_model = "demo"

[providers.fake]
base_url = "https://example.com/v1"
api_key = "test-key"

[models.demo]
provider = "fake"
model = "demo-model"
`
	if err := os.WriteFile(explicitPath, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte("unknown_top = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHTIANI_CONFIG", otherPath)

	stdout, stderr := captureOutput(func() {
		if code := handleManagedConfigCheck([]string{"--path", explicitPath}); code != 0 {
			t.Errorf("config check exit = %d", code)
		}
	})
	if !strings.Contains(stderr, "ignoring MACHTIANI_CONFIG=") {
		t.Fatalf("expected explicit-target notice, got stderr = %q", stderr)
	}
	if !strings.Contains(stdout, "Config OK: "+explicitPath) {
		t.Fatalf("explicit config was not checked: %s", stdout)
	}
}

func TestConfigAddPresetCreatesCompleteProviderAndModel(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	if code := handleConfigAddCommand([]string{"--preset", "deepseek", "--no-interactive"}); code != 0 {
		t.Fatalf("config add preset exit = %d", code)
	}
	raw := readTOML(t, filepath.Join(".machtiani", "config.toml"))
	if raw["default_model"] != "deepseek" {
		t.Fatalf("default_model = %#v", raw["default_model"])
	}
	provider := raw["providers"].(map[string]any)["deepseek"].(map[string]any)
	if provider["base_url"] != "https://api.deepseek.com" || provider["endpoint"] != "/chat/completions" {
		t.Fatalf("provider = %#v", provider)
	}
	if provider["api_key"] != "${DEEPSEEK_API_KEY}" {
		t.Fatalf("api_key = %#v", provider["api_key"])
	}
	model := raw["models"].(map[string]any)["deepseek"].(map[string]any)
	if model["model"] != "deepseek-v4-flash" || model["cache_enabled"] != false {
		t.Fatalf("model = %#v", model)
	}
}

func TestConfigAddPresetAllowsCompleteOverrides(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	if code := handleConfigAddCommand([]string{
		"--preset", "openrouter", "--url", "https://gateway.example/v1", "--endpoint", "/chat",
		"--api-key-env", "ROUTER_TOKEN", "--header", "X-Tenant=alpha", "--query", "version=2",
		"--model", "vendor/new-model", "--alias", "custom", "--reasoning", "experimental", "--no-interactive",
	}); code != 0 {
		t.Fatalf("config add preset overrides exit = %d", code)
	}
	raw := readTOML(t, filepath.Join(".machtiani", "config.toml"))
	provider := raw["providers"].(map[string]any)["openrouter"].(map[string]any)
	if provider["base_url"] != "https://gateway.example/v1" || provider["endpoint"] != "/chat" {
		t.Fatalf("provider = %#v", provider)
	}
	if provider["headers"].(map[string]any)["X-Tenant"] != "alpha" || provider["query"].(map[string]any)["version"] != "2" {
		t.Fatalf("provider advanced values = %#v", provider)
	}
	model := raw["models"].(map[string]any)["custom"].(map[string]any)
	effort := model["params"].(map[string]any)["reasoning"].(map[string]any)["effort"]
	if effort != "experimental" {
		t.Fatalf("reasoning effort = %#v", effort)
	}
}

func TestConfigProviderReasoningFormatAndModelParamsJSON(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	if code := handleConfigAddCommand([]string{
		"--provider", "custom", "--url", "https://api.example/v1", "--api-key", "secret",
		"--model", "demo", "--alias", "demo", "--no-interactive",
	}); code != 0 {
		t.Fatalf("config add exit = %d", code)
	}
	if code := handleConfigProviderCommand([]string{
		"set", "custom", "--reasoning-format", "reasoning_explicit", "--no-interactive",
	}); code != 0 {
		t.Fatalf("provider set exit = %d", code)
	}
	paramsJSON := `{"reasoning":{"effort":"high","budget_tokens":null,"enabled":true},"temperature":0.2}`
	if code := handleConfigModelCommand([]string{
		"set", "demo", "--param", "temperature=legacy", "--param-json", paramsJSON, "--no-interactive",
	}); code != 0 {
		t.Fatalf("model set exit = %d", code)
	}

	raw := readTOML(t, filepath.Join(".machtiani", "config.toml"))
	provider := raw["providers"].(map[string]any)["custom"].(map[string]any)
	if provider["reasoning_format"] != "reasoning_explicit" {
		t.Fatalf("reasoning_format = %#v", provider["reasoning_format"])
	}
	model := raw["models"].(map[string]any)["demo"].(map[string]any)
	if model["params_json"] != paramsJSON {
		t.Fatalf("params_json = %#v", model["params_json"])
	}

	resolved, _, err := llm.LoadGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	definition := resolved.Models["demo"]
	if definition.Params["temperature"] != float64(0.2) {
		t.Fatalf("params_json did not overlay native params: %#v", definition.Params)
	}
	reasoning := definition.Params["reasoning"].(map[string]any)
	if value, exists := reasoning["budget_tokens"]; !exists || value != nil {
		t.Fatalf("JSON null was not preserved: %#v", reasoning)
	}

	if code := handleConfigModelCommand([]string{"set", "demo", "--clear-params-json", "--no-interactive"}); code != 0 {
		t.Fatalf("clear params_json exit = %d", code)
	}
	if code := handleConfigProviderCommand([]string{"set", "custom", "--reasoning-format", "auto", "--no-interactive"}); code != 0 {
		t.Fatalf("clear reasoning format exit = %d", code)
	}
	raw = readTOML(t, filepath.Join(".machtiani", "config.toml"))
	provider = raw["providers"].(map[string]any)["custom"].(map[string]any)
	model = raw["models"].(map[string]any)["demo"].(map[string]any)
	if _, exists := provider["reasoning_format"]; exists {
		t.Fatalf("reasoning_format was not cleared: %#v", provider)
	}
	if _, exists := model["params_json"]; exists {
		t.Fatalf("params_json was not cleared: %#v", model)
	}
}

func TestConfigCatalogListAndShow(t *testing.T) {
	stdout, stderr := captureOutput(func() {
		if code := handleConfigCatalogCommand([]string{"list"}); code != 0 {
			t.Errorf("catalog list exit = %d", code)
		}
		if code := handleConfigCatalogCommand([]string{"show", "openai"}); code != 0 {
			t.Errorf("catalog show exit = %d", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	for _, want := range []string{"deepseek", "openrouter", "gpt-5.5", "OPENAI_API_KEY"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("catalog output missing %q: %s", want, stdout)
		}
	}
}

func TestConfigAddRejectsUnknownPreset(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	_, stderr := captureOutput(func() {
		if code := handleConfigAddCommand([]string{"--preset", "missing", "--no-interactive"}); code != 2 {
			t.Errorf("exit = %d", code)
		}
	})
	if !strings.Contains(stderr, "unknown provider preset") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestConfigResourceRenameAndReferenceAwareRemoval(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	if code := handleConfigAddCommand([]string{"--provider", "p", "--url", "https://example", "--api-key", "secret", "--model", "one", "--alias", "one", "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	if code := handleConfigModelCommand([]string{"add", "two", "--provider", "p", "--model", "two", "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	if code := handleConfigProviderCommand([]string{"rename", "p", "renamed", "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	if code := handleConfigModelCommand([]string{"rename", "one", "primary", "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	raw := readTOML(t, filepath.Join(".machtiani", "config.toml"))
	if raw["default_model"] != "primary" {
		t.Fatalf("default_model = %#v", raw["default_model"])
	}
	models := raw["models"].(map[string]any)
	if models["primary"].(map[string]any)["provider"] != "renamed" {
		t.Fatalf("models = %#v", models)
	}
	if code := handleConfigProviderCommand([]string{"remove", "renamed", "--no-interactive"}); code != 1 {
		t.Fatalf("referenced provider remove exit = %d", code)
	}
	if code := handleConfigModelCommand([]string{"remove", "primary", "--replacement", "two", "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	raw = readTOML(t, filepath.Join(".machtiani", "config.toml"))
	if raw["default_model"] != "two" {
		t.Fatalf("replacement default = %#v", raw["default_model"])
	}
}

func TestConfigCacheOverrideAndInherit(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	if code := handleConfigAddCommand([]string{"--provider", "p", "--url", "https://example", "--api-key", "secret", "--model", "one", "--alias", "one", "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	if code := handleConfigCacheCommand([]string{"disable", "--model", "one", "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	raw := readTOML(t, filepath.Join(".machtiani", "config.toml"))
	model := raw["models"].(map[string]any)["one"].(map[string]any)
	if model["cache_enabled"] != false {
		t.Fatalf("cache override = %#v", model["cache_enabled"])
	}
	if code := handleConfigCacheCommand([]string{"inherit", "--model", "one", "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	raw = readTOML(t, filepath.Join(".machtiani", "config.toml"))
	model = raw["models"].(map[string]any)["one"].(map[string]any)
	if _, ok := model["cache_enabled"]; ok {
		t.Fatalf("cache override survived: %#v", model)
	}
}

func TestConfigTargetExplicitPathOverridesEnvironment(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	envPath := filepath.Join(t.TempDir(), "environment.toml")
	explicitPath := filepath.Join(t.TempDir(), "explicit.toml")
	t.Setenv("MACHTIANI_CONFIG", envPath)
	stdout, stderr := captureOutput(func() {
		if code := handleConfigAddCommand([]string{"--path", explicitPath, "--provider", "p", "--url", "https://example", "--api-key", "secret", "--model", "one", "--alias", "one", "--no-interactive"}); code != 0 {
			t.Errorf("exit = %d", code)
		}
	})
	_ = stdout
	if !strings.Contains(stderr, "ignoring MACHTIANI_CONFIG") || !strings.Contains(stderr, explicitPath) {
		t.Fatalf("stderr = %q", stderr)
	}
	if _, err := os.Stat(explicitPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("environment path unexpectedly written: %v", err)
	}
}

func TestRemovedScalarConfigCommands(t *testing.T) {
	for _, command := range []string{"url", "api-key", "reasoning"} {
		_, stderr := captureOutput(func() {
			if code := handleConfigCommand([]string{command, "value"}); code != 2 {
				t.Errorf("%s exit = %d", command, code)
			}
		})
		if !strings.Contains(stderr, "was removed") {
			t.Errorf("%s stderr = %q", command, stderr)
		}
	}
}

func TestConfigAddNoInteractiveRequiresCompleteArguments(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	_, stderr := captureOutput(func() {
		if code := handleConfigAddCommand([]string{"--provider", "p", "--no-interactive"}); code != 2 {
			t.Errorf("exit = %d", code)
		}
	})
	if !strings.Contains(stderr, "new providers require") {
		t.Fatalf("stderr = %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(".machtiani", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("incomplete command wrote a config: %v", err)
	}
}

func TestInvalidModelMutationLeavesFileUnchanged(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	if code := handleConfigAddCommand([]string{"--provider", "p", "--url", "https://example", "--api-key", "secret", "--model", "one", "--alias", "one", "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	path := filepath.Join(".machtiani", "config.toml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code := handleConfigModelCommand([]string{"set", "one", "--provider", "missing", "--no-interactive"}); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("invalid mutation changed the configuration file")
	}
}

func TestProviderShowRedactsLiteralAPIKey(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	if code := handleConfigAddCommand([]string{"--provider", "p", "--url", "https://example", "--api-key", "super-secret", "--model", "one", "--alias", "one", "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	stdout, _ := captureOutput(func() {
		if code := handleConfigProviderCommand([]string{"show", "p"}); code != 0 {
			t.Errorf("exit = %d", code)
		}
	})
	if strings.Contains(stdout, "super-secret") || !strings.Contains(stdout, "[redacted]") {
		t.Fatalf("provider output = %q", stdout)
	}
}

func TestConfigMutationPreservesUnknownKeys(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	if code := handleConfigAddCommand([]string{"--provider", "p", "--url", "https://example", "--api-key", "secret", "--model", "one", "--alias", "one", "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	path := filepath.Join(".machtiani", "config.toml")
	raw := readTOML(t, path)
	raw["custom_extension"] = map[string]any{"enabled": true, "label": "keep-me"}
	writeTOML(t, path, raw)
	if code := handleConfigCacheCommand([]string{"set", "--trigger-threshold", "8192", "--control-json", `{"type":"ephemeral"}`, "--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	raw = readTOML(t, path)
	extension, ok := raw["custom_extension"].(map[string]any)
	if !ok || extension["label"] != "keep-me" {
		t.Fatalf("unknown extension lost: %#v", raw["custom_extension"])
	}
	defaults := raw["model_defaults"].(map[string]any)
	if defaults["cache_trigger_threshold"] != int64(8192) {
		t.Fatalf("threshold = %#v", defaults["cache_trigger_threshold"])
	}
}
