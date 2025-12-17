package llm

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

[prompts.planner]
system_template = "Planner system"
instance_template = "Planner instance"

[prompts.shell-agent]
format_error_template = "format-error"
action_observation_template = "Observation: {{.Output}}"

[planner]
step_limit = 7
cost_limit = 2.5

[shell-agent]
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
	if cfg.Prompts == nil || cfg.Prompts.Planner == nil {
		t.Fatalf("expected planner prompts to be parsed, got %+v", cfg.Prompts)
	}
	if cfg.Prompts.Planner.SystemTemplate != "Planner system" {
		t.Fatalf("expected planner system template, got %q", cfg.Prompts.Planner.SystemTemplate)
	}
	if cfg.Prompts.Planner.InstanceTemplate != "Planner instance" {
		t.Fatalf("expected planner instance template, got %q", cfg.Prompts.Planner.InstanceTemplate)
	}
	if cfg.Prompts.ShellAgent == nil {
		t.Fatalf("expected shell-agent prompts to be parsed, got %+v", cfg.Prompts)
	}
	if cfg.Prompts.ShellAgent.FormatErrorTemplate != "format-error" {
		t.Fatalf("expected shell-agent format_error_template, got %q", cfg.Prompts.ShellAgent.FormatErrorTemplate)
	}
	if cfg.Prompts.ShellAgent.ActionObservationTemplate != "Observation: {{.Output}}" {
		t.Fatalf("expected shell-agent action_observation_template, got %q", cfg.Prompts.ShellAgent.ActionObservationTemplate)
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
	if cfg.ShellAgent.StepLimit != 5 {
		t.Fatalf("expected step_limit 5 from legacy section, got %d", cfg.ShellAgent.StepLimit)
	}
	if cfg.Prompts == nil || cfg.Prompts.Planner == nil {
		t.Fatalf("expected planner prompts from legacy section")
	}
	if cfg.Prompts.Planner.SystemTemplate != "legacy system" {
		t.Fatalf("expected planner system template from legacy section, got %q", cfg.Prompts.Planner.SystemTemplate)
	}
	if cfg.Prompts.Planner.InstanceTemplate != "legacy instance" {
		t.Fatalf("expected planner instance template from legacy section, got %q", cfg.Prompts.Planner.InstanceTemplate)
	}
	if cfg.Prompts.ShellAgent != nil {
		t.Fatalf("expected shell-agent prompts to be nil when using legacy agent section, got %+v", cfg.Prompts.ShellAgent)
	}
	if cfg.Planner != nil {
		t.Fatalf("expected planner to be nil when only legacy agent section provided")
	}
}

func TestLoadMetaInstructionsPrefersTomlInOverrideDir(t *testing.T) {
	override := filepath.Join(t.TempDir(), "override")
	mustWriteFile(t, filepath.Join(override, "coding", "tasks.toml"), sampleCodingToml())

	doc, err := LoadMetaInstructions("coding", override, Config{}, "")
	if err != nil {
		t.Fatalf("LoadMetaInstructions returned error: %v", err)
	}
	if doc.Format != MetaInstructionsFormatTOML {
		t.Fatalf("expected TOML format, got %q", doc.Format)
	}
	if len(doc.Tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d", len(doc.Tasks))
	}
	if doc.Tasks[0].Title != "Create an issue for the engineering team" {
		t.Fatalf("unexpected first task title: %q", doc.Tasks[0].Title)
	}
	if doc.Path != filepath.Join(override, "coding", "tasks.toml") {
		t.Fatalf("expected path %s, got %s", filepath.Join(override, "coding", "tasks.toml"), doc.Path)
	}
}

func TestLoadMetaInstructionsSearchOrder(t *testing.T) {
	baseDir := t.TempDir()
	configDir := filepath.Join(baseDir, "config")
	mustWriteFile(t, filepath.Join(configDir, ".placeholder"), "")

	configPath := filepath.Join(configDir, "config.toml")
	mustWriteFile(t, configPath, "")

	mustWriteFile(t, filepath.Join(configDir, "coding", "tasks.toml"), sampleCodingToml())

	cfg := Config{
		MetaOrchestrator: &MetaOrchestratorConfig{
			InstructionDir: configDir,
		},
	}

	doc, err := LoadMetaInstructions("coding", "", cfg, configPath)
	if err != nil {
		t.Fatalf("LoadMetaInstructions returned error: %v", err)
	}
	if !strings.HasSuffix(doc.Path, filepath.Join("coding", "tasks.toml")) {
		t.Fatalf("expected coding/tasks.toml to be selected, got %s", doc.Path)
	}
	if doc.Format != MetaInstructionsFormatTOML {
		t.Fatalf("expected TOML format, got %q", doc.Format)
	}
}

func TestLoadMetaInstructionsInvalidTomlReturnsError(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "coding", "tasks.toml"), "[[tasks]\n title = \"broken\"")

	_, err := LoadMetaInstructions("coding", dir, Config{}, "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestLoadMetaInstructionsPermissionError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissions semantics differ on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "coding", "tasks.toml")
	mustWriteFile(t, path, sampleCodingToml())
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(path, 0o644)

	_, err := LoadMetaInstructions("coding", dir, Config{}, "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestParseMetaInstructionTOMLLoadsDescriptionFromExternalFile(t *testing.T) {
	path := filepath.Join("testdata", "meta_instructions", "external", "tasks.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	tasks, err := parseMetaInstructionTOML(data, path)
	if err != nil {
		t.Fatalf("parseMetaInstructionTOML: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Description != "Hello from inline" {
		t.Fatalf("unexpected inline description: %q", tasks[0].Description)
	}
	if tasks[1].Description != "Hello from file" {
		t.Fatalf("unexpected file description: %q", tasks[1].Description)
	}
}

func TestParseMetaInstructionTOMLFailsWhenExternalDescriptionMissing(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "tasks.toml")
	mustWriteFile(t, tomlPath, `[[tasks]]
title = "Missing"
description = "missing.txt"
`)

	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	_, err = parseMetaInstructionTOML(data, tomlPath)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestLoadGlobalConfigPromotesLegacyShellAgentTemplates(t *testing.T) {
	content := `listen = "127.0.0.1:0"

[shell-agent]
system_template = "agent system"
instance_template = "agent instance"
timeout_template = "legacy timeout"
format_error_template = "legacy format"
action_observation_template = "legacy action {{.Output}}"
lightweight_system_template = "legacy lw system"
lightweight_intent_template = "legacy lw intent"
lightweight_error_template = "legacy lw error"
lightweight_max_attempts = 3

[planner]
step_limit = 9
`

	path := filepath.Join(t.TempDir(), "config.toml")
	mustWriteFile(t, path, content)
	t.Setenv("MACHTIANI_CONFIG", path)
	ResetConfigForTesting()

	cfg, _, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig: %v", err)
	}
	if cfg.Prompts == nil || cfg.Prompts.Planner == nil {
		t.Fatalf("expected planner prompts from legacy shell-agent section")
	}
	if cfg.Prompts.Planner.SystemTemplate != "agent system" {
		t.Fatalf("expected planner system template from legacy shell-agent section, got %q", cfg.Prompts.Planner.SystemTemplate)
	}
	if cfg.Prompts.Planner.InstanceTemplate != "agent instance" {
		t.Fatalf("expected planner instance template from legacy shell-agent section, got %q", cfg.Prompts.Planner.InstanceTemplate)
	}
	if cfg.Prompts.ShellAgent == nil {
		t.Fatalf("expected shell-agent prompts from legacy shell-agent section")
	}
	if cfg.Prompts.ShellAgent.FormatErrorTemplate != "legacy format" {
		t.Fatalf("expected shell-agent format template, got %q", cfg.Prompts.ShellAgent.FormatErrorTemplate)
	}
	if cfg.Prompts.ShellAgent.ActionObservationTemplate != "legacy action {{.Output}}" {
		t.Fatalf("expected shell-agent action template, got %q", cfg.Prompts.ShellAgent.ActionObservationTemplate)
	}
	if cfg.Prompts.ShellAgent.LightweightSystemTemplate != "legacy lw system" {
		t.Fatalf("expected lightweight system template, got %q", cfg.Prompts.ShellAgent.LightweightSystemTemplate)
	}
	if cfg.Prompts.ShellAgent.LightweightIntentTemplate != "legacy lw intent" {
		t.Fatalf("expected lightweight intent template, got %q", cfg.Prompts.ShellAgent.LightweightIntentTemplate)
	}
	if cfg.Prompts.ShellAgent.LightweightErrorTemplate != "legacy lw error" {
		t.Fatalf("expected lightweight error template, got %q", cfg.Prompts.ShellAgent.LightweightErrorTemplate)
	}
	if cfg.ShellAgent.LightweightMaxAttempts != 3 {
		t.Fatalf("expected lightweight max attempts to remain 3, got %d", cfg.ShellAgent.LightweightMaxAttempts)
	}
	if cfg.ShellAgent.StepLimit != 9 {
		t.Fatalf("expected shell-agent step_limit to inherit 9, got %d", cfg.ShellAgent.StepLimit)
	}
}

func TestLoadGlobalConfigCopiesWorkspace(t *testing.T) {
	ResetConfigForTesting()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".machtiani"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath := filepath.Join(root, ".machtiani", "config.toml")
	if err := os.WriteFile(configPath, []byte("[workspace]\ngit_hydration = [{ root = 'tests/repositories/undici', branches = ['main'] }]\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)

	cfg, _, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig error: %v", err)
	}
	if cfg.Workspace == nil {
		t.Fatalf("expected workspace to be non-nil")
	}
	if len(cfg.Workspace.GitHydration) != 1 {
		t.Fatalf("expected 1 hydration rule, got %d", len(cfg.Workspace.GitHydration))
	}
}

func TestLoadGlobalConfigSupportsTemplateFiles(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "system.tpl"), "planner system file")
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "plan_prompt.tpl"), "plan prompt file")
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "plan_patch_rules.tpl"), "plan patch rules file")
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "plan_patch_strict_rules.tpl"), "plan patch strict rules file")
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "plan_patch_enabled_intro.tpl"), "enabled intro file")
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "plan_patch_disabled_intro.tpl"), "disabled intro file")
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "finalize_prompt.tpl"), "finalize prompt file")
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "review_prompt.tpl"), "review prompt file")
	mustWriteFile(t, filepath.Join(root, "templates", "shell-agent", "timeout_template.tpl"), "timeout file")
	mustWriteFile(t, filepath.Join(root, "templates", "shell-agent", "format_error_template.tpl"), "format file")
	mustWriteFile(t, filepath.Join(root, "templates", "shell-agent", "action_observation_template.txt"), "action observation file")
	mustWriteFile(t, filepath.Join(root, "templates", "shell-agent", "lightweight_system_template.txt"), "lw system file")
	mustWriteFile(t, filepath.Join(root, "templates", "shell-agent", "lightweight_intent_template.txt"), "lw intent file")
	mustWriteFile(t, filepath.Join(root, "templates", "shell-agent", "lightweight_error_template.txt"), "lw error file")
	mustWriteFile(t, filepath.Join(root, "templates", "file-discovery", "system.tpl"), "file discovery system")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "context_prefix.tpl"), "context prefix")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "context_block.tpl"), "context block {{.Stdout}}")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "header_user.tpl"), "header user")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "header_existing.tpl"), "header existing")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "conversation_history.tpl"), "history {{len .History}}")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "readme_system.tpl"), "readme system")

	content := `listen = "127.0.0.1:0"

[prompts.planner]
system_template = { file = "templates/planner/system.tpl" }
plan_prompt = { file = "templates/planner/plan_prompt.tpl" }
plan_patch_rules = { file = "templates/planner/plan_patch_rules.tpl" }
plan_patch_strict_rules = { file = "templates/planner/plan_patch_strict_rules.tpl" }
plan_patch_enabled_intro = { file = "templates/planner/plan_patch_enabled_intro.tpl" }
plan_patch_disabled_intro = { file = "templates/planner/plan_patch_disabled_intro.tpl" }
finalize_prompt = { file = "templates/planner/finalize_prompt.tpl" }
review_prompt = { file = "templates/planner/review_prompt.tpl" }

[prompts.shell-agent]
timeout_template = { file = "templates/shell-agent/timeout_template.tpl" }
format_error_template = { file = "templates/shell-agent/format_error_template.tpl" }
action_observation_template = { file = "templates/shell-agent/action_observation_template.txt" }
lightweight_system_template = { file = "templates/shell-agent/lightweight_system_template.txt" }
lightweight_intent_template = { file = "templates/shell-agent/lightweight_intent_template.txt" }
lightweight_error_template = { file = "templates/shell-agent/lightweight_error_template.txt" }

[prompts.file_discovery]
system_prompt_template = { file = "templates/file-discovery/system.tpl" }

[prompts.mct]
shell_agent_context_prefix = { file = "templates/mct/context_prefix.tpl" }
shell_agent_context_template = { file = "templates/mct/context_block.tpl" }
header_user_template = { file = "templates/mct/header_user.tpl" }
header_existing_template = { file = "templates/mct/header_existing.tpl" }
conversation_history_template = { file = "templates/mct/conversation_history.tpl" }
readme_system_template = { file = "templates/mct/readme_system.tpl" }
`

	configPath := filepath.Join(root, "config.toml")
	mustWriteFile(t, configPath, content)
	t.Setenv("MACHTIANI_CONFIG", configPath)
	ResetConfigForTesting()

	cfg, _, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig: %v", err)
	}
	if cfg.Prompts == nil || cfg.Prompts.Planner == nil {
		t.Fatalf("expected planner prompts from template files")
	}
	if cfg.Prompts.Planner.SystemTemplate != "planner system file" {
		t.Fatalf("expected planner system template from file, got %q", cfg.Prompts.Planner.SystemTemplate)
	}
	if cfg.Prompts.Planner.PlanPrompt != "plan prompt file" {
		t.Fatalf("expected planner plan prompt from file, got %q", cfg.Prompts.Planner.PlanPrompt)
	}
	if cfg.Prompts.Planner.PlanPatchRules != "plan patch rules file" {
		t.Fatalf("expected planner patch rules from file, got %q", cfg.Prompts.Planner.PlanPatchRules)
	}
	if cfg.Prompts.Planner.PlanPatchStrictRules != "plan patch strict rules file" {
		t.Fatalf("expected planner strict patch rules from file, got %q", cfg.Prompts.Planner.PlanPatchStrictRules)
	}
	if cfg.Prompts.Planner.PlanPatchEnabledIntro != "enabled intro file" {
		t.Fatalf("expected planner enabled intro from file, got %q", cfg.Prompts.Planner.PlanPatchEnabledIntro)
	}
	if cfg.Prompts.Planner.PlanPatchDisabledIntro != "disabled intro file" {
		t.Fatalf("expected planner disabled intro from file, got %q", cfg.Prompts.Planner.PlanPatchDisabledIntro)
	}
	if cfg.Prompts.Planner.FinalizePrompt != "finalize prompt file" {
		t.Fatalf("expected planner finalize prompt from file, got %q", cfg.Prompts.Planner.FinalizePrompt)
	}
	if cfg.Prompts.Planner.ReviewPrompt != "review prompt file" {
		t.Fatalf("expected planner review prompt from file, got %q", cfg.Prompts.Planner.ReviewPrompt)
	}
	if cfg.Prompts.ShellAgent == nil {
		t.Fatalf("expected shell-agent prompts from template files")
	}
	if cfg.Prompts.ShellAgent.TimeoutTemplate != "timeout file" {
		t.Fatalf("expected timeout template from file, got %q", cfg.Prompts.ShellAgent.TimeoutTemplate)
	}
	if cfg.Prompts.ShellAgent.FormatErrorTemplate != "format file" {
		t.Fatalf("expected format template from file, got %q", cfg.Prompts.ShellAgent.FormatErrorTemplate)
	}
	if cfg.Prompts.ShellAgent.ActionObservationTemplate != "action observation file" {
		t.Fatalf("expected action observation template from file, got %q", cfg.Prompts.ShellAgent.ActionObservationTemplate)
	}
	if cfg.Prompts.ShellAgent.LightweightSystemTemplate != "lw system file" {
		t.Fatalf("expected lightweight system template from file, got %q", cfg.Prompts.ShellAgent.LightweightSystemTemplate)
	}
	if cfg.Prompts.ShellAgent.LightweightIntentTemplate != "lw intent file" {
		t.Fatalf("expected lightweight intent template from file, got %q", cfg.Prompts.ShellAgent.LightweightIntentTemplate)
	}
	if cfg.Prompts.ShellAgent.LightweightErrorTemplate != "lw error file" {
		t.Fatalf("expected lightweight error template from file, got %q", cfg.Prompts.ShellAgent.LightweightErrorTemplate)
	}
	if cfg.Prompts.FileDiscovery == nil || cfg.Prompts.FileDiscovery.SystemPromptTemplate != "file discovery system" {
		t.Fatalf("expected file discovery system prompt from file, got %+v", cfg.Prompts.FileDiscovery)
	}
	if cfg.Prompts.MCT == nil {
		t.Fatalf("expected mct prompts from template files")
	}
	if cfg.Prompts.MCT.ShellAgentContextPrefix != "context prefix" {
		t.Fatalf("expected mct context prefix from file, got %q", cfg.Prompts.MCT.ShellAgentContextPrefix)
	}
	if cfg.Prompts.MCT.ShellAgentContextTemplate != "context block {{.Stdout}}" {
		t.Fatalf("expected mct context template from file, got %q", cfg.Prompts.MCT.ShellAgentContextTemplate)
	}
	if cfg.Prompts.MCT.HeaderUserTemplate != "header user" {
		t.Fatalf("expected mct header user template from file, got %q", cfg.Prompts.MCT.HeaderUserTemplate)
	}
	if cfg.Prompts.MCT.HeaderExistingTemplate != "header existing" {
		t.Fatalf("expected mct header existing template from file, got %q", cfg.Prompts.MCT.HeaderExistingTemplate)
	}
	if cfg.Prompts.MCT.ConversationHistoryTemplate != "history {{len .History}}" {
		t.Fatalf("expected mct conversation history template from file, got %q", cfg.Prompts.MCT.ConversationHistoryTemplate)
	}
	if cfg.Prompts.MCT.ReadmeSystemTemplate != "readme system" {
		t.Fatalf("expected readme system template from file, got %q", cfg.Prompts.MCT.ReadmeSystemTemplate)
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

func sampleCodingToml() string {
	return `[[tasks]]
step = 1
title = "Create an issue for the engineering team"
description = "Create an issue for the engineering team. Do not make any code changes or patches."
shell_agent = false
patch_mode = false

[[tasks]]
step = 2
title = "Implement the Issue"
description = "Implement the Issue."
shell_agent = true
patch_mode = true

[[tasks]]
step = 3
title = "Run available validations"
description = "Run available validations (tests, linters, or targeted reasoning) to confirm behavior and note any risks or follow-up work."
shell_agent = true
patch_mode = false

[[tasks]]
step = 4
title = "Summarize the results"
description = "Summarize the results, highlighting modifications, verification status, and remaining next steps for the parent session."
shell_agent = false
patch_mode = false
`
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
