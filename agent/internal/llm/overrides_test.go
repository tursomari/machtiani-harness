package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAPIKeyOverrides(t *testing.T) {
	overrides, err := ParseAPIKeyOverrides([]string{"OpenAI: key-one ", "anthropic:key-two"})
	if err != nil {
		t.Fatalf("ParseAPIKeyOverrides returned error: %v", err)
	}
	if len(overrides) != 2 {
		t.Fatalf("expected 2 overrides, got %d", len(overrides))
	}
	if overrides["openai"] != "key-one" {
		t.Fatalf("expected override for openai to be 'key-one', got %q", overrides["openai"])
	}
	if overrides["anthropic"] != "key-two" {
		t.Fatalf("expected override for anthropic to be 'key-two', got %q", overrides["anthropic"])
	}

	if _, err := ParseAPIKeyOverrides([]string{"invalid"}); err == nil {
		t.Fatalf("expected error for missing separator")
	}
}

func TestResolveModelWithOverridesPrefersOverridesThenConfig(t *testing.T) {
	t.Setenv("MACHTIANI_CONFIG", "")
	ResetConfigForTesting()
	t.Cleanup(ResetConfigForTesting)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	config := `default_model = "orch"

[providers.openai]
base_url = "https://example.com"
api_key = "config-key"

[models.orch]
provider = "openai"
model = "gpt-4"
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)

	overrides := map[string]string{"openai": "override-key"}
	resolved, err := ResolveModelWithOverrides("orch", overrides)
	if err != nil {
		t.Fatalf("ResolveModelWithOverrides error: %v", err)
	}
	if resolved.APIKey != "override-key" {
		t.Fatalf("expected override-key, got %q", resolved.APIKey)
	}
}

func TestResolveModelWithOverridesFallsBackToEnv(t *testing.T) {
	t.Setenv("MACHTIANI_CONFIG", "")
	ResetConfigForTesting()
	t.Cleanup(ResetConfigForTesting)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	config := `default_model = "orch"

[providers.openai]
base_url = "https://example.com"
api_key = ""

[models.orch]
provider = "openai"
model = "gpt-4"
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)
	t.Setenv("OPENAI_API_KEY", "env-key")

	resolved, err := ResolveModelWithOverrides("orch", nil)
	if err != nil {
		t.Fatalf("ResolveModelWithOverrides error: %v", err)
	}
	if resolved.APIKey != "env-key" {
		t.Fatalf("expected env-key, got %q", resolved.APIKey)
	}
}

func TestResolveModelWithProviderMismatchReturnsError(t *testing.T) {
	t.Setenv("MACHTIANI_CONFIG", "")
	ResetConfigForTesting()
	t.Cleanup(ResetConfigForTesting)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	config := `default_model = "orch"

[providers.openai]
base_url = "https://example.com"
api_key = "config-key"

[models.orch]
provider = "openai"
model = "gpt-4"
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)

	// Try to provide an API key for a different provider
	overrides := map[string]string{"openrouter": "override-key"}
	_, err := ResolveModelWithOverrides("orch", overrides)
	if err == nil {
		t.Fatalf("expected error for provider mismatch, but got none")
	}
	if !strings.Contains(err.Error(), "API key override provider") {
		t.Fatalf("expected error about provider mismatch, got: %v", err)
	}
	if !strings.Contains(err.Error(), "openrouter") {
		t.Fatalf("expected error to mention 'openrouter', got: %v", err)
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Fatalf("expected error to mention 'openai', got: %v", err)
	}
}

func TestValidateAPIKeyOverrideProviderValidatesMatch(t *testing.T) {
	tests := []struct {
		name              string
		overrides         map[string]string
		configuredProvider string
		shouldFail        bool
		errorContains     string
	}{
		{
			name:               "matching provider passes",
			overrides:          map[string]string{"openai": "key"},
			configuredProvider: "openai",
			shouldFail:         false,
		},
		{
			name:               "case insensitive matching",
			overrides:          map[string]string{"openai": "key"},
			configuredProvider: "OpenAI",
			shouldFail:         false,
		},
		{
			name:               "mismatched provider fails",
			overrides:          map[string]string{"openrouter": "key"},
			configuredProvider: "openai",
			shouldFail:         true,
			errorContains:      "openrouter",
		},
		{
			name:               "no overrides passes",
			overrides:          map[string]string{},
			configuredProvider: "openai",
			shouldFail:         false,
		},
		{
			name:               "nil overrides passes",
			overrides:          nil,
			configuredProvider: "openai",
			shouldFail:         false,
		},
		{
			name:               "multiple mismatched providers fail",
			overrides:          map[string]string{"anthropic": "key1", "openrouter": "key2"},
			configuredProvider: "openai",
			shouldFail:         true,
			errorContains:      "does not match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAPIKeyOverrideProvider(tt.overrides, "test-model", tt.configuredProvider)
			if tt.shouldFail && err == nil {
				t.Fatalf("expected error but got none")
			}
			if !tt.shouldFail && err != nil {
				t.Fatalf("expected no error but got: %v", err)
			}
			if tt.shouldFail && tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
				t.Fatalf("expected error to contain %q but got: %v", tt.errorContains, err)
			}
		})
	}
}
