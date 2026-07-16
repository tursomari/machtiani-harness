package session

import (
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func TestTurnContext_buildShellAgentRequest_equivalence(t *testing.T) {
	// Build a minimal conversation with one user and one assistant message.
	conv := conversation.New("test-session", "Test goal from user")
	conv.AddMessage("assistant", "I understand the task.", nil)

	// Wrap in a conversationRecorder (the minimal setup TurnContext expects).
	recorder := &conversationRecorder{
		conversation: conv,
	}

	lib := &shellagent.ShellAgentLibrary{
		ExtraInstructions:      "extra instructions here",
		FewShotVariant:         "system",
		CommandTag:             "command",
		AnswerTag:              "answer",
		EnforceEarlyCommands:   false,
		Prompts: &minisweagent.PromptsConfig{
			Planner: &minisweagent.PlannerPromptsConfig{
				SystemTemplate: "Planner system template placeholder",
			},
			ShellAgent: &minisweagent.ShellAgentPromptsConfig{
				SystemTemplate:   "Shell-agent system template placeholder",
				InstanceTemplate: "Instance template for task: {{.Task}}",
			},
		},
		Config: &llm.ShellAgentConfig{
			MaxSteps: 10,
		},
	}

	task := "Do something useful"

	t.Run("TurnIndex_0_fewshot_appended", func(t *testing.T) {
		tc := &TurnContext{
			TurnIndex:     0,
			Conversation:  recorder,
			ShellAgentLib: lib,
		}

		sessionID := "test-shell-session"
		verbose := false
		maxInputTokens := 4000

		// --- New (actual) path ---
		got, err := tc.buildShellAgentRequest(task, sessionID, verbose, maxInputTokens)
		if err != nil {
			t.Fatalf("buildShellAgentRequest failed: %v", err)
		}

		// --- Old (manual) path ---
		extraInstr := lib.ExtraInstructions + "\n" + shellagent.FewShotShellAgentExamples(lib.CommandTag)

		convMsgs := conv.ToLLMMessages()
		prebuilt, err := shellagent.BuildShellAgentMessages(
			convMsgs, lib.Prompts, extraInstr,
			lib.CWD,
			lib.AnswerTag, lib.CommandTag,
		)
		if err != nil {
			t.Fatalf("old BuildShellAgentMessages failed: %v", err)
		}

		manualMessages := make([]llm.Message, len(prebuilt))
		copy(manualMessages, prebuilt)

		extraVars := map[string]interface{}{}
		instPrompt, err := shellagent.RenderInstancePrompt(
			lib.Prompts, task, lib.Config, lib.Env,
			extraVars, lib.AnswerTag, lib.CommandTag,
		)
		if err != nil {
			t.Fatalf("old RenderInstancePrompt failed: %v", err)
		}

		manualMessages = append(manualMessages, llm.Message{
			Role:    "user",
			Content: instPrompt,
		})

		// Compare the key fields directly (no cmp.Diff to avoid unexported-field panics).
		if len(got.PreconstructedMessages) != len(manualMessages) {
			t.Errorf("PreconstructedMessages length: got %d, want %d", len(got.PreconstructedMessages), len(manualMessages))
			return
		}
		for i := range manualMessages {
			if got.PreconstructedMessages[i].Role != manualMessages[i].Role {
				t.Errorf("message[%d].Role: got %q, want %q", i, got.PreconstructedMessages[i].Role, manualMessages[i].Role)
			}
			if got.PreconstructedMessages[i].Content != manualMessages[i].Content {
				t.Errorf("message[%d].Content mismatch (-want +got):\n- %s\n+ %s", i, manualMessages[i].Content, got.PreconstructedMessages[i].Content)
			}
		}
		if got.Task != task {
			t.Errorf("Task: got %q, want %q", got.Task, task)
		}
		if got.SessionID != sessionID {
			t.Errorf("SessionID: got %q, want %q", got.SessionID, sessionID)
		}
		if got.PlannerTurn != 0 {
			t.Errorf("PlannerTurn: got %d, want 0", got.PlannerTurn)
		}
		if got.Verbose != verbose {
			t.Errorf("Verbose: got %v, want %v", got.Verbose, verbose)
		}
		if got.MaxInputTokens != maxInputTokens {
			t.Errorf("MaxInputTokens: got %d, want %d", got.MaxInputTokens, maxInputTokens)
		}
		if got.EnforceEarlyCommands != lib.EnforceEarlyCommands {
			t.Errorf("EnforceEarlyCommands: got %v, want %v", got.EnforceEarlyCommands, lib.EnforceEarlyCommands)
		}
		if got.AnswerTag != lib.AnswerTag {
			t.Errorf("AnswerTag: got %q, want %q", got.AnswerTag, lib.AnswerTag)
		}
		if got.CommandTag != lib.CommandTag {
			t.Errorf("CommandTag: got %q, want %q", got.CommandTag, lib.CommandTag)
		}
		// Pointer identity checks for Config, Prompts, Model, Env — must be the same objects.
		if got.Config != lib.Config {
			t.Error("Config pointer different — old and new pipeline use different Config objects")
		}
		if got.Prompts != lib.Prompts {
			t.Error("Prompts pointer different — old and new pipeline use different Prompts objects")
		}
	})

	t.Run("TurnIndex_5_fewshot_not_appended", func(t *testing.T) {
		tc := &TurnContext{
			TurnIndex:     5,
			Conversation:  recorder,
			ShellAgentLib: lib,
		}

		sessionID := "test-shell-session"
		verbose := false
		maxInputTokens := 4000

		// --- New (actual) path ---
		got, err := tc.buildShellAgentRequest(task, sessionID, verbose, maxInputTokens)
		if err != nil {
			t.Fatalf("buildShellAgentRequest failed: %v", err)
		}

		// --- Old (manual) path --- no few-shot appended for turn >= 3 ---
		extraInstr := lib.ExtraInstructions

		convMsgs := conv.ToLLMMessages()
		prebuilt, err := shellagent.BuildShellAgentMessages(
			convMsgs, lib.Prompts, extraInstr,
			lib.CWD,
			lib.AnswerTag, lib.CommandTag,
		)
		if err != nil {
			t.Fatalf("old BuildShellAgentMessages failed: %v", err)
		}

		manualMessages := make([]llm.Message, len(prebuilt))
		copy(manualMessages, prebuilt)

		extraVars := map[string]interface{}{}
		instPrompt, err := shellagent.RenderInstancePrompt(
			lib.Prompts, task, lib.Config, lib.Env,
			extraVars, lib.AnswerTag, lib.CommandTag,
		)
		if err != nil {
			t.Fatalf("old RenderInstancePrompt failed: %v", err)
		}

		manualMessages = append(manualMessages, llm.Message{
			Role:    "user",
			Content: instPrompt,
		})

		if len(got.PreconstructedMessages) != len(manualMessages) {
			t.Errorf("PreconstructedMessages length: got %d, want %d", len(got.PreconstructedMessages), len(manualMessages))
			return
		}
		for i := range manualMessages {
			if got.PreconstructedMessages[i].Role != manualMessages[i].Role {
				t.Errorf("message[%d].Role: got %q, want %q", i, got.PreconstructedMessages[i].Role, manualMessages[i].Role)
			}
			if got.PreconstructedMessages[i].Content != manualMessages[i].Content {
				t.Errorf("message[%d].Content mismatch (-want +got):\n- %s\n+ %s", i, manualMessages[i].Content, got.PreconstructedMessages[i].Content)
			}
		}
		if got.PlannerTurn != 5 {
			t.Errorf("PlannerTurn: got %d, want 5", got.PlannerTurn)
		}
	})
}
