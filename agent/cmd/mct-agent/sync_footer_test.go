package main

import (
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/session"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func TestSyncFooterTrackerAggregatesUsageAndActualModels(t *testing.T) {
	runtimes := session.PromptRuntimes{}
	runtimes.FileDiscovery.Extras = map[string]any{"reasoning": map[string]any{"effort": "low"}}
	runtimes.Answer.Extras = map[string]any{"reasoning_effort": "high"}
	tracker := newSyncFooterTracker(nil, runtimes)

	discovery := llm.ResolvedModel{ProviderName: "provider-a", Model: "discovery-fallback"}
	tracker.Observe(discovery, llm.UsageInfo{
		Stage:            "file-discovery",
		PromptTokens:     100,
		CompletionTokens: 20,
		CachedTokens:     40,
		UsageAvailable:   true,
	})
	tracker.Observe(discovery, llm.UsageInfo{
		Stage:            "file-discovery",
		PromptTokens:     60,
		CompletionTokens: 5,
		UsageAvailable:   true,
	})
	tracker.Observe(llm.ResolvedModel{ProviderName: "provider-b", Model: "answer-model"}, llm.UsageInfo{
		Stage:          "answer",
		UsageAvailable: false,
	})

	totals, metadata := tracker.snapshot()
	wantTotals := ui.TokenUsageUpdatedEvent{InputHit: 40, InputMiss: 120, Output: 25}
	if totals != wantTotals {
		t.Fatalf("totals = %+v, want %+v", totals, wantTotals)
	}
	if len(metadata.Models) != 2 {
		t.Fatalf("models = %+v", metadata.Models)
	}
	if got := metadata.Models[0]; got.Role != "discovery" || got.Label != "provider-a:discovery-fallback" || got.Reasoning != "low" {
		t.Fatalf("discovery model = %+v", got)
	}
	if got := metadata.Models[1]; got.Role != "answer" || got.Label != "provider-b:answer-model" || got.Reasoning != "high" {
		t.Fatalf("answer model = %+v", got)
	}
}

func TestSyncFooterTrackerClampsUsageAndKeepsSameModelForBothRoles(t *testing.T) {
	tracker := newSyncFooterTracker(nil, session.PromptRuntimes{})
	model := llm.ResolvedModel{Model: "shared-model"}
	tracker.Observe(model, llm.UsageInfo{
		Stage:            "file-discovery",
		PromptTokens:     20,
		CachedTokens:     30,
		CompletionTokens: -2,
		UsageAvailable:   true,
	})
	tracker.Observe(model, llm.UsageInfo{Stage: "answer", UsageAvailable: false})
	tracker.Observe(model, llm.UsageInfo{Stage: "unknown", PromptTokens: 999, UsageAvailable: true})

	totals, metadata := tracker.snapshot()
	if want := (ui.TokenUsageUpdatedEvent{InputHit: 30}); totals != want {
		t.Fatalf("totals = %+v, want %+v", totals, want)
	}
	if len(metadata.Models) != 2 || metadata.Models[0].Role != "discovery" || metadata.Models[1].Role != "answer" {
		t.Fatalf("models = %+v", metadata.Models)
	}
}

func TestSyncFooterTrackerEmitsCumulativeUpdates(t *testing.T) {
	bus := ui.NewEventBus(8)
	defer bus.Close()
	sub := bus.Subscribe()
	tracker := newSyncFooterTracker(bus, session.PromptRuntimes{})
	tracker.Observe(llm.ResolvedModel{Model: "model"}, llm.UsageInfo{
		Stage:            "answer",
		PromptTokens:     12,
		CompletionTokens: 3,
		UsageAvailable:   true,
	})

	first := <-sub
	if _, ok := first.(ui.FooterModelsUpdatedEvent); !ok {
		t.Fatalf("first event = %T, want FooterModelsUpdatedEvent", first)
	}
	second := <-sub
	usage, ok := second.(ui.TokenUsageUpdatedEvent)
	if !ok || usage != (ui.TokenUsageUpdatedEvent{InputMiss: 12, Output: 3}) {
		t.Fatalf("second event = %#v", second)
	}
}
