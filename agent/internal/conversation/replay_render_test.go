package conversation

import (
	"strings"
	"testing"
)

func TestRenderReplaySingleTurnFinishedConversation(t *testing.T) {
	conv := New("sess-replay-1", "Investigate the replay renderer")
	conv.AddMessage("assistant", "Which files implement conversation rendering?", map[string]any{
		"type":     messageTypeWorkRequest,
		"turn":     1,
		"decision": "Inspect the conversation package first.",
	})
	conv.AddMessage("assistant", "The renderer lives in conversation.go.", map[string]any{
		"type":            messageTypeWorkResult,
		"turn":            1,
		"retrieved_files": []string{"internal/conversation/conversation.go", "internal/conversation/conversation_test.go"},
		"chat_path":       "sessions/sess-replay-1/chat/machtiani-response.md",
	})
	conv.AddMessage("assistant", "The replay renderer is ready.", map[string]any{
		"type":   messageTypeFinal,
		"turns":  1,
		"capped": false,
	})

	got, err := RenderReplay(conv)
	if err != nil {
		t.Fatalf("RenderReplay() error = %v", err)
	}
	wantContents := []string{
		"Investigate the replay renderer",
		"Which files implement conversation rendering?",
		"The renderer lives in conversation.go.",
		"Inspect the conversation package first.",
		"internal/conversation/conversation.go",
		"internal/conversation/conversation_test.go",
		"sessions/sess-replay-1/chat/machtiani-response.md",
		"The replay renderer is ready.",
		"── ARTIFACTS ──",
		"── DECISION ──",
		"──── CONCLUSION (after 1 turn(s)) ────",
	}
	for _, want := range wantContents {
		if !strings.Contains(got, want) {
			t.Errorf("RenderReplay() missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "= MCT-AGENT TRANSCRIPT") {
		t.Fatalf("RenderReplay() returned the AsciiDoc transcript header:\n%s", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("RenderReplay() contains ANSI escapes: %q", got)
	}

	again, err := RenderReplay(conv)
	if err != nil {
		t.Fatalf("second RenderReplay() error = %v", err)
	}
	if got != again {
		t.Fatalf("RenderReplay() is not deterministic:\nfirst:  %q\nsecond: %q", got, again)
	}
}

func TestRenderReplayMultiTurnAndMessageSections(t *testing.T) {
	conv := New("sess-replay-multi", "Exercise every replay section")
	conv.AddMessage("assistant", "First question", map[string]any{"type": messageTypeAsk, "turn": 1})
	conv.AddMessage("assistant", "First answer", map[string]any{"type": messageTypeAnswer, "turn": 1})
	conv.AddMessage("assistant", "Which scope should I use?", map[string]any{"type": messageTypeUserInputRequest})
	conv.AddMessage("user", "Use all packages.", map[string]any{"type": messageTypeUserInputResponse})
	conv.AddMessage("assistant", "Second question", map[string]any{"type": messageTypeWorkRequest, "turn": 2})
	conv.AddMessage("assistant", "Second answer", map[string]any{"type": messageTypeWorkResult, "turn": 2})
	conv.AddMessage("assistant", "Raw diagnostic content", map[string]any{"type": messageTypeRaw})
	conv.AddMessage("system", "Recovered after restart", map[string]any{"type": messageTypeRecovery})
	conv.AddMessage("user", "Please continue", nil)

	got, err := RenderReplay(conv)
	if err != nil {
		t.Fatalf("RenderReplay() error = %v", err)
	}
	for _, want := range []string{
		"First question",
		"First answer",
		"Which scope should I use?",
		"Use all packages.",
		"Second question",
		"Second answer",
		"Raw diagnostic content",
		"Recovered after restart",
		"Please continue",
		"── GOAL ──",
		"──── TURN 1 ────",
		"──── TURN 2 ────",
		"── QUESTION ──",
		"── ANSWER ──",
		"── USER INPUT REQUEST ──",
		"── USER INPUT RESPONSE ──",
		"── RAW ──",
		"── SYSTEM MESSAGE ──",
		"── USER MESSAGE ──",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderReplay() missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderReplayMissingOptionalFields(t *testing.T) {
	conv := New("sess-replay-minimal", "Render a minimal persisted conversation")
	conv.Messages = append(conv.Messages,
		Message{Role: "assistant", Content: "Minimal question", Metadata: map[string]any{"type": messageTypeWorkRequest, "turn": 3}},
		Message{Role: "assistant", Content: "Minimal answer", Metadata: map[string]any{"type": messageTypeWorkResult, "turn": 3}},
		Message{Role: "assistant", Content: "Minimal conclusion", Metadata: map[string]any{"type": messageTypeFinalAnswer, "turns": 3}},
	)

	got, err := RenderReplay(conv)
	if err != nil {
		t.Fatalf("RenderReplay() error = %v", err)
	}
	for _, want := range []string{"──── TURN 3 ────", "Minimal question", "Minimal answer", "Minimal conclusion"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderReplay() missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Planner decision:", "Retrieved File Paths:", "mct chat:", "reached max-steps cap"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("RenderReplay() unexpectedly contains %q in:\n%s", unwanted, got)
		}
	}
	again, err := RenderReplay(conv)
	if err != nil {
		t.Fatalf("second RenderReplay() error = %v", err)
	}
	if got != again {
		t.Fatalf("RenderReplay() output changed between calls")
	}
}

func TestRenderReplayEmptyConversation(t *testing.T) {
	conv := New("sess-replay-empty", "Only the goal remains")

	got, err := RenderReplay(conv)
	if err != nil {
		t.Fatalf("RenderReplay() error = %v", err)
	}
	if !strings.Contains(got, "── GOAL ──") || !strings.Contains(got, "Only the goal remains") {
		t.Fatalf("RenderReplay() = %q, want framed goal", got)
	}
}

func TestRenderReplayUnknownMessageType(t *testing.T) {
	conv := New("sess-replay-unknown", "Reject unknown message types")
	conv.AddMessage("assistant", "Unknown", map[string]any{"type": "bogus"})

	if _, err := RenderReplay(conv); err == nil || !strings.Contains(err.Error(), `unhandled message type "bogus" in RenderReplay`) {
		t.Fatalf("RenderReplay() error = %v, want unhandled message type error", err)
	}
}

func TestRenderReplayNilConversation(t *testing.T) {
	if _, err := RenderReplay(nil); err == nil {
		t.Fatal("RenderReplay(nil) error = nil, want error")
	}
}
