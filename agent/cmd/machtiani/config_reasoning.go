package main

import "strings"

const (
	configReasoningEffort = "reasoning_effort"
	configReasoningObject = "reasoning"
)

func configuredReasoningFormat(providerName string, providers map[string]any) string {
	name := strings.ToLower(strings.TrimSpace(providerName))
	if provider, ok := providers[providerName].(map[string]any); ok {
		if value, ok := provider["reasoning_format"].(string); ok {
			value = strings.ToLower(strings.TrimSpace(value))
			if value == configReasoningEffort || value == configReasoningObject {
				return value
			}
		}
		if baseURL, ok := provider["base_url"].(string); ok {
			baseURL = strings.ToLower(baseURL)
			if strings.Contains(baseURL, "openrouter.ai") || strings.Contains(baseURL, "deepinfra.com") {
				return configReasoningObject
			}
		}
	}
	if name == "openrouter" || name == "deepinfra" {
		return configReasoningObject
	}
	return configReasoningEffort
}

func setConfiguredReasoning(entry map[string]any, providers map[string]any, effort string) {
	params, _ := entry["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	delete(params, configReasoningEffort)
	delete(params, configReasoningObject)
	providerName, _ := entry["provider"].(string)
	if configuredReasoningFormat(providerName, providers) == configReasoningObject {
		params[configReasoningObject] = map[string]any{"effort": strings.TrimSpace(effort)}
	} else {
		params[configReasoningEffort] = strings.TrimSpace(effort)
	}
	entry["params"] = params
}

func clearConfiguredReasoning(entry map[string]any) {
	params, _ := entry["params"].(map[string]any)
	if params == nil {
		return
	}
	delete(params, configReasoningEffort)
	if reasoning, ok := params[configReasoningObject].(map[string]any); ok {
		delete(reasoning, "effort")
		if len(reasoning) == 0 {
			delete(params, configReasoningObject)
		}
	}
	if len(params) == 0 {
		delete(entry, "params")
	} else {
		entry["params"] = params
	}
}
