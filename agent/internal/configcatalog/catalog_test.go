package configcatalog

import (
	"strings"
	"testing"
)

func TestEmbeddedCatalogIsValid(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(catalog.Providers) < 3 {
		t.Fatalf("provider count = %d, want at least 3", len(catalog.Providers))
	}
	deepseek, ok := catalog.Provider("deepseek")
	if !ok {
		t.Fatal("deepseek provider missing")
	}
	if deepseek.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("deepseek API key env = %q", deepseek.APIKeyEnv)
	}
	if _, ok := deepseek.Default(); !ok {
		t.Fatal("deepseek default model missing")
	}
	openai, ok := catalog.Provider("openai")
	if !ok {
		t.Fatal("openai provider missing")
	}
	if openai.DefaultModel != "gpt-5.6-sol" {
		t.Fatalf("openai default model = %q, want gpt-5.6-sol", openai.DefaultModel)
	}
	wantOpenAIModels := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"}
	if len(openai.Models) != len(wantOpenAIModels) {
		t.Fatalf("openai model count = %d, want %d", len(openai.Models), len(wantOpenAIModels))
	}
	for index, want := range wantOpenAIModels {
		if got := openai.Models[index].ID; got != want {
			t.Fatalf("openai model priority %d = %q, want %q", index+1, got, want)
		}
	}
	openrouter, ok := catalog.Provider("openrouter")
	if !ok || openrouter.ModelsURL != "https://openrouter.ai/api/v1/models" || openrouter.ModelsSearchParam != "q" {
		t.Fatalf("openrouter model discovery = %#v", openrouter)
	}
}

func TestValidateRejectsInsecureModelsURL(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	catalog.Providers[0].ModelsURL = "http://example.com/models"
	if err := Validate(catalog); err == nil || !strings.Contains(err.Error(), "models_url") {
		t.Fatalf("Validate() error = %v, want models_url error", err)
	}
}

func TestValidateRejectsDuplicateProviders(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	catalog.Providers = append(catalog.Providers, catalog.Providers[0])
	if err := Validate(catalog); err == nil || !strings.Contains(err.Error(), "duplicate provider") {
		t.Fatalf("Validate() error = %v, want duplicate provider error", err)
	}
}

func TestValidateRejectsMissingDefaultModel(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	catalog.Providers[0].DefaultModel = "missing"
	if err := Validate(catalog); err == nil || !strings.Contains(err.Error(), "default_model") {
		t.Fatalf("Validate() error = %v, want default_model error", err)
	}
}
