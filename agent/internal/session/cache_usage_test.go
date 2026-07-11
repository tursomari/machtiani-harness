package session

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/trajectory/listener"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func TestTokenUsageTotalsAggregateUsagePayloads(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		want    ui.TokenUsageUpdatedEvent
		ok      bool
	}{
		{
			name: "cached prompt tokens present",
			payload: map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 25,
				"prompt_tokens_details": map[string]any{
					"cached_tokens": 40,
				},
			},
			want: ui.TokenUsageUpdatedEvent{InputHit: 40, InputMiss: 60, Output: 25},
			ok:   true,
		},
		{
			name: "no cached tokens",
			payload: map[string]any{
				"prompt_tokens":     75,
				"completion_tokens": 10,
			},
			want: ui.TokenUsageUpdatedEvent{InputHit: 0, InputMiss: 75, Output: 10},
			ok:   true,
		},
		{
			name: "malformed fields ignored safely",
			payload: map[string]any{
				"prompt_tokens":     "not-a-number",
				"completion_tokens": []string{"bad"},
			},
			want: ui.TokenUsageUpdatedEvent{},
			ok:   false,
		},
		{
			name: "cached greater than prompt clamps miss",
			payload: map[string]any{
				"prompt_tokens":     20,
				"completion_tokens": 5,
				"prompt_tokens_details": map[string]any{
					"cached_tokens": 30,
				},
			},
			want: ui.TokenUsageUpdatedEvent{InputHit: 30, InputMiss: 0, Output: 5},
			ok:   true,
		},
		{
			name: "legacy flattened cached tokens",
			payload: map[string]any{
				"prompt_tokens":     50,
				"completion_tokens": 7,
				"cached_tokens":     12,
			},
			want: ui.TokenUsageUpdatedEvent{InputHit: 12, InputMiss: 38, Output: 7},
			ok:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tokenUsageDeltaFromPayload(tt.payload)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("delta = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTokenUsageTotalsAddEventAccumulates(t *testing.T) {
	var totals tokenUsageTotals
	events := []listener.Event{
		{Event: trajectory.Event{Payload: map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "cached_tokens": 25}}},
		{Event: trajectory.Event{Payload: map[string]any{"prompt_tokens": 60, "completion_tokens": 5}}},
		{Event: trajectory.Event{Payload: map[string]any{"prompt_tokens": "bad"}}},
	}

	if !totals.addEvent(events[0]) {
		t.Fatal("first event was not aggregated")
	}
	if !totals.addEvent(events[1]) {
		t.Fatal("second event was not aggregated")
	}
	if totals.addEvent(events[2]) {
		t.Fatal("malformed event should not be aggregated")
	}

	want := tokenUsageTotals{InputHit: 25, InputMiss: 135, Output: 15}
	if totals != want {
		t.Fatalf("totals = %+v, want %+v", totals, want)
	}
}

func TestStartLLMCacheUsageLoggerEmitsTokenUsageNotNotifications(t *testing.T) {
	path := t.TempDir() + "/trajectory.jsonl"
	writer, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "llm"})
	if err != nil {
		t.Fatalf("trajectory.New: %v", err)
	}
	defer writer.Close()

	bus := ui.NewEventBus(16)
	defer bus.Close()
	sub := bus.Subscribe()

	var diag bytes.Buffer
	initial := ui.TokenUsageUpdatedEvent{InputHit: 1000, InputMiss: 2000, Output: 3000}
	updates := make(chan ui.TokenUsageUpdatedEvent, 1)
	cancel, done, err := startLLMCacheUsageLogger(bus, path, &diag, initial, func(update ui.TokenUsageUpdatedEvent) {
		select {
		case updates <- update:
		default:
		}
	})
	if err != nil {
		t.Fatalf("startLLMCacheUsageLogger: %v", err)
	}
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = writer.Emit(context.Background(), trajectory.Event{
				Kind: "llm.cache.usage",
				Payload: map[string]any{
					"prompt_tokens":     100,
					"completion_tokens": 20,
					"prompt_tokens_details": map[string]any{
						"cached_tokens": 30,
					},
				},
			})
			_ = writer.Emit(context.Background(), trajectory.Event{
				Kind:    "llm.cache.injected",
				Payload: map[string]any{"anchor_index": 1},
			})
		case evt := <-sub:
			switch e := evt.(type) {
			case ui.NotificationEvent:
				if strings.Contains(e.Message, "[llm cache]") {
					t.Fatalf("unexpected cache notification: %s", e.Message)
				}
			case ui.TokenUsageUpdatedEvent:
				want := ui.TokenUsageUpdatedEvent{InputHit: 1030, InputMiss: 2070, Output: 3020}
				if e != want {
					t.Fatalf("token usage update = %+v, want %+v", e, want)
				}
				select {
				case got := <-updates:
					if got != want {
						t.Fatalf("callback update = %+v, want %+v", got, want)
					}
				default:
					t.Fatal("expected callback update")
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for token usage update; diagnostics: %s", diag.String())
		}
	}
}
