package subprocess

import (
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func TestApplyMessageTokenLimitDropsOldestAssistant(t *testing.T) {
	msgs := []minisweagent.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "intent"},
		{Role: "assistant", Content: strings.Repeat("a", 50)},
		{Role: "assistant", Content: strings.Repeat("b", 10)},
	}
	limit := estimateMessageTokens(msgs) - 10

	trimmed, notes := applyMessageTokenLimit(msgs, limit)
	if len(trimmed) != len(msgs)-1 {
		t.Fatalf("expected one message dropped, got %d", len(trimmed))
	}
	if trimmed[2].Role != "assistant" || !strings.HasPrefix(trimmed[2].Content, strings.Repeat("b", 10)) {
		t.Fatalf("expected oldest assistant removed; got %#v", trimmed)
	}
	assistants := 0
	for _, msg := range trimmed {
		if msg.Role == "assistant" {
			assistants++
		}
	}
	if assistants != 1 {
		t.Fatalf("expected exactly one assistant message remaining, got %d", assistants)
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "assistant") {
		t.Fatalf("expected assistant drop noted, got %v", notes)
	}
	if _, ok := findTruncationMarker(trimmed); !ok {
		t.Fatalf("expected truncation marker appended")
	}
}

func TestApplyMessageTokenLimitShortensOutputSnippet(t *testing.T) {
	snippetLines := []string{"l1", "l2", "l3", "l4", "l5", "l6"}
	retry := "The previous bash command failed.\nOutput snippet:\n" + strings.Join(snippetLines, "\n")
	msgs := []minisweagent.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "intent"},
		{Role: "user", Content: retry},
	}
	limit := estimateMessageTokens(msgs) - 5

	trimmed, notes := applyMessageTokenLimit(msgs, limit)
	if len(trimmed) != len(msgs) {
		t.Fatalf("expected no message drop, got %d", len(trimmed))
	}
	content := trimmed[2].Content
	marker := "\nOutput snippet:\n"
	pos := strings.Index(content, marker)
	if pos == -1 {
		t.Fatalf("expected output snippet marker, got %q", content)
	}
	after := content[pos+len(marker):]
	parts := strings.Split(after, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.HasPrefix(part, "[TRUNCATED:") {
			break
		}
		if strings.TrimSpace(part) == "" {
			continue
		}
		lines = append(lines, part)
	}
	if len(lines) != 5 {
		t.Fatalf("expected output to be truncated to 5 lines, got %d lines (%v)", len(lines), lines)
	}
	if !strings.Contains(content, "[TRUNCATED: output snippet shortened to 5 lines]") {
		t.Fatalf("expected truncation marker in retry message, got %q", content)
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "output") {
		t.Fatalf("expected output truncation noted, got %v", notes)
	}
}

func TestApplyMessageTokenLimitDropsOldestRetryUser(t *testing.T) {
	msgs := []minisweagent.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "intent"},
		{Role: "user", Content: "retry 1"},
		{Role: "user", Content: "retry 2"},
	}
	limit := estimateMessageTokens(msgs) - 4

	trimmed, notes := applyMessageTokenLimit(msgs, limit)
	if len(trimmed) != len(msgs)-1 {
		t.Fatalf("expected one retry message dropped, got %d", len(trimmed))
	}
	if !strings.HasPrefix(trimmed[2].Content, "retry 2") {
		t.Fatalf("expected older retry removed, got %q", trimmed[2].Content)
	}
	if len(notes) == 0 || !strings.Contains(notes[len(notes)-1], "retry") {
		t.Fatalf("expected retry drop noted, got %v", notes)
	}
}

func findTruncationMarker(msgs []minisweagent.Message) (int, bool) {
	for i, msg := range msgs {
		if strings.Contains(msg.Content, "[TRUNCATED:") {
			return i, true
		}
	}
	return -1, false
}
