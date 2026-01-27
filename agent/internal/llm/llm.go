package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

type Message struct {
	Role     string         `json:"role"`
	Content  string         `json:"content"`
	Metadata map[string]any `json:"-"`
}

var ErrNoChoices = errors.New("no choices returned")

var (
	streamingHTTPClient    = &http.Client{Timeout: 60 * time.Minute}
	nonStreamRetryBackoffs = []time.Duration{1 * time.Second, 3 * time.Second, 5 * time.Second}
)

const (
	testStubEnv           = "MCT_LLM_TEST_STUB" // test-only knob to bypass network LLM calls
	retryAfterCap         = 15 * time.Second
	llmInputLogEnv        = "MCT_LLM_INPUT_LOG" // optional debug log file path for full LLM request inputs
	llmStageEnv           = "MCT_LLM_STAGE"     // optional stage label for LLM calls (planner/shell-agent/etc)
	cacheAnchorMarkerText = "[cache anchor]"
)

func stageFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if raw := ctx.Value(contextKeyLLMStage{}); raw != nil {
		if v, ok := raw.(string); ok {
			if v := strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	return ""
}

type contextKeyLLMStage struct{}

// WithStage annotates the context so LLM request logs can identify the caller stage.
func WithStage(ctx context.Context, stage string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return ctx
	}
	return context.WithValue(ctx, contextKeyLLMStage{}, stage)
}

func llmInputLogPath() string {
	if env := strings.TrimSpace(os.Getenv(llmInputLogEnv)); env != "" {
		return env
	}
	if sid := strings.TrimSpace(os.Getenv("MACHTIANI_SESSION_ID")); sid != "" {
		if p, err := artifacts.SessionLLMInputsFile(sid); err == nil {
			return p
		}
	}
	cfg, err := loadConfig()
	if err != nil || cfg == nil {
		return ""
	}
	if cfg.config.Debug == nil {
		return ""
	}
	return strings.TrimSpace(cfg.config.Debug.LLMInputLogPath)
}

func appendLLMInputLog(path string, payload any) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(trimmed), 0o755); err != nil {
		return
	}
	redacted := redactLLMInput(payload)
	b, err := json.Marshal(redacted)
	if err != nil {
		return
	}
	f, err := os.OpenFile(trimmed, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(b)
	_, _ = f.WriteString("\n")
}

func redactLLMInput(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			lower := strings.ToLower(strings.TrimSpace(k))
			switch lower {
			case "api_key", "apikey", "authorization", "x-api-key":
				out[k] = "[redacted]"
			default:
				out[k] = redactLLMInput(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = redactLLMInput(item)
		}
		return out
	case []Message:
		out := make([]map[string]any, 0, len(t))
		for _, m := range t {
			out = append(out, map[string]any{"role": m.Role, "content": m.Content})
		}
		return out
	default:
		return v
	}
}

func modelSummary(model ResolvedModel) map[string]any {
	return map[string]any{
		"alias":    strings.TrimSpace(model.Alias),
		"provider": strings.TrimSpace(model.ProviderName),
		"model":    strings.TrimSpace(model.Model),
		"base_url": strings.TrimSpace(model.BaseURL),
		"endpoint": strings.TrimSpace(model.Endpoint),
	}
}

func applyCacheControl(ctx context.Context, messages []Message, model ResolvedModel) []any {
	if messages == nil {
		return nil
	}
	cacheKey := strings.TrimSpace(model.CacheKeyName)
	if cacheKey == "" || model.CacheTriggerThreshold <= 0 || len(model.CacheControl) == 0 {
		return messagesToAny(messages)
	}

	if len(messages) == 0 {
		return messagesToAny(messages)
	}

	totalTokens := 0
	for _, msg := range messages {
		totalTokens += estimateTokensFromMetadata(msg)
	}
	if totalTokens < model.CacheTriggerThreshold {
		return messagesToAny(messages)
	}

	lookback := model.CacheLookbackOffset
	if lookback <= 0 {
		lookback = 1
	}
	anchorIndex := len(messages) - lookback
	if anchorIndex < 0 {
		anchorIndex = 0
	} else if anchorIndex >= len(messages) {
		anchorIndex = len(messages) - 1
	}

	messageMap := func(msg Message) map[string]any {
		return map[string]any{
			"role":    msg.Role,
			"content": msg.Content,
		}
	}
	cacheAnchor := func(text string) map[string]any {
		return map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":   "text",
					"text":   text,
					cacheKey: model.CacheControl,
				},
			},
		}
	}

	if strings.EqualFold(strings.TrimSpace(messages[anchorIndex].Role), "assistant") {
		result := make([]any, 0, len(messages)+1)
		for i, msg := range messages {
			if i == anchorIndex {
				result = append(result, cacheAnchor(cacheAnchorMarkerText))
			}
			result = append(result, messageMap(msg))
		}

		emitLLMEvent(ctx, "info", "llm.cache.injected", map[string]any{
			"anchor_index":   anchorIndex,
			"total_messages": len(result),
			"total_tokens":   totalTokens,
			"threshold":      model.CacheTriggerThreshold,
			"lookback":       model.CacheLookbackOffset,
		}, nil)

		return result
	}

	result := make([]any, len(messages))
	for i, msg := range messages {
		if i != anchorIndex {
			result[i] = messageMap(msg)
			continue
		}
		result[i] = map[string]any{
			"role": msg.Role,
			"content": []any{
				map[string]any{
					"type":   "text",
					"text":   msg.Content,
					cacheKey: model.CacheControl,
				},
			},
		}
	}

	emitLLMEvent(ctx, "info", "llm.cache.injected", map[string]any{
		"anchor_index":   anchorIndex,
		"total_messages": len(messages),
		"total_tokens":   totalTokens,
		"threshold":      model.CacheTriggerThreshold,
		"lookback":       model.CacheLookbackOffset,
	}, nil)

	return result
}

func messagesToAny(messages []Message) []any {
	if messages == nil {
		return nil
	}
	result := make([]any, len(messages))
	for i, msg := range messages {
		result[i] = map[string]any{
			"role":    msg.Role,
			"content": msg.Content,
		}
	}
	return result
}

func estimateTokensFromMetadata(msg Message) int {
	if msg.Metadata != nil {
		if raw, ok := msg.Metadata["estimated_tokens"]; ok {
			switch val := raw.(type) {
			case int:
				return val
			case int64:
				return int(val)
			case float64:
				return int(val)
			}
		}
	}
	return EstimateMessageTokens(msg)
}

func classifyLLMError(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled", "context_canceled"
	}
	var httpErr *HTTPResponseError
	if errors.As(err, &httpErr) {
		return "http", classifyHTTPStatus(httpErr.Status)
	}
	var netErr *UnreachableHostError
	if errors.As(err, &netErr) {
		if code := classifyNetworkError(netErr.Err); code != "" {
			return "network", code
		}
		return "network", "network_unreachable"
	}
	if code := classifyNetworkError(err); code != "" {
		return "network", code
	}
	return "unknown", ""
}

// ClassifyError returns the normalized error category and code for use by other components.
func ClassifyError(err error) (string, string) {
	return classifyLLMError(err)
}

func classifyHTTPStatus(status int) string {
	switch status {
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return fmt.Sprintf("http_%d", status)
	case http.StatusUnauthorized, http.StatusForbidden:
		return "http_auth"
	case http.StatusRequestTimeout:
		return "http_timeout"
	}
	if status == 0 {
		return "http_unknown"
	}
	return fmt.Sprintf("http_%d", status)
}

func classifyNetworkError(err error) string {
	if err == nil {
		return ""
	}
	switch e := err.(type) {
	case syscall.Errno:
		switch e {
		case syscall.ECONNREFUSED:
			return "connection_refused"
		case syscall.ENETUNREACH:
			return "network_unreachable"
		case syscall.ECONNRESET:
			return "connection_reset"
		case syscall.ETIMEDOUT:
			return "network_timeout"
		}
	case *net.DNSError:
		return "dns_error"
	case *net.OpError:
		if e.Timeout() {
			return "network_timeout"
		}
		if e.Err != nil {
			if code := classifyNetworkError(e.Err); code != "" {
				return code
			}
		}
		if strings.EqualFold(e.Op, "dial") {
			return "dial_error"
		}
	case *url.Error:
		if e.Timeout() {
			return "network_timeout"
		}
		if code := classifyNetworkError(e.Err); code != "" {
			return code
		}
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no such host"):
		return "dns_error"
	case strings.Contains(msg, "network is unreachable"):
		return "network_unreachable"
	case strings.Contains(msg, "connection refused"):
		return "connection_refused"
	case strings.Contains(msg, "connection reset"):
		return "connection_reset"
	case strings.Contains(msg, "tls handshake timeout"):
		return "tls_handshake_timeout"
	case strings.Contains(msg, "i/o timeout"):
		return "network_timeout"
	case strings.Contains(msg, "dial tcp"):
		return "dial_error"
	}
	return ""
}

func appendErrorDetails(payload map[string]any, err error) {
	if err == nil {
		return
	}
	var httpErr *HTTPResponseError
	if errors.As(err, &httpErr) {
		payload["status"] = httpErr.Status
		if url := strings.TrimSpace(httpErr.URL); url != "" {
			payload["url"] = url
		}
	}
	var netErr *UnreachableHostError
	if errors.As(err, &netErr) {
		if url := strings.TrimSpace(netErr.URL); url != "" {
			if _, exists := payload["url"]; !exists {
				payload["url"] = url
			}
		}
	}
}

func emitLLMEvent(ctx context.Context, level, kind string, payload map[string]any, err error) {
	if ctx == nil {
		return
	}
	w, ok := trajectory.FromContext(ctx)
	if !ok || w == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["event_version"] = 1
	evt := trajectory.Event{
		Level:     level,
		Kind:      kind,
		SpanID:    trajectory.NewSpanID(),
		Payload:   payload,
		Component: "llm",
	}
	if parent, ok := trajectory.ParentSpanID(ctx); ok && parent != "" {
		evt.ParentSpanID = parent
	}
	if err != nil {
		category, code := classifyLLMError(err)
		evt.Err = &trajectory.ErrorInfo{Message: err.Error(), Category: category, Code: code}
	}
	if emitErr := w.Emit(ctx, evt); emitErr != nil {
		reportLLMTrajectoryError(emitErr)
	}
}

type llmAttemptMeta struct {
	Alias       string
	Mode        string
	Attempt     int
	MaxAttempts int
	Source      string
	Fallback    bool
	Metadata    map[string]any
}

type llmAttempt struct {
	writer   *trajectory.Writer
	span     trajectory.Span
	parent   string
	ctx      context.Context
	model    ResolvedModel
	meta     llmAttemptMeta
	started  time.Time
	finished bool
}

func startLLMAttempt(ctx context.Context, model ResolvedModel, meta llmAttemptMeta) (*llmAttempt, context.Context) {
	w, ok := trajectory.FromContext(ctx)
	if !ok || w == nil {
		return nil, ctx
	}
	parent, _ := trajectory.ParentSpanID(ctx)
	span := w.StartSpan(parent)
	payload := map[string]any{
		"event_version": 1,
		"model":         modelSummary(model),
	}
	mode := strings.TrimSpace(meta.Mode)
	if mode == "" {
		mode = "non-stream"
	}
	payload["mode"] = mode
	if meta.Attempt > 0 {
		payload["attempt"] = meta.Attempt
	}
	if meta.MaxAttempts > 0 {
		payload["max_attempts"] = meta.MaxAttempts
	}
	if alias := strings.TrimSpace(meta.Alias); alias != "" {
		payload["alias"] = alias
	}
	if source := strings.TrimSpace(meta.Source); source != "" {
		payload["source"] = source
	}
	if meta.Fallback {
		payload["fallback"] = true
	}
	var metadataCopy map[string]any
	if len(meta.Metadata) > 0 {
		metadataCopy = make(map[string]any, len(meta.Metadata))
		for k, v := range meta.Metadata {
			payload[k] = v
			metadataCopy[k] = v
		}
	}
	evt := trajectory.Event{Kind: "llm.request.start", SpanID: span.ID, ParentSpanID: parent, Payload: payload, Component: "llm"}
	if err := w.Emit(ctx, evt); err != nil {
		reportLLMTrajectoryError(err)
	}
	attemptCtx := trajectory.ContextWithParentSpan(ctx, span.ID)
	return &llmAttempt{
		writer:  w,
		span:    span,
		parent:  parent,
		ctx:     attemptCtx,
		model:   model,
		meta:    llmAttemptMeta{Alias: meta.Alias, Mode: mode, Attempt: meta.Attempt, MaxAttempts: meta.MaxAttempts, Source: meta.Source, Fallback: meta.Fallback, Metadata: metadataCopy},
		started: time.Now(),
	}, attemptCtx
}

func (a *llmAttempt) finish(err error, extra map[string]any) {
	if a == nil || a.writer == nil || a.finished {
		return
	}
	a.finished = true
	payload := map[string]any{
		"event_version": 1,
		"model":         modelSummary(a.model),
		"mode":          a.meta.Mode,
		"duration_ms":   time.Since(a.started).Milliseconds(),
	}
	if a.meta.Attempt > 0 {
		payload["attempt"] = a.meta.Attempt
	}
	if a.meta.MaxAttempts > 0 {
		payload["max_attempts"] = a.meta.MaxAttempts
	}
	if alias := strings.TrimSpace(a.meta.Alias); alias != "" {
		payload["alias"] = alias
	}
	if source := strings.TrimSpace(a.meta.Source); source != "" {
		payload["source"] = source
	}
	if a.meta.Fallback {
		payload["fallback"] = true
	}
	for k, v := range a.meta.Metadata {
		payload[k] = v
	}
	for k, v := range extra {
		payload[k] = v
	}
	level := "info"
	kind := "llm.request.end"
	var errInfo *trajectory.ErrorInfo
	if err != nil {
		level = "error"
		kind = "llm.request.error"
		category, code := classifyLLMError(err)
		errInfo = &trajectory.ErrorInfo{Message: err.Error(), Category: category, Code: code}
	}
	evt := trajectory.Event{
		Level:        level,
		Kind:         kind,
		SpanID:       a.span.ID,
		ParentSpanID: a.parent,
		Payload:      payload,
		Err:          errInfo,
		Component:    "llm",
	}
	if emitErr := a.writer.Emit(a.ctx, evt); emitErr != nil {
		reportLLMTrajectoryError(emitErr)
	}
}

func reportLLMTrajectoryError(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[trajectory] llm emit error: %v\n", err)
}

type partialResponseError struct {
	Prefix string
	Err    error
}

func (e *partialResponseError) Error() string {
	if e == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *partialResponseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type responseUsage struct {
	PromptTokens        int                  `json:"prompt_tokens"`
	CompletionTokens    int                  `json:"completion_tokens"`
	TotalTokens         int                  `json:"total_tokens"`
	CacheDiscount       *float64             `json:"cache_discount"`
	PromptTokensDetails *promptTokensDetails `json:"prompt_tokens_details"`
}

type promptTokensDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

func emitCacheUsage(ctx context.Context, model ResolvedModel, usage *responseUsage) {
	if usage == nil {
		return
	}
	if usage.PromptTokensDetails == nil && usage.CacheDiscount == nil {
		return
	}
	payload := map[string]any{
		"model":             modelSummary(model),
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
	}
	if usage.PromptTokensDetails != nil {
		payload["cached_tokens"] = usage.PromptTokensDetails.CachedTokens
		payload["cache_write_tokens"] = usage.PromptTokensDetails.CacheWriteTokens
	}
	if usage.CacheDiscount != nil {
		payload["cache_discount"] = *usage.CacheDiscount
	}
	emitLLMEvent(ctx, "info", "llm.cache.usage", payload, nil)
}

func emitStreamFallbackEvent(ctx context.Context, model ResolvedModel, err error, partial bool, prefixLen int) {
	payload := map[string]any{
		"model":              modelSummary(model),
		"mode":               "stream",
		"fallback_mode":      "non-stream",
		"partial_output":     partial,
		"partial_prefix_len": prefixLen,
	}
	appendErrorDetails(payload, err)
	emitLLMEvent(ctx, "warn", "llm.stream.fallback", payload, err)
}

func emitRetryEvent(ctx context.Context, model ResolvedModel, attempt, maxAttempts int, wait time.Duration, err error, meta llmAttemptMeta) {
	payload := map[string]any{
		"model":        modelSummary(model),
		"mode":         strings.TrimSpace(meta.Mode),
		"attempt":      attempt,
		"next_attempt": attempt + 1,
		"max_attempts": maxAttempts,
		"wait_ms":      wait.Milliseconds(),
	}
	if payload["mode"] == "" {
		payload["mode"] = "non-stream"
	}
	if alias := strings.TrimSpace(meta.Alias); alias != "" {
		payload["alias"] = alias
	}
	if source := strings.TrimSpace(meta.Source); source != "" {
		payload["source"] = source
	}
	if meta.Fallback {
		payload["fallback"] = true
	}
	for k, v := range meta.Metadata {
		payload[k] = v
	}
	appendErrorDetails(payload, err)
	emitLLMEvent(ctx, "warn", "llm.retry", payload, err)
}

func emitFailoverStartEvent(ctx context.Context, primary, fallback ResolvedModel, source, alias string, trigger error) {
	payload := map[string]any{
		"from":   modelSummary(primary),
		"to":     modelSummary(fallback),
		"source": source,
	}
	if alias = strings.TrimSpace(alias); alias != "" {
		payload["alias"] = alias
	}
	appendErrorDetails(payload, trigger)
	emitLLMEvent(ctx, "warn", "llm.failover.start", payload, trigger)
}

func emitFailoverResultEvent(ctx context.Context, primary, fallback ResolvedModel, source, alias string, resultErr error) {
	payload := map[string]any{
		"from":    modelSummary(primary),
		"to":      modelSummary(fallback),
		"source":  source,
		"success": resultErr == nil,
	}
	if alias = strings.TrimSpace(alias); alias != "" {
		payload["alias"] = alias
	}
	if resultErr != nil {
		appendErrorDetails(payload, resultErr)
		emitLLMEvent(ctx, "error", "llm.failover.result", payload, resultErr)
		return
	}
	emitLLMEvent(ctx, "info", "llm.failover.result", payload, nil)
}

func emitFallbackResolutionErrorEvent(ctx context.Context, alias string, err error) {
	payload := map[string]any{"alias": strings.TrimSpace(alias)}
	appendErrorDetails(payload, err)
	emitLLMEvent(ctx, "error", "llm.failover.resolve_error", payload, err)
}

func emitPrefixMismatchEvent(ctx context.Context, model ResolvedModel, expectedLen, fullLen int) {
	payload := map[string]any{
		"model":               modelSummary(model),
		"expected_prefix_len": expectedLen,
		"full_len":            fullLen,
	}
	emitLLMEvent(ctx, "warn", "llm.stream.prefix_mismatch", payload, nil)
}

func Chat(ctx context.Context, modelAlias string, extraParams map[string]any, messages []Message) (string, error) {
	resolved, err := ResolveModelWithOverrides(modelAlias, apiKeyOverridesFromContext(ctx))
	if err != nil {
		return "", err
	}
	return chatWithResolvedFallback(ctx, resolved, nil, nil, extraParams, messages, false, nil)
}

func ChatStream(ctx context.Context, modelAlias string, extraParams map[string]any, messages []Message, onToken func(string)) (string, error) {
	resolved, err := ResolveModelWithOverrides(modelAlias, apiKeyOverridesFromContext(ctx))
	if err != nil {
		return "", err
	}
	return chatWithResolvedFallback(ctx, resolved, nil, nil, extraParams, messages, true, onToken)
}

func ChatWithResolved(ctx context.Context, model ResolvedModel, extraParams map[string]any, messages []Message) (string, error) {
	return chatWithResolvedFallback(ctx, model, nil, nil, extraParams, messages, false, nil)
}

func ChatStreamWithResolved(ctx context.Context, model ResolvedModel, extraParams map[string]any, messages []Message, onToken func(string)) (string, error) {
	return chatWithResolvedFallback(ctx, model, nil, nil, extraParams, messages, true, onToken)
}

func ChatWithFallback(ctx context.Context, primaryAlias string, fallbackAliases []string, extraParams map[string]any, messages []Message) (string, error) {
	resolved, err := ResolveModelWithOverrides(primaryAlias, apiKeyOverridesFromContext(ctx))
	if err != nil {
		return "", err
	}
	return chatWithResolvedFallback(ctx, resolved, fallbackAliases, nil, extraParams, messages, false, nil)
}

func ChatStreamWithFallback(ctx context.Context, primaryAlias string, fallbackAliases []string, extraParams map[string]any, messages []Message, onToken func(string)) (string, error) {
	resolved, err := ResolveModelWithOverrides(primaryAlias, apiKeyOverridesFromContext(ctx))
	if err != nil {
		return "", err
	}
	return chatWithResolvedFallback(ctx, resolved, fallbackAliases, nil, extraParams, messages, true, onToken)
}

func ChatWithResolvedFallback(ctx context.Context, model ResolvedModel, fallbackAliases []string, fallbackModels []ResolvedModel, extraParams map[string]any, messages []Message) (string, error) {
	return chatWithResolvedFallback(ctx, model, fallbackAliases, fallbackModels, extraParams, messages, false, nil)
}

func ChatStreamWithResolvedFallback(ctx context.Context, model ResolvedModel, fallbackAliases []string, fallbackModels []ResolvedModel, extraParams map[string]any, messages []Message, onToken func(string)) (string, error) {
	return chatWithResolvedFallback(ctx, model, fallbackAliases, fallbackModels, extraParams, messages, true, onToken)
}

func chatWithResolvedFallback(ctx context.Context, primary ResolvedModel, fallbackAliases []string, fallbackModels []ResolvedModel, extraParams map[string]any, messages []Message, stream bool, onToken func(string)) (string, error) {
	if stub := strings.TrimSpace(os.Getenv(testStubEnv)); stub != "" {
		reply := buildStubResponse(stub, messages)
		if stream && onToken != nil {
			onToken(reply)
		}
		return reply, nil
	}
	if err := validateResolvedModel(primary); err != nil {
		return "", err
	}
	overrides := apiKeyOverridesFromContext(ctx)
	normalizedFallbacks := normalizeFallbackAliases(primary, fallbackAliases)
	targets := buildFallbackTargets(primary, normalizedFallbacks, fallbackModels)

	basePayload := mergeMaps(primary.Params, extraParams)
	basePayload["model"] = primary.Model
	basePayload["messages"] = applyCacheControl(ctx, messages, primary)
	stage := stageFromContext(ctx)
	if stage == "" {
		stage = strings.TrimSpace(os.Getenv(llmStageEnv))
	}
	appendLLMInputLog(llmInputLogPath(), map[string]any{
		"stage":   stage,
		"model":   modelSummary(primary),
		"stream":  stream,
		"payload": basePayload,
	})

	nonStreamBody, err := encodePayload(basePayload, false)
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	var result string
	var attemptErr error
	emittedPrefix := ""

	if stream {
		streamBody, err := encodePayload(basePayload, true)
		if err != nil {
			return "", fmt.Errorf("encode request: %w", err)
		}
		result, attemptErr = tryStreamThenFallback(ctx, primary, streamBody, nonStreamBody, onToken)
		if attemptErr == nil {
			return result, nil
		}
		var partial *partialResponseError
		if errors.As(attemptErr, &partial) {
			emittedPrefix = partial.Prefix
			attemptErr = partial.Err
		}
	} else {
		primaryMeta := llmAttemptMeta{
			Alias:  strings.TrimSpace(primary.Alias),
			Mode:   "non-stream",
			Source: "primary",
		}
		result, attemptErr = executeOnceWithRetries(ctx, primary, nonStreamBody, primaryMeta)
		if attemptErr == nil {
			return result, nil
		}
	}

	if len(targets) == 0 {
		if emittedPrefix != "" {
			return "", &partialResponseError{Prefix: emittedPrefix, Err: attemptErr}
		}
		return "", attemptErr
	}

	lastErr := attemptErr
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			if emittedPrefix != "" {
				return "", &partialResponseError{Prefix: emittedPrefix, Err: err}
			}
			return "", err
		}
		var fallbackModel ResolvedModel
		source := "alias"
		alias := strings.TrimSpace(target.alias)
		if target.resolved != nil {
			fallbackModel = CloneResolvedModel(*target.resolved)
			source = "resolved"
			if alias == "" {
				alias = strings.TrimSpace(fallbackModel.Alias)
			}
		} else {
			resolved, err := ResolveModelWithOverrides(target.alias, overrides)
			if err != nil {
				lastErr = fmt.Errorf("resolve fallback model %q: %w", target.alias, err)
				emitFallbackResolutionErrorEvent(ctx, target.alias, err)
				continue
			}
			fallbackModel = resolved
		}
		if sameResolvedModel(primary, fallbackModel) {
			continue
		}
		if err := validateResolvedModel(fallbackModel); err != nil {
			lastErr = err
			continue
		}
		payload := mergeMaps(fallbackModel.Params, extraParams)
		payload["model"] = fallbackModel.Model
		payload["messages"] = applyCacheControl(ctx, messages, fallbackModel)

		body, err := encodePayload(payload, false)
		if err != nil {
			lastErr = fmt.Errorf("encode request: %w", err)
			continue
		}

		emitFailoverStartEvent(ctx, primary, fallbackModel, source, alias, lastErr)
		fallbackMeta := llmAttemptMeta{
			Alias:    alias,
			Mode:     "non-stream",
			Source:   source,
			Fallback: true,
		}
		if alias != "" {
			fallbackMeta.Metadata = map[string]any{"fallback_alias": alias}
		}
		result, err = executeOnceWithRetries(ctx, fallbackModel, body, fallbackMeta)
		emitFailoverResultEvent(ctx, primary, fallbackModel, source, alias, err)
		if err != nil {
			lastErr = err
			continue
		}
		if stream {
			emitWithPrefix(ctx, onToken, emittedPrefix, result, fallbackModel)
		}
		return result, nil
	}

	if emittedPrefix != "" {
		return "", &partialResponseError{Prefix: emittedPrefix, Err: lastErr}
	}
	return "", lastErr
}

func validateResolvedModel(model ResolvedModel) error {
	if strings.TrimSpace(model.BaseURL) == "" {
		return errors.New("resolved model missing base URL")
	}
	if strings.TrimSpace(model.Model) == "" {
		return errors.New("resolved model missing upstream model name")
	}
	return nil
}

func normalizeFallbackAliases(primary ResolvedModel, aliases []string) []string {
	if len(aliases) == 0 {
		return nil
	}
	primaryAlias := strings.TrimSpace(primary.Alias)
	seen := make(map[string]struct{}, len(aliases))
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		trimmed := strings.TrimSpace(alias)
		if trimmed == "" {
			continue
		}
		if trimmed == primaryAlias {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

type fallbackTarget struct {
	alias    string
	resolved *ResolvedModel
}

func buildFallbackTargets(primary ResolvedModel, aliases []string, models []ResolvedModel) []fallbackTarget {
	targets := make([]fallbackTarget, 0, len(aliases)+len(models))
	seen := make(map[string]struct{}, len(aliases)+len(models)+1)
	seen[fallbackResolvedKey(primary)] = struct{}{}
	for _, alias := range aliases {
		key := "alias:" + strings.ToLower(alias)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, fallbackTarget{alias: alias})
	}
	for _, mdl := range models {
		clone := CloneResolvedModel(mdl)
		if sameResolvedModel(primary, clone) {
			continue
		}
		key := fallbackResolvedKey(clone)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, fallbackTarget{resolved: &clone})
	}
	return targets
}

func fallbackResolvedKey(m ResolvedModel) string {
	base := strings.ToLower(strings.TrimSpace(m.BaseURL))
	endpoint := strings.ToLower(strings.TrimSpace(m.Endpoint))
	model := strings.ToLower(strings.TrimSpace(m.Model))
	alias := strings.ToLower(strings.TrimSpace(m.Alias))
	return base + "|" + endpoint + "|" + model + "|" + alias
}

func sameResolvedModel(a, b ResolvedModel) bool {
	return strings.EqualFold(strings.TrimSpace(a.BaseURL), strings.TrimSpace(b.BaseURL)) &&
		strings.EqualFold(strings.TrimSpace(a.Endpoint), strings.TrimSpace(b.Endpoint)) &&
		strings.EqualFold(strings.TrimSpace(a.Model), strings.TrimSpace(b.Model))
}

func encodePayload(base map[string]any, stream bool) ([]byte, error) {
	payload := mergeMaps(base, map[string]any{"stream": stream})
	return json.Marshal(payload)
}

func tryStreamThenFallback(ctx context.Context, model ResolvedModel, streamBody, nonStreamBody []byte, onToken func(string)) (string, error) {
	var streamedPrefix strings.Builder
	emitted := false
	wrapped := func(tok string) {
		streamedPrefix.WriteString(tok)
		if onToken != nil {
			emitted = true
			onToken(tok)
		}
	}

	streamMeta := llmAttemptMeta{
		Alias:       strings.TrimSpace(model.Alias),
		Mode:        "stream",
		Attempt:     1,
		MaxAttempts: 1,
		Source:      "primary",
	}
	streamAttempt, attemptCtx := startLLMAttempt(ctx, model, streamMeta)
	if streamAttempt == nil {
		attemptCtx = ctx
	}
	result, usage, err := executeStream(attemptCtx, model, streamBody, wrapped)
	if streamAttempt != nil {
		extra := map[string]any{
			"partial_output":     emitted,
			"partial_prefix_len": streamedPrefix.Len(),
		}
		streamAttempt.finish(err, extra)
	}
	if err == nil {
		emitCacheUsage(attemptCtx, model, usage)
		return result, nil
	}
	emitStreamFallbackEvent(attemptCtx, model, err, emitted, streamedPrefix.Len())
	fallbackMeta := llmAttemptMeta{
		Alias:    strings.TrimSpace(model.Alias),
		Mode:     "non-stream",
		Source:   "primary",
		Fallback: true,
		Metadata: map[string]any{
			"fallback_mode": "non-stream",
		},
	}
	fallbackResult, fallbackErr := executeOnceWithRetries(ctx, model, nonStreamBody, fallbackMeta)
	if fallbackErr != nil {
		if emitted {
			return "", &partialResponseError{Prefix: streamedPrefix.String(), Err: fallbackErr}
		}
		return "", fallbackErr
	}
	prefix := ""
	if emitted {
		prefix = streamedPrefix.String()
	}
	emitWithPrefix(ctx, onToken, prefix, fallbackResult, model)
	return fallbackResult, nil
}

func executeStream(ctx context.Context, model ResolvedModel, body []byte, onToken func(string)) (string, *responseUsage, error) {
	req, err := buildRequest(ctx, model, body)
	if err != nil {
		return "", nil, err
	}
	return performStream(req, onToken)
}

func executeOnceWithRetries(ctx context.Context, model ResolvedModel, body []byte, meta llmAttemptMeta) (string, error) {
	maxAttempts := len(nonStreamRetryBackoffs) + 1
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		attemptMeta := meta
		attemptMeta.Attempt = attempt
		attemptMeta.MaxAttempts = maxAttempts
		attemptAttempt, attemptCtx := startLLMAttempt(ctx, model, attemptMeta)
		if attemptAttempt == nil {
			attemptCtx = ctx
		}
		req, err := buildRequest(attemptCtx, model, body)
		if err != nil {
			if attemptAttempt != nil {
				attemptAttempt.finish(err, map[string]any{"stage": "build_request"})
			}
			return "", err
		}
		result, usage, err := performNonStream(req)
		if attemptAttempt != nil {
			attemptAttempt.finish(err, nil)
		}
		if err == nil {
			emitCacheUsage(attemptCtx, model, usage)
			return result, nil
		}
		lastErr = err
		retryable := shouldRetry(err)
		if !retryable || attempt == maxAttempts {
			break
		}
		wait := retryDelay(err, nonStreamRetryBackoffs[attempt-1])
		emitRetryEvent(attemptCtx, model, attempt, maxAttempts, wait, err, attemptMeta)
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("non-stream retries exhausted")
}

func performStream(req *http.Request, onToken func(string)) (string, *responseUsage, error) {
	resp, err := streamingHTTPClient.Do(req)
	if err != nil {
		return "", nil, &UnreachableHostError{URL: req.URL.String(), Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", nil, &HTTPResponseError{URL: req.URL.String(), Status: resp.StatusCode, Body: strings.TrimSpace(string(b)), Header: resp.Header.Clone()}
	}

	var full strings.Builder
	var usage *responseUsage
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
				if payload == "[DONE]" {
					break
				}
				var obj struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
					Usage *responseUsage `json:"usage"`
				}
				if err := json.Unmarshal([]byte(payload), &obj); err == nil {
					if obj.Usage != nil {
						usage = obj.Usage
					}
					if len(obj.Choices) > 0 {
						tok := obj.Choices[0].Delta.Content
						if tok != "" {
							if onToken != nil {
								onToken(tok)
							}
							full.WriteString(tok)
						}
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return full.String(), usage, err
		}
	}
	return full.String(), usage, nil
}

func performNonStream(req *http.Request) (string, *responseUsage, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, &UnreachableHostError{URL: req.URL.String(), Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", nil, &HTTPResponseError{URL: req.URL.String(), Status: resp.StatusCode, Body: strings.TrimSpace(string(body)), Header: resp.Header.Clone()}
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *responseUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", nil, err
	}
	if len(parsed.Choices) == 0 {
		return "", nil, ErrNoChoices
	}
	return parsed.Choices[0].Message.Content, parsed.Usage, nil
}

func emitWithPrefix(ctx context.Context, onToken func(string), prefix, full string, model ResolvedModel) {
	if onToken == nil {
		return
	}
	if prefix == "" {
		onToken(full)
		return
	}
	if strings.HasPrefix(full, prefix) {
		suffix := full[len(prefix):]
		if suffix != "" {
			onToken(suffix)
		}
		return
	}
	emitPrefixMismatchEvent(ctx, model, len(prefix), len(full))
	onToken(full)
}

func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ue *UnreachableHostError
	if errors.As(err, &ue) {
		return true
	}
	var httpErr *HTTPResponseError
	if errors.As(err, &httpErr) {
		status := httpErr.Status
		switch status {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
			return false
		}
		return isRetryableStatus(status)
	}
	if isAmbiguousProtocolError(err) {
		return true
	}
	return false
}

func isRetryableStatus(status int) bool {
	if status == http.StatusTooManyRequests || status == http.StatusRequestTimeout || status == http.StatusTooEarly {
		return true
	}
	if status >= 500 && status <= 599 {
		return true
	}
	if status >= 520 && status <= 526 { // common CDN/proxy errors
		return true
	}
	return false
}

func isAmbiguousProtocolError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return true
	}
	var unmarshalErr *json.UnmarshalTypeError
	if errors.As(err, &unmarshalErr) {
		return true
	}
	return false
}

func retryDelay(err error, defaultDelay time.Duration) time.Duration {
	var httpErr *HTTPResponseError
	if errors.As(err, &httpErr) {
		if httpErr.Status == http.StatusTooManyRequests || httpErr.Status == http.StatusServiceUnavailable {
			if h := httpErr.Header; h != nil {
				if wait, ok := parseRetryAfter(h); ok {
					if wait > retryAfterCap {
						wait = retryAfterCap
					}
					return wait
				}
			}
		}
	}
	return defaultDelay
}

func parseRetryAfter(header http.Header) (time.Duration, bool) {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(value); err == nil {
		delta := time.Until(t)
		if delta < 0 {
			delta = 0
		}
		return delta, true
	}
	return 0, false
}

func buildRequest(ctx context.Context, model ResolvedModel, body []byte) (*http.Request, error) {
	endpoint := strings.TrimSpace(model.Endpoint)
	base := strings.TrimSpace(model.BaseURL)
	var target string
	switch {
	case endpoint == "":
		target = strings.TrimRight(base, "/") + "/chat/completions"
	case strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://"):
		target = endpoint
	default:
		target = strings.TrimRight(base, "/") + "/" + strings.TrimLeft(endpoint, "/")
	}

	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint %q: %w", target, err)
	}
	q := u.Query()
	for k, v := range model.Query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(model.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+model.APIKey)
	}
	for k, v := range model.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

func buildStubResponse(mode string, messages []Message) string {
	content := ""
	if len(messages) > 0 {
		content = messages[len(messages)-1].Content
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		trimmed = "(no prompt provided)"
	}
	if len(trimmed) > 400 {
		trimmed = trimmed[:400] + "\n...[truncated by stub]"
	}
	modeLabel := strings.TrimSpace(mode)
	if modeLabel == "" {
		modeLabel = "default"
	}
	return fmt.Sprintf("Stub LLM (%s) response\n\n%s", modeLabel, trimmed)
}

// Markdown streaming helpers remain unchanged

type MarkdownStreamer struct {
	renderer *glamour.TermRenderer
	buf      strings.Builder
	inCode   bool
}

func NewMarkdownStreamer() (*MarkdownStreamer, error) {
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		return nil, err
	}
	return &MarkdownStreamer{renderer: r}, nil
}

func (m *MarkdownStreamer) Feed(tok string) error {
	m.buf.WriteString(tok)
	content := m.buf.String()
	for {
		idx := strings.Index(content, "\n\n")
		if idx == -1 {
			break
		}
		block := content[:idx]
		remaining := content[idx+2:]
		lines := strings.Split(block, "\n")
		for _, ln := range lines {
			l := strings.TrimSpace(ln)
			if strings.HasPrefix(l, "```") {
				m.inCode = !m.inCode
			}
		}
		if m.inCode {
			// hold code blocks until exit fence
		} else {
			out, err := m.renderer.Render(strings.TrimRight(block, "\r\n"))
			if err == nil {
				fmt.Print(strings.TrimRight(out, "\n"))
			}
		}
		content = remaining
		m.buf.Reset()
		m.buf.WriteString(content)
	}
	return nil
}

func (m *MarkdownStreamer) Flush() error {
	s := strings.TrimRight(m.buf.String(), "\r\n")
	if s == "" {
		return nil
	}
	out, err := m.renderer.Render(s)
	if err != nil {
		return err
	}
	fmt.Print(strings.TrimRight(out, "\n"))
	m.buf.Reset()
	return nil
}
