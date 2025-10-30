package llm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocateConfigPrefersLocalWithinGitRoot(t *testing.T) {
	t.Setenv("MACHTIANI_CONFIG", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWriteFile(t, filepath.Join(home, ".machtiani", "config.toml"), "global = true\n")

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	localConfig := filepath.Join(repoDir, ".machtiani", "config.toml")
	mustWriteFile(t, localConfig, "local = true\n")

	subDir := filepath.Join(repoDir, "nested", "child")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subDir, err)
	}

	withWorkingDir(t, subDir, func() {
		path, err := locateConfig()
		if err != nil {
			t.Fatalf("locateConfig: %v", err)
		}
		if path != localConfig {
			t.Fatalf("expected local config %s, got %s", localConfig, path)
		}
	})
}

func TestLocateConfigFallsBackToGlobalConfig(t *testing.T) {
	t.Setenv("MACHTIANI_CONFIG", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalConfig := filepath.Join(home, ".machtiani", "config.toml")
	mustWriteFile(t, globalConfig, "global = true\n")

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	subDir := filepath.Join(repoDir, "deep")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subDir, err)
	}

	withWorkingDir(t, subDir, func() {
		path, err := locateConfig()
		if err != nil {
			t.Fatalf("locateConfig: %v", err)
		}
		if path != globalConfig {
			t.Fatalf("expected global config %s, got %s", globalConfig, path)
		}
	})
}

func TestLoadGlobalConfigParsesSections(t *testing.T) {
	content := `listen = "127.0.0.1:0"

[planner]
system_template = "Planner system"
instance_template = "Planner instance"
step_limit = 7
cost_limit = 2.5

[shell-agent]
format_error_template = "format-error"
lightweight_max_attempts = 4

[model]
model_name = "alias-model"
api_key = "direct-key"

[model.model_kwargs]
base_url = "https://example.com/v1"

[environment]
type = "local"
timeout = 45
cwd = "."

[environment.env_vars]
FOO = "bar"

[providers.fake]
base_url = "https://example.com/v1"
api_key = "provider-key"

[models.alias]
provider = "fake"
model = "alias-impl"
`

	path := filepath.Join(t.TempDir(), "config.toml")
	mustWriteFile(t, path, content)
	t.Setenv("MACHTIANI_CONFIG", path)
	ResetConfigForTesting()

	cfg, loadedPath, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig: %v", err)
	}
	if loadedPath != path {
		t.Fatalf("expected loaded path %q, got %q", path, loadedPath)
	}
	if cfg.Planner == nil || cfg.Planner.StepLimit != 7 {
		t.Fatalf("expected planner step_limit 7, got %+v", cfg.Planner)
	}
	if cfg.Planner.CostLimit != 2.5 {
		t.Fatalf("expected planner cost_limit 2.5, got %v", cfg.Planner.CostLimit)
	}
	if cfg.ShellAgent == nil {
		t.Fatalf("expected shell-agent config")
	}
	if cfg.ShellAgent.StepLimit != 7 {
		t.Fatalf("expected shell-agent step_limit fallback 7, got %+v", cfg.ShellAgent)
	}
	if cfg.ShellAgent.CostLimit != 2.5 {
		t.Fatalf("expected shell-agent cost_limit fallback 2.5, got %+v", cfg.ShellAgent)
	}
	if cfg.ShellAgent.SystemTemplate != "Planner system" {
		t.Fatalf("expected shell-agent system template from planner, got %q", cfg.ShellAgent.SystemTemplate)
	}
	if cfg.ShellAgent.FormatErrorTemplate != "format-error" {
		t.Fatalf("expected shell-agent format_error_template, got %q", cfg.ShellAgent.FormatErrorTemplate)
	}
	if cfg.ShellAgent.LightweightMaxAttempts != 4 {
		t.Fatalf("expected shell-agent lightweight_max_attempts 4, got %d", cfg.ShellAgent.LightweightMaxAttempts)
	}
	if cfg.Model == nil || strings.TrimSpace(cfg.Model.ModelName) != "alias-model" {
		t.Fatalf("expected model name alias-model, got %+v", cfg.Model)
	}
	if cfg.Model == nil || strings.TrimSpace(cfg.Model.APIKey) != "direct-key" {
		t.Fatalf("expected model api key direct-key, got %+v", cfg.Model)
	}
	if cfg.Model == nil || cfg.Model.ModelKwargs["base_url"] != "https://example.com/v1" {
		t.Fatalf("expected model_kwargs.base_url present, got %+v", cfg.Model)
	}
	if cfg.Environment == nil || strings.TrimSpace(cfg.Environment.Type) != "local" {
		t.Fatalf("expected environment type local, got %+v", cfg.Environment)
	}
	if cfg.Environment == nil || cfg.Environment.Timeout != 45 {
		t.Fatalf("expected environment timeout 45, got %+v", cfg.Environment)
	}
	if cfg.Environment == nil || cfg.Environment.EnvVars["FOO"] != "bar" {
		t.Fatalf("expected environment env_vars FOO=bar, got %+v", cfg.Environment)
	}
}

func TestLoadGlobalConfigSupportsLegacyAgentSection(t *testing.T) {
	content := `listen = "127.0.0.1:0"

[agent]
system_template = "legacy system"
instance_template = "legacy instance"
step_limit = 5
cost_limit = 1.5
lightweight_max_attempts = 2

[environment]
type = "local"

[models.foo]
provider = "fake"
`

	path := filepath.Join(t.TempDir(), "config.toml")
	mustWriteFile(t, path, content)
	t.Setenv("MACHTIANI_CONFIG", path)
	ResetConfigForTesting()

	cfg, _, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig: %v", err)
	}
	if cfg.ShellAgent == nil {
		t.Fatalf("expected shell-agent config from legacy section")
	}
	if cfg.ShellAgent.SystemTemplate != "legacy system" {
		t.Fatalf("expected system template from legacy section, got %q", cfg.ShellAgent.SystemTemplate)
	}
	if cfg.ShellAgent.StepLimit != 5 {
		t.Fatalf("expected step_limit 5 from legacy section, got %d", cfg.ShellAgent.StepLimit)
	}
	if cfg.Planner != nil {
		t.Fatalf("expected planner to be nil when only legacy agent section provided")
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()
	fn()
}
