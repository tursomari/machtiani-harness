package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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
	streamingHTTPClient = &http.Client{Timeout: 60 * time.Minute}
)

const (
	testStubEnv                               = "MCT_LLM_TEST_STUB" // test-only knob to bypass network LLM calls
	nonStreamRetryInitialBackoff              = 1 * time.Second
	nonStreamRetryMaxBackoff                  = 30 * time.Second
	retryAfterCap                             = 15 * time.Second
	maxRetries                                = 10 // hard ceiling for LLM retry loops
	probeTimeout                              = 10 * time.Minute
	llmInputLogEnv                            = "MCT_LLM_INPUT_LOG" // optional debug log file path for full LLM request inputs
	llmStageEnv                               = "MCT_LLM_STAGE"     // optional stage label for LLM calls (planner/shell-agent/etc)
	CacheAnchorMarkerText                     = "[cache anchor]"
	cacheAnchorMarkerText                     = CacheAnchorMarkerText
	CacheAnchorRetiredMetadataKey             = "cache_anchor_retired"
	CacheAnchorSequenceMetadataKey            = "anchor_seq"
	CacheAnchorTurnMetadataKey                = "anchor_turn"
	CacheAnchorTokensMetadataKey              = "anchor_tokens"
	CacheAnchorCachedTokensMetadataKey        = "anchor_cached_tokens"
	CacheAnchorInsertionPrefixHashMetadataKey = "insertion_prefix_hash"

	cacheWarningPrefix = "\u26a0 cache:"
)

// probeTimeoutOverride allows tests to override the default probeTimeout.
var probeTimeoutOverride time.Duration

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

type contextKeyCacheUsageObserver struct{}

type contextKeyVerbose struct{}

type contextKeyTranscript struct{}

type CacheUsageInfo struct {
	CachedTokens     int
	CacheWriteTokens int
	CacheDiscount    *float64
}

type CacheUsageObserver func(model ResolvedModel, usage CacheUsageInfo)

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

// WithVerbose annotates the context so downstream cache warning emitters can
// decide whether to produce verbose diagnostics (TUI + trajectory + transcript).
func WithVerbose(ctx context.Context, verbose bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if !verbose {
		return ctx
	}
	return context.WithValue(ctx, contextKeyVerbose{}, true)
}

func verboseFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx.Value(contextKeyVerbose{}).(bool)
	return ok && v
}

// WithTranscript attaches a transcript writer to the context for cache warning
// emission. The value is stored as interface{} to avoid an import cycle.
func WithTranscript(ctx context.Context, t interface{ AppendBlock(string) error }) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKeyTranscript{}, t)
}

type transcriptAppender interface {
	AppendBlock(string) error
}

func transcriptFromContext(ctx context.Context) (transcriptAppender, bool) {
	if ctx == nil {
		return nil, false
	}
	t, ok := ctx.Value(contextKeyTranscript{}).(transcriptAppender)
	return t, ok
}

func WithCacheUsageObserver(ctx context.Context, observer CacheUsageObserver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKeyCacheUsageObserver{}, observer)
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
		// Detection: threshold stripping — an active anchor exists but we're
		// stripping cache_control because total tokens dropped below threshold.
		if activeIdx := cacheAnchorMessageIndex(messages); activeIdx >= 0 {
			EmitCacheWarning(ctx, "threshold_stripped", map[string]any{
				"anchor_sequence":  metadataInt(messages[activeIdx].Metadata, CacheAnchorSequenceMetadataKey),
				"total_tokens":    totalTokens,
				"threshold":       model.CacheTriggerThreshold,
				"anchor_index":    activeIdx,
			})
		}
		return messagesToAny(messages)
	}

	textPart := func(text string, cacheValue map[string]any) map[string]any {
		part := map[string]any{
			"type": "text",
			"text": text,
		}
		if cacheValue != nil {
			part[cacheKey] = cacheValue
		}
		return part
	}
	messageMap := func(msg Message) map[string]any {
		return map[string]any{
			"role": msg.Role,
			"content": []any{
				textPart(msg.Content, nil),
			},
		}
	}
	cacheAnchor := func(text string) map[string]any {
		return map[string]any{
			"role": "user",
			"content": []any{
				textPart(text, model.CacheControl),
			},
		}
	}

	anchorIndex := cacheAnchorMessageIndex(messages)
	useStoredAnchor := anchorIndex >= 0
	anchorPrevIndex := -1
	if useStoredAnchor {
		anchorPrevIndex = cacheAnchorPreviousIndex(messages, anchorIndex)
	}
	if !useStoredAnchor {
		lookback := model.CacheLookbackOffset
		if lookback <= 0 {
			lookback = 1
		}
		anchorIndex = len(messages) - lookback
		if anchorIndex < 0 {
			anchorIndex = 0
		} else if anchorIndex >= len(messages) {
			anchorIndex = len(messages) - 1
		}
	}

	insertedAnchor := false
	if !useStoredAnchor && strings.EqualFold(strings.TrimSpace(messages[anchorIndex].Role), "assistant") {
		insertedAnchor = true
		result := make([]any, 0, len(messages)+1)
		for i, msg := range messages {
			if i == anchorIndex {
				result = append(result, cacheAnchor(cacheAnchorMarkerText))
			}
			result = append(result, messageMap(msg))
		}

		cacheDetails := cacheDiagnosticsDetailsForAnchor(messages, anchorIndex, anchorPrevIndex, insertedAnchor)
		// Drift detection: compare stored insertion hash with freshly computed prefix hash.
		detectCachePrefixDrift(ctx, useStoredAnchor, messages, anchorIndex, result)
		emitCacheDiagnostics(ctx, anchorIndex, len(result), totalTokens, model, result, cacheDetails)
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
				textPart(msg.Content, model.CacheControl),
			},
		}
	}

	cacheDetails := cacheDiagnosticsDetailsForAnchor(messages, anchorIndex, anchorPrevIndex, insertedAnchor)
	// Drift detection: compare stored insertion hash with freshly computed prefix hash.
	detectCachePrefixDrift(ctx, useStoredAnchor, messages, anchorIndex, result)
	emitCacheDiagnostics(ctx, anchorIndex, len(result), totalTokens, model, result, cacheDetails)
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

// FormatMessagesForHashing converts messages to the provider-formatted shape used
// by applyCacheControl, so that hashes computed at anchor-insertion time match
// hashes computed at call time. Each message's Content is wrapped in a textPart
// slice (without cache_control) to mirror the format produced by messageMap.
func FormatMessagesForHashing(msgs []Message) []any {
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{
			"role": m.Role,
			"content": []any{
				map[string]string{
					"type": "text",
					"text": m.Content,
				},
			},
		})
	}
	return out
}

func cacheAnchorMessageIndex(messages []Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if isCacheAnchorMessage(messages[i]) && !cacheAnchorRetired(messages[i].Metadata) {
			return i
		}
	}
	return -1
}

func cacheAnchorPreviousIndex(messages []Message, activeIndex int) int {
	if activeIndex <= 0 {
		return -1
	}
	for i := activeIndex - 1; i >= 0; i-- {
		if isCacheAnchorMessage(messages[i]) {
			return i
		}
	}
	return -1
}

func isCacheAnchorMessage(msg Message) bool {
	if msg.Metadata == nil {
		return false
	}
	raw, ok := msg.Metadata["type"]
	if !ok {
		return false
	}
	val, ok := raw.(string)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(val), "cache_anchor")
}

func cacheAnchorRetired(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	raw, ok := metadata[CacheAnchorRetiredMetadataKey]
	if !ok {
		return false
	}
	switch val := raw.(type) {
	case bool:
		return val
	case string:
		return strings.EqualFold(strings.TrimSpace(val), "true")
	case int:
		return val != 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	default:
		return false
	}
}

type cacheDiagnosticsDetails struct {
	AnchorPrevIndex     int
	TokensSinceAnchor   int
	MessagesSinceAnchor int
}

func cacheDiagnosticsDetailsForAnchor(messages []Message, anchorIndex, anchorPrevIndex int, insertedAnchor bool) cacheDiagnosticsDetails {
	startIndex := anchorIndex + 1
	if insertedAnchor {
		startIndex = anchorIndex
	}
	if startIndex < 0 {
		startIndex = 0
	}
	if startIndex > len(messages) {
		startIndex = len(messages)
	}
	return cacheDiagnosticsDetails{
		AnchorPrevIndex:     anchorPrevIndex,
		TokensSinceAnchor:   estimateTokensFromMessages(messages[startIndex:]),
		MessagesSinceAnchor: len(messages) - startIndex,
	}
}

func estimateTokensFromMessages(messages []Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateTokensFromMetadata(msg)
	}
	return total
}

func emitCacheDiagnostics(ctx context.Context, anchorIndex, messageCount, totalTokens int, model ResolvedModel, messages []any, details cacheDiagnosticsDetails) {
	payload := map[string]any{
		"anchor_index":          anchorIndex,
		"message_count":         messageCount,
		"total_messages":        messageCount,
		"total_tokens":          totalTokens,
		"threshold":             model.CacheTriggerThreshold,
		"lookback":              model.CacheLookbackOffset,
		"tokens_since_anchor":   details.TokensSinceAnchor,
		"messages_since_anchor": details.MessagesSinceAnchor,
	}
	if details.AnchorPrevIndex >= 0 {
		payload["anchor_prev_index"] = details.AnchorPrevIndex
		payload["anchor_rotated"] = true
	}
	// Always compute prefix hash (was previously gated on stage == "planner").
	if prefix := CachePrefixHash(messages, anchorIndex); prefix != "" {
		payload["prefix_hash"] = prefix
	}
	emitLLMEvent(ctx, "info", "llm.cache.injected", payload, nil)
}

// detectCachePrefixDrift checks whether the prefix content has changed since the
// active anchor was inserted. If the stored insertion_prefix_hash differs from a
// freshly computed hash, it emits a prefix_drift warning.
// This only fires when useStoredAnchor is true (i.e. an active anchor exists).
func detectCachePrefixDrift(ctx context.Context, useStoredAnchor bool, messages []Message, anchorIndex int, result []any) {
	if !useStoredAnchor || anchorIndex < 0 || anchorIndex >= len(messages) {
		return
	}
	storedHash, ok := messages[anchorIndex].Metadata[CacheAnchorInsertionPrefixHashMetadataKey]
	if !ok {
		return
	}
	storedHashStr, ok := storedHash.(string)
	if !ok || storedHashStr == "" {
		return
	}
	currentHash := CachePrefixHash(result, anchorIndex)
	if currentHash == "" || currentHash == storedHashStr {
		return
	}
	EmitCacheWarning(ctx, "prefix_drift", map[string]any{
		"anchor_sequence":        metadataInt(messages[anchorIndex].Metadata, CacheAnchorSequenceMetadataKey),
		"anchor_index":           anchorIndex,
		"prefix_hash_expected":   storedHashStr,
		"prefix_hash_actual":     currentHash,
	})
}

// EmitCacheWarning produces a cache warning when verbose mode is active.
// It writes a one-line TUI warning, appends a cache_warning event to the
// trajectory JSONL, and appends a WARNING block to the transcript.
// When verbose is off this is a no-op.
func EmitCacheWarning(ctx context.Context, reason string, payload map[string]any) {
	if !verboseFromContext(ctx) {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["reason"] = reason

	// TUI: single-line warning to stderr.
	var tuiMsg string
	switch reason {
	case "prefix_drift":
		tuiMsg = fmt.Sprintf("%s prefix drift — anchor seq %v, expected %s, got %s",
			cacheWarningPrefix, payload["anchor_sequence"],
			truncateHash(payload["prefix_hash_expected"]), truncateHash(payload["prefix_hash_actual"]))
	case "threshold_stripped":
		tuiMsg = fmt.Sprintf("%s stripped below threshold — %v total tokens, threshold %v — active anchor seq %v unmarked",
			cacheWarningPrefix, payload["total_tokens"], payload["threshold"], payload["anchor_sequence"])
	case "rotation_blocked":
		tuiMsg = fmt.Sprintf("%s rotation blocked — %v tokens past anchor, min cached floor %v — anchor seq %v pinned",
			cacheWarningPrefix, payload["tokens_since_anchor"], payload["min_cached_tokens"], payload["anchor_sequence"])
	default:
		tuiMsg = fmt.Sprintf("%s %s — %v", cacheWarningPrefix, reason, payload)
	}
	_, _ = fmt.Fprintln(os.Stderr, tuiMsg)

	// Trajectory: emit cache_warning event.
	w, ok := trajectory.FromContext(ctx)
	if ok && w != nil {
		evt := trajectory.Event{
			Level:   "warning",
			Kind:    "cache_warning",
			SpanID:  trajectory.NewSpanID(),
			Payload: payload,
		}
		component := stageFromContext(ctx)
		if component == "" {
			component = "llm"
		}
		evt.Component = component
		if parent, pok := trajectory.ParentSpanID(ctx); pok && parent != "" {
			evt.ParentSpanID = parent
		}
		if emitErr := w.Emit(ctx, evt); emitErr != nil {
			reportLLMTrajectoryError(emitErr)
		}
	}

	// Transcript: append WARNING block.
	if tr, ok := transcriptFromContext(ctx); ok {
		_ = tr.AppendBlock(formatCacheWarningTranscript(reason, payload))
	}
}

func formatCacheWarningTranscript(reason string, payload map[string]any) string {
	var b strings.Builder
	b.WriteString("\n")
	switch reason {
	case "prefix_drift":
		b.WriteString(fmt.Sprintf("WARNING: Cache prefix drift detected. Anchor sequence %v. Expected hash %s, actual %s. The provider will likely miss cache on this request.\n\n",
			payload["anchor_sequence"], truncateHash(payload["prefix_hash_expected"]), truncateHash(payload["prefix_hash_actual"])))
	case "threshold_stripped":
		b.WriteString(fmt.Sprintf("WARNING: Cache anchor unmarked below threshold. Anchor sequence %v. %v total tokens (threshold %v).\n\n",
			payload["anchor_sequence"], payload["total_tokens"], payload["threshold"]))
	case "rotation_blocked":
		b.WriteString(fmt.Sprintf("WARNING: Cache rotation blocked. Anchor sequence %v pinned. %v tokens past anchor (min cached floor %v).\n\n",
			payload["anchor_sequence"], payload["tokens_since_anchor"], payload["min_cached_tokens"]))
	default:
		b.WriteString(fmt.Sprintf("WARNING: Cache %s. %v\n\n", reason, payload))
	}
	return b.String()
}

func truncateHash(v any) string {
	switch val := v.(type) {
	case string:
		if len(val) > 8 {
			return val[:8] + "…"
		}
		return val
	default:
		return fmt.Sprintf("%v", v)
	}
}

func metadataInt(metadata map[string]any, key string) int {
	if metadata == nil {
		return 0
	}
	raw, ok := metadata[key]
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return parsed
		}
	}
	return 0
}

func CachePrefixHash(messages []any, anchorIndex int) string {
	if anchorIndex < 0 || anchorIndex > len(messages) {
		return ""
	}
	var b strings.Builder
	for i := 0; i < anchorIndex && i < len(messages); i++ {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		b.WriteString("role=")
		b.WriteString(stableValueString(msg["role"]))
		b.WriteString(";content=")
		b.WriteString(stableValueString(msg["content"]))
		b.WriteString("\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", sum[:])
}

func stableValueString(val any) string {
	switch v := val.(type) {
	case nil:
		return "null"
	case string:
		return strconv.Quote(v)
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case []any:
		var b strings.Builder
		b.WriteString("[")
		for i, item := range v {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(stableValueString(item))
		}
		b.WriteString("]")
		return b.String()
	case []string:
		items := make([]any, len(v))
		for i, item := range v {
			items[i] = item
		}
		return stableValueString(items)
	case map[string]string:
		items := make(map[string]any, len(v))
		for key, value := range v {
			items[key] = value
		}
		return stableValueString(items)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("{")
		for i, key := range keys {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(strconv.Quote(key))
			b.WriteString(":")
			b.WriteString(stableValueString(v[key]))
		}
		b.WriteString("}")
		return b.String()
	default:
		return fmt.Sprintf("%v", v)
	}
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
	component := stageFromContext(ctx)
	if component == "" {
		component = "llm"
	}
	evt := trajectory.Event{
		Level:     level,
		Kind:      kind,
		SpanID:    trajectory.NewSpanID(),
		Payload:   payload,
		Component: component,
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
	if observer := cacheUsageObserverFromContext(ctx); observer != nil {
		info := CacheUsageInfo{}
		if usage.PromptTokensDetails != nil {
			info.CachedTokens = usage.PromptTokensDetails.CachedTokens
			info.CacheWriteTokens = usage.PromptTokensDetails.CacheWriteTokens
		}
		if usage.CacheDiscount != nil {
			info.CacheDiscount = usage.CacheDiscount
		}
		observer(model, info)
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

func cacheUsageObserverFromContext(ctx context.Context) CacheUsageObserver {
	if ctx == nil {
		return nil
	}
	if raw := ctx.Value(contextKeyCacheUsageObserver{}); raw != nil {
		if observer, ok := raw.(CacheUsageObserver); ok {
			return observer
		}
	}
	return nil
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

func emitRetryEvent(ctx context.Context, model ResolvedModel, attempt int, wait time.Duration, err error, meta llmAttemptMeta) {
	payload := map[string]any{
		"model":        modelSummary(model),
		"mode":         strings.TrimSpace(meta.Mode),
		"attempt":      attempt,
		"next_attempt": attempt + 1,
		"wait_ms":      wait.Milliseconds(),
	}
	if payload["mode"] == "" {
		payload["mode"] = "non-stream"
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
	fallbackCtx := ctx
	wrapped := func(tok string) {
		streamedPrefix.WriteString(tok)
		if tok != "" {
			emitted = true
		}
		if onToken != nil {
			onToken(tok)
		}
	}

	var attemptErr error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		streamMeta := llmAttemptMeta{
			Alias:   strings.TrimSpace(model.Alias),
			Mode:    "stream",
			Attempt: attempt,
			Source:  "primary",
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
		attemptErr = err
		fallbackCtx = attemptCtx
		if !emitted && shouldRetry(err) {
			if attempt >= maxRetries {
				break
			}
			// For retryable errors, probe with a minimal request to avoid
			// burning full conversation tokens while waiting.
			emitRetryEvent(attemptCtx, model, attempt, 0, err, streamMeta)
			if probeErr := probeUntilReadyIndefinite(attemptCtx, model); probeErr != nil {
				return "", fmt.Errorf("liveness probe failed: %w", probeErr)
			}
			// Probe succeeded; retry the full stream request immediately.
			continue
		}
		break
	}

	emitStreamFallbackEvent(fallbackCtx, model, attemptErr, emitted, streamedPrefix.Len())
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

// probeModel sends a minimal request to the model to check if it's available.
// It's used to avoid burning full conversation tokens during 429 rate-limit retries.
func probeModel(ctx context.Context, model ResolvedModel) error {
	probePayload := map[string]any{
		"model":    model.Model,
		"messages": []Message{{Role: "user", Content: "."}},
		"max_tokens": 1,
	}
	body, err := json.Marshal(probePayload)
	if err != nil {
		return err
	}
	req, err := buildRequest(ctx, model, body)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &UnreachableHostError{URL: req.URL.String(), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return &HTTPResponseError{URL: req.URL.String(), Status: resp.StatusCode, Body: strings.TrimSpace(string(b)), Header: resp.Header.Clone()}
	}
	return nil
}

func isRateLimitError(err error) bool {
	var httpErr *HTTPResponseError
	if errors.As(err, &httpErr) {
		return httpErr.Status == http.StatusTooManyRequests
	}
	return false
}

// probeUntilReady loops with a small delay, sending minimal probe requests,
// until the model returns 200 or the probe timeout expires.
func probeUntilReady(ctx context.Context, model ResolvedModel) error {
	timeout := probeTimeout
	if probeTimeoutOverride > 0 {
		timeout = probeTimeoutOverride
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for attempt := 1; ; attempt++ {
		if err := probeCtx.Err(); err != nil {
			return fmt.Errorf("probe timed out after %v: %w", timeout, err)
		}
		err := probeModel(probeCtx, model)
		if err == nil {
			return nil
		}
		if !shouldRetry(err) {
			return fmt.Errorf("probe failed with non-retryable error: %w", err)
		}
		if attempt >= maxRetries {
			return fmt.Errorf("probe retry limit exhausted after %d attempts: %w", maxRetries, err)
		}
		wait := retryDelay(err, nonStreamRetryBackoff(attempt))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-probeCtx.Done():
				timer.Stop()
				return fmt.Errorf("probe timed out after %v: %w", timeout, probeCtx.Err())
			case <-timer.C:
			}
		}
	}
}

// probeUntilReadyIndefinite probes the model with minimal requests using
// exponential backoff, continuing indefinitely until a probe succeeds, the
// context is cancelled, or a non-retryable error occurs. Unlike
// probeUntilReady, there is no timeout or max-retry limit; the probe loop
// only stops when the server becomes available or the context is cancelled.
func probeUntilReadyIndefinite(ctx context.Context, model ResolvedModel) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("probe cancelled: %w", err)
		}
		err := probeModel(ctx, model)
		if err == nil {
			return nil
		}
		if !shouldRetry(err) {
			return fmt.Errorf("probe failed with non-retryable error: %w", err)
		}
		wait := retryDelay(err, nonStreamRetryBackoff(attempt))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("probe cancelled: %w", ctx.Err())
			case <-timer.C:
			}
		}
	}
}

func executeOnceWithRetries(ctx context.Context, model ResolvedModel, body []byte, meta llmAttemptMeta) (string, error) {
	var lastErr error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		attemptMeta := meta
		attemptMeta.Attempt = attempt
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
		if !retryable {
			break
		}
		if attempt >= maxRetries {
			break
		}
		// For retryable errors, probe with a minimal request to avoid
		// burning full conversation tokens while waiting.
		emitRetryEvent(attemptCtx, model, attempt, 0, err, attemptMeta)
		if probeErr := probeUntilReadyIndefinite(attemptCtx, model); probeErr != nil {
			return "", fmt.Errorf("liveness probe failed: %w", probeErr)
		}
		// Probe succeeded; retry the full request immediately.
	}
	if lastErr != nil {
		return "", fmt.Errorf("retry limit exhausted after %d attempts: %w", maxRetries, lastErr)
	}
	return "", errors.New("non-stream request failed without a retryable error")
}

func nonStreamRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	wait := nonStreamRetryInitialBackoff
	for retry := 1; retry < attempt; retry++ {
		if wait >= nonStreamRetryMaxBackoff {
			return nonStreamRetryMaxBackoff
		}
		wait *= 2
		if wait >= nonStreamRetryMaxBackoff {
			return nonStreamRetryMaxBackoff
		}
	}
	return wait
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
