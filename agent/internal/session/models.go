package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	promptsvc "github.com/tursomari/machtiani/agent/internal/core/prompt"
	"github.com/tursomari/machtiani/agent/internal/llm"
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
	apiKeyOverrides  map[string]string
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

func (m modelRuntime) footerLabel() string {
	return FooterModelLabel(m.resolved)
}

// FooterModelLabel returns the compact provider/model label shared by command
// footers.
func FooterModelLabel(resolved llm.ResolvedModel) string {
	resolvedModel := strings.TrimSpace(resolved.Model)
	provider := strings.TrimSpace(resolved.ProviderName)
	switch {
	case provider != "" && resolvedModel != "":
		return fmt.Sprintf("%s:%s", provider, resolvedModel)
	case resolvedModel != "":
		return resolvedModel
	case provider != "":
		return provider
	}
	return strings.TrimSpace(resolved.BaseURL)
}

type componentModelRuntimes struct {
	orchestrator  modelRuntime
	answer        modelRuntime
	fileDiscovery modelRuntime
	shellAgent    modelRuntime
}

func persistedModelSelection(models componentModelRuntimes, shellAgentAlias string) *conversation.ModelSelectionState {
	selection := &conversation.ModelSelectionState{
		OrchestratorAlias:  modelRuntimeAlias(models.orchestrator),
		AnswerAlias:        modelRuntimeAlias(models.answer),
		FileDiscoveryAlias: modelRuntimeAlias(models.fileDiscovery),
		ShellAgentAlias:    strings.TrimSpace(shellAgentAlias),
	}
	if selection.OrchestratorAlias == "" {
		selection.DirectModel = strings.TrimSpace(models.orchestrator.resolved.Model)
		selection.DirectBaseURL = strings.TrimSpace(models.orchestrator.resolved.BaseURL)
	}
	return selection
}

func modelRuntimeAlias(runtime modelRuntime) string {
	if !runtime.usingAlias {
		return ""
	}
	return strings.TrimSpace(runtime.alias)
}

// PromptRuntimes exposes resolved model runtimes for callers outside the
// session package without leaking internal types.
type PromptRuntimes struct {
	Orchestrator  promptsvc.ModelRuntime
	Answer        promptsvc.ModelRuntime
	FileDiscovery promptsvc.ModelRuntime
	ShellAgent    promptsvc.ModelRuntime
}

// ResolvePromptRuntimes returns the resolved model runtimes used for prompt
// execution based on the supplied session config and parameter overrides.
func ResolvePromptRuntimes(cfg Config, global llm.Config, paramPairs, paramJSON []string, apiKeyOverrides map[string]string) (PromptRuntimes, error) {
	models, err := resolveModelRuntimes(newLegacyConfig(cfg), global, paramPairs, paramJSON, apiKeyOverrides)
	if err != nil {
		return PromptRuntimes{}, err
	}
	return PromptRuntimes{
		Orchestrator:  models.orchestrator.toPromptRuntime(),
		Answer:        models.answer.toPromptRuntime(),
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
		APIKeyOverrides:  llm.CopyAPIKeyOverridesForRuntime(m.apiKeyOverrides),
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
		apiKeyOverrides:  llm.CopyAPIKeyOverridesForRuntime(m.apiKeyOverrides),
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

func footerModelMetadata(models componentModelRuntimes) ui.FooterModelMetadata {
	shellAgent := models.shellAgent
	if strings.TrimSpace(shellAgent.footerLabel()) == "" {
		shellAgent = models.orchestrator
	}
	return ui.FooterModelMetadata{
		Models: []ui.FooterModelDisplay{
			{Role: "planner", Label: models.orchestrator.footerLabel(), Reasoning: reasoningEffort(models.orchestrator)},
			{Role: "shell", Label: shellAgent.footerLabel(), Reasoning: reasoningEffort(shellAgent)},
		},
	}
}

// persistFooterMetadata records the run-only footer inputs in conversation.json
// so a later attach reproduces the resolved display without consulting its own
// environment. It returns whether the persisted record changed, which makes
// resume and future runtime changes safe to save without needless rewrites.
func persistFooterMetadata(conv *conversation.Conversation, cwd string, maxInputTokens int, mode string, models ui.FooterModelMetadata) bool {
	if conv == nil {
		return false
	}
	persistedCWD := strings.TrimSpace(cwd)
	if conv.Footer != nil && conv.Footer.CWD != "" {
		persistedCWD = conv.Footer.CWD
	}
	state := &conversation.FooterState{
		CWD:            persistedCWD,
		MaxInputTokens: maxInputTokens,
		Mode:           strings.TrimSpace(mode),
		Models:         make([]conversation.FooterModelState, 0, len(models.Models)),
	}
	if state.MaxInputTokens < 0 {
		state.MaxInputTokens = 0
	}
	for _, model := range models.Models {
		state.Models = append(state.Models, conversation.FooterModelState{
			Role:      strings.TrimSpace(model.Role),
			Label:     strings.TrimSpace(model.Label),
			Reasoning: strings.TrimSpace(model.Reasoning),
		})
	}
	if footerStatesEqual(conv.Footer, state) {
		return false
	}
	conv.Footer = state
	return true
}

func footerStatesEqual(a, b *conversation.FooterState) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.CWD != b.CWD || a.MaxInputTokens != b.MaxInputTokens || a.Mode != b.Mode || len(a.Models) != len(b.Models) {
		return false
	}
	for i := range a.Models {
		if a.Models[i] != b.Models[i] {
			return false
		}
	}
	return true
}

func reasoningEffort(runtime modelRuntime) string {
	if effort := reasoningEffortFromParams(runtime.extras); effort != "" {
		return effort
	}
	return reasoningEffortFromParams(runtime.resolved.Params)
}

// PromptRuntimeFooterReasoning returns the effective reasoning label for an
// observed prompt runtime. Runtime overrides win, followed by the actual model
// and the configured primary model.
func PromptRuntimeFooterReasoning(runtime promptsvc.ModelRuntime, actual llm.ResolvedModel) string {
	if effort := reasoningEffortFromParams(runtime.Extras); effort != "" {
		return effort
	}
	if effort := reasoningEffortFromParams(actual.Params); effort != "" {
		return effort
	}
	return reasoningEffortFromParams(runtime.Resolved.Params)
}

func reasoningEffortFromParams(params map[string]any) string {
	if len(params) == 0 {
		return ""
	}
	if effort := valueAsString(params["reasoning_effort"]); effort != "" {
		return effort
	}
	reasoning, ok := params["reasoning"]
	if !ok {
		return ""
	}
	switch v := reasoning.(type) {
	case map[string]any:
		return valueAsString(v["effort"])
	case map[string]string:
		return strings.TrimSpace(v["effort"])
	}
	return ""
}

func valueAsString(v any) string {
	switch typed := v.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
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

func resolveModelRuntimes(cfg legacyConfig, global llm.Config, paramPairs, paramJSON []string, apiKeyOverrides map[string]string) (componentModelRuntimes, error) {
	extras, err := llm.ParseParamOverrides(paramPairs, paramJSON)
	if err != nil {
		return componentModelRuntimes{}, err
	}
	primary := modelRuntime{
		extras:          extras,
		paramPairs:      append([]string(nil), paramPairs...),
		paramJSON:       append([]string(nil), paramJSON...),
		apiKeyOverrides: llm.CopyAPIKeyOverridesForRuntime(apiKeyOverrides),
	}

	orchAlias := firstNonEmpty(
		strings.TrimSpace(cfg.orchModel),
		strings.TrimSpace(os.Getenv("MACHTIANI_ORCH_MODEL")),
		strings.TrimSpace(os.Getenv("MACHTIANI_MODEL")),
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
		resolved, err := llm.ResolveModelWithOverrides(orchAlias, apiKeyOverrides)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		primary.resolved = resolved
		primary.alias = orchAlias
		primary.usingAlias = true
	default:
		if defaultAlias, err := llm.DefaultModelAlias(); err == nil {
			if resolved, err2 := llm.ResolveModelWithOverrides(defaultAlias, apiKeyOverrides); err2 == nil {
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
		cfg.fileDiscoveryModel,
		cfg.answerModel,
		os.Getenv("MACHTIANI_MODEL"),
		os.Getenv("MACHTIANI_ORCH_MODEL"),
		os.Getenv("MACHTIANI_ANSWER_MODEL"),
	}, directBaseURL, directAPIKey, directModel)

	fileDiscovery := cloneModelRuntime(primary)
	fdAlias := firstNonEmpty(strings.TrimSpace(cfg.fileDiscoveryModel), strings.TrimSpace(os.Getenv("MACHTIANI_FILE_DISCOVERY_MODEL")))
	if strings.TrimSpace(fdAlias) != "" {
		resolved, err := llm.ResolveModelWithOverrides(fdAlias, apiKeyOverrides)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		fileDiscovery.resolved = resolved
		fileDiscovery.alias = fdAlias
		fileDiscovery.usingAlias = true
	}
	ensureFallbackToPrimary(&fileDiscovery, primary)

	answer := cloneModelRuntime(primary)
	answerAlias := firstNonEmpty(strings.TrimSpace(cfg.answerModel), strings.TrimSpace(os.Getenv("MACHTIANI_ANSWER_MODEL")))
	if strings.TrimSpace(answerAlias) != "" {
		resolved, err := llm.ResolveModelWithOverrides(answerAlias, apiKeyOverrides)
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
		sr, err := resolveShellAgentRuntime(global, cfg.shellAgentModel, apiKeyOverrides, primary)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		shellAgent = sr
	}

	return componentModelRuntimes{
		orchestrator:  primary,
		answer:        answer,
		fileDiscovery: fileDiscovery,
		shellAgent:    shellAgent,
	}, nil
}

func resolveShellAgentRuntime(global llm.Config, override string, apiKeyOverrides map[string]string, primaryFallback modelRuntime) (modelRuntime, error) {
	var rt modelRuntime
	trimmedOverride := strings.TrimSpace(override)
	rt.apiKeyOverrides = llm.CopyAPIKeyOverridesForRuntime(apiKeyOverrides)

	alias := trimmedOverride
	if alias == "" {
		resolved := primaryFallback.resolved
		if strings.TrimSpace(resolved.Model) != "" || strings.TrimSpace(resolved.BaseURL) != "" || strings.TrimSpace(resolved.ProviderName) != "" {
			return cloneModelRuntime(primaryFallback), nil
		}
		defaultAlias, err := llm.DefaultModelAlias()
		if err != nil {
			return modelRuntime{}, fmt.Errorf("resolve default shell agent model: %w", err)
		}
		alias = strings.TrimSpace(defaultAlias)
		if alias == "" {
			return modelRuntime{}, fmt.Errorf("resolve default shell agent model: empty alias")
		}
	}

	resolved, err := llm.ResolveModelWithOverrides(alias, apiKeyOverrides)
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
	return "", "", ""
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
