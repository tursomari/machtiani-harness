package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

// setupConfigTest creates a temp dir, chdir into it, calls
// llm.ResetConfigForTesting, and returns the original dir and a cleanup
// function.  The cleanup is registered with t.Cleanup.
func setupConfigTest(t *testing.T) (origDir string, cleanup func()) {
	t.Helper()
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	llm.ResetConfigForTesting()
	cleanup = func() {
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("chdir back: %v", err)
		}
	}
	t.Cleanup(cleanup)
	return origDir, cleanup
}

// captureOutput runs fn and captures both stdout and stderr.
func captureOutput(fn func()) (stdout, stderr string) {
	oldStdout := os.Stdout
	oldStderr := os.Stderr

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, rOut)
		outCh <- buf.String()
	}()
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, rErr)
		errCh <- buf.String()
	}()

	fn()

	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	return <-outCh, <-errCh
}

// writeTOML encodes the given map as TOML and writes it to path.
// Directories are created as needed.
func writeTOML(t *testing.T, path string, data map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(data); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}

// readTOML reads the file at path and decodes it into a map[string]any.
func readTOML(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// TestSetNestedConfig_CreatesIntermediateMaps
// ---------------------------------------------------------------------------

func TestSetNestedConfig_CreatesIntermediateMaps(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()

	configPath := filepath.Join(".machtiani", "config.toml")
	writeTOML(t, configPath, llm.DefaultMinimalConfigMap())

	if err := setNestedConfig(configPath, "high",
		"models", "default", "params", "reasoning", "effort"); err != nil {
		t.Fatalf("setNestedConfig: %v", err)
	}

	cfg := readTOML(t, configPath)

	models, ok := cfg["models"].(map[string]any)
	if !ok {
		t.Fatalf("models is not map[string]any, got %T", cfg["models"])
	}
	def, ok := models["default"].(map[string]any)
	if !ok {
		t.Fatalf("models.default is not map[string]any, got %T", models["default"])
	}
	params, ok := def["params"].(map[string]any)
	if !ok {
		t.Fatalf("models.default.params is not map[string]any, got %T", def["params"])
	}
	reasoning, ok := params["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("models.default.params.reasoning is not map[string]any, got %T", params["reasoning"])
	}
	if reasoning["effort"] != "high" {
		t.Errorf("expected effort = %q, got %q", "high", reasoning["effort"])
	}
}

// ---------------------------------------------------------------------------
// TestSetNestedConfig_OverwritesNonMap
// ---------------------------------------------------------------------------

func TestSetNestedConfig_OverwritesNonMap(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()

	configPath := filepath.Join(".machtiani", "config.toml")
	cfg := map[string]any{
		"default_model": "",
		"models": map[string]any{
			"default": map[string]any{
				"params": "not-a-map", // a string, not a map
			},
		},
	}
	writeTOML(t, configPath, cfg)

	if err := setNestedConfig(configPath, "high",
		"models", "default", "params", "reasoning", "effort"); err != nil {
		t.Fatalf("setNestedConfig: %v", err)
	}

	data := readTOML(t, configPath)

	models, ok := data["models"].(map[string]any)
	if !ok {
		t.Fatalf("models is not map[string]any, got %T", data["models"])
	}
	def, ok := models["default"].(map[string]any)
	if !ok {
		t.Fatalf("models.default is not map[string]any, got %T", models["default"])
	}
	params, ok := def["params"].(map[string]any)
	if !ok {
		t.Fatalf("models.default.params is not map[string]any, got %T", def["params"])
	}
	reasoning, ok := params["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("models.default.params.reasoning is not map[string]any, got %T", params["reasoning"])
	}
	if reasoning["effort"] != "high" {
		t.Errorf("expected effort = %q, got %q", "high", reasoning["effort"])
	}
}

// ---------------------------------------------------------------------------
// TestConfigURLCommand_UpdatesField
// ---------------------------------------------------------------------------

func TestConfigURLCommand_UpdatesField(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()

	configPath := filepath.Join(".machtiani", "config.toml")
	writeTOML(t, configPath, llm.DefaultMinimalConfigMap())

	// Set initial value via setNestedConfig so we can later verify it changed.
	if err := setNestedConfig(configPath, "old", "providers", "default", "base_url"); err != nil {
		t.Fatalf("setNestedConfig (initial): %v", err)
	}

	code := handleConfigURLCommand([]string{"new"})
	if code != 0 {
		t.Fatalf("expected return code 0, got %d", code)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, `base_url = "new"`) {
		t.Errorf("expected base_url = \"new\", got:\n%s", s)
	}
	if strings.Contains(s, `base_url = "old"`) {
		t.Errorf("old base_url should no longer be present:\n%s", s)
	}
}

// ---------------------------------------------------------------------------
// TestConfigAPIKeyCommand_UpdatesField
// ---------------------------------------------------------------------------

func TestConfigAPIKeyCommand_UpdatesField(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()

	configPath := filepath.Join(".machtiani", "config.toml")
	writeTOML(t, configPath, llm.DefaultMinimalConfigMap())

	if err := setNestedConfig(configPath, "old-key", "providers", "default", "api_key"); err != nil {
		t.Fatalf("setNestedConfig (initial): %v", err)
	}

	code := handleConfigAPIKeyCommand([]string{"new-key"})
	if code != 0 {
		t.Fatalf("expected return code 0, got %d", code)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, `api_key = "new-key"`) {
		t.Errorf("expected api_key = \"new-key\", got:\n%s", s)
	}
	if strings.Contains(s, `api_key = "old-key"`) {
		t.Errorf("old api_key should no longer be present:\n%s", s)
	}
}

// ---------------------------------------------------------------------------
// TestConfigModelCommand_UpdatesField
// ---------------------------------------------------------------------------

func TestConfigModelCommand_UpdatesField(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()

	configPath := filepath.Join(".machtiani", "config.toml")
	writeTOML(t, configPath, llm.DefaultMinimalConfigMap())

	if err := setNestedConfig(configPath, "old-model", "models", "default", "model"); err != nil {
		t.Fatalf("setNestedConfig (initial): %v", err)
	}

	code := handleConfigModelCommand([]string{"new-model"})
	if code != 0 {
		t.Fatalf("expected return code 0, got %d", code)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, `model = "new-model"`) {
		t.Errorf("expected model = \"new-model\", got:\n%s", s)
	}
	if strings.Contains(s, `model = "old-model"`) {
		t.Errorf("old model should no longer be present:\n%s", s)
	}
}

// ---------------------------------------------------------------------------
// TestConfigReasoningCommand_UpdatesField
// ---------------------------------------------------------------------------

func TestConfigReasoningCommand_UpdatesField(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()

	configPath := filepath.Join(".machtiani", "config.toml")
	writeTOML(t, configPath, llm.DefaultMinimalConfigMap())

	if err := setNestedConfig(configPath, "low", "models", "default", "params", "reasoning", "effort"); err != nil {
		t.Fatalf("setNestedConfig (initial): %v", err)
	}

	code := handleConfigReasoningCommand([]string{"high"})
	if code != 0 {
		t.Fatalf("expected return code 0, got %d", code)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, `effort = "high"`) {
		t.Errorf("expected effort = \"high\", got:\n%s", s)
	}
	if strings.Contains(s, `effort = "low"`) {
		t.Errorf("old effort value should no longer be present:\n%s", s)
	}
}

// ---------------------------------------------------------------------------
// TestConfigCommand_NoConfigFile
// ---------------------------------------------------------------------------

func TestConfigCommand_NoConfigFile(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()

	subcommands := []struct {
		name string
		fn   func([]string) int
		args []string
	}{
		{"url", handleConfigURLCommand, []string{"x"}},
		{"api-key", handleConfigAPIKeyCommand, []string{"x"}},
		{"model", handleConfigModelCommand, []string{"x"}},
		{"reasoning", handleConfigReasoningCommand, []string{"x"}},
	}

	for _, sc := range subcommands {
		t.Run(sc.name, func(t *testing.T) {
			llm.ResetConfigForTesting()

			var code int
			stderr := ""
			_, stderr = captureOutput(func() {
				code = sc.fn(sc.args)
			})

			if code != 1 {
				t.Errorf("expected return code 1, got %d", code)
			}
			if !strings.Contains(stderr, "config file not found") {
				t.Errorf("expected stderr to contain 'config file not found', got: %s", stderr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestConfigCommand_BackwardCompatibility
// ---------------------------------------------------------------------------

func TestConfigCommand_BackwardCompatibility(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()

	configPath := filepath.Join(".machtiani", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write a config with an extra "dead" field that should survive the
	// round-trip through setNestedConfig.
	content := []byte(`
default_model = ""
listen = "127.0.0.1:8042"

[models.default]
model = "old-model"
`)
	if err := os.WriteFile(configPath, content, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code := handleConfigModelCommand([]string{"new-model"})
	if code != 0 {
		t.Fatalf("expected return code 0, got %d", code)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	s := string(data)

	// The new model value should be present.
	if !strings.Contains(s, `model = "new-model"`) {
		t.Errorf("expected model = \"new-model\" in config:\n%s", s)
	}

	// The dead field should survive.
	if !strings.Contains(s, `listen = "127.0.0.1:8042"`) {
		t.Errorf("expected listen = \"127.0.0.1:8042\" to survive in config:\n%s", s)
	}
}

// ---------------------------------------------------------------------------
// TestConfigShowCommand_NoConfig
// ---------------------------------------------------------------------------

func TestConfigShowCommand_NoConfig(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	llm.ResetConfigForTesting()

	var code int
	stdout, _ := captureOutput(func() {
		code = handleConfigShowCommand([]string{})
	})

	if code != 0 {
		t.Fatalf("expected return code 0, got %d", code)
	}

	if !strings.Contains(stdout, "planner.max_turns = 150 # default") {
		t.Errorf("expected 'planner.max_turns = 150 # default' in stdout, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "environment.command_timeout = 9999 # default") {
		t.Errorf("expected 'environment.command_timeout = 9999 # default' in stdout, got:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// TestConfigShowCommand_WithConfig
// ---------------------------------------------------------------------------

func TestConfigShowCommand_WithConfig(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	llm.ResetConfigForTesting()

	configPath := filepath.Join(".machtiani", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write a config that overrides step_limit away from the default of 110.
	content := []byte(`
default_model = ""

[planner]
max_turns = 200
`)
	if err := os.WriteFile(configPath, content, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var code int
	stdout, _ := captureOutput(func() {
		code = handleConfigShowCommand([]string{})
	})

	if code != 0 {
		t.Fatalf("expected return code 0, got %d", code)
	}

	// The overridden value should be annotated with "# config.toml".
	if !strings.Contains(stdout, "planner.max_turns = 200 # config.toml") {
		t.Errorf("expected 'planner.max_turns = 200 # config.toml' in stdout, got:\n%s", stdout)
	}

	// Default values should still be annotated with "# default".
	if !strings.Contains(stdout, "environment.command_timeout = 9999 # default") {
		t.Errorf("expected 'environment.command_timeout = 9999 # default' in stdout, got:\n%s", stdout)
	}
}
