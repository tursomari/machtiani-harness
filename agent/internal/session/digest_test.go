package session

import (
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
)

func TestCanonicalHashIgnoresSessionID(t *testing.T) {
	conv := conversation.New("agent-original", "Keep the semantic content stable")
	conv.Goal = "Keep the semantic content stable"
	conv.Status = "completed"
	conv.TurnsCompleted = 2

	other := *conv
	other.SessionID = "agent-fork"

	want, err := CanonicalConversationDigest(conv)
	if err != nil {
		t.Fatalf("CanonicalConversationDigest(original): %v", err)
	}
	got, err := CanonicalConversationDigest(&other)
	if err != nil {
		t.Fatalf("CanonicalConversationDigest(fork): %v", err)
	}
	if got != want {
		t.Fatalf("digest changed with session ID: got %s want %s", got, want)
	}
}

func TestCanonicalHashDetectsDivergence(t *testing.T) {
	newConversation := func() *conversation.Conversation {
		conv := conversation.New("agent-digest", "Original goal")
		conv.Goal = "Current goal"
		conv.Status = "active"
		conv.TurnsCompleted = 1
		return conv
	}

	t.Run("message", func(t *testing.T) {
		conv := newConversation()
		before, err := CanonicalConversationDigest(conv)
		if err != nil {
			t.Fatalf("CanonicalConversationDigest(before): %v", err)
		}
		conv.AddMessage("assistant", "New semantic content", nil)
		after, err := CanonicalConversationDigest(conv)
		if err != nil {
			t.Fatalf("CanonicalConversationDigest(after): %v", err)
		}
		if after == before {
			t.Fatalf("digest did not change after appending a message: %s", after)
		}
	})

	t.Run("goal", func(t *testing.T) {
		conv := newConversation()
		before, err := CanonicalConversationDigest(conv)
		if err != nil {
			t.Fatalf("CanonicalConversationDigest(before): %v", err)
		}
		conv.Goal = "A diverged goal"
		after, err := CanonicalConversationDigest(conv)
		if err != nil {
			t.Fatalf("CanonicalConversationDigest(after): %v", err)
		}
		if after == before {
			t.Fatalf("digest did not change after changing the goal: %s", after)
		}
	})
}
