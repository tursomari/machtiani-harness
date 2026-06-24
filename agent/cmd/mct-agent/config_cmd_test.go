package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestHandleConfigProviderCommands(t *testing.T) {
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

	// 1. Initialize a valid config.
	t.Run("init", func(t *testing.T) {
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
	})

	// 2. Update provider URL.
	t.Run("url", func(t *testing.T) {
		code := handleConfigProviderCommand([]string{"url", "https://new-url.com"})
		if code != 0 {
			t.Fatalf("expected return code 0, got %d", code)
		}

		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("reading config.toml: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, `base_url = "https://new-url.com"`) {
			t.Fatalf("config.toml content missing new base_url:\n%s", content)
		}
	})

	// 3. Update API key.
	t.Run("api-key", func(t *testing.T) {
		code := handleConfigProviderCommand([]string{"api-key", "new-key"})
		if code != 0 {
			t.Fatalf("expected return code 0, got %d", code)
		}

		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("reading config.toml: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, `api_key = "new-key"`) {
			t.Fatalf("config.toml content missing new api_key:\n%s", content)
		}
	})

	// 4. Update model.
	t.Run("model", func(t *testing.T) {
		code := handleConfigProviderCommand([]string{"model", "gpt-5"})
		if code != 0 {
			t.Fatalf("expected return code 0, got %d", code)
		}

		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("reading config.toml: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, `model = "gpt-5"`) {
			t.Fatalf("config.toml content missing new model:\n%s", content)
		}
	})

	// 5. Update reasoning effort.
	t.Run("reasoning", func(t *testing.T) {
		code := handleConfigProviderCommand([]string{"reasoning", "low"})
		if code != 0 {
			t.Fatalf("expected return code 0, got %d", code)
		}

		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("reading config.toml: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, `effort = "low"`) {
			t.Fatalf("config.toml content missing new reasoning effort:\n%s", content)
		}
	})

	// 6. Error when config does not exist in a different directory.
	t.Run("error no config", func(t *testing.T) {
		emptyDir := t.TempDir()
		if err := os.Chdir(emptyDir); err != nil {
			t.Fatalf("chdir to empty dir: %v", err)
		}
		defer func() {
			if err := os.Chdir(tmpDir); err != nil {
				t.Fatalf("chdir back to tmpDir: %v", err)
			}
		}()

		llm.ResetConfigForTesting()
		code := handleConfigProviderCommand([]string{"url", "x"})
		if code == 0 {
			t.Fatalf("expected non-zero return code when config does not exist, got %d", code)
		}
	})
}
