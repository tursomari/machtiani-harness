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

func setupConfigTest(t *testing.T) (origDir string, cleanup func()) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
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

func TestSetNestedConfig_CreatesIntermediateMaps(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	configPath := filepath.Join(".machtiani", "config.toml")
	writeTOML(t, configPath, llm.DefaultMinimalConfigMap())
	if err := setNestedConfig(configPath, "high", "models", "default", "params", "reasoning", "effort"); err != nil {
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

func TestSetNestedConfig_OverwritesNonMap(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	configPath := filepath.Join(".machtiani", "config.toml")
	cfg := map[string]any{
		"default_model": "",
		"models": map[string]any{
			"default": map[string]any{
				"params": "not-a-map",
			},
		},
	}
	writeTOML(t, configPath, cfg)
	if err := setNestedConfig(configPath, "high", "models", "default", "params", "reasoning", "effort"); err != nil {
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

func TestConfigURLCommand_UpdatesField(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	configPath := filepath.Join(".machtiani", "config.toml")
	writeTOML(t, configPath, llm.DefaultMinimalConfigMap())
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

func TestConfigModelCommand_UpdatesField(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	configPath := filepath.Join(".machtiani", "config.toml")
	writeTOML(t, configPath, llm.DefaultMinimalConfigMap())
	if err := setNestedConfig(configPath, "old-model", "models", "default", "model"); err != nil {
		t.Fatalf("setNestedConfig (initial): %v", err)
	}
	code := handleLegacyConfigModelCommand([]string{"new-model"})
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
		{"model", handleLegacyConfigModelCommand, []string{"x"}},
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

func TestConfigCommand_BackwardCompatibility(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	configPath := filepath.Join(".machtiani", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte(`
default_model = ""
listen = "127.0.0.1:8042"

[models.default]
model = "old-model"
`)
	if err := os.WriteFile(configPath, content, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	code := handleLegacyConfigModelCommand([]string{"new-model"})
	if code != 0 {
		t.Fatalf("expected return code 0, got %d", code)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `model = "new-model"`) {
		t.Errorf("expected model = \"new-model\" in config:\n%s", s)
	}
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

	for _, title := range []string{"Providers", "Models", "Planner", "Shell Agent", "Environment", "General"} {
		if !strings.Contains(stdout, title) {
			t.Errorf("missing section title %q", title)
		}
	}
	if strings.Contains(stdout, "(config.toml)") {
		t.Error("unexpected (config.toml) source tag in no-config output")
	}
	if strings.Contains(stdout, "(flag)") {
		t.Error("unexpected (flag) source tag in no-config output")
	}
	if !strings.Contains(stdout, "(default)") {
		t.Error("missing (default) source tag in no-config output")
	}
	if !strings.Contains(stdout, "Turn budget and timeout") {
		t.Error("missing Planner description")
	}
	if !strings.Contains(stdout, "Step budget and finalize window") {
		t.Error("missing Shell Agent description")
	}
	if !strings.Contains(stdout, "Execution environment and workspace settings") {
		t.Error("missing Environment description")
	}
	if !strings.Contains(stdout, "Top-level defaults and flags") {
		t.Error("missing General description")
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

	content := []byte(`default_model = ""

[planner]
max_turns = 200

[providers.default]
base_url = "https://api.openai.com"
api_key = "sk-test-key"

[models.default]
provider = "default"
model = "gpt-4o"
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

	for _, title := range []string{"Providers", "Models", "Planner", "General"} {
		if !strings.Contains(stdout, title) {
			t.Errorf("missing section title %q", title)
		}
	}
	if !strings.Contains(stdout, "max_turns") {
		t.Error("missing planner.max_turns")
	}
	if !strings.Contains(stdout, "(config.toml)") {
		t.Error("missing (config.toml) source tag for set field")
	}
	if !strings.Contains(stdout, "(default)") {
		t.Error("missing (default) source tag for unset field")
	}
	if !strings.Contains(stdout, "[default]") {
		t.Error("missing [default] alias sub-header under Providers or Models")
	}
	if strings.Contains(stdout, "sk-test-key") {
		t.Error("API key should be masked in output")
	}
	if !strings.Contains(stdout, "********") {
		t.Error("API key should appear as ********")
	}
}

// ---------------------------------------------------------------------------
// TestConfigShowCommand_SortedAliases
// ---------------------------------------------------------------------------

func TestConfigShowCommand_SortedAliases(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	llm.ResetConfigForTesting()

	configPath := filepath.Join(".machtiani", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	content := []byte(`default_model = ""

[providers.zebra]
base_url = "https://zebra.example.com"

[providers.alpha]
base_url = "https://alpha.example.com"

[providers.mike]
base_url = "https://mike.example.com"

[models.bravo]
provider = "bravo"
model = "model-b"

[models.alpha]
provider = "alpha"
model = "model-a"
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

	alphaIdx := strings.Index(stdout, "[alpha]")
	bravoIdx := strings.Index(stdout, "[bravo]")
	mikeIdx := strings.Index(stdout, "[mike]")
	zebraIdx := strings.Index(stdout, "[zebra]")

	if alphaIdx == -1 || bravoIdx == -1 || zebraIdx == -1 {
		t.Fatal("missing expected alias sub-headers")
	}
	if alphaIdx > mikeIdx || mikeIdx > zebraIdx {
		t.Error("provider aliases not sorted alphabetically")
	}
	if alphaIdx > bravoIdx {
		t.Error("model aliases not sorted alphabetically")
	}
}

// ---------------------------------------------------------------------------
// TestConfigShowCommand_FullFlag
// ---------------------------------------------------------------------------

func TestConfigShowCommand_FullFlag(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	llm.ResetConfigForTesting()

	var code int
	stdout, _ := captureOutput(func() {
		code = handleConfigShowCommand([]string{"--full"})
	})

	if code != 0 {
		t.Fatalf("expected return code 0, got %d", code)
	}

	if !strings.Contains(stdout, "Output paths for final answers and transcripts") {
		t.Error("missing Files section description under --full")
	}
	if !strings.Contains(stdout, "final_file") {
		t.Error("missing final_file under --full")
	}
	if !strings.Contains(stdout, "transcript_file") {
		t.Error("missing transcript_file under --full")
	}
	if !strings.Contains(stdout, "Telemetry and debugging trajectory settings") {
		t.Error("missing Trajectory section description under --full")
	}
	if !strings.Contains(stdout, "trajectory.enabled") {
		t.Error("missing trajectory.enabled under --full")
	}
}

// ---------------------------------------------------------------------------
// TestConfigShowCommand_ApiKeyMasked
// ---------------------------------------------------------------------------

func TestConfigShowCommand_ApiKeyMasked(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	llm.ResetConfigForTesting()

	configPath := filepath.Join(".machtiani", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	content := []byte(`default_model = ""

[providers.svc]
base_url = "https://api.example.com"
api_key = "secret-12345"
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

	if strings.Contains(stdout, "secret-12345") {
		t.Error("API key should not appear in plain text")
	}
	if !strings.Contains(stdout, "********") {
		t.Error("API key should be masked as ********")
	}
}

// ---------------------------------------------------------------------------
// TestConfigShowCommand_EmptyValues
// ---------------------------------------------------------------------------

func TestConfigShowCommand_EmptyValues(t *testing.T) {
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

	if !strings.Contains(stdout, "environment.type") {
		t.Error("missing environment.type (empty but should be present)")
	}
	if strings.Contains(stdout, "environment.cwd") {
		t.Error("removed environment.cwd should not be present")
	}
}

// ---------------------------------------------------------------------------
// TestConfigShowCommand_SourceFileFlag
// ---------------------------------------------------------------------------

func TestConfigShowCommand_SourceFileFlag(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	llm.ResetConfigForTesting()

	configPath := filepath.Join(".machtiani", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	content := []byte(`default_model = ""

[planner]
max_turns = 200
`)
	if err := os.WriteFile(configPath, content, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var code int
	stdout, _ := captureOutput(func() {
		code = handleConfigShowCommand([]string{"--verbose"})
	})

	if code != 0 {
		t.Fatalf("expected return code 0, got %d", code)
	}

	if !strings.Contains(stdout, "verbose") {
		t.Error("missing verbose line")
	}
	if !strings.Contains(stdout, "(flag)") {
		t.Error("missing (flag) source tag for flag-set field")
	}
	if !strings.Contains(stdout, "max_turns") {
		t.Error("missing planner.max_turns line")
	}
	if strings.Count(stdout, "(config.toml)") < 1 {
		t.Error("expected at least one (config.toml) source tag")
	}
}

// ---------------------------------------------------------------------------
// TestConfigShowCommand_ParamSource
// ---------------------------------------------------------------------------

func TestConfigShowCommand_ParamSource(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	llm.ResetConfigForTesting()

	configPath := filepath.Join(".machtiani", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	content := []byte(`default_model = ""

[models.default]
provider = "default"
model = "gpt-4o"
params = { temperature = 0.7 }
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

	if !strings.Contains(stdout, "temperature") {
		t.Error("missing model param temperature")
	}
	if !strings.Contains(stdout, "(default)") && !strings.Contains(stdout, "(config.toml)") {
		t.Error("missing source tag for model param")
	}
}

// ---------------------------------------------------------------------------
// TestConfigShowCommand_KeyDetail_Valid
// ---------------------------------------------------------------------------
