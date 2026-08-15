package models

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func TestLLMAdapterQueryStub(t *testing.T) {
	t.Setenv("MACHTIANI_LLM_TEST_STUB", "adapter-test")

	cfg := &minisweagent.ModelConfig{
		ModelName: "anthropic/claude-haiku-4.5",
		APIKey:    "test-key",
		ModelKwargs: map[string]interface{}{
			"base_url":    "https://example.com",
			"temperature": 0.0,
		},
	}

	model, err := NewLLMAdapterModel(cfg, nil)
	if err != nil {
		t.Fatalf("NewLLMAdapterModel error: %v", err)
	}

	messages := []minisweagent.Message{{Role: "user", Content: "Hello"}}
	res, err := model.Query(context.Background(), messages)
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}

	if !strings.Contains(res.Content, "Stub LLM") {
		t.Fatalf("unexpected content: %q", res.Content)
	}

	if model.NCalls() != 1 {
		t.Fatalf("NCalls() = %d, want 1", model.NCalls())
	}

	resolved, ok := res.Extra["resolved_model"].(map[string]any)
	if !ok {
		t.Fatalf("resolved_model extra missing: %+v", res.Extra)
	}
	if resolved["base_url"] != "https://example.com" {
		t.Fatalf("base_url = %v, want https://example.com", resolved["base_url"])
	}
}

func TestLLMAdapterQueryAuthError(t *testing.T) {
	original := chatWithResolvedFallback
	t.Cleanup(func() { chatWithResolvedFallback = original })

	chatWithResolvedFallback = func(context.Context, llm.ResolvedModel, []string, []llm.ResolvedModel, map[string]any, []llm.Message) (string, error) {
		return "", &llm.HTTPResponseError{Status: http.StatusUnauthorized}
	}

	cfg := &minisweagent.ModelConfig{
		ModelName: "anthropic/claude-haiku-4.5",
		APIKey:    "test-key",
		ModelKwargs: map[string]interface{}{
			"base_url": "https://example.com",
		},
	}

	model, err := NewLLMAdapterModel(cfg, nil)
	if err != nil {
		t.Fatalf("NewLLMAdapterModel error: %v", err)
	}

	_, err = model.Query(context.Background(), nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %T", err)
	}
	if model.NCalls() != 1 {
		t.Fatalf("NCalls() = %d, want 1", model.NCalls())
	}
}

func TestBuildResolvedModelParsesFallbacks(t *testing.T) {
	cfg := &minisweagent.ModelConfig{
		ModelName: "direct-alias",
		APIKey:    "direct-key",
		ModelKwargs: map[string]interface{}{
			"base_url":         "https://primary.example.com",
			"fallback_aliases": []interface{}{"secondary", " tertiary "},
			"fallback_models": []interface{}{
				map[string]interface{}{
					"alias":    "direct-fallback",
					"base_url": "https://fallback.example.com",
					"model":    "fallback-model",
				},
			},
			"temperature": 0.2,
		},
	}

	resolved, aliases, models, extras, err := buildResolvedModel(cfg, nil)
	if err != nil {
		t.Fatalf("buildResolvedModel error: %v", err)
	}
	if resolved.BaseURL != "https://primary.example.com" {
		t.Fatalf("resolved.BaseURL = %q, want https://primary.example.com", resolved.BaseURL)
	}
	if len(aliases) != 2 || aliases[0] != "secondary" || aliases[1] != "tertiary" {
		t.Fatalf("unexpected aliases: %#v", aliases)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 fallback model, got %d", len(models))
	}
	if models[0].BaseURL != "https://fallback.example.com" {
		t.Fatalf("fallback base_url = %q, want https://fallback.example.com", models[0].BaseURL)
	}
	if len(extras) != 1 || extras["temperature"].(float64) != 0.2 {
		t.Fatalf("unexpected extras: %#v", extras)
	}
}

func TestBuildResolvedModelFallsBackToDefaultAlias(t *testing.T) {
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	config := []byte(`default_model = "shell-default"

[providers.shell-default]
base_url = "https://api.shell.example"
api_key = "provider-key"

[models.shell-default]
provider = "shell-default"
model = "gpt-shell"
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)

	cfg := &minisweagent.ModelConfig{
		ModelKwargs: map[string]any{
			"headers":     map[string]any{"X-Test": "123"},
			"query":       map[string]any{"mode": "fast"},
			"temperature": 0.4,
		},
		APIKey: " override-key ",
	}

	resolved, aliases, models, extras, err := buildResolvedModel(cfg, nil)
	if err != nil {
		t.Fatalf("buildResolvedModel error: %v", err)
	}
	if resolved.Alias != "shell-default" {
		t.Fatalf("resolved alias = %q, want shell-default", resolved.Alias)
	}
	if cfg.ModelName != "shell-default" {
		t.Fatalf("cfg.ModelName = %q, want shell-default", cfg.ModelName)
	}
	if resolved.APIKey != "override-key" {
		t.Fatalf("resolved APIKey = %q, want override-key", resolved.APIKey)
	}
	if len(aliases) != 0 {
		t.Fatalf("fallback aliases = %#v, want empty", aliases)
	}
	if len(models) != 0 {
		t.Fatalf("fallback models = %#v, want empty", models)
	}
	if resolved.Headers == nil || resolved.Headers["X-Test"] != "123" {
		t.Fatalf("resolved header X-Test = %q, want 123", resolved.Headers["X-Test"])
	}
	if resolved.Query == nil || resolved.Query["mode"] != "fast" {
		t.Fatalf("resolved query mode = %q, want fast", resolved.Query["mode"])
	}
	if extras == nil {
		t.Fatalf("extras is nil")
	}
	if temp, ok := extras["temperature"].(float64); !ok || temp != 0.4 {
		t.Fatalf("extra temperature = %#v, want 0.4", extras["temperature"])
	}
}

func TestBuildResolvedModel_OverridesWinOverCfgAPIKey(t *testing.T) {
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	// Define two providers and an alias mapped to openai.
	config := []byte(`default_model = "gpt-5"

[providers.openai]
base_url = "https://api.openai.com/v1"
api_key = "cfg-openai-key"

[providers.openrouter]
base_url = "https://openrouter.ai/api/v1"
api_key = "cfg-openrouter-key"

[models.gpt-5]
provider = "openai"
model = "gpt-5"
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)

	// Simulate an env-derived cfg.APIKey from the wrong provider (e.g., an OpenRouter key)
	cfg := &minisweagent.ModelConfig{
		ModelName: "gpt-5",
		APIKey:    "sk-or-v1-should-not-win",
	}

	overrides := map[string]string{
		"openai":     "sk-openai-correct",
		"openrouter": "sk-openrouter-wrong",
	}

	resolved, _, _, _, err := buildResolvedModel(cfg, overrides)
	if err != nil {
		t.Fatalf("buildResolvedModel error: %v", err)
	}
	if got := strings.TrimSpace(resolved.ProviderName); got != "openai" {
		t.Fatalf("provider = %q, want openai", got)
	}
	if got := strings.TrimSpace(resolved.BaseURL); got != "https://api.openai.com/v1" {
		t.Fatalf("base_url = %q, want https://api.openai.com/v1", got)
	}
	if got := strings.TrimSpace(resolved.APIKey); got != "sk-openai-correct" {
		t.Fatalf("api_key = %q, want sk-openai-correct (CLI override should win over cfg.APIKey)", got)
	}
}
