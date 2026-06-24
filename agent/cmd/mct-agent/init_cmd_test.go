package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
}
