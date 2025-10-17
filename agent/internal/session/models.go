package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/mct/llm"
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

func resolveModelRuntimes(cfg legacyConfig, paramPairs, paramJSON []string) (componentModelRuntimes, error) {
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

	hasDirectFlags := strings.TrimSpace(cfg.openAIAPIKey) != "" || strings.TrimSpace(cfg.openAIBaseURL) != "" || strings.TrimSpace(cfg.openAIModel) != ""

	directAPIKey := firstNonEmpty(strings.TrimSpace(cfg.openAIAPIKey), strings.TrimSpace(os.Getenv("OPENAI_API_KEY")), strings.TrimSpace(os.Getenv("AGENT_MODEL_API_KEY")))
	directBaseURL := firstNonEmpty(strings.TrimSpace(cfg.openAIBaseURL), strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), strings.TrimSpace(os.Getenv("AGENT_MODEL_BASE_URL")))
	directModel := firstNonEmpty(strings.TrimSpace(cfg.openAIModel), strings.TrimSpace(os.Getenv("OPENAI_MODEL")), strings.TrimSpace(os.Getenv("AGENT_MODEL")))

	switch {
	case hasDirectFlags:
		missing := missingDirect(directAPIKey, directBaseURL, directModel)
		if len(missing) > 0 {
			return componentModelRuntimes{}, &missingConfigError{items: missing}
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
		missing := missingDirect(directAPIKey, directBaseURL, directModel)
		if len(missing) > 0 {
			return componentModelRuntimes{}, &missingConfigError{items: missing}
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

	return componentModelRuntimes{
		orchestrator:  primary,
		answer:        answer,
		patcher:       patcher,
		fileDiscovery: fileDiscovery,
	}, nil
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
