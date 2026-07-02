package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/trajectory/listener"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func startLLMCacheDiagnosticsLogger(bus *ui.EventBus, path string, diagWriter io.Writer) (context.CancelFunc, <-chan struct{}, error) {
	sub, err := listener.New(path)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	opts := listener.SubscribeOptions{
		FollowFromLatest: true,
		Kinds:            []string{"llm.cache.injected"},
		ErrorHandler: func(err error) {
			fmt.Fprintf(diagWriter, "[trajectory] cache diagnostics listener error: %v\n", err)
		},
	}
	go func() {
		defer close(done)
		err := sub.Subscribe(ctx, opts, func(ctx context.Context, evt listener.Event) error {
			if msg := formatLLMCacheDiagnosticsEvent(evt); strings.TrimSpace(msg) != "" {
				bus.Emit(ui.NotificationEvent{Level: ui.NotificationInfo, Message: msg})
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(diagWriter, "[trajectory] cache diagnostics listener stopped: %v\n", err)
		}
	}()
	return cancel, done, nil
}

func formatLLMCacheDiagnosticsEvent(evt listener.Event) string {
	payload := evt.Payload
	if payload == nil {
		return ""
	}
	parts := []string{}
	if anchorIndex, ok := intFromAny(payload["anchor_index"]); ok {
		parts = append(parts, fmt.Sprintf("anchor=%d", anchorIndex))
	}
	if prevIndex, ok := intFromAny(payload["anchor_prev_index"]); ok {
		parts = append(parts, fmt.Sprintf("prev=%d", prevIndex))
	}
	if rotated, ok := boolFromAny(payload["anchor_rotated"]); ok && rotated {
		parts = append(parts, "rotated")
	}
	if messages, ok := intFromAny(payload["message_count"]); ok {
		parts = append(parts, fmt.Sprintf("messages=%d", messages))
	}
	if totalTokens, ok := intFromAny(payload["total_tokens"]); ok {
		parts = append(parts, fmt.Sprintf("tokens=%d", totalTokens))
	}
	if tokensSince, ok := intFromAny(payload["tokens_since_anchor"]); ok {
		parts = append(parts, fmt.Sprintf("since-anchor=%d", tokensSince))
	}
	if messagesSince, ok := intFromAny(payload["messages_since_anchor"]); ok {
		parts = append(parts, fmt.Sprintf("since-msgs=%d", messagesSince))
	}
	if lookback, ok := intFromAny(payload["lookback"]); ok {
		parts = append(parts, fmt.Sprintf("lookback=%d", lookback))
	}
	if threshold, ok := intFromAny(payload["threshold"]); ok {
		parts = append(parts, fmt.Sprintf("threshold=%d", threshold))
	}
	if prefix := strings.TrimSpace(stringFromAny(payload["prefix_hash"])); prefix != "" {
		if len(prefix) > 12 {
			prefix = prefix[:12]
		}
		parts = append(parts, fmt.Sprintf("prefix=%s", prefix))
	}
	if len(parts) == 0 {
		return ""
	}
	component := strings.TrimSpace(evt.Component)
	if component == "" {
		component = strings.TrimSpace(evt.Event.Component)
	}
	if component == "" {
		component = "unknown"
	}
	return fmt.Sprintf("[llm cache] injected (%s): %s", component, strings.Join(parts, " "))
}
