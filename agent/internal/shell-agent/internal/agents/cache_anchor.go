package agents

import (
	"strconv"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

type resolvedModelProvider interface {
	ResolvedModel() llm.ResolvedModel
}

type cacheUsageTracker struct {
	maxCachedTokens int
}

func (t *cacheUsageTracker) Observe(_ llm.ResolvedModel, usage llm.CacheUsageInfo) {
	if usage.CachedTokens > t.maxCachedTokens {
		t.maxCachedTokens = usage.CachedTokens
	}
}

func (t *cacheUsageTracker) UpdateMessages(messages []minisweagent.Message) {
	if t == nil || len(messages) == 0 || t.maxCachedTokens <= 0 {
		return
	}
	anchorIndex := activeCacheAnchorIndex(messages)
	if anchorIndex < 0 {
		return
	}
	metadata := messages[anchorIndex].Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	current := anchorCachedTokens(metadata)
	if t.maxCachedTokens > current {
		metadata[llm.CacheAnchorCachedTokensMetadataKey] = t.maxCachedTokens
	}
	messages[anchorIndex].Metadata = metadata
}

func (a *DefaultAgent) resolvedModel() (llm.ResolvedModel, bool) {
	if a == nil || a.RunConfig.Model == nil {
		return llm.ResolvedModel{}, false
	}
	provider, ok := a.RunConfig.Model.(resolvedModelProvider)
	if !ok {
		return llm.ResolvedModel{}, false
	}
	return provider.ResolvedModel(), true
}

func (a *DefaultAgent) ensureCacheAnchor(sysContent string, messages []minisweagent.Message, step int, model llm.ResolvedModel) []minisweagent.Message {
	if !cacheControlEnabled(model) {
		return messages
	}
	if estimatePromptTokens(messages) < model.CacheTriggerThreshold {
		return messages
	}
	anchorIndex := activeCacheAnchorIndex(a.State.Messages)
	if anchorIndex < 0 {
		insertIndex := cacheAnchorInsertIndex(cacheAnchorIndexForPrompt(len(messages), model.CacheLookbackOffset), messages)
		a.insertCacheAnchor(insertIndex, step, estimatePromptTokens(messages))
		result := a.queryMessagesWithSystem(sysContent)
		stampCachePrefixHash(result)
		return result
	}
	if !shouldRotateCacheAnchor(model, a.State.Messages[anchorIndex].Metadata, messages) {
		return messages
	}
	markCacheAnchorRetired(a.State.Messages, anchorIndex)
	insertIndex := cacheAnchorInsertIndex(len(messages)-1, messages)
	a.insertCacheAnchor(insertIndex, step, estimatePromptTokens(messages))
	result := a.queryMessagesWithSystem(sysContent)
	stampCachePrefixHash(result)
	return result
}

func (a *DefaultAgent) insertCacheAnchor(index, step, anchorTokens int) {
	metadata := newCacheAnchorMetadata(a.State.Messages, step, anchorTokens)
	msg := minisweagent.Message{Role: "user", Content: llm.CacheAnchorMarkerText, Metadata: metadata}
	msg = a.messageWithEstimatedTokens(msg)
	if index < 0 {
		index = 0
	}
	if index > len(a.State.Messages) {
		index = len(a.State.Messages)
	}
	a.State.Messages = append(a.State.Messages, minisweagent.Message{})
	copy(a.State.Messages[index+1:], a.State.Messages[index:])
	a.State.Messages[index] = msg
}

func (a *DefaultAgent) syncCacheAnchorMetadata(messages []minisweagent.Message) {
	if len(messages) == 0 || len(a.State.Messages) == 0 {
		return
	}
	anchorIndex := activeCacheAnchorIndex(messages)
	if anchorIndex < 0 {
		return
	}
	anchorMetadata := copyMetadata(messages[anchorIndex].Metadata)
	if anchorMetadata == nil {
		return
	}
	storedIndex := activeCacheAnchorIndex(a.State.Messages)
	if storedIndex < 0 {
		return
	}
	a.State.Messages[storedIndex].Metadata = anchorMetadata
}

func cacheControlEnabled(model llm.ResolvedModel) bool {
	return strings.TrimSpace(model.CacheKeyName) != "" && model.CacheTriggerThreshold > 0 && len(model.CacheControl) > 0
}

func activeCacheAnchorIndex(messages []minisweagent.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if !isCacheAnchorMessage(messages[i].Metadata) {
			continue
		}
		if cacheAnchorRetired(messages[i].Metadata) {
			continue
		}
		return i
	}
	return -1
}

func isCacheAnchorMessage(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	val, ok := metadata["type"].(string)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(val), "cache_anchor")
}

func cacheAnchorRetired(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	if raw, ok := metadata[llm.CacheAnchorRetiredMetadataKey]; ok {
		switch v := raw.(type) {
		case bool:
			return v
		case string:
			return strings.EqualFold(strings.TrimSpace(v), "true")
		case int:
			return v != 0
		case int64:
			return v != 0
		case float64:
			return v != 0
		}
	}
	return false
}

func shouldRotateCacheAnchor(model llm.ResolvedModel, anchorMetadata map[string]any, messages []minisweagent.Message) bool {
	if model.CacheReanchorTokens <= 0 && model.CacheReanchorMessages <= 0 {
		return false
	}
	anchorIndex := activeCacheAnchorIndex(messages)
	if anchorIndex < 0 {
		return false
	}
	if model.CacheReanchorTokens > 0 {
		tokensSince := estimatePromptTokens(messages[anchorIndex+1:])
		if tokensSince >= model.CacheReanchorTokens {
			if cacheReanchorMinSatisfied(anchorMetadata, model.CacheReanchorMinCachedTokens) {
				return true
			}
		}
	}
	if model.CacheReanchorMessages > 0 {
		messagesSince := len(messages) - anchorIndex - 1
		if messagesSince >= model.CacheReanchorMessages {
			if cacheReanchorMinSatisfied(anchorMetadata, model.CacheReanchorMinCachedTokens) {
				return true
			}
		}
	}
	return false
}

func cacheReanchorMinSatisfied(anchorMetadata map[string]any, minCachedTokens int) bool {
	if minCachedTokens <= 0 {
		return true
	}
	return anchorCachedTokens(anchorMetadata) >= minCachedTokens
}

func anchorCachedTokens(metadata map[string]any) int {
	return metadataInt(metadata, llm.CacheAnchorCachedTokensMetadataKey)
}

func markCacheAnchorRetired(messages []minisweagent.Message, index int) {
	if index < 0 || index >= len(messages) {
		return
	}
	metadata := messages[index].Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata[llm.CacheAnchorRetiredMetadataKey] = true
	messages[index].Metadata = metadata
}

func newCacheAnchorMetadata(messages []minisweagent.Message, step, anchorTokens int) map[string]any {
	seq := nextCacheAnchorSeq(messages)
	metadata := map[string]any{
		"type":                             "cache_anchor",
		llm.CacheAnchorSequenceMetadataKey: seq,
		llm.CacheAnchorTurnMetadataKey:     step,
		llm.CacheAnchorTokensMetadataKey:   anchorTokens,
	}
	return metadata
}

func nextCacheAnchorSeq(messages []minisweagent.Message) int {
	maxSeq := 0
	count := 0
	for _, msg := range messages {
		if !isCacheAnchorMessage(msg.Metadata) {
			continue
		}
		count++
		if seq := metadataInt(msg.Metadata, llm.CacheAnchorSequenceMetadataKey); seq > maxSeq {
			maxSeq = seq
		}
	}
	if maxSeq > 0 {
		return maxSeq + 1
	}
	if count > 0 {
		return count + 1
	}
	return 1
}

func cacheAnchorIndexForPrompt(messageCount int, lookback int) int {
	if messageCount <= 0 {
		return 0
	}
	if lookback <= 0 {
		lookback = 1
	}
	anchorIndex := messageCount - lookback
	if anchorIndex < 0 {
		return 0
	}
	if anchorIndex >= messageCount {
		return messageCount - 1
	}
	return anchorIndex
}

func cacheAnchorInsertIndex(anchorIndex int, messages []minisweagent.Message) int {
	if len(messages) == 0 {
		return 0
	}
	if anchorIndex < 0 {
		anchorIndex = 0
	}
	if anchorIndex >= len(messages) {
		anchorIndex = len(messages) - 1
	}
	insertIndex := anchorIndex
	role := strings.ToLower(strings.TrimSpace(messages[anchorIndex].Role))
	if role == "user" {
		if anchorIndex == len(messages)-1 {
			insertIndex = anchorIndex
		} else {
			insertIndex = anchorIndex + 1
		}
	}
	if strings.EqualFold(strings.TrimSpace(messages[0].Role), "system") && insertIndex == 0 {
		insertIndex = 1
	}
	if insertIndex < 0 {
		insertIndex = 0
	}
	if insertIndex > len(messages) {
		insertIndex = len(messages)
	}
	return insertIndex
}

func estimatePromptTokens(messages []minisweagent.Message) int {
	total := 0
	for _, msg := range messages {
		if msg.Metadata != nil {
			if raw, ok := msg.Metadata["estimated_tokens"]; ok {
				switch val := raw.(type) {
				case int:
					total += val
					continue
				case int64:
					total += int(val)
					continue
				case float64:
					total += int(val)
					continue
				}
			}
		}
		total += llm.EstimateTokens(msg.Content)
	}
	return total
}

func metadataInt(metadata map[string]any, key string) int {
	if metadata == nil {
		return 0
	}
	if raw, ok := metadata[key]; ok {
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
	}
	return 0
}

func copyMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// stampCachePrefixHash computes the insertion-time cache prefix hash for the
// active anchor in messages and stores it in the anchor's metadata.
func stampCachePrefixHash(messages []minisweagent.Message) {
	anchorIndex := activeCacheAnchorIndex(messages)
	if anchorIndex < 0 {
		return
	}
	prefixMessages := messages[:anchorIndex]
	formatted := llm.FormatMessagesForHashing(toLLMMessages(prefixMessages))
	hash := llm.CachePrefixHash(formatted, anchorIndex)
	if hash == "" {
		return
	}
	if messages[anchorIndex].Metadata == nil {
		messages[anchorIndex].Metadata = map[string]any{}
	}
	messages[anchorIndex].Metadata[llm.CacheAnchorInsertionPrefixHashMetadataKey] = hash
}

// toLLMMessages converts shell-agent messages to llm.Message values for use
// with the shared cache-prefix hashing functions.
func toLLMMessages(msgs []minisweagent.Message) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = llm.Message{Role: m.Role, Content: m.Content}
	}
	return out
}
