package llm

import (
	"os"
	"path/filepath"
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
