package llm

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

// Diagnostic is a machine-readable configuration problem.
type Diagnostic struct {
	Severity   string
	Path       string
	Code       string
	Message    string
	Suggestion string
}

func (d Diagnostic) String() string {
	message := d.Message
	if strings.TrimSpace(d.Path) != "" {
		message = d.Path + ": " + message
	}
	if strings.TrimSpace(d.Suggestion) != "" {
		message += " (" + d.Suggestion + ")"
	}
	return message
}

// ValidationError aggregates configuration diagnostics into one error.
type ValidationError struct {
	Path        string
	Diagnostics []Diagnostic
}

func (e *ValidationError) Error() string {
	if e == nil || len(e.Diagnostics) == 0 {
		return "configuration validation failed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "configuration validation failed for %s:", e.Path)
	for _, diagnostic := range e.Diagnostics {
		fmt.Fprintf(&b, "\n  - %s", diagnostic.String())
	}
	return b.String()
}

func diagnosticsError(path string, diagnostics []Diagnostic) error {
	if len(diagnostics) == 0 {
		return nil
	}
	sortDiagnostics(diagnostics)
	return &ValidationError{Path: path, Diagnostics: diagnostics}
}

func sortDiagnostics(diagnostics []Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		if diagnostics[i].Path != diagnostics[j].Path {
			return diagnostics[i].Path < diagnostics[j].Path
		}
		return diagnostics[i].Code < diagnostics[j].Code
	})
}

func configError(path, code, message string) Diagnostic {
	return Diagnostic{Severity: "error", Path: path, Code: code, Message: message}
}

func validateRawConfig(path string, raw map[string]any) error {
	var diagnostics []Diagnostic
	top := map[string]string{
		"default_model": "string", "verbose": "bool", "persist_tmp_data": "bool", "dry_run": "bool",
		"shell_agent_enabled": "bool", "shell_agent_model": "string", "answer_model": "string",
		"file_discovery_model": "string", "answer_tag": "string", "tag": "string", "enable_tag_format": "bool",
		"final_file": "string", "transcript_file": "string", "file_discovery_trajectory": "string",
		"file_discovery_output_dir": "string", "planner": "table", "shell-agent": "table", "agent": "table",
		"prompts": "table", "environment": "table", "trajectory": "table", "providers": "table",
		"models": "table", "model_defaults": "table", "mode": "table", "ui": "table",
	}
	validateKnownMap(raw, "", top, &diagnostics)

	plannerFields := map[string]string{
		"max_turns": "integer", "turn_timeout": "integer",
		"system_template": "template", "instance_template": "template", "timeout_template": "template",
		"format_error_template": "template", "ask_prompt": "template", "plan_system_prompt": "template",
		"plan_prompt": "template", "finalize_prompt": "template", "review_prompt": "template",
	}
	shellFields := map[string]string{
		"max_steps": "integer", "finalize_remaining_steps": "integer", "system_template": "template",
		"command_supervisor_after": "integer", "command_supervisor_timeout": "integer",
		"command_supervisor_failure_limit": "integer", "command_supervisor_max_steps": "integer",
		"command_supervisor_deadline_buffer": "integer",
		"instance_template":                  "template", "timeout_template": "template", "format_error_template": "template",
		"action_observation_template": "template", "lightweight_system_template": "template",
		"lightweight_intent_template": "template", "lightweight_error_template": "template",
	}
	legacyAgentFields := map[string]string{
		"max_steps": "integer", "finalize_remaining_steps": "integer", "system_template": "template", "instance_template": "template",
	}
	validateSection(raw, "planner", plannerFields, &diagnostics)
	validateSection(raw, "shell-agent", shellFields, &diagnostics)
	validateSection(raw, "agent", legacyAgentFields, &diagnostics)
	validateSection(raw, "environment", map[string]string{
		"type": "string", "command_timeout": "integer", "max_command_output_bytes": "integer",
	}, &diagnostics)
	validateSection(raw, "trajectory", map[string]string{
		"enabled": "bool", "file": "string", "verbose_llm": "bool", "stream_tokens": "bool",
		"excerpt": "integer", "omit_repo_root": "bool",
	}, &diagnostics)
	validateSection(raw, "ui", map[string]string{"theme": "string", "glyphs": "string", "banner": "bool", "motion": "string"}, &diagnostics)
	validateModeRaw(raw, &diagnostics)
	validatePromptsRaw(raw, plannerFields, shellFields, &diagnostics)
	validateProvidersRaw(raw, &diagnostics)
	modelDefaultFields := cacheFieldTypes()
	modelDefaultFields["context_length"] = "integer"
	validateSection(raw, "model_defaults", modelDefaultFields, &diagnostics)
	validateModelsRaw(raw, &diagnostics)
	return diagnosticsError(path, diagnostics)
}

func validateKnownMap(data map[string]any, prefix string, fields map[string]string, diagnostics *[]Diagnostic) {
	for key, value := range data {
		path := joinConfigPath(prefix, key)
		want, ok := fields[key]
		if !ok {
			message := "unknown configuration key"
			if path == "planner.max_input_tokens" {
				message = "removed; delete this key and configure models.<alias>.context_length instead"
			} else if path == "environment.cwd" {
				message = "removed; delete this key. Shell commands now start in the directory where machtiani was launched"
			}
			diagnostic := configError(path, "unknown_key", message)
			if suggestion := nearestConfigKey(key, fields); suggestion != "" {
				diagnostic.Suggestion = "did you mean " + suggestion + "?"
			}
			*diagnostics = append(*diagnostics, diagnostic)
			continue
		}
		if !matchesConfigType(value, want) {
			*diagnostics = append(*diagnostics, configError(path, "wrong_type", fmt.Sprintf("expected %s, got %T", want, value)))
		}
		if want == "template" {
			validateTemplateValue(path, value, diagnostics)
		}
	}
}

func nearestConfigKey(key string, fields map[string]string) string {
	best := ""
	bestDistance := 4
	for candidate := range fields {
		distance := editDistance(key, candidate)
		if distance < bestDistance || (distance == bestDistance && candidate < best) {
			best = candidate
			bestDistance = distance
		}
	}
	if bestDistance > 3 {
		return ""
	}
	return best
}

func editDistance(left, right string) int {
	a := []rune(left)
	b := []rune(right)
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, ar := range a {
		current := make([]int, len(b)+1)
		current[0] = i + 1
		for j, br := range b {
			cost := 0
			if ar != br {
				cost = 1
			}
			current[j+1] = minInt(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}

func minInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func validateSection(raw map[string]any, name string, fields map[string]string, diagnostics *[]Diagnostic) {
	value, exists := raw[name]
	if !exists {
		return
	}
	table, ok := toMap(value)
	if !ok {
		return
	}
	validateKnownMap(table, name, fields, diagnostics)
}

func validateTemplateValue(path string, value any, diagnostics *[]Diagnostic) {
	table, ok := toMap(value)
	if !ok {
		return
	}
	validateKnownMap(table, path, map[string]string{"file": "string"}, diagnostics)
	if _, exists := table["file"]; !exists {
		*diagnostics = append(*diagnostics, configError(path, "missing_field", "template table must contain file"))
	}
}

func validatePromptsRaw(raw map[string]any, plannerFields, shellFields map[string]string, diagnostics *[]Diagnostic) {
	prompts, ok := toMap(raw["prompts"])
	if !ok {
		return
	}
	validateKnownMap(prompts, "prompts", map[string]string{
		"planner": "table", "shell-agent": "table", "file_discovery": "table", "mct": "table",
	}, diagnostics)
	if section, ok := toMap(prompts["planner"]); ok {
		fields := copyFieldTypes(plannerFields)
		delete(fields, "max_turns")
		delete(fields, "turn_timeout")
		validateKnownMap(section, "prompts.planner", fields, diagnostics)
	}
	if section, ok := toMap(prompts["shell-agent"]); ok {
		fields := copyFieldTypes(shellFields)
		delete(fields, "max_steps")
		delete(fields, "finalize_remaining_steps")
		validateKnownMap(section, "prompts.shell-agent", fields, diagnostics)
	}
	if section, ok := toMap(prompts["file_discovery"]); ok {
		validateKnownMap(section, "prompts.file_discovery", map[string]string{"system_prompt_template": "template"}, diagnostics)
	}
	if section, ok := toMap(prompts["mct"]); ok {
		fields := map[string]string{}
		for _, key := range []string{"shell_agent_context_prefix", "shell_agent_prompt_notice", "header_user_template", "header_existing_template", "conversation_history_template", "readme_system_template", "shell_agent_context_template", "full_diff_note"} {
			fields[key] = "template"
		}
		validateKnownMap(section, "prompts.mct", fields, diagnostics)
	}
}

func validateProvidersRaw(raw map[string]any, diagnostics *[]Diagnostic) {
	providers, ok := toMap(raw["providers"])
	if !ok {
		return
	}
	for name, value := range providers {
		path := "providers." + name
		provider, ok := toMap(value)
		if !ok {
			*diagnostics = append(*diagnostics, configError(path, "wrong_type", "expected table"))
			continue
		}
		validateKnownMap(provider, path, map[string]string{
			"transport": "string", "profile": "string", "command": "string",
			"base_url": "string", "api_key": "string", "endpoint": "string", "reasoning_format": "string",
			"headers": "table", "query": "table",
		}, diagnostics)
		for _, mapName := range []string{"headers", "query"} {
			if values, ok := toMap(provider[mapName]); ok {
				for key, entry := range values {
					if _, ok := entry.(string); !ok {
						*diagnostics = append(*diagnostics, configError(path+"."+mapName+"."+key, "wrong_type", "expected string"))
					}
				}
			}
		}
	}
}

func validateModelsRaw(raw map[string]any, diagnostics *[]Diagnostic) {
	models, ok := toMap(raw["models"])
	if !ok {
		return
	}
	fields := cacheFieldTypes()
	fields["provider"] = "string"
	fields["model"] = "string"
	fields["context_length"] = "integer"
	fields["params"] = "table"
	fields["params_json"] = "string"
	// reasoning is the one supported legacy inline request parameter. New
	// configuration should place it below [models.<alias>.params].
	fields["reasoning"] = "table"
	fields["reasoning_effort"] = "string"
	for name, value := range models {
		path := "models." + name
		model, ok := toMap(value)
		if !ok {
			*diagnostics = append(*diagnostics, configError(path, "wrong_type", "expected table"))
			continue
		}
		validateKnownMap(model, path, fields, diagnostics)
	}
}

func cacheFieldTypes() map[string]string {
	return map[string]string{
		"cache_enabled": "bool", "cache_key_name": "string", "cache_control": "table",
		"cache_trigger_threshold": "integer", "cache_lookback_offset": "integer", "cache_reanchor_tokens": "integer",
		"cache_reanchor_messages": "integer", "cache_reanchor_min_cached_tokens": "integer",
	}
}

func validateModeRaw(raw map[string]any, diagnostics *[]Diagnostic) {
	mode, ok := toMap(raw["mode"])
	if !ok {
		return
	}
	validateKnownMap(mode, "mode", map[string]string{"instruction_dir": "string", "modes": "table"}, diagnostics)
	modes, ok := toMap(mode["modes"])
	if !ok {
		return
	}
	for name, value := range modes {
		entry, ok := toMap(value)
		if !ok {
			*diagnostics = append(*diagnostics, configError("mode.modes."+name, "wrong_type", "expected table"))
			continue
		}
		validateKnownMap(entry, "mode.modes."+name, map[string]string{"instruction_file": "string"}, diagnostics)
	}
}

func matchesConfigType(value any, want string) bool {
	switch want {
	case "string":
		_, ok := value.(string)
		return ok
	case "bool":
		_, ok := value.(bool)
		return ok
	case "integer":
		_, ok := toInt(value)
		return ok
	case "table":
		_, ok := toMap(value)
		return ok
	case "template":
		if _, ok := value.(string); ok {
			return true
		}
		_, ok := toMap(value)
		return ok
	default:
		return false
	}
}

func joinConfigPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func copyFieldTypes(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// ValidationOptions controls command-specific semantic checks.
type ValidationOptions struct {
	RequireAllCredentials bool
	RequireDefaultModel   bool
}

// ValidateConfig performs whole-file semantic validation.
func ValidateConfig(cfg Config, path string, options ValidationOptions) []Diagnostic {
	var diagnostics []Diagnostic
	checkAlias := func(path, alias string) {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return
		}
		if _, ok := cfg.Models[alias]; !ok {
			diagnostics = append(diagnostics, configError(path, "invalid_reference", fmt.Sprintf("model alias %q is not defined", alias)))
		}
	}
	checkAlias("default_model", cfg.DefaultModel)
	if options.RequireDefaultModel && strings.TrimSpace(cfg.DefaultModel) == "" {
		diagnostics = append(diagnostics, configError("default_model", "missing_field", "default_model is required"))
	}
	checkAlias("answer_model", cfg.AnswerModel)
	checkAlias("shell_agent_model", cfg.ShellAgentModel)
	checkAlias("file_discovery_model", cfg.FileDiscoveryModel)
	if cfg.ModelDefaults != nil {
		validateContextLength("model_defaults.context_length", cfg.ModelDefaults.ContextLength, &diagnostics)
		validateNonnegativeCacheFields("model_defaults", cfg.ModelDefaults.CacheTriggerThreshold, cfg.ModelDefaults.CacheLookbackOffset,
			cfg.ModelDefaults.CacheReanchorTokens, cfg.ModelDefaults.CacheReanchorMessages, cfg.ModelDefaults.CacheReanchorMinCachedTokens, &diagnostics)
	}

	for name, model := range cfg.Models {
		modelPath := "models." + name
		if strings.TrimSpace(name) == "" {
			diagnostics = append(diagnostics, configError("models", "invalid_name", "model alias cannot be empty"))
		}
		providerName := strings.TrimSpace(model.Provider)
		if providerName == "" {
			diagnostics = append(diagnostics, configError(modelPath+".provider", "missing_field", "provider is required"))
		} else if _, ok := cfg.Providers[providerName]; !ok {
			diagnostics = append(diagnostics, configError(modelPath+".provider", "invalid_reference", fmt.Sprintf("provider %q is not defined", providerName)))
		}
		if strings.TrimSpace(model.Model) == "" {
			diagnostics = append(diagnostics, configError(modelPath+".model", "missing_field", "model is required"))
		}
		if model.contextLengthSet || model.ContextLength != 0 {
			validateContextLength(modelPath+".context_length", model.ContextLength, &diagnostics)
		}
		validateNonnegativeModelFields(modelPath, model, &diagnostics)
		if _, err := resolveEffectiveCache(cfg.ModelDefaults, model, name, path); err != nil {
			diagnostics = append(diagnostics, configError(modelPath, "invalid_cache", err.Error()))
		}
		if options.RequireAllCredentials && providerName != "" {
			if provider, ok := cfg.Providers[providerName]; ok {
				if strings.TrimSpace(provider.Transport) != "model-host" {
					if err := validateProviderCredential(name, providerName, provider.APIKey); err != nil {
						diagnostics = append(diagnostics, configError("providers."+providerName+".api_key", "missing_credential", fmt.Sprintf("model %q: %v", name, err)))
					}
				}
			}
		}
	}

	for name, provider := range cfg.Providers {
		providerPath := "providers." + name
		if strings.TrimSpace(name) == "" {
			diagnostics = append(diagnostics, configError("providers", "invalid_name", "provider name cannot be empty"))
		}
		transport := strings.TrimSpace(provider.Transport)
		if transport == "" {
			transport = "openai-chat"
		}
		if transport != "openai-chat" && transport != "model-host" {
			diagnostics = append(diagnostics, configError(providerPath+".transport", "invalid_value", "must be openai-chat or model-host"))
		}
		if transport == "model-host" {
			if strings.TrimSpace(provider.Profile) == "" {
				diagnostics = append(diagnostics, configError(providerPath+".profile", "missing_field", "profile is required for model-host transport"))
			}
			if strings.TrimSpace(provider.APIKey) != "" {
				diagnostics = append(diagnostics, configError(providerPath+".api_key", "invalid_value", "model-host credentials belong in the referenced private profile"))
			}
			continue
		}
		baseURL := strings.TrimSpace(provider.BaseURL)
		if baseURL == "" {
			diagnostics = append(diagnostics, configError(providerPath+".base_url", "missing_field", "base_url is required"))
		} else if parsed, err := url.Parse(baseURL); err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			diagnostics = append(diagnostics, configError(providerPath+".base_url", "invalid_url", "must be an absolute HTTP(S) URL"))
		}
		if endpoint := strings.TrimSpace(provider.Endpoint); endpoint != "" {
			if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
				parsed, err := url.Parse(endpoint)
				if err != nil || parsed.Host == "" {
					diagnostics = append(diagnostics, configError(providerPath+".endpoint", "invalid_url", "absolute endpoint must be a valid HTTP(S) URL"))
				}
			} else if !strings.HasPrefix(endpoint, "/") {
				diagnostics = append(diagnostics, configError(providerPath+".endpoint", "invalid_value", "endpoint must start with / or be an absolute HTTP(S) URL"))
			}
		}
		if strings.TrimSpace(provider.APIKey) != "" {
			if _, _, err := resolveConfiguredAPIKey(provider.APIKey); err != nil {
				diagnostics = append(diagnostics, configError(providerPath+".api_key", "invalid_placeholder", err.Error()))
			}
		}
		if format := strings.TrimSpace(provider.ReasoningFormat); format != "" && format != reasoningFormatEffort && format != reasoningFormatObject && format != reasoningFormatExplicit {
			diagnostics = append(diagnostics, configError(providerPath+".reasoning_format", "invalid_value", "must be reasoning_effort, reasoning, or reasoning_explicit"))
		}
	}

	if cfg.Planner != nil {
		if cfg.Planner.MaxTurns <= 0 {
			diagnostics = append(diagnostics, configError("planner.max_turns", "invalid_value", "must be positive"))
		}
		validateNonnegative("planner.turn_timeout", cfg.Planner.TurnTimeout, &diagnostics)
	}
	if cfg.ShellAgent != nil {
		if cfg.ShellAgent.MaxSteps <= 0 {
			diagnostics = append(diagnostics, configError("shell-agent.max_steps", "invalid_value", "must be positive"))
		}
		if cfg.ShellAgent.FinalizeRemainingSteps < 0 || cfg.ShellAgent.FinalizeRemainingSteps > cfg.ShellAgent.MaxSteps {
			diagnostics = append(diagnostics, configError("shell-agent.finalize_remaining_steps", "invalid_value", "must be between zero and max_steps"))
		}
		validateNonnegative("shell-agent.command_supervisor_after", cfg.ShellAgent.CommandSupervisorAfter, &diagnostics)
		if cfg.ShellAgent.CommandSupervisorAfter > 0 {
			if cfg.ShellAgent.CommandSupervisorTimeout <= 0 {
				diagnostics = append(diagnostics, configError("shell-agent.command_supervisor_timeout", "invalid_value", "must be positive when command supervision is enabled"))
			}
			if cfg.ShellAgent.CommandSupervisorFailureLimit <= 0 {
				diagnostics = append(diagnostics, configError("shell-agent.command_supervisor_failure_limit", "invalid_value", "must be positive when command supervision is enabled"))
			}
			if cfg.ShellAgent.CommandSupervisorMaxSteps <= 0 {
				diagnostics = append(diagnostics, configError("shell-agent.command_supervisor_max_steps", "invalid_value", "must be positive when command supervision is enabled"))
			}
			if cfg.ShellAgent.CommandSupervisorDeadlineBuffer < cfg.ShellAgent.CommandSupervisorTimeout {
				diagnostics = append(diagnostics, configError("shell-agent.command_supervisor_deadline_buffer", "invalid_value", "must be at least command_supervisor_timeout"))
			}
			if cfg.Environment != nil && cfg.ShellAgent.CommandSupervisorDeadlineBuffer >= cfg.Environment.CommandTimeout {
				diagnostics = append(diagnostics, configError("shell-agent.command_supervisor_deadline_buffer", "invalid_value", "must be less than environment.command_timeout"))
			}
		}
	}
	if cfg.Environment != nil {
		if strings.TrimSpace(cfg.Environment.Type) != "local" {
			diagnostics = append(diagnostics, configError("environment.type", "invalid_value", "only local is supported"))
		}
		validateNonnegative("environment.command_timeout", cfg.Environment.CommandTimeout, &diagnostics)
		if cfg.Environment.MaxCommandOutputBytes <= 0 {
			diagnostics = append(diagnostics, configError("environment.max_command_output_bytes", "invalid_value", "must be positive"))
		}
	}
	if cfg.Trajectory != nil {
		validateNonnegative("trajectory.excerpt", cfg.Trajectory.Excerpt, &diagnostics)
	}
	validateModePaths(cfg.Mode, path, &diagnostics)
	validatePromptTemplates(cfg.Prompts, &diagnostics)
	sortDiagnostics(diagnostics)
	return diagnostics
}

func validateProviderCredential(modelAlias, providerName, configured string) error {
	if configKey := strings.TrimSpace(configured); configKey != "" {
		resolved, envName, err := resolveConfiguredAPIKey(configKey)
		if err != nil {
			return err
		}
		if envName != "" && strings.TrimSpace(resolved) == "" {
			return fmt.Errorf("configured environment variable %s is unset or empty", envName)
		}
		if strings.TrimSpace(resolved) != "" {
			return nil
		}
	}
	envCandidates := []string{providerEnvVarName(providerName)}
	if aliasEnv := providerEnvVarName(modelAlias); aliasEnv != "" && aliasEnv != envCandidates[0] {
		envCandidates = append(envCandidates, aliasEnv)
	}
	for _, envName := range envCandidates {
		if strings.TrimSpace(os.Getenv(envName)) != "" {
			return nil
		}
	}
	hint := providerEnvVarName(providerName)
	if hint == "" {
		hint = "<PROVIDER>_API_KEY"
	}
	return fmt.Errorf("missing credential; set %s, configure api_key, or use --api-key", hint)
}

func validateModePaths(mode *ModeConfig, configPath string, diagnostics *[]Diagnostic) {
	if mode == nil {
		return
	}
	configDir := filepath.Dir(configPath)
	if strings.TrimSpace(mode.InstructionDir) != "" {
		candidates, err := resolveModeInstructionDir(mode.InstructionDir, configDir)
		if err != nil || !hasExistingPath(candidates, true) {
			*diagnostics = append(*diagnostics, configError("mode.instruction_dir", "unreadable_path", "configured instruction directory does not exist or is not readable"))
		}
	}
	seen := make(map[string]struct{})
	for name, override := range mode.Modes {
		if _, ok := seen[strings.ToLower(name)]; ok {
			continue
		}
		seen[strings.ToLower(name)] = struct{}{}
		if strings.TrimSpace(override.InstructionFile) == "" {
			continue
		}
		candidates, err := resolveModeInstructionFile(override.InstructionFile, configDir)
		if err != nil || !hasExistingPath(candidates, false) {
			*diagnostics = append(*diagnostics, configError("mode.modes."+name+".instruction_file", "unreadable_path", "configured instruction file does not exist or is not readable"))
		}
	}
}

func hasExistingPath(candidates []string, wantDirectory bool) bool {
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() == wantDirectory {
			return true
		}
	}
	return false
}

func validateNonnegative(path string, value int, diagnostics *[]Diagnostic) {
	if value < 0 {
		*diagnostics = append(*diagnostics, configError(path, "invalid_value", "must be zero or positive"))
	}
}

func validateContextLength(path string, value int, diagnostics *[]Diagnostic) {
	if value < 4096 {
		*diagnostics = append(*diagnostics, configError(path, "invalid_value", "must be at least 4096 total tokens"))
	}
}

func validateNonnegativeModelFields(path string, model ModelDefinition, diagnostics *[]Diagnostic) {
	validateNonnegativeCacheFields(path, model.CacheTriggerThreshold, model.CacheLookbackOffset, model.CacheReanchorTokens,
		model.CacheReanchorMessages, model.CacheReanchorMinCachedTokens, diagnostics)
}

func validateNonnegativeCacheFields(path string, trigger, lookback, reanchorTokens, reanchorMessages, minCachedTokens int, diagnostics *[]Diagnostic) {
	fields := []struct {
		name  string
		value int
	}{
		{"cache_trigger_threshold", trigger}, {"cache_lookback_offset", lookback},
		{"cache_reanchor_tokens", reanchorTokens}, {"cache_reanchor_messages", reanchorMessages},
		{"cache_reanchor_min_cached_tokens", minCachedTokens},
	}
	for _, field := range fields {
		validateNonnegative(path+"."+field.name, field.value, diagnostics)
	}
}

func validatePromptTemplates(prompts *PromptsConfig, diagnostics *[]Diagnostic) {
	if prompts == nil {
		return
	}
	funcs := template.FuncMap{"trim": strings.TrimSpace, "join": strings.Join}
	check := func(path, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		if _, err := template.New(path).Funcs(funcs).Parse(value); err != nil {
			*diagnostics = append(*diagnostics, configError(path, "invalid_template", err.Error()))
		}
	}
	if prompts.Planner != nil {
		p := prompts.Planner
		check("prompts.planner.system_template", p.SystemTemplate)
		check("prompts.planner.instance_template", p.InstanceTemplate)
		check("prompts.planner.timeout_template", p.TimeoutTemplate)
		check("prompts.planner.format_error_template", p.FormatErrorTemplate)
		check("prompts.planner.ask_prompt", p.AskPrompt)
		check("prompts.planner.plan_system_prompt", p.PlanSystemPrompt)
		check("prompts.planner.plan_prompt", p.PlanPrompt)
		check("prompts.planner.finalize_prompt", p.FinalizePrompt)
		check("prompts.planner.review_prompt", p.ReviewPrompt)
	}
	if prompts.ShellAgent != nil {
		p := prompts.ShellAgent
		check("prompts.shell-agent.system_template", p.SystemTemplate)
		check("prompts.shell-agent.instance_template", p.InstanceTemplate)
		check("prompts.shell-agent.timeout_template", p.TimeoutTemplate)
		check("prompts.shell-agent.format_error_template", p.FormatErrorTemplate)
		check("prompts.shell-agent.action_observation_template", p.ActionObservationTemplate)
		check("prompts.shell-agent.lightweight_system_template", p.LightweightSystemTemplate)
		check("prompts.shell-agent.lightweight_intent_template", p.LightweightIntentTemplate)
		check("prompts.shell-agent.lightweight_error_template", p.LightweightErrorTemplate)
	}
	if prompts.FileDiscovery != nil {
		check("prompts.file_discovery.system_prompt_template", prompts.FileDiscovery.SystemPromptTemplate)
	}
	if prompts.MCT != nil {
		p := prompts.MCT
		check("prompts.mct.shell_agent_context_prefix", p.ShellAgentContextPrefix)
		check("prompts.mct.shell_agent_prompt_notice", p.ShellAgentPromptNotice)
		check("prompts.mct.header_user_template", p.HeaderUserTemplate)
		check("prompts.mct.header_existing_template", p.HeaderExistingTemplate)
		check("prompts.mct.conversation_history_template", p.ConversationHistoryTemplate)
		check("prompts.mct.readme_system_template", p.ReadmeSystemTemplate)
		check("prompts.mct.shell_agent_context_template", p.ShellAgentContextTemplate)
		check("prompts.mct.full_diff_note", p.FullDiffNote)
	}
}

// ValidateConfigError returns an aggregate error for semantic diagnostics.
func ValidateConfigError(cfg Config, path string, options ValidationOptions) error {
	return diagnosticsError(path, ValidateConfig(cfg, path, options))
}
