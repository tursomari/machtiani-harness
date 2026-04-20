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
	if first.Metadata != nil {
		t.Fatalf("expected plain user goal message metadata, got %v", first.Metadata)
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

func TestToTranscriptRendersUserMessageBlock(t *testing.T) {
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
	if err := tr.AppendRaw("\n=== USER MESSAGE\n\n" + feedback + "\n"); err != nil {
		t.Fatalf("AppendRaw error: %v", err)
	}
	expected := tr.Content()

	conv := New("sess-goal-update", goal)
	conv.AddMessage("user", feedback, nil)

	got, err := conv.ToTranscript()
	if err != nil {
		t.Fatalf("ToTranscript returned error: %v", err)
	}
	if got != expected {
		t.Fatalf("user message transcript mismatch:\nwant:\n%s\n----\n got:\n%s", expected, got)
	}
}

func TestToTranscriptRendersFinalConclusionBlock(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := transcript.New("sess-final")
	if err != nil {
		t.Fatalf("transcript.New error: %v", err)
	}
	defer tr.Close()

	goal := "Finish docs"
	if err := tr.WriteHeader(goal, "", "sess-final", nil); err != nil {
		t.Fatalf("WriteHeader error: %v", err)
	}
	if err := tr.WriteFinal("Final answer", 2, true); err != nil {
		t.Fatalf("WriteFinal error: %v", err)
	}
	expected := tr.Content()

	conv := New("sess-final", goal)
	conv.AddMessage("assistant", "Final answer", map[string]any{"type": "final", "turns": 2, "capped": true})

	got, err := conv.ToTranscript()
	if err != nil {
		t.Fatalf("ToTranscript returned error: %v", err)
	}
	if got != expected {
		t.Fatalf("final transcript mismatch:\nwant:\n%s\n----\n got:\n%s", expected, got)
	}
}

func TestToChatMessagesEmptyConversation(t *testing.T) {
	conv := &Conversation{}
	messages := conv.ToChatMessages("System prompt")
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Role != "system" {
		t.Fatalf("expected system role, got %q", messages[0].Role)
	}
	if messages[0].Content != "System prompt" {
		t.Fatalf("unexpected system prompt: %q", messages[0].Content)
	}
	if messages[0].Metadata == nil {
		t.Fatalf("expected metadata on system message")
	}
	if _, ok := messages[0].Metadata["estimated_tokens"].(int); !ok {
		t.Fatalf("expected estimated_tokens in metadata, got %v", messages[0].Metadata)
	}
}

func TestToChatMessagesSingleTurn(t *testing.T) {
	conv := New("sess-chat", "Investigate issue")
	conv.AddMessage("assistant", "What happened?", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", "All good", map[string]any{"type": "answer", "turn": 1, "retrieved_files": []string{"README.md"}})

	messages := conv.ToChatMessages("System prompt")
	wantRoles := []string{"system", "user", "assistant", "assistant"}
	if len(messages) != len(wantRoles) {
		t.Fatalf("expected %d messages, got %d", len(wantRoles), len(messages))
	}
	for i, role := range wantRoles {
		if messages[i].Role != role {
			t.Fatalf("message %d role = %q, want %q", i, messages[i].Role, role)
		}
		if messages[i].Metadata == nil {
			t.Fatalf("message %d missing metadata", i)
		}
		if _, ok := messages[i].Metadata["estimated_tokens"].(int); !ok {
			t.Fatalf("message %d missing token estimate", i)
		}
	}
	if messages[1].Content != "Investigate issue" {
		t.Fatalf("unexpected goal content: %q", messages[1].Content)
	}
	if messages[2].Content != "[work_request] What happened?" {
		t.Fatalf("unexpected ask content: %q", messages[2].Content)
	}
	if messages[2].Metadata["decision"] != "ask" {
		t.Fatalf("unexpected ask metadata: %v", messages[2].Metadata)
	}
	if messages[2].Metadata["turn"] != 1 {
		t.Fatalf("unexpected ask turn metadata: %v", messages[2].Metadata)
	}
}

func TestToChatMessagesKeepsPlainUserFollowUp(t *testing.T) {
	conv := New("sess-goal", "Initial goal")
	conv.AddMessage("assistant", "Question", map[string]any{"type": "ask", "turn": 1, "decision": "ask"})
	conv.AddMessage("assistant", "Answer", map[string]any{"type": "answer", "turn": 1})
	conv.AddMessage("user", "Updated goal", nil)
	conv.AddMessage("assistant", "Next question", map[string]any{"type": "ask", "turn": 2, "decision": "ask"})

	messages := conv.ToChatMessages("")
	wantRoles := []string{"user", "assistant", "assistant", "user", "assistant"}
	if len(messages) != len(wantRoles) {
		t.Fatalf("expected %d messages, got %d", len(wantRoles), len(messages))
	}
	for i, role := range wantRoles {
		if messages[i].Role != role {
			t.Fatalf("message %d role = %q, want %q", i, messages[i].Role, role)
		}
	}
	if messages[3].Content != "Updated goal" {
		t.Fatalf("unexpected plain user content: %q", messages[3].Content)
	}
}

func TestToChatMessagesPrefixesProtocolTags(t *testing.T) {
	conv := New("sess-protocol", "Initial goal")
	conv.AddMessage("assistant", "Do you want the safer fix?", map[string]any{"type": "user_input_request"})
	conv.AddMessage("user", "Use the safer fix.", map[string]any{"type": "user_input_response"})
	conv.AddMessage("assistant", "Inspect the failing test and summarize the root cause.", map[string]any{"type": "work_request", "turn": 1})
	conv.AddMessage("assistant", "The root cause is a stale cache invalidation path.", map[string]any{"type": "work_result", "turn": 1})

	messages := conv.ToChatMessages("")
	got := []string{messages[1].Content, messages[2].Content, messages[3].Content, messages[4].Content}
	want := []string{
		"[user_input_request] Do you want the safer fix?",
		"[user_input_response] Use the safer fix.",
		"[work_request] Inspect the failing test and summarize the root cause.",
		"[work_result] The root cause is a stale cache invalidation path.",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message %d content = %q, want %q", i+1, got[i], want[i])
		}
	}
}

func TestToChatMessagesIncludesCacheAnchor(t *testing.T) {
	conv := New("sess-anchor", "Goal")
	conv.AddMessage("user", "[cache anchor]", map[string]any{"type": "cache_anchor"})
	conv.AddMessage("assistant", "Answer", map[string]any{"type": "answer", "turn": 1})

	messages := conv.ToChatMessages("")
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	if messages[1].Role != "user" {
		t.Fatalf("expected cache anchor role user, got %q", messages[1].Role)
	}
	if messages[1].Content != "[cache anchor]" {
		t.Fatalf("unexpected cache anchor content: %q", messages[1].Content)
	}
}

func TestToChatMessagesIncludesFinalAnswer(t *testing.T) {
	conv := New("sess-final-chat", "Goal")
	conv.AddMessage("assistant", "Final answer", map[string]any{"type": "final", "turns": 3, "capped": false})

	messages := conv.ToChatMessages("")
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[1].Role != "assistant" {
		t.Fatalf("expected final answer role assistant, got %q", messages[1].Role)
	}
	if messages[1].Content != "Final answer" {
		t.Fatalf("unexpected final answer content: %q", messages[1].Content)
	}
	if messages[1].Metadata["type"] != "final" {
		t.Fatalf("unexpected final answer metadata: %v", messages[1].Metadata)
	}
	if messages[1].Metadata["turns"] != 3 {
		t.Fatalf("unexpected final answer turn count: %v", messages[1].Metadata)
	}
}

func TestUnmarshalRejectsLegacyVisibleTagTypes(t *testing.T) {
	data := []byte(`{"session_id":"sess-legacy","original_goal":"Goal","messages":[{"role":"user","content":"Goal","metadata":{"type":"original_goal"}}],"created_at":"2026-04-18T00:00:00Z","updated_at":"2026-04-18T00:00:00Z"}`)
	if _, err := Unmarshal(data); err == nil {
		t.Fatal("expected legacy visible tag unmarshal failure")
	} else if !strings.Contains(err.Error(), "unsupported legacy conversation message type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractFullDiffsDedupesByFile(t *testing.T) {
	conv := New("sess-full-diff", "Goal")
	diffA1 := "File: a.txt\n\nFull diff:\n```diff\n+ a1\n```"
	diffB1 := "File: b.txt\n\nFull diff:\n```diff\n+ b1\n```"
	diffA2 := "File: a.txt\n\nFull diff:\n```diff\n+ a2\n```"

	conv.AddMessage("assistant", "note", map[string]any{"type": "full_diff", "turn": 1, "file": "a.txt", "diff": diffA1})
	conv.AddMessage("assistant", "note", map[string]any{"type": "full_diff", "turn": 2, "file": "b.txt", "diff": diffB1})
	conv.AddMessage("assistant", "note", map[string]any{"type": "full_diff", "turn": 3, "file": "a.txt", "diff": diffA2})

	got := ExtractFullDiffs(conv)
	want := diffB1 + "\n\n" + diffA2
	if got != want {
		t.Fatalf("unexpected full diffs:\nwant:\n%s\n----\n got:\n%s", want, got)
	}
}

func TestExtractFullDiffsNormalizesFileSuffixes(t *testing.T) {
	conv := New("sess-full-diff-normalize", "Goal")
	diffOld := "File: README.md (deleted in workspace)\n\nFull diff:\n```diff\n- old\n```"
	diffNew := "File: README.md\n\nFull diff:\n```diff\n+ new\n```"

	conv.AddMessage("assistant", "note", map[string]any{"type": "full_diff", "turn": 1, "file": "README.md (deleted in workspace)", "diff": diffOld})
	conv.AddMessage("assistant", "note", map[string]any{"type": "full_diff", "turn": 2, "file": "README.md", "diff": diffNew})

	got := ExtractFullDiffs(conv)
	if got != diffNew {
		t.Fatalf("unexpected full diffs:\nwant:\n%s\n----\n got:\n%s", diffNew, got)
	}
}
