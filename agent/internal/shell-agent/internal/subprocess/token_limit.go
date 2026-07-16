package subprocess

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

const (
	messageOverheadTokens = 4
	truncatedMarker       = "[TRUNCATED"
)

// applyMessageTokenLimit enforces maxInputTokens on the provided messages by
// gradually trimming lower-priority content. It returns the possibly modified
// slice along with human-readable notes describing the operations applied.
func applyMessageTokenLimit(messages []minisweagent.Message, maxInputTokens int) ([]minisweagent.Message, []string) {
	if maxInputTokens <= 0 {
		return messages, nil
	}

	trimmed := make([]minisweagent.Message, len(messages))
	copy(trimmed, messages)

	notes := make([]string, 0, 3)
	tracker := newTruncationTracker(trimmed)

	for {
		if len(trimmed) == 0 {
			break
		}
		total := estimateMessageTokens(trimmed)
		if total <= maxInputTokens {
			break
		}

		if dropOldestAssistant(&trimmed, &notes, tracker) {
			continue
		}
		if shortenRetryOutput(&trimmed, &notes, tracker) {
			continue
		}
		if dropOldestRetryUser(&trimmed, &notes, tracker) {
			continue
		}
		break
	}

	if len(notes) > 0 {
		trimmed = appendTruncationNotes(trimmed, notes, tracker)
	}
	reanchorCacheAnchor(trimmed, tracker)
	return trimmed, notes
}

func estimateMessageTokens(messages []minisweagent.Message) int {
	total := 0
	for _, msg := range messages {
		total += llm.EstimateTokens(msg.Content)
		total += messageOverheadTokens
	}
	return total
}

func dropOldestAssistant(messages *[]minisweagent.Message, notes *[]string, tracker *truncationTracker) bool {
	if messages == nil || len(*messages) == 0 {
		return false
	}

	indices := make([]int, 0, len(*messages))
	for idx, msg := range *messages {
		if msg.Role == "assistant" {
			indices = append(indices, idx)
		}
	}
	if len(indices) <= 1 {
		return false
	}

	dropIdx := indices[0]
	*messages = append((*messages)[:dropIdx], (*messages)[dropIdx+1:]...)
	if tracker != nil {
		tracker.markDrop(dropIdx)
	}
	*notes = append(*notes, "removed older assistant response")
	return true
}

func shortenRetryOutput(messages *[]minisweagent.Message, notes *[]string, tracker *truncationTracker) bool {
	if messages == nil {
		return false
	}
	const outputMarker = "\nOutput snippet:\n"
	changed := false

	for idx := range *messages {
		if idx <= 1 {
			continue
		}
		msg := &(*messages)[idx]
		if msg.Role != "user" {
			continue
		}
		markerPos := strings.Index(msg.Content, outputMarker)
		if markerPos == -1 {
			continue
		}
		if strings.Contains(msg.Content[markerPos:], truncatedMarker) {
			continue
		}
		snippet := msg.Content[markerPos+len(outputMarker):]
		lines := strings.Split(snippet, "\n")
		if len(lines) <= 5 {
			continue
		}
		trimmedLines := lines[:5]
		trimmedSnippet := strings.Join(trimmedLines, "\n")
		rebuilt := msg.Content[:markerPos+len(outputMarker)] + trimmedSnippet
		rebuilt = strings.TrimRight(rebuilt, "\n") + "\n" + "[TRUNCATED: output snippet shortened to 5 lines]"
		msg.Content = rebuilt
		if tracker != nil {
			tracker.markMutate(idx)
		}
		*notes = append(*notes, fmt.Sprintf("shortened retry output snippet in message %d", idx))
		changed = true
	}
	return changed
}

func dropOldestRetryUser(messages *[]minisweagent.Message, notes *[]string, tracker *truncationTracker) bool {
	if messages == nil || len(*messages) == 0 {
		return false
	}
	lastRetryIdx := -1
	for i := len(*messages) - 1; i >= 0; i-- {
		if (*messages)[i].Role == "user" && i > 1 {
			lastRetryIdx = i
			break
		}
	}
	if lastRetryIdx == -1 {
		return false
	}
	for i := 2; i < lastRetryIdx; i++ {
		if (*messages)[i].Role != "user" {
			continue
		}
		if tracker != nil && tracker.isActiveAnchor(i) {
			continue
		}
		if isCacheAnchorMessage((*messages)[i].Metadata) && !cacheAnchorRetired((*messages)[i].Metadata) {
			continue
		}
		*notes = append(*notes, fmt.Sprintf("removed older retry user message at index %d", i))
		*messages = append((*messages)[:i], (*messages)[i+1:]...)
		if tracker != nil {
			tracker.markDrop(i)
		}
		return true
	}
	return false
}

func appendTruncationNotes(messages []minisweagent.Message, notes []string, tracker *truncationTracker) []minisweagent.Message {
	if len(notes) == 0 {
		return messages
	}
	idx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.TrimSpace(messages[i].Content) != "" {
			if tracker != nil && tracker.isActiveAnchor(i) {
				continue
			}
			idx = i
			break
		}
	}
	if idx == -1 {
		for i := len(messages) - 1; i >= 0; i-- {
			if tracker != nil && tracker.isActiveAnchor(i) {
				continue
			}
			idx = i
			break
		}
	}
	summary := fmt.Sprintf("[TRUNCATED: %s]", strings.Join(notes, "; "))
	if idx < 0 {
		return append(messages, minisweagent.Message{Role: "system", Content: summary})
	}
	existing := strings.TrimSpace(messages[idx].Content)
	if existing == "" {
		messages[idx].Content = summary
		if tracker != nil {
			tracker.markMutate(idx)
		}
		return messages
	}
	if strings.Contains(existing, "[TRUNCATED") {
		messages[idx].Content = existing + "\n" + summary
		if tracker != nil {
			tracker.markMutate(idx)
		}
		return messages
	}
	messages[idx].Content = existing + "\n" + summary
	if tracker != nil {
		tracker.markMutate(idx)
	}
	return messages
}

// ApplyMessageTokenLimit exposes the internal token limiting helper for reuse by the main agent loop.
func ApplyMessageTokenLimit(messages []minisweagent.Message, maxInputTokens int) ([]minisweagent.Message, []string) {
	return applyMessageTokenLimit(messages, maxInputTokens)
}

type truncationTracker struct {
	anchorIndex         int
	trimmedBeforeAnchor bool
}

func newTruncationTracker(messages []minisweagent.Message) *truncationTracker {
	return &truncationTracker{anchorIndex: activeCacheAnchorIndex(messages)}
}

func (t *truncationTracker) isActiveAnchor(idx int) bool {
	if t == nil {
		return false
	}
	return t.anchorIndex >= 0 && idx == t.anchorIndex
}

func (t *truncationTracker) markDrop(idx int) {
	if t == nil || t.anchorIndex < 0 {
		return
	}
	if idx < t.anchorIndex {
		t.trimmedBeforeAnchor = true
		t.anchorIndex--
	}
}

func (t *truncationTracker) markMutate(idx int) {
	if t == nil || t.anchorIndex < 0 {
		return
	}
	if idx < t.anchorIndex {
		t.trimmedBeforeAnchor = true
	}
}

func reanchorCacheAnchor(messages []minisweagent.Message, tracker *truncationTracker) {
	if tracker == nil || !tracker.trimmedBeforeAnchor {
		return
	}
	anchorIndex := activeCacheAnchorIndex(messages)
	if anchorIndex < 0 {
		return
	}
	step := findStepFromMessages(messages)
	anchorTokens := estimateCachePrefixTokens(messages, anchorIndex)
	metadata := copyMetadata(messages[anchorIndex].Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if cacheAnchorTokens(metadata) == anchorTokens {
		return
	}
	metadata["type"] = "cache_anchor"
	metadata[llm.CacheAnchorSequenceMetadataKey] = nextCacheAnchorSeq(messages)
	if step > 0 {
		metadata[llm.CacheAnchorTurnMetadataKey] = step
	}
	metadata[llm.CacheAnchorTokensMetadataKey] = anchorTokens
	messages[anchorIndex].Metadata = metadata
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

func findStepFromMessages(messages []minisweagent.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		metadata := messages[i].Metadata
		if metadata == nil {
			continue
		}
		if step := metadataInt(metadata, "step"); step > 0 {
			return step
		}
		if step := metadataInt(metadata, "Step"); step > 0 {
			return step
		}
	}
	return 0
}

func estimateCachePrefixTokens(messages []minisweagent.Message, anchorIndex int) int {
	if anchorIndex >= len(messages) {
		anchorIndex = len(messages) - 1
	}
	if anchorIndex < 0 {
		return 0
	}
	total := 0
	for i := 0; i <= anchorIndex; i++ {
		msg := messages[i]
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

func cacheAnchorTokens(metadata map[string]any) int {
	return metadataInt(metadata, llm.CacheAnchorTokensMetadataKey)
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
