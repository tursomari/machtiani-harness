package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
