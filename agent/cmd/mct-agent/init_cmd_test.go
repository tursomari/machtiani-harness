package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestHandleInitCommand(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("chdir back: %v", err)
		}
	}()
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)

	// 1. No flags provided - pflag parses successfully but required flags
	//    are missing, so expects return code 1.
	t.Run("no flags", func(t *testing.T) {
		code := handleInitCommand([]string{})
		if code != 1 {
			t.Fatalf("expected return code 1, got %d", code)
		}
	})

	// 2. Required flags missing - expects return code 1.
	t.Run("missing required flags", func(t *testing.T) {
		code := handleInitCommand([]string{
			"--provider-url", "https://example.com",
		})
		if code != 1 {
			t.Fatalf("expected return code 1, got %d", code)
		}
	})

	// 3. Successful init - verifies return code 0, config file exists, and
	//    file content contains expected values.
	t.Run("successful init", func(t *testing.T) {
		code := handleInitCommand([]string{
			"--provider-url", "https://api.example.com",
			"--api-key", "sk-test123",
			"--model", "gpt-4o",
			"--reasoning", "high",
		})
		if code != 0 {
			t.Fatalf("expected return code 0, got %d", code)
		}

		configPath := filepath.Join(".machtiani", "config.toml")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			t.Fatalf("expected config.toml to exist at %s", configPath)
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("reading config.toml: %v", err)
		}
		content := string(data)

		checks := []string{
			"https://api.example.com",
			"sk-test123",
			"gpt-4o",
			`effort = "high"`,
			`default_model = "default"`,
		}
		for _, check := range checks {
			if !strings.Contains(content, check) {
				t.Fatalf("config.toml content missing %q:\n%s", check, content)
			}
		}
	})

	// 4. --force flag: config already exists from previous subtest; init
	//    without --force should fail (code 1), then with --force should
	//    succeed (code 0).
	t.Run("force flag", func(t *testing.T) {
		args := []string{
			"--provider-url", "https://api.example.com",
			"--api-key", "sk-test123",
			"--model", "gpt-4o",
		}

		code := handleInitCommand(args)
		if code != 1 {
			t.Fatalf("expected return code 1 for existing config without --force, got %d", code)
		}

		code = handleInitCommand(append(args, "--force"))
		if code != 0 {
			t.Fatalf("expected return code 0 with --force, got %d", code)
		}
	})

	// 5. Custom alias - verifies config contains the alias.
	t.Run("custom alias", func(t *testing.T) {
		code := handleInitCommand([]string{
			"--provider-url", "https://api.example.com",
			"--api-key", "sk-test123",
			"--model", "gpt-4o",
			"--alias", "my-model",
			"--force",
		})
		if code != 0 {
			t.Fatalf("expected return code 0, got %d", code)
		}

		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("reading config.toml: %v", err)
		}
		content := string(data)

		checks := []string{
			`default_model = "my-model"`,
			"my-model",
		}
		for _, check := range checks {
			if !strings.Contains(content, check) {
				t.Fatalf("config.toml content missing %q:\n%s", check, content)
			}
		}
	})

	// 6. parses with LoadGlobalConfig - verifies that the generated config can
	//    be parsed by LoadGlobalConfig and contains expected values.
	t.Run("parses with LoadGlobalConfig", func(t *testing.T) {
		llm.ResetConfigForTesting()

		code := handleInitCommand([]string{
			"--provider-url", "https://api.example.com",
			"--api-key", "sk-test123",
			"--model", "gpt-4o",
			"--force",
		})
		if code != 0 {
			t.Fatalf("expected return code 0, got %d", code)
		}

		if err := os.Chdir(".machtiani"); err != nil {
			t.Fatalf("chdir .machtiani: %v", err)
		}
		defer func() {
			if err := os.Chdir(tmpDir); err != nil {
				t.Fatalf("chdir back to tmpDir: %v", err)
			}
		}()

		cfg, _, err := llm.LoadGlobalConfig()
		if err != nil {
			t.Fatalf("LoadGlobalConfig: %v", err)
		}

		if cfg.DefaultModel != "default" {
			t.Fatalf("expected DefaultModel 'default', got %q", cfg.DefaultModel)
		}

		if cfg.Providers == nil {
			t.Fatal("expected Providers to be non-nil")
		}
		prov, ok := cfg.Providers["default"]
		if !ok {
			t.Fatal("expected Providers to have key 'default'")
		}
		if prov.BaseURL != "https://api.example.com" {
			t.Fatalf("expected Providers['default'].BaseURL 'https://api.example.com', got %q", prov.BaseURL)
		}

		if cfg.Models == nil {
			t.Fatal("expected Models to be non-nil")
		}
		modelDef, ok := cfg.Models["default"]
		if !ok {
			t.Fatal("expected Models to have key 'default'")
		}
		if modelDef.Model != "gpt-4o" {
			t.Fatalf("expected Models['default'].Model 'gpt-4o', got %q", modelDef.Model)
		}

		llm.ResetConfigForTesting()
	})
}
