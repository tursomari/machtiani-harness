package conversation

import (
	"reflect"
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

func TestRenderReplayWithShellActions(t *testing.T) {
	conv := New("sess-replay-actions", "Show announced shell steps")
	conv.AddMessage("assistant", "Inspect the implementation.", map[string]any{
		"type": messageTypeWorkRequest,
		"turn": 2,
	})
	conv.AddMessage("assistant", "Inspection complete.", map[string]any{
		"type": messageTypeWorkResult,
		"turn": 2,
	})
	actions := []ShellActionRecord{
		{Version: 1, Turn: 2, Sequence: 2, Description: "Run the focused tests", Command: "go test ./internal/conversation", Step: 2, StepLimit: 8, CommandsExecuted: 2, RemainingSteps: 6},
		{Version: 1, Turn: 2, Sequence: 1, Description: "Inspect the renderer", Command: "sed -n '1,200p' replay_render.go", Step: 1, StepLimit: 8, CommandsExecuted: 1, RemainingSteps: 7},
		{Version: 1, Turn: 2, Sequence: 1, Description: "duplicate", Command: "must-not-render"},
	}

	got, err := RenderReplayWithOptions(conv, ReplayOptions{ShellActions: actions})
	if err != nil {
		t.Fatalf("RenderReplayWithOptions() error = %v", err)
	}
	for _, want := range []string{
		"── SHELL STEPS ──",
		"Step 1 of 8",
		"Inspect the renderer",
		"$ sed -n '1,200p' replay_render.go",
		"[commands executed: 1 · remaining steps: 7]",
		"Step 2 of 8",
		"$ go test ./internal/conversation",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "must-not-render") {
		t.Fatalf("duplicate sequence rendered:\n%s", got)
	}
	if first, second := strings.Index(got, "$ sed"), strings.Index(got, "$ go test"); first < 0 || second < 0 || first >= second {
		t.Fatalf("actions not rendered in sequence order:\n%s", got)
	}
	if question, shell, answer := strings.Index(got, "── QUESTION ──"), strings.Index(got, "── SHELL STEPS ──"), strings.Index(got, "── ANSWER ──"); question < 0 || shell <= question || answer <= shell {
		t.Fatalf("shell steps not rendered between question and answer:\n%s", got)
	}
}

func TestRenderReplayShellActionOptionsPreserveLegacyOutput(t *testing.T) {
	conv := New("sess-replay-actions-compat", "Preserve replay output")
	conv.AddMessage("assistant", "Question", map[string]any{"type": messageTypeWorkRequest, "turn": 1})
	conv.AddMessage("assistant", "Answer", map[string]any{"type": messageTypeWorkResult, "turn": 1})
	legacy, err := RenderReplay(conv)
	if err != nil {
		t.Fatal(err)
	}

	withoutActions, err := RenderReplayWithOptions(conv, ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if withoutActions != legacy {
		t.Fatalf("zero-action replay changed:\nlegacy: %q\nnew:    %q", legacy, withoutActions)
	}

	suppressed, err := RenderReplayWithOptions(conv, ReplayOptions{
		NoShellSteps: true,
		ShellActions: []ShellActionRecord{{Turn: 1, Sequence: 1, Command: "echo hidden"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if suppressed != legacy {
		t.Fatalf("NoShellSteps replay changed legacy output:\nlegacy: %q\nnew:    %q", legacy, suppressed)
	}
}

func TestRenderReplayDeltaWithLateShellActions(t *testing.T) {
	conv := New("sess-replay-actions-delta", "Tail shell actions")
	conv.AddMessage("assistant", "Question", map[string]any{"type": messageTypeWorkRequest, "turn": 3})
	previous := append([]Message(nil), conv.Messages...)
	actions := []ShellActionRecord{{Turn: 3, Sequence: 4, Description: "Late action", Command: "git status"}}

	got, err := RenderReplayDeltaWithOptions(conv, previous, ReplayOptions{ShellActions: actions})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"──── TURN 3 SHELL STEPS ────", "Late action", "$ git status"} {
		if !strings.Contains(got, want) {
			t.Errorf("late action delta missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Question") || strings.Contains(got, "── GOAL ──") {
		t.Fatalf("late action delta repeated conversation history:\n%s", got)
	}

	ordered := normalizeShellActions([]ShellActionRecord{
		{Turn: 3, Sequence: 2, Command: "second"},
		{Turn: 2, Sequence: 3, Command: "third"},
		{Turn: 2, Sequence: 1, Command: "first"},
	})
	if gotOrder := []string{ordered[0].Command, ordered[1].Command, ordered[2].Command}; !reflect.DeepEqual(gotOrder, []string{"first", "third", "second"}) {
		t.Fatalf("normalized order = %#v", gotOrder)
	}
}
