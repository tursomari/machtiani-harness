package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedConfigLoadsPrivateCredentials(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	for _, name := range []string{"personal", "dearmachine"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			mustWriteFile(t, path, `credentials_file = "credentials.env"
default_model = "deepseek"
[providers.deepseek]
base_url = "https://example.test"
api_key_ref = "DEEPSEEK_API_KEY"
[models.deepseek]
provider = "deepseek"
model = "fixture"
`)
			key := name + "-fixture-key"
			file := filepath.Join(dir, "credentials.env")
			if err := os.WriteFile(file, []byte("DEEPSEEK_API_KEY="+key+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MACHTIANI_CONFIG", path)
			ResetConfigForTesting()
			model, err := ResolveModel("deepseek")
			if err != nil {
				t.Fatal(err)
			}
			if model.APIKey != key {
				t.Fatal("wrong selected credential")
			}
			t.Setenv("DEEPSEEK_API_KEY", "explicit-fixture")
			model, err = ResolveModel("deepseek")
			if err != nil || model.APIKey != key {
				t.Fatal("inherited environment overrode selected credential file")
			}
			t.Setenv("DEEPSEEK_API_KEY", "")
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			_, err = ResolveModel("deepseek")
			if err == nil || strings.Contains(err.Error(), key) {
				t.Fatal("missing credential must fail without exposing key")
			}
		})
	}
}

func TestEnvironmentProviderRemainsIndependentOfFileProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	mustWriteFile(t, path, `credentials_file = "credentials.env"
default_model = "environment"
[providers.environment]
base_url = "https://example.test"
api_key = "${ENV_ONLY_KEY}"
[models.environment]
provider = "environment"
model = "fixture"
`)
	t.Setenv("MACHTIANI_CONFIG", path)
	t.Setenv("ENV_ONLY_KEY", "environment-fixture")
	ResetConfigForTesting()
	resolved, err := ResolveModel("environment")
	if err != nil || resolved.APIKey != "environment-fixture" {
		t.Fatal("environment-only provider unexpectedly requires a credential file", err)
	}
}
