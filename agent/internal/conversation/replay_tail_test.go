package conversation

import (
	"reflect"
	"strings"
	"testing"
)

func TestIdentifyNewMessagesReturnsOnlyAppendedTail(t *testing.T) {
	previous := replayTailMessages(1)
	next := replayTailMessages(3)

	got := IdentifyNewMessages(previous, next)
	want := next[len(previous):]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IdentifyNewMessages() = %#v, want %#v", got, want)
	}
	for i, wantContent := range []string{
		"Question for turn 2",
		"Answer for turn 2",
		"Question for turn 3",
		"Answer for turn 3",
	} {
		if got[i].Content != wantContent {
			t.Errorf("new message %d content = %q, want %q", i, got[i].Content, wantContent)
		}
	}

	if duplicate := IdentifyNewMessages(next, next); len(duplicate) != 0 {
		t.Fatalf("unchanged snapshot returned %d duplicate messages: %#v", len(duplicate), duplicate)
	}
	if stale := IdentifyNewMessages(next, previous); len(stale) != 0 {
		t.Fatalf("older snapshot returned %d duplicate messages: %#v", len(stale), stale)
	}
}

func TestIdentifyNewMessagesAcrossRapidAtomicPublishes(t *testing.T) {
	snapshots := [][]Message{
		replayTailMessages(1),
		replayTailMessagesWithPartialTurn(1, 2),
		replayTailMessages(2),
		replayTailMessages(3),
		replayTailMessages(3),
	}

	previous := snapshots[0]
	var got []Message
	for _, next := range snapshots[1:] {
		got = append(got, IdentifyNewMessages(previous, next)...)
		previous = next
	}
	want := snapshots[3][len(snapshots[0]):]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("messages accumulated across publishes = %#v, want %#v", got, want)
	}
}

func TestRenderReplayDeltaOmitsHistoryAndRepeatedTurnHeading(t *testing.T) {
	conv := New("sess-tail-delta", "Tail a running conversation")
	conv.Messages = replayTailMessages(1)
	previous := append([]Message(nil), conv.Messages...)

	conv.Messages = replayTailMessagesWithPartialTurn(1, 2)
	questionDelta, err := RenderReplayDelta(conv, previous)
	if err != nil {
		t.Fatalf("RenderReplayDelta(question) error = %v", err)
	}
	previous = append([]Message(nil), conv.Messages...)
	conv.Messages = replayTailMessages(2)
	answerDelta, err := RenderReplayDelta(conv, previous)
	if err != nil {
		t.Fatalf("RenderReplayDelta(answer) error = %v", err)
	}

	combined := questionDelta + answerDelta
	for _, want := range []string{"Question for turn 2", "Answer for turn 2", "──── TURN 2 ────", "── QUESTION ──", "── ANSWER ──"} {
		if !strings.Contains(combined, want) {
			t.Errorf("combined delta missing %q:\n%s", want, combined)
		}
	}
	for _, unwanted := range []string{"Tail a running conversation", "Question for turn 1", "Answer for turn 1", "── GOAL ──"} {
		if strings.Contains(combined, unwanted) {
			t.Errorf("combined delta repeated history %q:\n%s", unwanted, combined)
		}
	}
	if got := strings.Count(combined, "──── TURN 2 ────"); got != 1 {
		t.Fatalf("TURN 2 heading count = %d, want 1:\n%s", got, combined)
	}
}

func replayTailMessages(turns int) []Message {
	messages := []Message{{Role: "user", Content: "Tail a running conversation"}}
	for turn := 1; turn <= turns; turn++ {
		messages = append(messages, replayTailTurn(turn)...)
	}
	return messages
}

func replayTailMessagesWithPartialTurn(completedTurns, partialTurn int) []Message {
	messages := replayTailMessages(completedTurns)
	return append(messages, replayTailTurn(partialTurn)[0])
}

func replayTailTurn(turn int) []Message {
	return []Message{
		{
			Role:    "assistant",
			Content: "Question for turn " + string(rune('0'+turn)),
			Metadata: map[string]any{
				"type":     messageTypeWorkRequest,
				"turn":     turn,
				"decision": "Decision for turn " + string(rune('0'+turn)),
			},
		},
		{
			Role:    "assistant",
			Content: "Answer for turn " + string(rune('0'+turn)),
			Metadata: map[string]any{
				"type": messageTypeWorkResult,
				"turn": turn,
			},
		},
	}
}
