package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/tursomari/machtiani/agent/internal/trajectory/listener"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

type failoverLogTracker struct {
	mu       sync.Mutex
	sessions map[string]*failoverSessionState
}

type failoverSessionState struct {
	mu     sync.Mutex
	models map[string]*failoverModelState
}

type failoverModelState struct {
	failoverNotified bool
	resultNotified   bool
	lastParentSpanID string
}

func newFailoverLogTracker() *failoverLogTracker {
	return &failoverLogTracker{sessions: make(map[string]*failoverSessionState)}
}

func (t *failoverLogTracker) process(evt listener.Event) string {
	switch evt.Kind {
	case "llm.failover.start":
		return t.handleFailoverStart(evt)
	case "llm.failover.result":
		return t.handleFailoverResult(evt)
	case "llm.request.end":
		return t.handleRequestEnd(evt)
	case "llm.failover.resolve_error":
		return formatLLMFailoverEvent(evt)
	default:
		return ""
	}
}

func (t *failoverLogTracker) handleFailoverStart(evt listener.Event) string {
	primaryKey := primaryModelKey(evt.Payload)
	if primaryKey == "" {
		return formatLLMFailoverEvent(evt)
	}
	sess := t.sessionState(evt.SessionID)
	if !sess.markStart(primaryKey, evt.ParentSpanID) {
		return ""
	}
	return formatLLMFailoverEvent(evt)
}

func (t *failoverLogTracker) handleFailoverResult(evt listener.Event) string {
	primaryKey := primaryModelKey(evt.Payload)
	if primaryKey == "" {
		return formatLLMFailoverEvent(evt)
	}
	sess := t.sessionState(evt.SessionID)
	if !sess.shouldLogResult(primaryKey, evt.ParentSpanID) {
		return ""
	}
	return formatLLMFailoverEvent(evt)
}

func (t *failoverLogTracker) handleRequestEnd(evt listener.Event) string {
	if isFallbackAttempt(evt.Payload) {
		return ""
	}
	alias := strings.TrimSpace(stringFromAny(evt.Payload["alias"]))
	modelSummary, _ := evt.Payload["model"].(map[string]any)
	primaryKey := modelIdentifier(alias, modelSummary)
	if primaryKey == "" {
		return ""
	}
	sess := t.sessionState(evt.SessionID)
	if !sess.reset(primaryKey) {
		return ""
	}
	description := strings.TrimSpace(describeFailoverModel(modelSummary))
	if description == "" || description == "unknown model" {
		description = primaryKey
	}
	return fmt.Sprintf("[llm failover] recovered: %s responding normally", description)
}

func (t *failoverLogTracker) sessionState(sessionID string) *failoverSessionState {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		id = "__global__"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sessions == nil {
		t.sessions = make(map[string]*failoverSessionState)
	}
	if state, ok := t.sessions[id]; ok {
		return state
	}
	state := &failoverSessionState{models: make(map[string]*failoverModelState)}
	t.sessions[id] = state
	return state
}

func (s *failoverSessionState) markStart(key, parentSpanID string) bool {
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.models == nil {
		s.models = make(map[string]*failoverModelState)
	}
	state, ok := s.models[key]
	if !ok {
		state = &failoverModelState{}
		s.models[key] = state
	}
	if parentSpanID != "" {
		parentSpanID = strings.TrimSpace(parentSpanID)
	}
	if state.failoverNotified && state.lastParentSpanID == parentSpanID {
		return false
	}
	state.failoverNotified = true
	state.resultNotified = false
	state.lastParentSpanID = parentSpanID
	return true
}

func (s *failoverSessionState) shouldLogResult(key, parentSpanID string) bool {
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.models == nil {
		return false
	}
	state, ok := s.models[key]
	if !ok {
		return false
	}
	if parentSpanID != "" {
		parentSpanID = strings.TrimSpace(parentSpanID)
	}
	if !state.failoverNotified {
		return false
	}
	if state.resultNotified && state.lastParentSpanID == parentSpanID {
		return false
	}
	state.resultNotified = true
	state.lastParentSpanID = parentSpanID
	return true
}

func (s *failoverSessionState) reset(key string) bool {
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.models == nil {
		return false
	}
	state, ok := s.models[key]
	if !ok || !state.failoverNotified {
		return false
	}
	delete(s.models, key)
	return true
}

func primaryModelKey(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	from, _ := payload["from"].(map[string]any)
	alias := strings.TrimSpace(stringFromAny(from["alias"]))
	return modelIdentifier(alias, from)
}

func modelIdentifier(alias string, summary map[string]any) string {
	alias = strings.TrimSpace(alias)
	if alias != "" {
		return alias
	}
	if len(summary) == 0 {
		return ""
	}
	baseURL := strings.TrimSpace(stringFromAny(summary["base_url"]))
	endpoint := strings.TrimSpace(stringFromAny(summary["endpoint"]))
	modelName := strings.TrimSpace(stringFromAny(summary["model"]))
	provider := strings.TrimSpace(stringFromAny(summary["provider"]))
	if key := joinKeyParts(baseURL, endpoint, modelName); key != "" {
		return key
	}
	if key := joinKeyParts(provider, modelName); key != "" {
		return key
	}
	return ""
}

func joinKeyParts(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	return strings.Join(filtered, "|")
}

func isFallbackAttempt(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	v, ok := payload["fallback"]
	if !ok {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return strings.EqualFold(strings.TrimSpace(val), "true")
	default:
		return false
	}
}

func startLLMFailoverLogger(display *ui.TerminalDisplay, path string) (context.CancelFunc, <-chan struct{}, error) {
	sub, err := listener.New(path)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	tracker := newFailoverLogTracker()
	opts := listener.SubscribeOptions{
		FollowFromLatest: true,
		Kinds:            []string{"llm.failover.start", "llm.failover.result", "llm.failover.resolve_error", "llm.request.end"},
		ErrorHandler: func(err error) {
			fmt.Fprintf(os.Stderr, "[trajectory] failover listener error: %v\n", err)
		},
	}
	go func() {
		defer close(done)
		err := sub.Subscribe(ctx, opts, func(ctx context.Context, evt listener.Event) error {
			if msg := tracker.process(evt); strings.TrimSpace(msg) != "" {
				display.Notify(msg)
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "[trajectory] failover listener stopped: %v\n", err)
		}
	}()
	return cancel, done, nil
}

func formatLLMFailoverEvent(evt listener.Event) string {
	payload := evt.Payload
	alias := strings.TrimSpace(stringFromAny(payload["alias"]))
	source := strings.TrimSpace(stringFromAny(payload["source"]))
	meta := formatFailoverMeta(alias, source)
	switch evt.Kind {
	case "llm.failover.start":
		from := describeFailoverModel(payload["from"])
		to := describeFailoverModel(payload["to"])
		reason := strings.TrimSpace(errorMessageFromEvent(evt))
		msg := "[llm failover] triggered"
		if meta != "" {
			msg += " " + meta
		}
		msg += fmt.Sprintf(": %s -> %s", from, to)
		if reason != "" {
			msg += fmt.Sprintf(" (reason: %s)", reason)
		}
		return msg
	case "llm.failover.result":
		from := describeFailoverModel(payload["from"])
		to := describeFailoverModel(payload["to"])
		success, _ := payload["success"].(bool)
		msg := "[llm failover]"
		if success {
			msg += " succeeded"
			if meta != "" {
				msg += " " + meta
			}
			msg += fmt.Sprintf(": now using %s (previous %s)", to, from)
			return msg
		}
		msg += " failed"
		if meta != "" {
			msg += " " + meta
		}
		reason := strings.TrimSpace(errorMessageFromEvent(evt))
		if reason == "" {
			reason = "unknown error"
		}
		if status := strings.TrimSpace(stringFromAny(payload["status"])); status != "" && status != "0" {
			reason = fmt.Sprintf("%s (status %s)", reason, status)
		}
		msg += fmt.Sprintf(": attempted %s (fallback from %s); %s", to, from, reason)
		if url := strings.TrimSpace(stringFromAny(payload["url"])); url != "" {
			msg += fmt.Sprintf(" [%s]", url)
		}
		return msg
	case "llm.failover.resolve_error":
		alias := strings.TrimSpace(stringFromAny(payload["alias"]))
		meta := formatFailoverMeta(alias, "")
		reason := strings.TrimSpace(errorMessageFromEvent(evt))
		if reason == "" {
			reason = "failed to resolve fallback model"
		}
		msg := "[llm failover] resolution error"
		if meta != "" {
			msg += " " + meta
		}
		msg += ": " + reason
		return msg
	default:
		return ""
	}
}

func describeFailoverModel(value any) string {
	m, ok := value.(map[string]any)
	if !ok || len(m) == 0 {
		return "unknown model"
	}
	alias := strings.TrimSpace(stringFromAny(m["alias"]))
	model := strings.TrimSpace(stringFromAny(m["model"]))
	provider := strings.TrimSpace(stringFromAny(m["provider"]))
	parts := make([]string, 0, 3)
	if alias != "" {
		parts = append(parts, alias)
	}
	if model != "" && !strings.EqualFold(model, alias) {
		parts = append(parts, model)
	}
	if provider != "" {
		parts = append(parts, fmt.Sprintf("(%s)", provider))
	}
	if len(parts) == 0 {
		return "unknown model"
	}
	return strings.Join(parts, " ")
}

func errorMessageFromEvent(evt listener.Event) string {
	if evt.Err != nil {
		return evt.Err.Message
	}
	if msg := strings.TrimSpace(stringFromAny(evt.Payload["error_message"])); msg != "" {
		return msg
	}
	return ""
}

func formatFailoverMeta(alias, source string) string {
	var parts []string
	if alias = strings.TrimSpace(alias); alias != "" {
		parts = append(parts, "alias="+alias)
	}
	if source = strings.TrimSpace(source); source != "" {
		parts = append(parts, "source="+source)
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func stringFromAny(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case fmt.Stringer:
		return val.String()
	case float64, float32, int, int32, int64, uint, uint32, uint64, bool:
		return fmt.Sprint(val)
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
}
