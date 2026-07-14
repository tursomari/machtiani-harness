package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
)

const (
	reasoningFormatEffort   = "reasoning_effort"
	reasoningFormatObject   = "reasoning"
	reasoningFormatExplicit = "reasoning_explicit"
)

var learnedReasoningFormats sync.Map

func parseParamsJSON(value string) (map[string]any, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	result, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("must contain a top-level JSON object")
	}
	return result, nil
}

func preferredReasoningFormat(providerName, baseURL, configured string) string {
	configured = strings.ToLower(strings.TrimSpace(configured))
	if configured == reasoningFormatEffort || configured == reasoningFormatObject || configured == reasoningFormatExplicit {
		return configured
	}
	name := strings.ToLower(strings.TrimSpace(providerName))
	host := strings.ToLower(strings.TrimSpace(baseURL))
	if name == "openrouter" || name == "deepinfra" || strings.Contains(host, "openrouter.ai") || strings.Contains(host, "deepinfra.com") {
		return reasoningFormatObject
	}
	return reasoningFormatEffort
}

func reasoningCacheKey(model ResolvedModel) string {
	return strings.ToLower(strings.TrimSpace(model.ProviderName)) + "|" +
		strings.ToLower(strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")) + "|" +
		strings.ToLower(strings.TrimSpace(model.Endpoint)) + "|" +
		strings.ToLower(strings.TrimSpace(model.Model))
}

func standardizedReasoningEffort(payload map[string]any) (string, bool) {
	if effort, ok := payload[reasoningFormatEffort].(string); ok && strings.TrimSpace(effort) != "" {
		return strings.TrimSpace(effort), true
	}
	reasoning, ok := payload[reasoningFormatObject].(map[string]any)
	if !ok || len(reasoning) != 1 {
		return "", false
	}
	effort, ok := reasoning["effort"].(string)
	return strings.TrimSpace(effort), ok && strings.TrimSpace(effort) != ""
}

func reasoningFormatsFor(model ResolvedModel, payload map[string]any) (string, []string, bool) {
	effort, ok := standardizedReasoningEffort(payload)
	if !ok {
		return "", nil, false
	}
	preferred := preferredReasoningFormat(model.ProviderName, model.BaseURL, model.ReasoningFormat)
	if learned, ok := learnedReasoningFormats.Load(reasoningCacheKey(model)); ok {
		if value, ok := learned.(string); ok && value != "" {
			preferred = value
		}
	}
	ordered := []string{preferred, reasoningFormatEffort, reasoningFormatObject, reasoningFormatExplicit}
	seen := map[string]bool{}
	formats := make([]string, 0, 3)
	for _, format := range ordered {
		if !seen[format] {
			seen[format] = true
			formats = append(formats, format)
		}
	}
	return effort, formats, true
}

func payloadWithReasoningFormat(payload map[string]any, effort, format string) map[string]any {
	out := deepCopyMap(payload)
	delete(out, reasoningFormatEffort)
	delete(out, reasoningFormatObject)
	switch format {
	case reasoningFormatObject:
		out[reasoningFormatObject] = map[string]any{"effort": effort}
	case reasoningFormatExplicit:
		enabled := !strings.EqualFold(strings.TrimSpace(effort), "none")
		out[reasoningFormatObject] = map[string]any{"effort": effort, "budget_tokens": nil, "enabled": enabled}
	default:
		out[reasoningFormatEffort] = effort
	}
	return out
}

func reasoningShapeError(err error, attempted string) (retry bool, sameTopLevelRejected bool) {
	var httpErr *HTTPResponseError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadRequest {
		return false, false
	}
	var body struct {
		Error struct {
			Param   string `json:"param"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(httpErr.Body), &body)
	param := strings.ToLower(strings.TrimSpace(body.Error.Param))
	message := strings.ToLower(body.Error.Message + " " + httpErr.Body)
	mentionsReasoning := strings.Contains(param, "reasoning") || strings.Contains(message, "reasoning")
	if !mentionsReasoning {
		return false, false
	}
	unknown := strings.Contains(strings.ToLower(body.Error.Code), "unknown") || strings.Contains(message, "unknown parameter") || strings.Contains(message, "unsupported parameter")
	if attempted == reasoningFormatObject && unknown && (param == "reasoning" || strings.Contains(message, "'reasoning'")) {
		return false, true
	}
	return true, false
}

func rememberReasoningFormat(model ResolvedModel, from, to string) {
	key := reasoningCacheKey(model)
	previous, loaded := learnedReasoningFormats.LoadOrStore(key, to)
	if loaded && previous == to {
		return
	}
	alias := strings.TrimSpace(model.Alias)
	if alias == "" {
		alias = strings.TrimSpace(model.Model)
	}
	fmt.Fprintf(os.Stderr, "Warning: model %q rejected reasoning format %s; using %s for the remainder of this run. Configuration was not changed.\n", alias, from, to)
}

func reasoningFailureGuidance(model ResolvedModel, effort string, attempts map[string]error) error {
	formats := make([]string, 0, len(attempts))
	for format := range attempts {
		formats = append(formats, format)
	}
	sort.Strings(formats)
	parts := make([]string, 0, len(formats))
	for _, format := range formats {
		parts = append(parts, fmt.Sprintf("%s: %v", format, attempts[format]))
	}
	alias := strings.TrimSpace(model.Alias)
	if alias == "" {
		alias = strings.TrimSpace(model.Model)
	}
	example := fmt.Sprintf(`mct-agent config model set %s --clear-reasoning --param-json '{"reasoning":{"effort":%q,"budget_tokens":null,"enabled":true}}' --no-interactive`, alias, effort)
	return fmt.Errorf("provider rejected compatible reasoning request shapes (%s)\nTo configure provider-specific parameters, run:\n  %s\nOr use: mct-agent config -> Manage models -> Additional request parameters", strings.Join(parts, "; "), example)
}
