package session

import (
	"bytes"
	"context"
	"io"
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

func TestTokenUsageTrackerKeepsPlannerContextStableDuringChildCalls(t *testing.T) {
	tracker := newTokenUsageTracker(ui.TokenUsageUpdatedEvent{
		InputHit:           1000,
		InputMiss:          2000,
		Output:             3000,
		ActivePromptTokens: 80,
	})

	shellUpdate, ok := tracker.addEvent(listener.Event{
		Component: "shell-agent",
		Event: trajectory.Event{Payload: map[string]any{
			"prompt_tokens":     100,
			"cached_tokens":     30,
			"completion_tokens": 20,
		}},
	})
	if !ok {
		t.Fatal("shell-agent usage was not aggregated")
	}
	wantShell := ui.TokenUsageUpdatedEvent{
		InputHit:           1030,
		InputMiss:          2070,
		Output:             3020,
		ActivePromptTokens: 80,
	}
	if shellUpdate != wantShell {
		t.Fatalf("shell-agent update = %+v, want %+v", shellUpdate, wantShell)
	}

	plannerUpdate, ok := tracker.addEvent(listener.Event{
		Component: "planner",
		Event: trajectory.Event{Payload: map[string]any{
			"prompt_tokens":     60,
			"cached_tokens":     20,
			"completion_tokens": 10,
		}},
	})
	if !ok {
		t.Fatal("planner usage was not aggregated")
	}
	wantPlanner := ui.TokenUsageUpdatedEvent{
		InputHit:           1050,
		InputMiss:          2110,
		Output:             3030,
		ActivePromptTokens: 60,
	}
	if plannerUpdate != wantPlanner {
		t.Fatalf("planner update = %+v, want %+v", plannerUpdate, wantPlanner)
	}
}

func TestReadLatestPlannerPromptFromTrajectoryBackfillsPlannerOnly(t *testing.T) {
	path := t.TempDir() + "/trajectory.jsonl"
	writer, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("trajectory.New: %v", err)
	}
	for _, evt := range []trajectory.Event{
		{Component: "planner", Kind: "llm.cache.usage", Payload: map[string]any{"prompt_tokens": 100, "cached_tokens": 40}},
		{Component: "shell-agent", Kind: "llm.cache.usage", Payload: map[string]any{"prompt_tokens": 500}},
		{Component: "planner", Kind: "llm.cache.usage", Payload: map[string]any{"prompt_tokens": 60}},
		{Component: "planner", Kind: "planner.response", Payload: map[string]any{"prompt_tokens": 999}},
	} {
		if err := writer.Emit(context.Background(), evt); err != nil {
			t.Fatalf("emit trajectory event: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close trajectory writer: %v", err)
	}

	got, err := readLatestPlannerPromptFromTrajectory(path)
	if err != nil {
		t.Fatalf("readLatestPlannerPromptFromTrajectory: %v", err)
	}
	want := plannerPromptUsage{ActivePromptTokens: 60, Found: true}
	if got != want {
		t.Fatalf("planner usage = %+v, want %+v", got, want)
	}
}

func TestStartLLMCacheUsageLoggerSeedsLegacyPlannerStats(t *testing.T) {
	path := t.TempDir() + "/trajectory.jsonl"
	writer, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("trajectory.New: %v", err)
	}
	for _, evt := range []trajectory.Event{
		{Component: "planner", Kind: "llm.cache.usage", Payload: map[string]any{"prompt_tokens": 100}},
		{Component: "shell-agent", Kind: "llm.cache.usage", Payload: map[string]any{"prompt_tokens": 500}},
	} {
		if err := writer.Emit(context.Background(), evt); err != nil {
			t.Fatalf("emit trajectory event: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close trajectory writer: %v", err)
	}

	initial := ui.TokenUsageUpdatedEvent{InputHit: 10, InputMiss: 20, Output: 5}
	var seeded ui.TokenUsageUpdatedEvent
	cancel, done, err := startLLMCacheUsageLogger(nil, path, io.Discard, initial, func(update ui.TokenUsageUpdatedEvent) {
		seeded = update
	})
	if err != nil {
		t.Fatalf("startLLMCacheUsageLogger: %v", err)
	}
	cancel()
	<-done

	want := initial
	want.ActivePromptTokens = 100
	if seeded != want {
		t.Fatalf("seeded usage = %+v, want %+v", seeded, want)
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
	initial := ui.TokenUsageUpdatedEvent{InputHit: 1000, InputMiss: 2000, Output: 3000, ActivePromptTokens: 80}
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
				Component: "planner",
				Kind:      "llm.cache.usage",
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
				want := ui.TokenUsageUpdatedEvent{InputHit: 1030, InputMiss: 2070, Output: 3020, ActivePromptTokens: 100}
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
