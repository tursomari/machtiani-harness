package llm

import (
	"context"
	"testing"
)

func TestApplyCacheControlSkipsBelowThreshold(t *testing.T) {
	model := ResolvedModel{
		CacheKeyName:          "cache_control",
		CacheControl:          map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold: 100,
		CacheLookbackOffset:   2,
	}
	messages := []Message{
		{Role: "user", Content: "hello", Metadata: map[string]any{"estimated_tokens": 20}},
		{Role: "assistant", Content: "world", Metadata: map[string]any{"estimated_tokens": 30}},
	}

	got := applyCacheControl(context.Background(), messages, model)
	if len(got) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(got))
	}
	if anchor := cacheAnchorIndex(t, got, model.CacheKeyName); anchor != -1 {
		t.Fatalf("expected no cache anchor, got %d", anchor)
	}
	for i, raw := range got {
		msg := messageMap(t, raw)
		content, ok := msg["content"].(string)
		if !ok {
			t.Fatalf("message %d content should be string, got %T", i, msg["content"])
		}
		if content != messages[i].Content {
			t.Fatalf("message %d content mismatch: got %q want %q", i, content, messages[i].Content)
		}
	}
}

func TestApplyCacheControlInjectsAtLookback(t *testing.T) {
	model := ResolvedModel{
		CacheKeyName:          "cache_control",
		CacheControl:          map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold: 100,
		CacheLookbackOffset:   2,
	}
	messages := []Message{
		{Role: "user", Content: "first", Metadata: map[string]any{"estimated_tokens": 60}},
		{Role: "assistant", Content: "second", Metadata: map[string]any{"estimated_tokens": 60}},
		{Role: "user", Content: "third", Metadata: map[string]any{"estimated_tokens": 60}},
		{Role: "assistant", Content: "fourth", Metadata: map[string]any{"estimated_tokens": 60}},
	}

	got := applyCacheControl(context.Background(), messages, model)
	if len(got) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(got))
	}
	anchor := cacheAnchorIndex(t, got, model.CacheKeyName)
	if anchor != 2 {
		t.Fatalf("expected anchor index 2, got %d", anchor)
	}
	anchorMsg := messageMap(t, got[anchor])
	parts := contentParts(t, anchorMsg)
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(parts))
	}
	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected content part map, got %T", parts[0])
	}
	if part["type"] != "text" {
		t.Fatalf("expected content type text, got %v", part["type"])
	}
	if part["text"] != messages[anchor].Content {
		t.Fatalf("expected cached text %q, got %v", messages[anchor].Content, part["text"])
	}
	control, ok := part[model.CacheKeyName].(map[string]any)
	if !ok {
		t.Fatalf("expected cache control map, got %T", part[model.CacheKeyName])
	}
	if control["type"] != "ephemeral" {
		t.Fatalf("expected cache control type ephemeral, got %v", control["type"])
	}
	for i, raw := range got {
		if i == anchor {
			continue
		}
		msg := messageMap(t, raw)
		if _, ok := msg["content"].(string); !ok {
			t.Fatalf("message %d content should be string, got %T", i, msg["content"])
		}
	}
}

func TestApplyCacheControlInsertsMarkerAfterAssistant(t *testing.T) {
	model := ResolvedModel{
		CacheKeyName:          "cache_control",
		CacheControl:          map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold: 10,
		CacheLookbackOffset:   2,
	}
	messages := []Message{
		{Role: "user", Content: "intro", Metadata: map[string]any{"estimated_tokens": 6}},
		{Role: "assistant", Content: "reply", Metadata: map[string]any{"estimated_tokens": 6}},
		{Role: "assistant", Content: "followup", Metadata: map[string]any{"estimated_tokens": 6}},
		{Role: "assistant", Content: "tail", Metadata: map[string]any{"estimated_tokens": 6}},
	}

	got := applyCacheControl(context.Background(), messages, model)
	if len(got) != len(messages)+1 {
		t.Fatalf("expected %d messages, got %d", len(messages)+1, len(got))
	}
	anchor := cacheAnchorIndex(t, got, model.CacheKeyName)
	if anchor != 2 {
		t.Fatalf("expected anchor index 2, got %d", anchor)
	}
	anchorMsg := messageMap(t, got[anchor])
	if anchorMsg["role"] != "user" {
		t.Fatalf("expected anchor role user, got %v", anchorMsg["role"])
	}
	parts := contentParts(t, anchorMsg)
	if len(parts) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(parts))
	}
	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected content part map, got %T", parts[0])
	}
	if part["text"] != cacheAnchorMarkerText {
		t.Fatalf("expected cached text %q, got %v", cacheAnchorMarkerText, part["text"])
	}
	if _, ok := part[model.CacheKeyName]; !ok {
		t.Fatalf("expected cache control field on anchor")
	}
	originalIndex := 0
	for i, raw := range got {
		if i == anchor {
			continue
		}
		msg := messageMap(t, raw)
		content, ok := msg["content"].(string)
		if !ok {
			t.Fatalf("message %d content should be string, got %T", i, msg["content"])
		}
		if content != messages[originalIndex].Content {
			t.Fatalf("message %d content mismatch: got %q want %q", i, content, messages[originalIndex].Content)
		}
		originalIndex++
	}
	if originalIndex != len(messages) {
		t.Fatalf("expected to see %d original messages, saw %d", len(messages), originalIndex)
	}
}

func cacheAnchorIndex(t *testing.T, messages []any, cacheKey string) int {
	t.Helper()
	anchor := -1
	for i, raw := range messages {
		msg := messageMap(t, raw)
		content := msg["content"]
		parts, ok := content.([]any)
		if !ok {
			continue
		}
		if len(parts) != 1 {
			t.Fatalf("message %d expected 1 content part, got %d", i, len(parts))
		}
		part, ok := parts[0].(map[string]any)
		if !ok {
			t.Fatalf("message %d content part should be map, got %T", i, parts[0])
		}
		if _, ok := part[cacheKey]; ok {
			if anchor != -1 {
				t.Fatalf("multiple cache anchors found")
			}
			anchor = i
		}
	}
	return anchor
}

func messageMap(t *testing.T, msg any) map[string]any {
	t.Helper()
	m, ok := msg.(map[string]any)
	if !ok {
		t.Fatalf("expected message map, got %T", msg)
	}
	return m
}

func contentParts(t *testing.T, msg map[string]any) []any {
	t.Helper()
	parts, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("expected content array, got %T", msg["content"])
	}
	return parts
}
