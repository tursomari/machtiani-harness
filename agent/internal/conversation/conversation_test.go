package conversation

import (
	"encoding/json"
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

func TestToTranscriptErrorsOnUnknownMessageType(t *testing.T) {
	conv := New("sess-unknown-type", "Goal")
	conv.AddMessage("assistant", "mystery payload", map[string]any{"type": "unknown_future_type", "turn": 1})

	_, err := conv.ToTranscript()
	if err == nil {
		t.Fatalf("expected ToTranscript to error on unknown message type")
	}
	if !strings.Contains(err.Error(), "unknown_future_type") {
		t.Fatalf("expected error to mention the offending type, got: %v", err)
	}
}

func TestToChatMessagesSkipsBookkeepingTypes(t *testing.T) {
	conv := New("sess-bookkeeping", "Goal")
	conv.AddMessage("assistant", "validation block", map[string]any{"type": "patch_validation", "turn": 1})
	conv.AddMessage("assistant", "plan block", map[string]any{"type": "patch_plan_created", "turn": 1})
	conv.AddMessage("assistant", "updated plan", map[string]any{"type": "patch_plan_updated", "turn": 2})

	messages := conv.ToChatMessages("")
	// Only the seeded goal message ("Goal") should be present.
	if len(messages) != 1 {
		t.Fatalf("expected bookkeeping messages to be filtered from chat stream, got %d messages: %+v", len(messages), messages)
	}
	if messages[0].Content != "Goal" {
		t.Fatalf("unexpected message content: %q", messages[0].Content)
	}
}

func TestSuspendedUserInputStateRoundTrip(t *testing.T) {
	original := &SuspendedUserInputState{
		Kind:        "user-directed-ask",
		Question:    "Do you want the safer fix?",
		Context:     "The safer fix preserves behavior.",
		Reason:      "Testing round-trip",
		OriginalAsk: "What fix do you want?",
	}

	data, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent returned error: %v", err)
	}

	var restored SuspendedUserInputState
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	if original.Kind != restored.Kind {
		t.Fatalf("Kind mismatch: got %q want %q", restored.Kind, original.Kind)
	}
	if original.Question != restored.Question {
		t.Fatalf("Question mismatch: got %q want %q", restored.Question, original.Question)
	}
	if original.Context != restored.Context {
		t.Fatalf("Context mismatch: got %q want %q", restored.Context, original.Context)
	}
	if original.Reason != restored.Reason {
		t.Fatalf("Reason mismatch: got %q want %q", restored.Reason, original.Reason)
	}
	if original.OriginalAsk != restored.OriginalAsk {
		t.Fatalf("OriginalAsk mismatch: got %q want %q", restored.OriginalAsk, original.OriginalAsk)
	}
}

func TestPlannerProgressStateRoundTrip(t *testing.T) {
	original := &PlannerProgressState{
		SuccessFiles: []string{"README.md", "docs/index.md"},
	}

	data, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent returned error: %v", err)
	}

	var restored PlannerProgressState
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	if len(original.SuccessFiles) != len(restored.SuccessFiles) {
		t.Fatalf("SuccessFiles length mismatch: got %d want %d", len(restored.SuccessFiles), len(original.SuccessFiles))
	}
	for i := range original.SuccessFiles {
		if original.SuccessFiles[i] != restored.SuccessFiles[i] {
			t.Fatalf("SuccessFiles[%d] mismatch: got %q want %q", i, restored.SuccessFiles[i], original.SuccessFiles[i])
		}
	}
}

func TestConversationRoundTripWithResumabilityFields(t *testing.T) {
	conv := New("roundtrip-resume", "Test goal")
	conv.ShellAgentResumable = true
	conv.ShellAgentTrajectoryPath = "/tmp/traj.json"
	conv.ShellAgentInterruptStep = 3
	conv.TurnsCompleted = 5
	conv.Goal = "Custom goal"
	conv.OriginalPrompt = "Original prompt text"
	conv.SuspendedUserInput = &SuspendedUserInputState{
		Kind:        "ask",
		Question:    "Which path?",
		Context:     "Testing",
		Reason:      "test",
		OriginalAsk: "Which?",
	}
	conv.PlannerProgress = &PlannerProgressState{
		SuccessFiles: []string{"a.go"},
	}
	conv.Modes = []string{"coding"}
	conv.ModeInstructionDir = "/modes"
	conv.PlannerOverlay = "overlay1"
	conv.TaskDescription = "A task"
	conv.Status = "running"
	conv.RuntimeStats = NewRuntimeStatsState(12345, 1234, 56789, 1000)
	conv.RuntimeStats.PlannerActivePromptTokens = 25000
	conv.RuntimeStats.Normalize()

	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if !strings.Contains(string(data), `"input_hit_tokens_display": "1,234"`) {
		t.Fatalf("expected human-readable input hit tokens in JSON:\n%s", string(data))
	}
	if !strings.Contains(string(data), `"input_miss_tokens_display": "56,789"`) {
		t.Fatalf("expected human-readable input miss tokens in JSON:\n%s", string(data))
	}
	if !strings.Contains(string(data), `"output_tokens_display": "1,000"`) {
		t.Fatalf("expected human-readable output tokens in JSON:\n%s", string(data))
	}

	loaded, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}

	if loaded.SessionID != conv.SessionID {
		t.Fatalf("SessionID mismatch: got %q want %q", loaded.SessionID, conv.SessionID)
	}
	if loaded.OriginalGoal != conv.OriginalGoal {
		t.Fatalf("OriginalGoal mismatch: got %q want %q", loaded.OriginalGoal, conv.OriginalGoal)
	}
	if loaded.RuntimeStats.PlannerActivePromptTokens != 25000 {
		t.Fatalf("planner runtime stats = %+v", loaded.RuntimeStats)
	}
	if loaded.ShellAgentResumable != conv.ShellAgentResumable {
		t.Fatalf("ShellAgentResumable mismatch: got %v want %v", loaded.ShellAgentResumable, conv.ShellAgentResumable)
	}
	if loaded.ShellAgentTrajectoryPath != conv.ShellAgentTrajectoryPath {
		t.Fatalf("ShellAgentTrajectoryPath mismatch: got %q want %q", loaded.ShellAgentTrajectoryPath, conv.ShellAgentTrajectoryPath)
	}
	if loaded.ShellAgentInterruptStep != conv.ShellAgentInterruptStep {
		t.Fatalf("ShellAgentInterruptStep mismatch: got %d want %d", loaded.ShellAgentInterruptStep, conv.ShellAgentInterruptStep)
	}
	if loaded.TurnsCompleted != conv.TurnsCompleted {
		t.Fatalf("TurnsCompleted mismatch: got %d want %d", loaded.TurnsCompleted, conv.TurnsCompleted)
	}
	if loaded.Goal != conv.Goal {
		t.Fatalf("Goal mismatch: got %q want %q", loaded.Goal, conv.Goal)
	}
	if loaded.OriginalPrompt != conv.OriginalPrompt {
		t.Fatalf("OriginalPrompt mismatch: got %q want %q", loaded.OriginalPrompt, conv.OriginalPrompt)
	}
	if loaded.RuntimeStats == nil {
		t.Fatalf("RuntimeStats mismatch: got nil, want non-nil")
	}
	if loaded.RuntimeStats.ActiveElapsedMS != 12345 {
		t.Fatalf("RuntimeStats.ActiveElapsedMS mismatch: got %d", loaded.RuntimeStats.ActiveElapsedMS)
	}
	if loaded.RuntimeStats.InputHitTokens != 1234 || loaded.RuntimeStats.InputMissTokens != 56789 || loaded.RuntimeStats.OutputTokens != 1000 {
		t.Fatalf("RuntimeStats token mismatch: %+v", loaded.RuntimeStats)
	}

	if conv.SuspendedUserInput == nil {
		if loaded.SuspendedUserInput != nil {
			t.Fatalf("SuspendedUserInput mismatch: got non-nil, want nil")
		}
	} else {
		if loaded.SuspendedUserInput == nil {
			t.Fatalf("SuspendedUserInput mismatch: got nil, want non-nil")
		} else {
			if loaded.SuspendedUserInput.Kind != conv.SuspendedUserInput.Kind {
				t.Fatalf("SuspendedUserInput.Kind mismatch: got %q want %q", loaded.SuspendedUserInput.Kind, conv.SuspendedUserInput.Kind)
			}
			if loaded.SuspendedUserInput.Question != conv.SuspendedUserInput.Question {
				t.Fatalf("SuspendedUserInput.Question mismatch: got %q want %q", loaded.SuspendedUserInput.Question, conv.SuspendedUserInput.Question)
			}
			if loaded.SuspendedUserInput.Context != conv.SuspendedUserInput.Context {
				t.Fatalf("SuspendedUserInput.Context mismatch: got %q want %q", loaded.SuspendedUserInput.Context, conv.SuspendedUserInput.Context)
			}
			if loaded.SuspendedUserInput.Reason != conv.SuspendedUserInput.Reason {
				t.Fatalf("SuspendedUserInput.Reason mismatch: got %q want %q", loaded.SuspendedUserInput.Reason, conv.SuspendedUserInput.Reason)
			}
			if loaded.SuspendedUserInput.OriginalAsk != conv.SuspendedUserInput.OriginalAsk {
				t.Fatalf("SuspendedUserInput.OriginalAsk mismatch: got %q want %q", loaded.SuspendedUserInput.OriginalAsk, conv.SuspendedUserInput.OriginalAsk)
			}
		}
	}

	if conv.PlannerProgress == nil {
		if loaded.PlannerProgress != nil {
			t.Fatalf("PlannerProgress mismatch: got non-nil, want nil")
		}
	} else {
		if loaded.PlannerProgress == nil {
			t.Fatalf("PlannerProgress mismatch: got nil, want non-nil")
		} else {
			if len(loaded.PlannerProgress.SuccessFiles) != len(conv.PlannerProgress.SuccessFiles) {
				t.Fatalf("PlannerProgress.SuccessFiles length mismatch: got %d want %d", len(loaded.PlannerProgress.SuccessFiles), len(conv.PlannerProgress.SuccessFiles))
			}
			for i := range conv.PlannerProgress.SuccessFiles {
				if loaded.PlannerProgress.SuccessFiles[i] != conv.PlannerProgress.SuccessFiles[i] {
					t.Fatalf("PlannerProgress.SuccessFiles[%d] mismatch: got %q want %q", i, loaded.PlannerProgress.SuccessFiles[i], conv.PlannerProgress.SuccessFiles[i])
				}
			}
		}
	}

	if len(loaded.Modes) != len(conv.Modes) {
		t.Fatalf("Modes length mismatch: got %d want %d", len(loaded.Modes), len(conv.Modes))
	}
	for i := range conv.Modes {
		if loaded.Modes[i] != conv.Modes[i] {
			t.Fatalf("Modes[%d] mismatch: got %q want %q", i, loaded.Modes[i], conv.Modes[i])
		}
	}

	if loaded.ModeInstructionDir != conv.ModeInstructionDir {
		t.Fatalf("ModeInstructionDir mismatch: got %q want %q", loaded.ModeInstructionDir, conv.ModeInstructionDir)
	}
	if loaded.PlannerOverlay != conv.PlannerOverlay {
		t.Fatalf("PlannerOverlay mismatch: got %q want %q", loaded.PlannerOverlay, conv.PlannerOverlay)
	}
	if loaded.TaskDescription != conv.TaskDescription {
		t.Fatalf("TaskDescription mismatch: got %q want %q", loaded.TaskDescription, conv.TaskDescription)
	}
	if loaded.Status != conv.Status {
		t.Fatalf("Status mismatch: got %q want %q", loaded.Status, conv.Status)
	}
}
