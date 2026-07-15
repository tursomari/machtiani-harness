package llm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadGlobalConfigAggregatesStructuralDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `default_model = 42
unknown_top = true

[planner]
max_turs = 7
max_turns = "many"

[models.demo]
provider = "fake"
model = "demo"
temperature = 0.2
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHTIANI_CONFIG", path)
	ResetConfigForTesting()
	t.Cleanup(ResetConfigForTesting)

	_, _, err := LoadGlobalConfig()
	if err == nil {
		t.Fatal("expected structural validation error")
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	wantPaths := []string{"default_model", "models.demo.temperature", "planner.max_turs", "planner.max_turns", "unknown_top"}
	for _, want := range wantPaths {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing diagnostic path %q in %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "did you mean max_turns?") {
		t.Fatalf("expected typo suggestion, got %v", err)
	}
}

func TestLoadConfigAcceptsLegacyInlineReasoning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `default_model = "demo"

[providers.fake]
base_url = "https://example.com/v1"

[models.demo]
provider = "fake"
model = "demo-model"
reasoning = { effort = "high" }
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("legacy inline reasoning failed validation: %v", err)
	}
	reasoning, ok := cfg.Models["demo"].Params["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("legacy reasoning was not preserved in params: %#v", cfg.Models["demo"].Params)
	}
}

func TestValidateConfigAuditsDormantReferencesWithoutCredentials(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Models = map[string]ModelDefinition{
		"active":  {Provider: "good", Model: "active-model"},
		"dormant": {Provider: "missing", Model: "dormant-model"},
	}
	cfg.Providers = map[string]ProviderConfig{
		"good": {BaseURL: "https://example.com/v1"},
	}

	diagnostics := ValidateConfig(cfg, "config.toml", ValidationOptions{})
	if !hasDiagnostic(diagnostics, "models.dormant.provider", "invalid_reference") {
		t.Fatalf("expected dormant provider diagnostic, got %#v", diagnostics)
	}
	if hasDiagnostic(diagnostics, "providers.good.api_key", "missing_credential") {
		t.Fatalf("automatic semantic audit must not require dormant credentials: %#v", diagnostics)
	}
}

func TestValidateConfigCheckRequiresAllCredentials(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel = "demo"
	cfg.Models = map[string]ModelDefinition{"demo": {Provider: "fake", Model: "demo-model"}}
	cfg.Providers = map[string]ProviderConfig{"fake": {BaseURL: "https://example.com/v1"}}

	diagnostics := ValidateConfig(cfg, "config.toml", ValidationOptions{RequireAllCredentials: true, RequireDefaultModel: true})
	if !hasDiagnostic(diagnostics, "providers.fake.api_key", "missing_credential") {
		t.Fatalf("expected missing credential diagnostic, got %#v", diagnostics)
	}
}

func TestValidateConfigCheckAcceptsAliasCredentialEnvironment(t *testing.T) {
	t.Setenv("DEMO_API_KEY", "alias-key")
	cfg := DefaultConfig()
	cfg.DefaultModel = "demo"
	cfg.Models = map[string]ModelDefinition{"demo": {Provider: "custom", Model: "demo-model"}}
	cfg.Providers = map[string]ProviderConfig{"custom": {BaseURL: "https://example.com/v1"}}

	diagnostics := ValidateConfig(cfg, "config.toml", ValidationOptions{RequireAllCredentials: true, RequireDefaultModel: true})
	if hasDiagnostic(diagnostics, "providers.custom.api_key", "missing_credential") {
		t.Fatalf("alias-specific credential should satisfy validation: %#v", diagnostics)
	}
}

func TestCurrentInheritedCacheSchemaPassesValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `default_model = "cached"

[model_defaults]
cache_enabled = true
cache_key_name = "cache_control"
cache_control = { type = "ephemeral" }
cache_trigger_threshold = 4096
cache_lookback_offset = 1

[providers.fake]
base_url = "https://example.com/v1"

[models.cached]
provider = "fake"
model = "cached-model"

[models.disabled]
provider = "fake"
model = "disabled-model"
cache_enabled = false
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("current cache schema failed structural validation: %v", err)
	}
	if err := ValidateConfigError(cfg, path, ValidationOptions{}); err != nil {
		t.Fatalf("current cache schema failed semantic validation: %v", err)
	}
}

func TestIncompleteInheritedCacheFailsSemanticValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `default_model = "demo"

[model_defaults]
cache_enabled = true
cache_trigger_threshold = 4096

[providers.fake]
base_url = "https://example.com/v1"

[models.demo]
provider = "fake"
model = "demo-model"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := ValidateConfig(cfg, path, ValidationOptions{})
	if !hasDiagnostic(diagnostics, "models.demo", "invalid_cache") {
		t.Fatalf("expected inherited cache diagnostic, got %#v", diagnostics)
	}
}

func TestValidationDiagnosticsDoNotExposeCredentials(t *testing.T) {
	secret := "super-secret-value"
	cfg := DefaultConfig()
	cfg.DefaultModel = "demo"
	cfg.Models = map[string]ModelDefinition{"demo": {Provider: "fake", Model: "demo-model"}}
	cfg.Providers = map[string]ProviderConfig{
		"fake": {BaseURL: "not-a-url", APIKey: secret},
	}
	err := ValidateConfigError(cfg, "config.toml", ValidationOptions{RequireAllCredentials: true})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("diagnostic exposed credential: %v", err)
	}
}

func TestValidationAcceptsAbsoluteProviderEndpoint(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel = "demo"
	cfg.Models = map[string]ModelDefinition{"demo": {Provider: "fake", Model: "demo-model"}}
	cfg.Providers = map[string]ProviderConfig{
		"fake": {BaseURL: "https://example.com/v1", Endpoint: "https://gateway.example.com/chat"},
	}
	if err := ValidateConfigError(cfg, "config.toml", ValidationOptions{}); err != nil {
		t.Fatalf("runtime-supported absolute endpoint failed validation: %v", err)
	}
}

func TestExplicitZeroLimitsSurviveMergeAndFailSemanticValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `[shell-agent]
max_steps = 0
finalize_remaining_steps = 0

[environment]
type = "local"
command_timeout = 0
max_command_output_bytes = 0
cwd = "."
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHTIANI_CONFIG", path)
	ResetConfigForTesting()
	t.Cleanup(ResetConfigForTesting)

	cfg, loadedPath, err := LoadGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := ValidateConfig(cfg, loadedPath, ValidationOptions{})
	if !hasDiagnostic(diagnostics, "shell-agent.max_steps", "invalid_value") {
		t.Fatalf("expected explicit max_steps=0 to fail, got %#v", diagnostics)
	}
	if !hasDiagnostic(diagnostics, "environment.max_command_output_bytes", "invalid_value") {
		t.Fatalf("expected explicit max_command_output_bytes=0 to fail, got %#v", diagnostics)
	}
}

func TestDocumentedConfigFixturesPassStructuralAndSemanticValidation(t *testing.T) {
	for _, name := range []string{"config.minimal.toml", "config.comprehensive.toml"} {
		t.Run(name, func(t *testing.T) {
			path, err := filepath.Abs(filepath.Join("..", "..", "..", "docs", "examples", name))
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("MACHTIANI_CONFIG", path)
			ResetConfigForTesting()
			t.Cleanup(ResetConfigForTesting)

			cfg, loadedPath, err := LoadGlobalConfig()
			if err != nil {
				t.Fatalf("fixture failed structural validation: %v", err)
			}
			if err := ValidateConfigError(cfg, loadedPath, ValidationOptions{}); err != nil {
				t.Fatalf("fixture failed semantic validation: %v", err)
			}
		})
	}
}

func hasDiagnostic(diagnostics []Diagnostic, path, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Path == path && diagnostic.Code == code {
			return true
		}
	}
	return false
}
