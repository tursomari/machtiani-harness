package main

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/configcatalog"
)

func TestDiscoverProviderModelsFiltersAndAuthenticates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "deepseek" {
			t.Errorf("q = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[
			{"id":"openai/gpt-5","name":"GPT 5","description":"OpenAI model"},
			{"id":"deepseek/deepseek-v4-pro","name":"DeepSeek V4 Pro","description":"Reasoning model"},
			{"id":"deepseek/deepseek-v4-flash","name":"DeepSeek V4 Flash","description":"Fast model"},
			{"id":"deepseek/deepseek-v4-flash","name":"Duplicate","description":"Ignored"}
		]}`)
	}))
	defer server.Close()

	models, err := discoverProviderModels(server.Client(), configcatalog.Provider{Name: "Test", ModelsURL: server.URL, ModelsSearchParam: "q"}, "test-secret", "deepseek")
	if err != nil {
		t.Fatalf("discoverProviderModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2: %#v", len(models), models)
	}
	if models[0].ID != "deepseek/deepseek-v4-pro" || models[1].ID != "deepseek/deepseek-v4-flash" {
		t.Fatalf("models = %#v", models)
	}
}

func TestDiscoverProviderModelsRejectsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "do not expose this response", http.StatusUnauthorized)
	}))
	defer server.Close()
	_, err := discoverProviderModels(server.Client(), configcatalog.Provider{Name: "Test", ModelsURL: server.URL}, "bad-secret", "")
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") || strings.Contains(err.Error(), "do not expose") {
		t.Fatalf("error = %v", err)
	}
}

func TestSuggestedModelAlias(t *testing.T) {
	models := map[string]any{"deepseek-v4-pro": map[string]any{}}
	if got := uniqueModelAliasSuggestion("deepseek/deepseek-v4-pro", models); got != "deepseek-v4-pro-2" {
		t.Fatalf("alias = %q", got)
	}
	if got := suggestedModelAlias("vendor/Model:Free"); got != "model-free" {
		t.Fatalf("alias = %q", got)
	}
}

func TestConfiguredProviderRetainsCatalogDiscovery(t *testing.T) {
	catalog, err := configcatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		entry map[string]any
	}{
		{name: "openrouter", entry: map[string]any{"base_url": "https://proxy.example/v1"}},
		{name: "renamed-router", entry: map[string]any{"base_url": "https://openrouter.ai/api/v1/"}},
	} {
		provider, ok := catalogProviderForConfigured(test.name, test.entry, catalog)
		if !ok || provider.ID != "openrouter" || provider.ModelsURL == "" {
			t.Fatalf("catalogProviderForConfigured(%q) = %#v, %v", test.name, provider, ok)
		}
	}
	if _, ok := catalogProviderForConfigured("custom", map[string]any{"base_url": "https://example.com/v1"}, catalog); ok {
		t.Fatal("custom provider unexpectedly matched the catalog")
	}
}

func TestConfiguredProviderCredential(t *testing.T) {
	t.Setenv("ROUTER_TEST_KEY", "environment-secret")
	if got := configuredProviderCredential(map[string]any{"api_key": "${ROUTER_TEST_KEY}"}); got != "environment-secret" {
		t.Fatalf("environment credential = %q", got)
	}
	if got := configuredProviderCredential(map[string]any{"api_key": "literal-secret"}); got != "literal-secret" {
		t.Fatalf("literal credential = %q", got)
	}
}

func TestSearchableCatalogDefaultsModelMenuToSearch(t *testing.T) {
	catalog, err := configcatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	openrouter, ok := catalog.Provider("openrouter")
	if !ok {
		t.Fatal("openrouter missing from catalogue")
	}
	options := catalogModelMenuOptions(openrouter)
	if len(options) == 0 || options[0].value != "search" {
		t.Fatalf("first OpenRouter model option = %#v, want catalogue search", options)
	}
	if options[1].value != openrouter.DefaultModel {
		t.Fatalf("second OpenRouter model option = %q, want flagship %q", options[1].value, openrouter.DefaultModel)
	}
}

func TestPromptModelAliasExplainsAndAcceptsSuggestedAlias(t *testing.T) {
	var got string
	var promptErr error
	stdout, stderr := captureOutput(func() {
		got, promptErr = promptModelAlias(bufio.NewReader(strings.NewReader("\n")), "openrouter")
	})
	if promptErr != nil {
		t.Fatalf("promptModelAlias: %v", promptErr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if got != "openrouter" {
		t.Fatalf("alias = %q, want suggested alias", got)
	}
	for _, want := range []string{
		"Model alias / shortened model name",
		"Press Enter to use the suggested alias",
		"Alias [openrouter]:",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("prompt missing %q: %q", want, stdout)
		}
	}
}
