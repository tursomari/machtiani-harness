package conversation

import (
	"os"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/transcript"
)

func TestConversationMarshalRoundTrip(t *testing.T) {
	conv := New("sess-1", "Investigate issue")
	conv.AddMessage("assistant", "What happened?", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", "All good", map[string]any{"type": "answer", "turn": 1, "retrieved_files": []string{"README.md"}, "chat_path": "/tmp/chat.md"})

	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	loaded, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	if loaded.SessionID != conv.SessionID {
		t.Fatalf("unexpected session id: got %s want %s", loaded.SessionID, conv.SessionID)
	}
	if loaded.OriginalGoal != conv.OriginalGoal {
		t.Fatalf("unexpected goal: got %q want %q", loaded.OriginalGoal, conv.OriginalGoal)
	}
	if loaded.CreatedAt.IsZero() || loaded.UpdatedAt.IsZero() {
		t.Fatalf("expected timestamps to be set")
	}
	if len(loaded.Messages) != len(conv.Messages) {
		t.Fatalf("unexpected message count: got %d want %d", len(loaded.Messages), len(conv.Messages))
	}
	foundAsk := false
	for _, msg := range loaded.Messages {
		if msg.Metadata != nil && msg.Metadata["type"] == "ask" {
			if msg.Turn == nil || *msg.Turn != 1 {
				t.Fatalf("expected ask turn to round-trip")
			}
			foundAsk = true
		}
	}
	if !foundAsk {
		t.Fatalf("expected to find ask message")
	}
}

func TestNewSeedsOriginalGoalMessage(t *testing.T) {
	goal := "Track down regression"
	conv := New("seed-goal", goal)
	if len(conv.Messages) == 0 {
		t.Fatalf("expected initial goal message")
	}
	first := conv.Messages[0]
	if first.Role != "user" {
		t.Fatalf("expected goal message role user, got %s", first.Role)
	}
	if first.Metadata == nil || first.Metadata["type"] != "original_goal" {
		t.Fatalf("expected original_goal metadata, got %v", first.Metadata)
	}
	if strings.TrimSpace(first.Content) != goal {
		t.Fatalf("expected goal content preserved, got %q", first.Content)
	}
}

func TestToTranscriptMatchesLegacyTurn(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := transcript.New("sess-to-transcript")
	if err != nil {
		t.Fatalf("transcript.New error: %v", err)
	}
	defer tr.Close()

	goal := "Finish docs"
	if err := tr.WriteHeader(goal, "", "sess-to-transcript", nil); err != nil {
		t.Fatalf("WriteHeader error: %v", err)
	}
	question := "What should we update?"
	retrieved := []string{"README.md", "main.go"}
	answer := "Update README with usage."
	savedPath := "/tmp/machtiani/chat.md"
	if err := tr.WriteTurn(1, question, savedPath, retrieved, answer, "ask"); err != nil {
		t.Fatalf("WriteTurn error: %v", err)
	}
	expected := tr.Content()

	conv := New("sess-to-transcript", goal)
	conv.AddMessage("assistant", question, map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", answer, map[string]any{"type": "answer", "turn": 1, "retrieved_files": retrieved, "chat_path": savedPath})

	got, err := conv.ToTranscript()
	if err != nil {
		t.Fatalf("ToTranscript returned error: %v", err)
	}
	if got != expected {
		t.Fatalf("transcript mismatch:\nwant:\n%s\n----\n got:\n%s", expected, got)
	}
}

func TestCurrentGoalUsesLatestUpdate(t *testing.T) {
	conv := New("sess-goal", "Initial goal")
	conv.AddMessage("user", "First update", map[string]any{"type": "goal_update"})
	conv.AddMessage("assistant", "Answer", map[string]any{"type": "answer", "turn": 1})
	conv.AddMessage("user", "Latest goal", map[string]any{"type": "goal_update"})

	if got := conv.CurrentGoal(); got != "Latest goal" {
		t.Fatalf("unexpected current goal: got %q want %q", got, "Latest goal")
	}
}

func TestToTranscriptRendersGoalUpdateBlock(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := transcript.New("sess-goal-update")
	if err != nil {
		t.Fatalf("transcript.New error: %v", err)
	}
	defer tr.Close()

	goal := "Stabilize build"
	if err := tr.WriteHeader(goal, "", "sess-goal-update", nil); err != nil {
		t.Fatalf("WriteHeader error: %v", err)
	}
	feedback := "Please focus on unit tests first."
	if err := tr.AppendRaw("\n=== GOAL UPDATE\n\n" + feedback + "\n"); err != nil {
		t.Fatalf("AppendRaw error: %v", err)
	}
	expected := tr.Content()

	conv := New("sess-goal-update", goal)
	conv.AddMessage("user", feedback, map[string]any{"type": "goal_update"})

	got, err := conv.ToTranscript()
	if err != nil {
		t.Fatalf("ToTranscript returned error: %v", err)
	}
	if got != expected {
		t.Fatalf("goal update transcript mismatch:\nwant:\n%s\n----\n got:\n%s", expected, got)
	}
}
