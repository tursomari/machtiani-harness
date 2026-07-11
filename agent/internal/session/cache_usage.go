package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/trajectory/listener"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func startLLMCacheUsageLogger(bus *ui.EventBus, path string, diagWriter io.Writer, initial ui.TokenUsageUpdatedEvent, onUpdate func(ui.TokenUsageUpdatedEvent)) (context.CancelFunc, <-chan struct{}, error) {
	sub, err := listener.New(path)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	opts := listener.SubscribeOptions{
		FollowFromLatest: true,
		Kinds:            []string{"llm.cache.usage"},
		ErrorHandler: func(err error) {
			fmt.Fprintf(diagWriter, "[trajectory] cache usage listener error: %v\n", err)
		},
	}
	totals := tokenUsageTotals{
		InputHit:  initial.InputHit,
		InputMiss: initial.InputMiss,
		Output:    initial.Output,
	}
	go func() {
		defer close(done)
		err := sub.Subscribe(ctx, opts, func(ctx context.Context, evt listener.Event) error {
			if totals.addEvent(evt) {
				update := ui.TokenUsageUpdatedEvent{
					InputHit:  totals.InputHit,
					InputMiss: totals.InputMiss,
					Output:    totals.Output,
				}
				if onUpdate != nil {
					onUpdate(update)
				}
				if bus != nil {
					bus.Emit(update)
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(diagWriter, "[trajectory] cache usage listener stopped: %v\n", err)
		}
	}()
	return cancel, done, nil
}

type tokenUsageTotals struct {
	InputHit  int
	InputMiss int
	Output    int
}

func (t *tokenUsageTotals) addEvent(evt listener.Event) bool {
	if t == nil || evt.Payload == nil {
		return false
	}
	delta, ok := tokenUsageDeltaFromPayload(evt.Payload)
	if !ok {
		return false
	}
	t.InputHit += delta.InputHit
	t.InputMiss += delta.InputMiss
	t.Output += delta.Output
	return true
}

func tokenUsageDeltaFromPayload(payload map[string]any) (ui.TokenUsageUpdatedEvent, bool) {
	var delta ui.TokenUsageUpdatedEvent
	var saw bool

	promptTokens, ok := intFromAny(payload["prompt_tokens"])
	if ok {
		saw = true
	}
	cachedTokens, cachedOK := cachedTokensFromPayload(payload)
	if cachedOK {
		saw = true
	}
	completionTokens, completionOK := intFromAny(payload["completion_tokens"])
	if completionOK {
		saw = true
	}
	if !saw {
		return delta, false
	}

	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if promptTokens < 0 {
		promptTokens = 0
	}
	inputMiss := promptTokens - cachedTokens
	if inputMiss < 0 {
		inputMiss = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}

	delta.InputHit = cachedTokens
	delta.InputMiss = inputMiss
	delta.Output = completionTokens
	return delta, true
}

func cachedTokensFromPayload(payload map[string]any) (int, bool) {
	if payload == nil {
		return 0, false
	}
	if details, ok := mapFromAny(payload["prompt_tokens_details"]); ok {
		if cachedTokens, ok := intFromAny(details["cached_tokens"]); ok {
			return cachedTokens, true
		}
	}
	return intFromAny(payload["cached_tokens"])
}

func mapFromAny(v any) (map[string]any, bool) {
	switch val := v.(type) {
	case map[string]any:
		return val, true
	}
	return nil, false
}

func intFromAny(v any) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case int32:
		return int(val), true
	case float64:
		return int(val), true
	case float32:
		return int(val), true
	case json.Number:
		if i, err := val.Int64(); err == nil {
			return int(i), true
		}
		if f, err := val.Float64(); err == nil {
			return int(f), true
		}
	case string:
		trimmed := strings.TrimSpace(val)
		if trimmed == "" {
			return 0, false
		}
		if i, err := strconv.Atoi(trimmed); err == nil {
			return i, true
		}
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return int(f), true
		}
	}
	return 0, false
}

func floatFromAny(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case int32:
		return float64(val), true
	case json.Number:
		if f, err := val.Float64(); err == nil {
			return f, true
		}
	case string:
		trimmed := strings.TrimSpace(val)
		if trimmed == "" {
			return 0, false
		}
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func boolFromAny(v any) (bool, bool) {
	switch val := v.(type) {
	case bool:
		return val, true
	case int:
		return val != 0, true
	case int64:
		return val != 0, true
	case float64:
		return val != 0, true
	case string:
		trimmed := strings.TrimSpace(val)
		if trimmed == "" {
			return false, false
		}
		if parsed, err := strconv.ParseBool(trimmed); err == nil {
			return parsed, true
		}
	}
	return false, false
}
