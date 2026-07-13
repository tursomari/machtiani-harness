package main

import (
	"bytes"
	"io"
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
		deps, _, _ := testInitDeps("", "", false)
		code := handleInitCommandWithDeps([]string{}, deps)
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
			`cache_enabled = true`,
			`cache_key_name = "cache_control"`,
			`cache_trigger_threshold = 4096`,
		}
		for _, check := range checks {
			if !strings.Contains(content, check) {
				t.Fatalf("config.toml content missing %q:\n%s", check, content)
			}
		}
		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("stat config.toml: %v", err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Fatalf("expected config permissions 0600, got %04o", got)
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

func TestHandleInitCommandInteractiveWizard(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, stdout, stderr := testInitDeps("https://api.example.com/v1\ngpt-5\n\n\n\n", "sk-hidden", true)
		code := handleInitCommandWithDeps(nil, deps)
		if code != 0 {
			t.Fatalf("expected return code 0, got %d; stderr=%s", code, stderr.String())
		}
		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("read generated config: %v", err)
		}
		content := string(data)
		for _, expected := range []string{
			`base_url = "https://api.example.com/v1"`,
			`api_key = "sk-hidden"`,
			`model = "gpt-5"`,
			`effort = "medium"`,
			`default_model = "default"`,
			`cache_enabled = true`,
			`cache_control`,
		} {
			if !strings.Contains(content, expected) {
				t.Fatalf("config missing %q:\n%s", expected, content)
			}
		}
		if strings.Contains(stdout.String(), "sk-hidden") {
			t.Fatalf("wizard echoed API key: %s", stdout.String())
		}
		if !strings.Contains(stdout.String(), "Enable caching? [Y/n]") {
			t.Fatalf("wizard did not show caching prompt: %s", stdout.String())
		}
	})
}

func TestHandleInitCommandInteractiveDeclinesCache(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, _, stderr := testInitDeps("https://api.example.com/v1\ngpt-5\nhigh\nwork\nmaybe\nNo\n", "sk-hidden", true)
		code := handleInitCommandWithDeps(nil, deps)
		if code != 0 {
			t.Fatalf("expected return code 0, got %d; stderr=%s", code, stderr.String())
		}
		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("read generated config: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, `cache_enabled = false`) {
			t.Fatalf("expected caching to be disabled:\n%s", content)
		}
		if strings.Contains(content, "cache_key_name") {
			t.Fatalf("disabled defaults should omit cache payload:\n%s", content)
		}
	})
}

func TestHandleInitCommandNoCacheFlag(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, stdout, stderr := testInitDeps("https://api.example.com/v1\ngpt-5\n\n\n", "sk-hidden", true)
		code := handleInitCommandWithDeps([]string{"--no-cache"}, deps)
		if code != 0 {
			t.Fatalf("expected return code 0, got %d; stderr=%s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "disabled by --no-cache") {
			t.Fatalf("expected no-cache notice: %s", stdout.String())
		}
		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("read generated config: %v", err)
		}
		if !strings.Contains(string(data), `cache_enabled = false`) {
			t.Fatalf("expected caching disabled:\n%s", data)
		}
	})
}

func TestHandleInitCommandInteractiveEOFLeavesNoConfig(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, _, _ := testInitDeps("https://api.example.com/v1\n", "sk-hidden", true)
		code := handleInitCommandWithDeps(nil, deps)
		if code != 1 {
			t.Fatalf("expected return code 1, got %d", code)
		}
		if _, err := os.Stat(filepath.Join(".machtiani", "config.toml")); !os.IsNotExist(err) {
			t.Fatalf("expected no config after EOF, got %v", err)
		}
	})
}

func TestHandleInitCommandFlaggedNoCache(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, _, stderr := testInitDeps("", "", false)
		code := handleInitCommandWithDeps([]string{
			"--provider-url", "https://api.example.com/v1",
			"--api-key", "sk-test",
			"--model", "gpt-5",
			"--no-cache",
		}, deps)
		if code != 0 {
			t.Fatalf("expected return code 0, got %d; stderr=%s", code, stderr.String())
		}
		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("read generated config: %v", err)
		}
		if !strings.Contains(string(data), `cache_enabled = false`) || strings.Contains(string(data), "cache_key_name") {
			t.Fatalf("unexpected no-cache config:\n%s", data)
		}
	})
}

func testInitDeps(input, password string, terminal bool) (initCommandDeps, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return initCommandDeps{
		in:         strings.NewReader(input),
		out:        stdout,
		errOut:     stderr,
		stdinFD:    0,
		isTerminal: func(int) bool { return terminal },
		readPassword: func(int) ([]byte, error) {
			if password == "" {
				return nil, io.EOF
			}
			return []byte(password), nil
		},
	}, stdout, stderr
}

func withInitWorkingDir(t *testing.T, fn func()) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}()
	llm.ResetConfigForTesting()
	defer llm.ResetConfigForTesting()
	fn()
}
