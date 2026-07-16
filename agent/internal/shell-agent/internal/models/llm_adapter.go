package models

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

var chatWithResolvedFallback = llm.ChatWithResolvedFallback

type LLMAdapterModel struct {
	config          *minisweagent.ModelConfig
	resolved        llm.ResolvedModel
	fallbackAliases []string
	fallbackModels  []llm.ResolvedModel
	extraParams     map[string]any
	apiKeyOverrides map[string]string

	mu     sync.Mutex
	nCalls int
}

func NewLLMAdapterModel(cfg *minisweagent.ModelConfig, overrides map[string]string) (*LLMAdapterModel, error) {
	if cfg == nil {
		cfg = &minisweagent.ModelConfig{}
	}

	resolved, fallbackAliases, fallbackModels, extraParams, err := buildResolvedModel(cfg, overrides)
	if err != nil {
		return nil, err
	}

	return &LLMAdapterModel{
		config:          cfg,
		resolved:        resolved,
		fallbackAliases: fallbackAliases,
		fallbackModels:  fallbackModels,
		extraParams:     extraParams,
		apiKeyOverrides: llm.CopyAPIKeyOverridesForRuntime(overrides),
	}, nil
}

// Clone returns a fresh *LLMAdapterModel that shares the same
// configuration (config, resolved, fallback aliases/models,
// extra params, API key overrides) as the receiver but with a
// fresh, zero-initialised nCalls counter. The library path uses
// Clone() inside its per-run factory so each Run() invocation
// starts with a counter at 0.
func (m *LLMAdapterModel) Clone() *LLMAdapterModel {
	return &LLMAdapterModel{
		config:          m.config,
		resolved:        m.resolved,
		fallbackAliases: m.fallbackAliases,
		fallbackModels:  m.fallbackModels,
		extraParams:     m.extraParams,
		apiKeyOverrides: m.apiKeyOverrides,
	}
}

func (m *LLMAdapterModel) Query(ctx context.Context, messages []minisweagent.Message, opts ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.incrementCall()

	llmMsgs := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		llmMsgs = append(llmMsgs, llm.Message{
			Role:     msg.Role,
			Content:  msg.Content,
			Metadata: mergeMessageMetadata(msg.Metadata, msg.Extra),
		})
	}

	llmCtx := llm.WithStage(llm.WithAPIKeyOverrides(ctx, m.apiKeyOverrides), "shell-agent")
	content, err := chatWithResolvedFallback(llmCtx, m.resolved, m.fallbackAliases, m.fallbackModels, m.extraParams, llmMsgs)
	if err != nil {
		if category, code := llm.ClassifyError(err); category == "http" && code == "http_auth" {
			return minisweagent.QueryResult{}, &AuthError{Message: fmt.Sprintf("llm auth error: %v", err)}
		}
		return minisweagent.QueryResult{}, err
	}

	extra := map[string]any{
		"resolved_model": map[string]any{
			"alias":    strings.TrimSpace(m.resolved.Alias),
			"provider": strings.TrimSpace(m.resolved.ProviderName),
			"model":    strings.TrimSpace(m.resolved.Model),
			"base_url": strings.TrimSpace(m.resolved.BaseURL),
			"endpoint": strings.TrimSpace(m.resolved.Endpoint),
		},
	}

	return minisweagent.QueryResult{Content: content, Extra: extra}, nil
}

func (m *LLMAdapterModel) ResolvedModel() llm.ResolvedModel {
	return m.resolved
}

func (m *LLMAdapterModel) Config() interface{} { return m.config }

func (m *LLMAdapterModel) Cost() float64 {
	return 0
}

func (m *LLMAdapterModel) NCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nCalls
}

func (m *LLMAdapterModel) GetTemplateVars() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return map[string]interface{}{
		"n_model_calls": m.nCalls,
		"model_name":    m.config.ModelName,
		"NModelCalls":   m.nCalls,
		"ModelName":     m.config.ModelName,
	}
}

func (m *LLMAdapterModel) incrementCall() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nCalls++
}

func mergeMessageMetadata(metadata map[string]any, extra map[string]interface{}) map[string]any {
	if len(metadata) == 0 && len(extra) == 0 {
		return nil
	}
	merged := make(map[string]any, len(metadata)+len(extra))
	for k, v := range extra {
		merged[k] = v
	}
	for k, v := range metadata {
		merged[k] = v
	}
	return merged
}

type AuthError struct {
	Message string
}

func (e *AuthError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func buildResolvedModel(cfg *minisweagent.ModelConfig, overrides map[string]string) (llm.ResolvedModel, []string, []llm.ResolvedModel, map[string]any, error) {
	kwargsCopy := copyAnyMap(cfg.ModelKwargs)

	baseURL := takeString(kwargsCopy, "base_url")
	endpoint := takeString(kwargsCopy, "endpoint")
	if endpoint == "" && baseURL != "" {
		endpoint = "/chat/completions"
	}
	headers, err := takeStringMap(kwargsCopy, "headers")
	if err != nil {
		return llm.ResolvedModel{}, nil, nil, nil, fmt.Errorf("model_kwargs.headers: %w", err)
	}
	query, err := takeStringMap(kwargsCopy, "query")
	if err != nil {
		return llm.ResolvedModel{}, nil, nil, nil, fmt.Errorf("model_kwargs.query: %w", err)
	}

	fallbackAliases := takeStringSlice(kwargsCopy, "fallback_aliases")
	fallbackModels, additionalAliases, err := takeFallbackModels(kwargsCopy)
	if err != nil {
		return llm.ResolvedModel{}, nil, nil, nil, err
	}
	if len(additionalAliases) > 0 {
		fallbackAliases = append(fallbackAliases, additionalAliases...)
	}

	if key := takeString(kwargsCopy, "api_key"); key != "" && strings.TrimSpace(cfg.APIKey) == "" {
		cfg.APIKey = key
	}

	resolved := llm.ResolvedModel{}
	trimmedAPIKey := strings.TrimSpace(cfg.APIKey)
	if trimmedAPIKey != cfg.APIKey {
		cfg.APIKey = trimmedAPIKey
	}
	switch {
	case baseURL != "":
		resolved = llm.ResolvedModel{
			Alias:        strings.TrimSpace(cfg.ModelName),
			ProviderName: "direct",
			BaseURL:      baseURL,
			APIKey:       trimmedAPIKey,
			Endpoint:     endpoint,
			Model:        strings.TrimSpace(cfg.ModelName),
			Headers:      headers,
			Query:        query,
			Params:       map[string]any{},
		}
	default:
		alias := strings.TrimSpace(cfg.ModelName)
		if alias == "" {
			defaultAlias, err := llm.DefaultModelAlias()
			if err != nil {
				return llm.ResolvedModel{}, nil, nil, nil, fmt.Errorf("resolve default model alias: %w", err)
			}
			alias = strings.TrimSpace(defaultAlias)
			if alias == "" {
				return llm.ResolvedModel{}, nil, nil, nil, fmt.Errorf("resolve default model alias: empty alias")
			}
		}
		if alias != cfg.ModelName {
			cfg.ModelName = alias
		}
		model, err := llm.ResolveModelWithOverrides(alias, overrides)
		if err != nil {
			return llm.ResolvedModel{}, nil, nil, nil, fmt.Errorf("resolve model %q: %w", alias, err)
		}
		resolved = model
		// If an explicit API key is provided via cfg (env or direct), do not
		// let it clobber a matching provider/alias override coming from CLI.
		// Precedence: provider/alias override > cfg.APIKey (env/direct) > config file/env defaults.
		if trimmedAPIKey != "" {
			if !hasOverrideFor(overrides, resolved.ProviderName, alias) {
				resolved.APIKey = trimmedAPIKey
			}
		}
		if len(headers) > 0 {
			if resolved.Headers == nil {
				resolved.Headers = map[string]string{}
			}
			for k, v := range headers {
				resolved.Headers[k] = v
			}
		}
		if len(query) > 0 {
			if resolved.Query == nil {
				resolved.Query = map[string]string{}
			}
			for k, v := range query {
				resolved.Query[k] = v
			}
		}
		if endpoint != "" {
			resolved.Endpoint = endpoint
		}
	}

	if strings.TrimSpace(resolved.Endpoint) == "" {
		resolved.Endpoint = "/chat/completions"
	}

	extraParams := kwargsCopy
	if len(extraParams) == 0 {
		extraParams = nil
	}
	return resolved, fallbackAliases, fallbackModels, extraParams, nil
}

func copyAnyMap(in map[string]interface{}) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// hasOverrideFor reports whether any of the given names (provider or alias)
// has a corresponding API key override. Matching is case-insensitive and
// trims whitespace. The overrides map is expected to have normalized keys
// (lower-case) as produced by llm.ParseAPIKeyOverrides.
func hasOverrideFor(overrides map[string]string, names ...string) bool {
	if len(overrides) == 0 {
		return false
	}
	for _, n := range names {
		key := strings.ToLower(strings.TrimSpace(n))
		if key == "" {
			continue
		}
		if _, ok := overrides[key]; ok {
			return true
		}
	}
	return false
}

func takeString(m map[string]any, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	delete(m, key)
	if raw == nil {
		return ""
	}
	switch val := raw.(type) {
	case string:
		return strings.TrimSpace(val)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", val))
	}
}

func takeStringMap(m map[string]any, key string) (map[string]string, error) {
	raw, ok := m[key]
	if !ok {
		return nil, nil
	}
	delete(m, key)
	if raw == nil {
		return nil, nil
	}
	out := map[string]string{}
	switch val := raw.(type) {
	case map[string]any:
		for k, v := range val {
			if v == nil {
				continue
			}
			out[k] = strings.TrimSpace(fmt.Sprintf("%v", v))
		}
	case map[string]string:
		for k, v := range val {
			out[k] = v
		}
	default:
		return nil, fmt.Errorf("expected map, got %T", raw)
	}
	return out, nil
}

func takeStringSlice(m map[string]any, key string) []string {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	delete(m, key)
	if raw == nil {
		return nil
	}
	switch val := raw.(type) {
	case []string:
		return copyStringSlice(val)
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if item == nil {
				continue
			}
			trimmed := strings.TrimSpace(fmt.Sprintf("%v", item))
			if trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		trimmed := strings.TrimSpace(fmt.Sprintf("%v", raw))
		if trimmed == "" {
			return nil
		}
		return []string{trimmed}
	}
}

func copyStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	for i, v := range out {
		out[i] = strings.TrimSpace(v)
	}
	return out
}

func takeFallbackModels(m map[string]any) ([]llm.ResolvedModel, []string, error) {
	raw, ok := m["fallback_models"]
	if !ok {
		return nil, nil, nil
	}
	delete(m, "fallback_models")
	if raw == nil {
		return nil, nil, nil
	}

	var items []any
	switch val := raw.(type) {
	case []interface{}:
		items = val
	case []string:
		for _, s := range val {
			items = append(items, s)
		}
	default:
		trimmed := strings.TrimSpace(fmt.Sprintf("%v", raw))
		if trimmed == "" {
			return nil, nil, nil
		}
		return nil, []string{trimmed}, nil
	}

	models := make([]llm.ResolvedModel, 0, len(items))
	aliases := make([]string, 0, len(items))

	for idx, item := range items {
		switch val := item.(type) {
		case string:
			trimmed := strings.TrimSpace(val)
			if trimmed != "" {
				aliases = append(aliases, trimmed)
			}
		case map[string]any:
			model, err := resolvedFromMap(val)
			if err != nil {
				return nil, nil, fmt.Errorf("fallback_models[%d]: %w", idx, err)
			}
			models = append(models, model)
		default:
			return nil, nil, fmt.Errorf("fallback_models[%d]: unsupported type %T", idx, item)
		}
	}

	return models, aliases, nil
}

func resolvedFromMap(data map[string]any) (llm.ResolvedModel, error) {
	baseURL := valueAsString(data, "base_url")
	modelName := valueAsString(data, "model")
	if baseURL == "" || modelName == "" {
		return llm.ResolvedModel{}, fmt.Errorf("base_url and model are required")
	}

	apiKey := valueAsString(data, "api_key")
	endpoint := valueAsString(data, "endpoint")

	headers := map[string]string{}
	if raw, ok := data["headers"]; ok {
		converted, err := toStringMap(raw)
		if err != nil {
			return llm.ResolvedModel{}, fmt.Errorf("headers: %w", err)
		}
		headers = converted
	}
	query := map[string]string{}
	if raw, ok := data["query"]; ok {
		converted, err := toStringMap(raw)
		if err != nil {
			return llm.ResolvedModel{}, fmt.Errorf("query: %w", err)
		}
		query = converted
	}

	params := map[string]any{}
	if raw, ok := data["params"].(map[string]any); ok {
		params = copyAnyMap(raw)
	}

	alias := valueAsString(data, "alias")

	if endpoint == "" {
		endpoint = "/chat/completions"
	}

	return llm.ResolvedModel{
		Alias:    alias,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Endpoint: endpoint,
		Model:    modelName,
		Headers:  headers,
		Query:    query,
		Params:   params,
	}, nil
}

func toStringMap(raw any) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}
	switch val := raw.(type) {
	case map[string]string:
		out := make(map[string]string, len(val))
		for k, v := range val {
			out[k] = v
		}
		return out, nil
	case map[string]any:
		out := make(map[string]string, len(val))
		for k, v := range val {
			if v == nil {
				continue
			}
			out[k] = strings.TrimSpace(fmt.Sprintf("%v", v))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected map, got %T", raw)
	}
}

func valueAsString(data map[string]any, key string) string {
	raw, ok := data[key]
	if !ok || raw == nil {
		return ""
	}
	switch val := raw.(type) {
	case string:
		return strings.TrimSpace(val)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", val))
	}
}
