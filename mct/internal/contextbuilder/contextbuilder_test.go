package contextbuilder

import (
	"strings"
	"testing"
)

func TestBuildIncludesConversationHistory(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "Initial goal"},
		{Role: "assistant", Content: "First answer", Files: []string{"file.go"}},
	}

	combined, included := Build("Follow-up question?", nil, history, Options{IncludeHistory: true})

	if len(included) != 0 {
		t.Fatalf("expected no included files, got %v", included)
	}

	for _, snippet := range []string{
		"Conversation History:",
		"1. User:\nInitial goal",
		"2. Assistant (Files: file.go):\nFirst answer",
		"Current Request:\nFollow-up question?",
	} {
		if !strings.Contains(combined, snippet) {
			t.Fatalf("expected combined prompt to contain %q, got %q", snippet, combined)
		}
	}
}

func TestBuildSkipsHistoryWhenNotRequested(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "Initial goal"},
		{Role: "assistant", Content: "First answer", Files: []string{"file.go"}},
	}

	combined, _ := Build("Follow-up question?", nil, history, Options{})

	if strings.Contains(combined, "Conversation History:") {
		t.Fatalf("did not expect conversation history, got %q", combined)
	}
	if !strings.Contains(combined, "Follow-up question?") {
		t.Fatalf("expected combined prompt to contain the current request, got %q", combined)
	}
}
