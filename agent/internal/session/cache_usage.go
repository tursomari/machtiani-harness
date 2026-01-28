package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/trajectory/listener"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func startLLMCacheUsageLogger(display *ui.TerminalDisplay, path string) (context.CancelFunc, <-chan struct{}, error) {
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
			fmt.Fprintf(os.Stderr, "[trajectory] cache usage listener error: %v\n", err)
		},
	}
	go func() {
		defer close(done)
		err := sub.Subscribe(ctx, opts, func(ctx context.Context, evt listener.Event) error {
			if msg := formatLLMCacheUsageEvent(evt); strings.TrimSpace(msg) != "" {
				display.Notify(msg)
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "[trajectory] cache usage listener stopped: %v\n", err)
		}
	}()
	return cancel, done, nil
}

func formatLLMCacheUsageEvent(evt listener.Event) string {
	if evt.Payload == nil {
		return ""
	}
	parts := []string{}
	if promptTokens, ok := intFromAny(evt.Payload["prompt_tokens"]); ok {
		parts = append(parts, fmt.Sprintf("prompt=%d", promptTokens))
	}
	if cachedTokens, ok := intFromAny(evt.Payload["cached_tokens"]); ok {
		parts = append(parts, fmt.Sprintf("cached=%d", cachedTokens))
	}
	if writeTokens, ok := intFromAny(evt.Payload["cache_write_tokens"]); ok {
		parts = append(parts, fmt.Sprintf("cache-write=%d", writeTokens))
	}
	if discount, ok := floatFromAny(evt.Payload["cache_discount"]); ok {
		parts = append(parts, fmt.Sprintf("discount=%.2f", discount))
	}
	if len(parts) == 0 {
		return ""
	}
	modelLabel := describeFailoverModel(evt.Payload["model"])
	componentLabel := strings.TrimSpace(evt.Component)
	if componentLabel == "" {
		componentLabel = "unknown"
	}
	if modelLabel != "" && modelLabel != "unknown model" {
		return fmt.Sprintf("[llm cache] %s (%s): %s", modelLabel, componentLabel, strings.Join(parts, " "))
	}
	return fmt.Sprintf("[llm cache] (%s): %s", componentLabel, strings.Join(parts, " "))
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
