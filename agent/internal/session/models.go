package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	promptsvc "github.com/tursomari/machtiani/agent/internal/mct/prompt"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

type modelRuntime struct {
	resolved         llm.ResolvedModel
	alias            string
	usingAlias       bool
	extras           map[string]any
	paramPairs       []string
	paramJSON        []string
	fallbackAliases  []string
	fallbackResolved []llm.ResolvedModel
}

func (m modelRuntime) displayLabel() string {
	alias := strings.TrimSpace(m.alias)
	resolvedModel := strings.TrimSpace(m.resolved.Model)
	provider := strings.TrimSpace(m.resolved.ProviderName)
	var providerModel string
	switch {
	case provider != "" && resolvedModel != "":
		providerModel = fmt.Sprintf("%s:%s", provider, resolvedModel)
	case resolvedModel != "":
		providerModel = resolvedModel
	case provider != "":
		providerModel = provider
	}
	switch {
	case alias != "" && providerModel != "" && !strings.EqualFold(alias, providerModel):
		return fmt.Sprintf("%s (%s)", alias, providerModel)
	case alias != "":
		return alias
	case providerModel != "":
		return providerModel
	}
	baseURL := strings.TrimSpace(m.resolved.BaseURL)
	if baseURL != "" {
		return baseURL
	}
	return ""
}

type componentModelRuntimes struct {
	orchestrator  modelRuntime
	answer        modelRuntime
	patcher       modelRuntime
	fileDiscovery modelRuntime
	shellAgent    modelRuntime
}

// PromptRuntimes exposes resolved model runtimes for callers outside the
// session package without leaking internal types.
type PromptRuntimes struct {
	Orchestrator  promptsvc.ModelRuntime
	Answer        promptsvc.ModelRuntime
	Patcher       promptsvc.ModelRuntime
	FileDiscovery promptsvc.ModelRuntime
	ShellAgent    promptsvc.ModelRuntime
}

// ResolvePromptRuntimes returns the resolved model runtimes used for prompt
// execution based on the supplied session config and parameter overrides.
func ResolvePromptRuntimes(cfg Config, global llm.Config, paramPairs, paramJSON []string) (PromptRuntimes, error) {
	models, err := resolveModelRuntimes(newLegacyConfig(cfg), global, paramPairs, paramJSON)
	if err != nil {
		return PromptRuntimes{}, err
	}
	return PromptRuntimes{
		Orchestrator:  models.orchestrator.toPromptRuntime(),
		Answer:        models.answer.toPromptRuntime(),
		Patcher:       models.patcher.toPromptRuntime(),
		FileDiscovery: models.fileDiscovery.toPromptRuntime(),
		ShellAgent:    models.shellAgent.toPromptRuntime(),
	}, nil
}

func (m modelRuntime) toPromptRuntime() promptsvc.ModelRuntime {
	return promptsvc.ModelRuntime{
		Resolved:         llm.CloneResolvedModel(m.resolved),
		Alias:            m.alias,
		UsingAlias:       m.usingAlias,
		Extras:           copyExtras(m.extras),
		ParamPairs:       append([]string(nil), m.paramPairs...),
		ParamJSON:        append([]string(nil), m.paramJSON...),
		FallbackAliases:  append([]string(nil), m.fallbackAliases...),
		FallbackResolved: cloneResolvedModels(m.fallbackResolved),
	}
}

func cloneModelRuntime(m modelRuntime) modelRuntime {
	return modelRuntime{
		resolved:         llm.CloneResolvedModel(m.resolved),
		alias:            m.alias,
		usingAlias:       m.usingAlias,
		extras:           copyExtras(m.extras),
		paramPairs:       append([]string(nil), m.paramPairs...),
		paramJSON:        append([]string(nil), m.paramJSON...),
		fallbackAliases:  append([]string(nil), m.fallbackAliases...),
		fallbackResolved: cloneResolvedModels(m.fallbackResolved),
	}
}

func ensureFallbackToPrimary(target *modelRuntime, primary modelRuntime) {
	if target == nil {
		return
	}
	target.fallbackAliases = prependAliasIfMissing(target.fallbackAliases, primary.alias)
	target.fallbackResolved = prependResolvedModel(target.fallbackResolved, primary.resolved)
}

func copyExtras(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneResolvedModels(in []llm.ResolvedModel) []llm.ResolvedModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]llm.ResolvedModel, 0, len(in))
	for _, m := range in {
		out = append(out, llm.CloneResolvedModel(m))
	}
	return out
}

func prependAliasIfMissing(list []string, alias string) []string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return list
	}
	for _, existing := range list {
		if strings.EqualFold(strings.TrimSpace(existing), alias) {
			return list
		}
	}
	return append([]string{alias}, list...)
}

func prependResolvedModel(list []llm.ResolvedModel, model llm.ResolvedModel) []llm.ResolvedModel {
	if strings.TrimSpace(model.Model) == "" {
		return list
	}
	for _, existing := range list {
		if resolvedModelsEqual(existing, model) {
			return list
		}
	}
	return append([]llm.ResolvedModel{llm.CloneResolvedModel(model)}, list...)
}

func resolvedModelsEqual(a, b llm.ResolvedModel) bool {
	return strings.EqualFold(strings.TrimSpace(a.BaseURL), strings.TrimSpace(b.BaseURL)) &&
		strings.EqualFold(strings.TrimSpace(a.Endpoint), strings.TrimSpace(b.Endpoint)) &&
		strings.EqualFold(strings.TrimSpace(a.Model), strings.TrimSpace(b.Model))
}

func describeModel(system string, runtime modelRuntime) string {
	label := strings.TrimSpace(runtime.displayLabel())
	if label == "" {
		return ""
	}
	return fmt.Sprintf("%s model: %s", system, label)
}

func promptOptions(lines ...string) *ui.PromptOptions {
	var meta []string
	for _, line := range lines {
		clean := strings.TrimSpace(line)
		if clean == "" {
			continue
		}
		meta = append(meta, clean)
	}
	if len(meta) == 0 {
		return nil
	}
	return &ui.PromptOptions{Metadata: meta}
}

func resolveModelRuntimes(cfg legacyConfig, global llm.Config, paramPairs, paramJSON []string) (componentModelRuntimes, error) {
	extras, err := llm.ParseParamOverrides(paramPairs, paramJSON)
	if err != nil {
		return componentModelRuntimes{}, err
	}
	primary := modelRuntime{
		extras:     extras,
		paramPairs: append([]string(nil), paramPairs...),
		paramJSON:  append([]string(nil), paramJSON...),
	}

	orchAlias := firstNonEmpty(
		strings.TrimSpace(cfg.orchModel),
		strings.TrimSpace(os.Getenv("MCT_ORCH_MODEL")),
		strings.TrimSpace(os.Getenv("MCT_MODEL")),
		strings.TrimSpace(cfg.agentModel),
	)

	cliAPIKey := strings.TrimSpace(cfg.openAIAPIKey)
	cliBaseURL := strings.TrimSpace(cfg.openAIBaseURL)
	cliModel := strings.TrimSpace(cfg.openAIModel)

	envAPIKey := firstNonEmpty(strings.TrimSpace(os.Getenv("OPENAI_API_KEY")), strings.TrimSpace(os.Getenv("AGENT_MODEL_API_KEY")))
	envBaseURL := firstNonEmpty(strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), strings.TrimSpace(os.Getenv("AGENT_MODEL_BASE_URL")))
	envModel := firstNonEmpty(strings.TrimSpace(os.Getenv("OPENAI_MODEL")), strings.TrimSpace(os.Getenv("AGENT_MODEL")))

	configAPIKey, configBaseURL, configModel := configModelDefaults(global)

	directAPIKey := strings.TrimSpace(firstNonEmpty(envAPIKey, cliAPIKey, configAPIKey))
	directBaseURL := strings.TrimSpace(firstNonEmpty(envBaseURL, cliBaseURL, configBaseURL))
	directModel := strings.TrimSpace(firstNonEmpty(envModel, cliModel, configModel))

	explicitDirect := cliAPIKey != "" || cliBaseURL != "" || cliModel != ""
	missingDirectValues := missingDirect(directAPIKey, directBaseURL, directModel)

	switch {
	case explicitDirect:
		if len(missingDirectValues) > 0 {
			return componentModelRuntimes{}, &missingConfigError{items: missingDirectValues}
		}
		resolved, err := llm.NewDirectModel(directBaseURL, directAPIKey, directModel)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		primary.resolved = resolved
		primary.usingAlias = false
	case strings.TrimSpace(orchAlias) != "":
		resolved, err := llm.ResolveModel(orchAlias)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		primary.resolved = resolved
		primary.alias = orchAlias
		primary.usingAlias = true
	default:
		if defaultAlias, err := llm.DefaultModelAlias(); err == nil {
			if resolved, err2 := llm.ResolveModel(defaultAlias); err2 == nil {
				primary.resolved = resolved
				primary.alias = defaultAlias
				primary.usingAlias = true
			}
		}
	}

	if strings.TrimSpace(primary.resolved.Model) == "" {
		if len(missingDirectValues) > 0 {
			return componentModelRuntimes{}, &missingConfigError{items: missingDirectValues}
		}
		resolved, err := llm.NewDirectModel(directBaseURL, directAPIKey, directModel)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		primary.resolved = resolved
		primary.usingAlias = false
	}

	applyFallbacks(&primary, []string{
		cfg.agentModel,
		cfg.patcherModel,
		cfg.fileDiscoveryModel,
		cfg.answerModel,
		os.Getenv("MCT_MODEL"),
		os.Getenv("MCT_ORCH_MODEL"),
		os.Getenv("MCT_ANSWER_MODEL"),
	}, directBaseURL, directAPIKey, directModel)

	patcher := cloneModelRuntime(primary)
	patcherAlias := firstNonEmpty(strings.TrimSpace(cfg.patcherModel), strings.TrimSpace(os.Getenv("MCT_PATCHER_MODEL")))
	if strings.TrimSpace(patcherAlias) != "" {
		resolved, err := llm.ResolveModel(patcherAlias)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		patcher.resolved = resolved
		patcher.alias = patcherAlias
		patcher.usingAlias = true
	}

	fileDiscovery := cloneModelRuntime(primary)
	fdAlias := firstNonEmpty(strings.TrimSpace(cfg.fileDiscoveryModel), strings.TrimSpace(os.Getenv("MCT_FILE_DISCOVERY_MODEL")))
	if strings.TrimSpace(fdAlias) != "" {
		resolved, err := llm.ResolveModel(fdAlias)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		fileDiscovery.resolved = resolved
		fileDiscovery.alias = fdAlias
		fileDiscovery.usingAlias = true
	}
	ensureFallbackToPrimary(&patcher, primary)
	ensureFallbackToPrimary(&fileDiscovery, primary)

	answer := cloneModelRuntime(primary)
	answerAlias := firstNonEmpty(strings.TrimSpace(cfg.answerModel), strings.TrimSpace(os.Getenv("MCT_ANSWER_MODEL")))
	if strings.TrimSpace(answerAlias) != "" {
		resolved, err := llm.ResolveModel(answerAlias)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		answer.resolved = resolved
		answer.alias = answerAlias
		answer.usingAlias = true
		answer.fallbackAliases = nil
		answer.fallbackResolved = nil
	}
	ensureFallbackToPrimary(&answer, primary)

	shellAgent := modelRuntime{}
	if cfg.shellAgent || strings.TrimSpace(cfg.shellAgentModel) != "" {
		sr, err := resolveShellAgentRuntime(global, cfg.shellAgentModel)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		shellAgent = sr
	}

	return componentModelRuntimes{
		orchestrator:  primary,
		answer:        answer,
		patcher:       patcher,
		fileDiscovery: fileDiscovery,
		shellAgent:    shellAgent,
	}, nil
}

func resolveShellAgentRuntime(global llm.Config, override string) (modelRuntime, error) {
	var rt modelRuntime
	trimmedOverride := strings.TrimSpace(override)

	var baseURL, endpoint, modelName string
	if global.Model != nil {
		modelName = strings.TrimSpace(global.Model.ModelName)
		if trimmedOverride == "" && len(global.Model.ModelKwargs) > 0 {
			baseURL = trimAny(global.Model.ModelKwargs["base_url"])
			endpoint = trimAny(global.Model.ModelKwargs["endpoint"])
			if modelName == "" {
				modelName = trimAny(global.Model.ModelKwargs["model"])
			}
		}
		if modelName == "" {
			modelName = trimmedOverride
		}
	}

	alias := trimmedOverride
	if alias == "" {
		alias = modelName
	}

	if baseURL != "" {
		if endpoint == "" {
			endpoint = "/chat/completions"
		}
		resolved := llm.ResolvedModel{
			Alias:        strings.TrimSpace(alias),
			ProviderName: "direct",
			BaseURL:      baseURL,
			Endpoint:     endpoint,
			Model:        strings.TrimSpace(modelName),
			Params:       map[string]any{},
		}
		rt.resolved = resolved
		if strings.TrimSpace(alias) != "" {
			rt.alias = strings.TrimSpace(alias)
			rt.usingAlias = true
		}
		return rt, nil
	}

	if strings.TrimSpace(alias) == "" {
		defaultAlias, err := llm.DefaultModelAlias()
		if err != nil {
			return modelRuntime{}, fmt.Errorf("resolve default shell agent model: %w", err)
		}
		alias = strings.TrimSpace(defaultAlias)
		if alias == "" {
			return modelRuntime{}, fmt.Errorf("resolve default shell agent model: empty alias")
		}
	}

	resolved, err := llm.ResolveModel(alias)
	if err != nil {
		return modelRuntime{}, fmt.Errorf("resolve shell agent model %q: %w", alias, err)
	}
	rt.resolved = resolved
	rt.alias = alias
	rt.usingAlias = true
	return rt, nil
}

func trimAny(val any) string {
	switch v := val.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func missingDirect(apiKey, baseURL, model string) []string {
	var missing []string
	if strings.TrimSpace(apiKey) == "" {
		missing = append(missing, "--openai-api-key or OPENAI_API_KEY")
	}
	if strings.TrimSpace(baseURL) == "" {
		missing = append(missing, "--openai-base-url or OPENAI_BASE_URL")
	}
	if strings.TrimSpace(model) == "" {
		missing = append(missing, "--openai-model or OPENAI_MODEL")
	}
	return missing
}

type missingConfigError struct {
	items []string
}

func (e *missingConfigError) Error() string {
	return "missing model configuration"
}

// MissingConfigItems unwraps a missing configuration error and returns the
// actionable flag/env suggestions when available.
func MissingConfigItems(err error) ([]string, bool) {
	if err == nil {
		return nil, false
	}
	var miss *missingConfigError
	if errors.As(err, &miss) {
		return append([]string(nil), miss.items...), true
	}
	return nil, false
}

func applyFallbacks(rt *modelRuntime, candidates []string, directBase, directKey, directModel string) {
	if rt == nil {
		return
	}
	primaryAlias := strings.TrimSpace(rt.alias)
	seen := make(map[string]struct{}, len(candidates))
	rt.fallbackAliases = nil
	for _, cand := range candidates {
		trimmed := strings.TrimSpace(cand)
		if trimmed == "" {
			continue
		}
		if primaryAlias != "" && strings.EqualFold(primaryAlias, trimmed) {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		rt.fallbackAliases = append(rt.fallbackAliases, trimmed)
	}
	trimmedModel := strings.TrimSpace(directModel)
	trimmedBase := strings.TrimSpace(directBase)
	trimmedKey := strings.TrimSpace(directKey)
	if trimmedModel != "" && trimmedBase != "" && trimmedKey != "" {
		if resolved, err := llm.NewDirectModel(trimmedBase, trimmedKey, trimmedModel); err == nil {
			rt.fallbackResolved = append(rt.fallbackResolved, resolved)
		}
	}
}

func configModelDefaults(global llm.Config) (apiKey, baseURL, model string) {
	if global.Model == nil {
		return "", "", ""
	}
	apiKey = strings.TrimSpace(global.Model.APIKey)
	if kwargs := global.Model.ModelKwargs; kwargs != nil {
		if v, ok := kwargs["api_key"].(string); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				apiKey = trimmed
			}
		}
		if v, ok := kwargs["base_url"].(string); ok {
			baseURL = strings.TrimSpace(v)
		}
		if v, ok := kwargs["model"].(string); ok {
			model = strings.TrimSpace(v)
		}
	}
	if model == "" {
		model = strings.TrimSpace(global.Model.ModelName)
	}
	return apiKey, baseURL, model
}

func resolveFileDiscoveryTrajectory(cfg legacyConfig, sessionID string) (string, error) {
	override := strings.TrimSpace(cfg.fileDiscoveryTrajectory)
	outDir := strings.TrimSpace(cfg.fileDiscoveryOutputDir)
	if override != "" && outDir != "" {
		return "", errors.New("cannot combine --file-discovery-trajectory with --file-discovery-output-dir")
	}
	if override != "" {
		path := override
		if !filepath.IsAbs(path) {
			abs, err := filepath.Abs(path)
			if err != nil {
				return "", fmt.Errorf("resolve file-discovery trajectory path: %w", err)
			}
			path = abs
		}
		if !cfg.dryRun {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", fmt.Errorf("create file-discovery trajectory directory: %w", err)
			}
		}
		return path, nil
	}

	dir := outDir
	if dir == "" {
		path, err := artifacts.FileDiscoveryTrajectoryPath(sessionID)
		if err != nil {
			return "", err
		}
		if cfg.dryRun {
			return path, nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", fmt.Errorf("create file-discovery output dir: %w", err)
		}
		return path, nil
	}
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("resolve file-discovery output dir: %w", err)
		}
		dir = abs
	}
	if !cfg.dryRun {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create file-discovery output dir: %w", err)
		}
	}
	name := "file-discovery.jsonl"
	return filepath.Join(dir, name), nil
}
