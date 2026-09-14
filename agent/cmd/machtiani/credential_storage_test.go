package main

import (
	"github.com/tursomari/machtiani/agent/internal/llm"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeKeySavedSeparatelyAndResolved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path := filepath.Join(home, ".config", "machtiani", "config.toml")
	t.Setenv("MACHTIANI_CONFIG", path)
	code := handleConfigAddCommand([]string{"--provider", "fixture", "--url", "https://example.test", "--api-key", "private-fixture-value", "--model", "fixture", "--alias", "fixture", "--no-interactive"})
	if code != 0 {
		t.Fatal(code)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "private-fixture-value") {
		t.Fatal("key stored in config")
	}
	creds := filepath.Join(filepath.Dir(path), "credentials.env")
	info, err := os.Stat(creds)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential permissions")
	}
	llm.ResetConfigForTesting()
	model, err := llm.ResolveModel("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if model.APIKey != "private-fixture-value" {
		t.Fatal("stored key not resolved")
	}
}

func TestInitHonorsSelectedConfigurationWithoutCreatingPersonalConfig(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	home := os.Getenv("HOME")
	selected := filepath.Join(home, ".config/dearmachine/machtiani/config.toml")
	t.Setenv("MACHTIANI_CONFIG", selected)
	if code := handleInitCommand([]string{"--no-interactive", "--provider", "fixture", "--url", "https://example.test", "--api-key", "private-fixture", "--model", "fixture", "--alias", "fixture"}); code != 0 {
		t.Fatal(code)
	}
	if _, err := os.Stat(selected); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config/machtiani/config.toml")); !os.IsNotExist(err) {
		t.Fatal("explicit initialization touched personal configuration")
	}
	before, _ := os.ReadFile(selected)
	if code := handleInitCommand([]string{"--no-interactive"}); code != 0 {
		t.Fatal(code)
	}
	after, _ := os.ReadFile(selected)
	if string(before) != string(after) {
		t.Fatal("repeated initialization changed configuration")
	}
}
