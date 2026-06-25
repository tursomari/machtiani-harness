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
	content := `

[prompts.planner]
system_template = "Planner system"
instance_template = "Planner instance"

[prompts.shell-agent]
format_error_template = "format-error"
action_observation_template = "Observation: {{.Output}}"

[planner]
step_limit = 7

[shell-agent]

[model]
model_name = "alias-model"
api_key = "direct-key"

[model.model_kwargs]
base_url = "https://example.com/v1"

[environment]
type = "local"
timeout = 45
cwd = "."

[providers.fake]
base_url = "https://example.com/v1"
api_key = "provider-key"

[models.alias]
provider = "fake"
model = "alias-impl"

[ignore]
paths = ["tests/tmp/", "tests/repositories/undici"]
extensions = [".log", ".tmp"]
git_synced_only = true
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
	if cfg.ShellAgent == nil {
		t.Fatalf("expected shell-agent config")
	}
	if cfg.ShellAgent.StepLimit != 7 {
		t.Fatalf("expected shell-agent step_limit fallback 7, got %+v", cfg.ShellAgent)
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
}

func TestLoadGlobalConfigSupportsLegacyAgentSection(t *testing.T) {
	content := `

[agent]
system_template = "legacy system"
instance_template = "legacy instance"
step_limit = 5

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

func TestLoadGlobalConfigParsesModelCacheConfig(t *testing.T) {
	content := `listen = "127.0.0.1:0"

[providers.fake]
base_url = "https://example.com/v1"
api_key = "provider-key"

[models.cached]
provider = "fake"
model = "cached-model"
cache_key_name = "cache_control"
cache_control = { type = "ephemeral" }
cache_trigger_threshold = 100
cache_lookback_offset = 3
cache_reanchor_tokens = 500
cache_reanchor_messages = 8
cache_reanchor_min_cached_tokens = 200
temperature = 0.2
`

	path := filepath.Join(t.TempDir(), "config.toml")
	mustWriteFile(t, path, content)
	t.Setenv("MACHTIANI_CONFIG", path)
	ResetConfigForTesting()

	cfg, _, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig: %v", err)
	}
	model, ok := cfg.Models["cached"]
	if !ok {
		t.Fatalf("expected cached model definition")
	}
	if model.CacheKeyName != "cache_control" {
		t.Fatalf("expected cache_key_name cache_control, got %q", model.CacheKeyName)
	}
	if model.CacheTriggerThreshold != 100 {
		t.Fatalf("expected cache_trigger_threshold 100, got %d", model.CacheTriggerThreshold)
	}
	if model.CacheLookbackOffset != 3 {
		t.Fatalf("expected cache_lookback_offset 3, got %d", model.CacheLookbackOffset)
	}
	if model.CacheReanchorTokens != 500 {
		t.Fatalf("expected cache_reanchor_tokens 500, got %d", model.CacheReanchorTokens)
	}
	if model.CacheReanchorMessages != 8 {
		t.Fatalf("expected cache_reanchor_messages 8, got %d", model.CacheReanchorMessages)
	}
	if model.CacheReanchorMinCachedTokens != 200 {
		t.Fatalf("expected cache_reanchor_min_cached_tokens 200, got %d", model.CacheReanchorMinCachedTokens)
	}
	if model.CacheControl["type"] != "ephemeral" {
		t.Fatalf("expected cache_control type ephemeral, got %v", model.CacheControl)
	}
	if model.Params["temperature"] != 0.2 {
		t.Fatalf("expected temperature param, got %v", model.Params["temperature"])
	}
	if _, ok := model.Params["cache_key_name"]; ok {
		t.Fatalf("cache_key_name should not be in params")
	}
}

func TestLoadGlobalConfigDefaultsReanchorFields(t *testing.T) {
	content := `listen = "127.0.0.1:0"

[providers.fake]
base_url = "https://example.com/v1"
api_key = "provider-key"

[models.cached]
provider = "fake"
model = "cached-model"
cache_key_name = "cache_control"
cache_control = { type = "ephemeral" }
cache_trigger_threshold = 100
`

	path := filepath.Join(t.TempDir(), "config.toml")
	mustWriteFile(t, path, content)
	t.Setenv("MACHTIANI_CONFIG", path)
	ResetConfigForTesting()

	cfg, _, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig: %v", err)
	}
	model, ok := cfg.Models["cached"]
	if !ok {
		t.Fatalf("expected cached model definition")
	}
	if model.CacheReanchorTokens != 0 {
		t.Fatalf("expected default cache_reanchor_tokens 0, got %d", model.CacheReanchorTokens)
	}
	if model.CacheReanchorMessages != 0 {
		t.Fatalf("expected default cache_reanchor_messages 0, got %d", model.CacheReanchorMessages)
	}
	if model.CacheReanchorMinCachedTokens != 0 {
		t.Fatalf("expected default cache_reanchor_min_cached_tokens 0, got %d", model.CacheReanchorMinCachedTokens)
	}
}

func TestLoadModeInstructionsPrefersTomlInOverrideDir(t *testing.T) {
	override := filepath.Join(t.TempDir(), "override")
	mustWriteFile(t, filepath.Join(override, "coding", "tasks.toml"), sampleCodingToml())

	doc, err := LoadModeInstructions("coding", override, Config{}, "")
	if err != nil {
		t.Fatalf("LoadModeInstructions returned error: %v", err)
	}
	if doc.Format != ModeInstructionsFormatTOML {
		t.Fatalf("expected TOML format, got %q", doc.Format)
	}
	if strings.TrimSpace(doc.Task.Title) == "" {
		t.Fatalf("expected task title, got empty")
	}
	if doc.Task.Title != "Implement and validate" {
		t.Fatalf("unexpected task title: %q", doc.Task.Title)
	}
	if doc.Path != filepath.Join(override, "coding", "tasks.toml") {
		t.Fatalf("expected path %s, got %s", filepath.Join(override, "coding", "tasks.toml"), doc.Path)
	}
}

func TestLoadModeInstructionsSearchOrder(t *testing.T) {
	baseDir := t.TempDir()
	configDir := filepath.Join(baseDir, "config")
	mustWriteFile(t, filepath.Join(configDir, ".placeholder"), "")

	configPath := filepath.Join(configDir, "config.toml")
	mustWriteFile(t, configPath, "")

	mustWriteFile(t, filepath.Join(configDir, "coding", "tasks.toml"), sampleCodingToml())

	cfg := Config{
		Mode: &ModeConfig{
			InstructionDir: configDir,
		},
	}

	doc, err := LoadModeInstructions("coding", "", cfg, configPath)
	if err != nil {
		t.Fatalf("LoadModeInstructions returned error: %v", err)
	}
	if !strings.HasSuffix(doc.Path, filepath.Join("coding", "tasks.toml")) {
		t.Fatalf("expected coding/tasks.toml to be selected, got %s", doc.Path)
	}
	if doc.Format != ModeInstructionsFormatTOML {
		t.Fatalf("expected TOML format, got %q", doc.Format)
	}
}

func TestLoadModeInstructionsInvalidTomlReturnsError(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "coding", "tasks.toml"), "[[tasks]\n title = \"broken\"")

	_, err := LoadModeInstructions("coding", dir, Config{}, "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestLoadModeInstructionsPermissionError(t *testing.T) {
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

	_, err := LoadModeInstructions("coding", dir, Config{}, "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestParseModeInstructionTOMLLoadsDescriptionFromExternalFile(t *testing.T) {
	path := filepath.Join("testdata", "mode_instructions", "external", "tasks.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	task, err := parseModeInstructionTOML(data, path)
	if err != nil {
		t.Fatalf("parseModeInstructionTOML: %v", err)
	}
	if task.Description != "Hello from inline" {
		t.Fatalf("unexpected description: %q", task.Description)
	}
	if task.Instruction != "Do the inline task" {
		t.Fatalf("unexpected instruction: %q", task.Instruction)
	}
}

func TestParseModeInstructionTOMLLoadsSystemPromptFromExternalFile(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "description.md"), "Investigate schema drift")
	mustWriteFile(t, filepath.Join(dir, "instruction.md"), "Check backward compatibility")
	mustWriteFile(t, filepath.Join(dir, "overlay.md"), "Prioritize backward-compatible changes")
	tomlPath := filepath.Join(dir, "tasks.toml")
	mustWriteFile(t, tomlPath, `title = "Review"
description = "description.md"
instruction = "instruction.md"
system_prompt = "overlay.md"
`)

	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	task, err := parseModeInstructionTOML(data, tomlPath)
	if err != nil {
		t.Fatalf("parseModeInstructionTOML: %v", err)
	}
	if task.Instruction != "Check backward compatibility" {
		t.Fatalf("unexpected instruction: %q", task.Instruction)
	}
	if task.SystemPrompt != "Prioritize backward-compatible changes" {
		t.Fatalf("unexpected system prompt: %q", task.SystemPrompt)
	}
}

func TestParseModeInstructionTOMLFailsWhenExternalInstructionMissing(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "tasks.toml")
	mustWriteFile(t, tomlPath, `title = "Review"
instruction = "missing.txt"
`)

	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	_, err = parseModeInstructionTOML(data, tomlPath)
	if err == nil || !strings.Contains(err.Error(), "resolve instruction") {
		t.Fatalf("expected missing instruction error, got %v", err)
	}
}

func TestParseModeInstructionTOMLFailsWhenExternalDescriptionMissing(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "tasks.toml")
	mustWriteFile(t, tomlPath, `title = "Missing"
description = "missing.txt"
`)

	data, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	_, err = parseModeInstructionTOML(data, tomlPath)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestLoadGlobalConfigPromotesLegacyShellAgentTemplates(t *testing.T) {
	content := `

[shell-agent]
system_template = "agent system"
instance_template = "agent instance"
timeout_template = "legacy timeout"
format_error_template = "legacy format"
action_observation_template = "legacy action {{.Output}}"
lightweight_system_template = "legacy lw system"
lightweight_intent_template = "legacy lw intent"
lightweight_error_template = "legacy lw error"

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
	if cfg.ShellAgent.StepLimit != 9 {
		t.Fatalf("expected shell-agent step_limit to inherit 9, got %d", cfg.ShellAgent.StepLimit)
	}
}

func TestLoadGlobalConfigSupportsTemplateFiles(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "system.tpl"), "planner system file")
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "plan_system_prompt.tpl"), "planner plan system file")
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "plan_prompt.tpl"), "plan prompt file")
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "plan_patch_rules.tpl"), "plan patch rules file")
	mustWriteFile(t, filepath.Join(root, "templates", "planner", "plan_patch_strict_rules.tpl"), "plan patch strict rules file")
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
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "shell_agent_prompt_notice.tpl"), "shell agent prompt notice")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "header_user.tpl"), "header user")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "header_existing.tpl"), "header existing")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "conversation_history.tpl"), "history {{len .History}}")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "readme_system.tpl"), "readme system")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "patch_success_note.tpl"), "patch success note")
	mustWriteFile(t, filepath.Join(root, "templates", "mct", "full_diff_note.tpl"), "full diff note")

	content := `

[prompts.planner]
system_template = { file = "templates/planner/system.tpl" }
plan_system_prompt = { file = "templates/planner/plan_system_prompt.tpl" }
plan_prompt = { file = "templates/planner/plan_prompt.tpl" }
plan_patch_rules = { file = "templates/planner/plan_patch_rules.tpl" }
plan_patch_strict_rules = { file = "templates/planner/plan_patch_strict_rules.tpl" }
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
shell_agent_prompt_notice = { file = "templates/mct/shell_agent_prompt_notice.tpl" }
header_user_template = { file = "templates/mct/header_user.tpl" }
header_existing_template = { file = "templates/mct/header_existing.tpl" }
conversation_history_template = { file = "templates/mct/conversation_history.tpl" }
readme_system_template = { file = "templates/mct/readme_system.tpl" }
patch_success_note = { file = "templates/mct/patch_success_note.tpl" }
full_diff_note = { file = "templates/mct/full_diff_note.tpl" }
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
	if cfg.Prompts.Planner.PlanSystemPrompt != "planner plan system file" {
		t.Fatalf("expected planner plan system prompt from file, got %q", cfg.Prompts.Planner.PlanSystemPrompt)
	}
	if cfg.Prompts.Planner.PlanPrompt != "plan prompt file" {
		t.Fatalf("expected planner plan prompt from file, got %q", cfg.Prompts.Planner.PlanPrompt)
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
	if cfg.Prompts.MCT.ShellAgentPromptNotice != "shell agent prompt notice" {
		t.Fatalf("expected mct shell agent prompt notice from file, got %q", cfg.Prompts.MCT.ShellAgentPromptNotice)
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
	if cfg.Prompts.MCT.FullDiffNote != "full diff note" {
		t.Fatalf("expected full diff note from file, got %q", cfg.Prompts.MCT.FullDiffNote)
	}
}

func TestDefaultMinimalConfig(t *testing.T) {
	cfg := DefaultMinimalConfig()

	if cfg.DefaultModel != "" {
		t.Fatalf("expected DefaultModel \"\", got %q", cfg.DefaultModel)
	}

	if cfg.Planner == nil {
		t.Fatal("expected Planner to be non-nil")
	} else {
		if cfg.Planner.StepLimit != 110 {
			t.Fatalf("expected Planner.StepLimit 110, got %d", cfg.Planner.StepLimit)
		}
	}

	if cfg.ShellAgent == nil {
		t.Fatal("expected ShellAgent to be non-nil")
	} else {
		if cfg.ShellAgent.FinalizeRemainingSteps != 10 {
			t.Fatalf("expected ShellAgent.FinalizeRemainingSteps 10, got %d", cfg.ShellAgent.FinalizeRemainingSteps)
		}
		if cfg.ShellAgent.StepLimit != 0 {
			t.Fatalf("expected ShellAgent.StepLimit 0, got %d", cfg.ShellAgent.StepLimit)
		}
	}

	if cfg.Environment == nil {
		t.Fatal("expected Environment to be non-nil")
	} else {
		if cfg.Environment.Type != "local" {
			t.Fatalf("expected Environment.Type local, got %q", cfg.Environment.Type)
		}
		if cfg.Environment.Timeout != 9999 {
			t.Fatalf("expected Environment.Timeout 9999, got %d", cfg.Environment.Timeout)
		}
		if cfg.Environment.CWD != "." {
			t.Fatalf("expected Environment.CWD \".\", got %q", cfg.Environment.CWD)
		}
		if cfg.Environment.MaxCommandOutputBytes != 0 {
			t.Fatalf("expected Environment.MaxCommandOutputBytes 0, got %d", cfg.Environment.MaxCommandOutputBytes)
		}
	}

	if cfg.Model != nil {
		t.Fatalf("expected Model to be nil, got %+v", cfg.Model)
	}
	if cfg.Debug != nil {
		t.Fatalf("expected Debug to be nil, got %+v", cfg.Debug)
	}
	if cfg.Prompts != nil {
		t.Fatalf("expected Prompts to be nil, got %+v", cfg.Prompts)
	}
	if cfg.Mode != nil {
		t.Fatalf("expected Mode to be nil, got %+v", cfg.Mode)
	}
	if cfg.Providers != nil {
		t.Fatalf("expected Providers to be nil, got %+v", cfg.Providers)
	}
	if cfg.Models != nil {
		t.Fatalf("expected Models to be nil, got %+v", cfg.Models)
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
	return `title = "Implement and validate"
description = "Implement the patch to solve the Goal, make code changes and tests, run validations, and summarize."
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
