package main

import (
	"strings"
	"sync"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/session"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

type syncFooterTracker struct {
	mu       sync.Mutex
	bus      *ui.EventBus
	runtimes session.PromptRuntimes
	totals   ui.TokenUsageUpdatedEvent
	models   []ui.FooterModelDisplay
	seen     map[string]struct{}
}

func newSyncFooterTracker(bus *ui.EventBus, runtimes session.PromptRuntimes) *syncFooterTracker {
	return &syncFooterTracker{
		bus:      bus,
		runtimes: runtimes,
		seen:     make(map[string]struct{}),
	}
}

func (t *syncFooterTracker) Observe(model llm.ResolvedModel, usage llm.UsageInfo) {
	if t == nil {
		return
	}
	role := syncFooterRole(usage.Stage)
	if role == "" {
		return
	}

	t.mu.Lock()
	modelChanged := t.addModelLocked(role, model)
	usageChanged := usage.UsageAvailable
	if usageChanged {
		promptTokens := max(usage.PromptTokens, 0)
		cachedTokens := max(usage.CachedTokens, 0)
		completionTokens := max(usage.CompletionTokens, 0)
		t.totals.InputHit += cachedTokens
		t.totals.InputMiss += max(promptTokens-cachedTokens, 0)
		t.totals.Output += completionTokens
	}
	models := ui.FooterModelMetadata{Models: append([]ui.FooterModelDisplay(nil), t.models...)}
	totals := t.totals
	t.mu.Unlock()

	if t.bus == nil {
		return
	}
	if modelChanged {
		t.bus.Emit(ui.FooterModelsUpdatedEvent{Models: models})
	}
	if usageChanged {
		t.bus.Emit(totals)
	}
}

func (t *syncFooterTracker) addModelLocked(role string, model llm.ResolvedModel) bool {
	label := session.FooterModelLabel(model)
	if label == "" {
		return false
	}
	key := strings.Join([]string{role, strings.TrimSpace(model.ProviderName), strings.TrimSpace(model.BaseURL), strings.TrimSpace(model.Model)}, "\x00")
	if _, ok := t.seen[key]; ok {
		return false
	}
	t.seen[key] = struct{}{}
	runtime := t.runtimes.Answer
	if role == "discovery" {
		runtime = t.runtimes.FileDiscovery
	}
	t.models = append(t.models, ui.FooterModelDisplay{
		Role:      role,
		Label:     label,
		Reasoning: session.PromptRuntimeFooterReasoning(runtime, model),
	})
	return true
}

func syncFooterRole(stage string) string {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "file-discovery":
		return "discovery"
	case "answer":
		return "answer"
	default:
		return ""
	}
}

func (t *syncFooterTracker) snapshot() (ui.TokenUsageUpdatedEvent, ui.FooterModelMetadata) {
	if t == nil {
		return ui.TokenUsageUpdatedEvent{}, ui.FooterModelMetadata{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.totals, ui.FooterModelMetadata{Models: append([]ui.FooterModelDisplay(nil), t.models...)}
}
