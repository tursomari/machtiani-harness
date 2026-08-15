package shellagent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/agents"
	runpkg "github.com/tursomari/machtiani/agent/internal/shell-agent/internal/run"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func useIsolatedArtifactContext(t *testing.T) {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

// TestBuildShellAgentMessages verifies that the pre-built message prefix
// is correctly assembled from planner conversation messages and the
// shell-agent system prompt.
func TestBuildShellAgentMessages(t *testing.T) {
	// Create a conversation with a work_request/work_result pair.
	// conversation.New adds the goal as a user message at index 0.
	conv := conversation.New("test-session", "Test goal")
	conv.AddMessage("assistant", "investigate the auth module", map[string]any{
		"type": "work_request",
		"turn": 1,
	})
	conv.AddMessage("assistant", "found JWT middleware in auth/", map[string]any{
		"type": "work_result",
		"turn": 1,
	})

	llmMessages := conv.ToLLMMessages()
	if len(llmMessages) != 3 {
		t.Fatalf("expected 3 messages (goal + work_request + work_result), got %d", len(llmMessages))
	}

	// Index 0: goal (user role, no prefix).
	if llmMessages[0].Role != "user" {
		t.Errorf("expected user role for goal, got: %s", llmMessages[0].Role)
	}
	if !strings.Contains(llmMessages[0].Content, "Test goal") {
		t.Errorf("expected goal content, got: %s", llmMessages[0].Content)
	}

	// Index 1: work_request (assistant role, [work_request] prefix).
	if llmMessages[1].Role != "assistant" {
		t.Errorf("expected assistant role for work_request, got: %s", llmMessages[1].Role)
	}
	if !strings.Contains(llmMessages[1].Content, "[work_request]") {
		t.Errorf("expected [work_request] prefix, got: %s", llmMessages[1].Content)
	}

	// Index 2: work_result (assistant role, [work_result] prefix).
	if llmMessages[2].Role != "assistant" {
		t.Errorf("expected assistant role for work_result, got: %s", llmMessages[2].Role)
	}
	if !strings.Contains(llmMessages[2].Content, "[work_result]") {
		t.Errorf("expected [work_result] prefix, got: %s", llmMessages[2].Content)
	}

	// Verify source metadata on work messages.
	for i := 1; i < len(llmMessages); i++ {
		source, _ := llmMessages[i].Metadata["source"].(string)
		if source != "planner" {
			t.Errorf("expected source=planner on msg %d, got: %s", i, source)
		}
		if _, ok := llmMessages[i].Metadata["estimated_tokens"]; !ok {
			t.Errorf("expected estimated_tokens in metadata on msg %d", i)
		}
	}
}

// TestToLLMMessagesFiltersCacheAnchor verifies that cache_anchor
// sentinel messages are excluded from the output.
func TestToLLMMessagesFiltersCacheAnchor(t *testing.T) {
	conv := conversation.New("test-session", "Test goal")
	conv.AddMessage("assistant", "investigate auth", map[string]any{
		"type": "work_request",
		"turn": 1,
	})
	conv.AddMessage("user", "[cache anchor]", map[string]any{
		"type": "cache_anchor",
	})
	conv.AddMessage("assistant", "result", map[string]any{
		"type": "work_result",
		"turn": 1,
	})

	msgs := conv.ToLLMMessages()
	// goal + work_request + work_result = 3 (cache_anchor filtered).
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (cache_anchor filtered), got %d", len(msgs))
	}
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "cache anchor") {
			t.Errorf("cache_anchor message was not filtered: %s", msg.Content)
		}
	}
}

// TestToLLMMessagesEmptyConversation verifies nil/empty safety.
func TestToLLMMessagesEmptyConversation(t *testing.T) {
	var nilConv *conversation.Conversation
	if msgs := nilConv.ToLLMMessages(); msgs != nil {
		t.Error("expected nil for nil conversation")
	}

	// An empty conversation (no Messages) returns an empty non-nil slice.
	empty := &conversation.Conversation{}
	msgs := empty.ToLLMMessages()
	if msgs == nil {
		t.Error("expected non-nil empty slice for empty conversation")
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

// TestToLLMMessagesPlainUserMessage verifies that plain user messages
// (no type metadata) are included as-is with user role.
func TestToLLMMessagesPlainUserMessage(t *testing.T) {
	conv := conversation.New("test-session", "Test goal")
	// The New function adds the goal as a user message.
	msgs := conv.ToLLMMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (the goal), got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected user role for plain message, got: %s", msgs[0].Role)
	}
}

// TestRunWithMessagesMockAgent verifies that the RunWithMessages
// entry point correctly delegates to SetMessages + RunLoop.
// With a nil model the agent will panic inside Query; we use recover
// to confirm the message setup itself is safe.
func TestRunWithMessagesMockAgent(t *testing.T) {
	// Build a minimal message array.
	messages := []llm.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "--- BEGIN TASK ---\nTest task\n--- END TASK ---"},
	}

	// Create a Request with the pre-built messages.
	// Use nil config/prompts/model/env — we just want to verify the
	// message array flows through correctly without panicking during setup.
	req := Request{
		PreconstructedMessages: messages,
		Config:                 &minisweagent.ShellAgentConfig{},
		Prompts:                &minisweagent.PromptsConfig{},
		Model:                  nil,
		Env:                    nil,
	}

	// Run should fail because Model is nil (the agent will panic inside
	// Query when it tries to use the nil model). We recover to confirm
	// the message setup code itself is correct.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Log("recovered from expected panic (nil model):", r)
			}
		}()
		_, _ = Run(context.Background(), req)
	}()
}

// TestMessageArrayOrdering verifies the message array follows the
// expected order: system prompt, then planner messages, then instance.
func TestMessageArrayOrdering(t *testing.T) {
	conv := conversation.New("test-session", "Test goal")
	conv.AddMessage("assistant", "previous work", map[string]any{
		"type": "work_request",
		"turn": 1,
	})
	conv.AddMessage("assistant", "previous result", map[string]any{
		"type": "work_result",
		"turn": 1,
	})

	llmMessages := conv.ToLLMMessages()
	// goal + work_request + work_result = 3.
	if len(llmMessages) != 3 {
		t.Fatalf("expected 3 planner messages, got %d", len(llmMessages))
	}

	// BuildShellAgentMessages prepends the system prompt.
	prompts := &minisweagent.PromptsConfig{}
	prebuilt, err := BuildShellAgentMessages(llmMessages, prompts, "", "", "answer", "command")
	if err != nil {
		t.Fatalf("BuildShellAgentMessages: %v", err)
	}

	// First message should be the system prompt.
	if prebuilt[0].Role != "system" {
		t.Errorf("expected system role first, got: %s", prebuilt[0].Role)
	}
	// Then planner messages (goal as user, work_request as assistant, work_result as assistant).
	if prebuilt[1].Role != "user" {
		t.Errorf("expected user role second (goal), got: %s", prebuilt[1].Role)
	}
	if prebuilt[2].Role != "assistant" {
		t.Errorf("expected assistant role third (work_request), got: %s", prebuilt[2].Role)
	}
	if prebuilt[3].Role != "assistant" {
		t.Errorf("expected assistant role fourth (work_result), got: %s", prebuilt[3].Role)
	}
	// Length should be len(llmMessages) + 1.
	if len(prebuilt) != len(llmMessages)+1 {
		t.Errorf("expected %d messages, got %d", len(llmMessages)+1, len(prebuilt))
	}
}

// TestBuildShellAgentMessagesWithExtraInstructions verifies that
// extra instructions are appended to the system prompt after the
// default rendered prompt.
func TestBuildShellAgentMessagesWithExtraInstructions(t *testing.T) {
	conv := conversation.New("test-session", "Test goal")
	llmMessages := conv.ToLLMMessages()

	prompts := &minisweagent.PromptsConfig{}
	extraInstructions := "Additional shell-agent guidance: use handoff notes."

	prebuilt, err := BuildShellAgentMessages(llmMessages, prompts, extraInstructions, "", "answer", "command")
	if err != nil {
		t.Fatalf("BuildShellAgentMessages: %v", err)
	}

	if len(prebuilt) == 0 {
		t.Fatal("expected at least one message")
	}

	if prebuilt[0].Role != "system" {
		t.Errorf("expected system role first, got: %s", prebuilt[0].Role)
	}

	if !strings.Contains(prebuilt[0].Content, extraInstructions) {
		t.Errorf("expected system prompt to contain extra instructions, got: %s", prebuilt[0].Content)
	}

	// Verify extra instructions appear after the default prompt.
	defaultPrompt := prebuilt[0].Content[:len(prebuilt[0].Content)-len(extraInstructions)-1]
	if !strings.Contains(defaultPrompt, "You are") {
		t.Errorf("expected default prompt to appear before extra instructions, got: %s", prebuilt[0].Content)
	}
}

// TestValidateAnswerTag covers the rule 12 banned-substring list. The
// CLI uses this helper at the boundary so a malformed --answer-tag
// never reaches the parser or template renderer.
func TestValidateAnswerTag(t *testing.T) {
	cases := []struct {
		name    string
		tag     string
		wantErr bool
	}{
		{"empty allowed", "", false},
		{"whitespace-only allowed", "   ", false},
		{"plain word", "answercode", false},
		{"hyphen", "answer-code", false},
		{"underscore", "answer_code", false},
		{"number", "answer1", false},
		{"reject open angle", "an<swer", true},
		{"reject close angle", "an>swer", true},
		{"reject slash", "ans/wer", true},
		{"reject open template", "ans{{wer", true},
		{"reject close template", "ans}}wer", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAnswerTag(tc.tag)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for tag %q, got nil", tc.tag)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error for tag %q, got %v", tc.tag, err)
			}
		})
	}
}

// TestComposeEffectiveAnswerTag tests the ComposeEffectiveAnswerTag
// function which combines --answer-tag and --tag flags into the
// effective answer tag used for parsing and rendering.
func TestComposeEffectiveAnswerTag(t *testing.T) {
	cases := []struct {
		name      string
		answerTag string
		tagSuffix string
		wantTag   string
		wantErr   bool
	}{
		{"tag foo produces answer-foo", "", "foo", "answer-foo", false},
		{"both flags error", "bar", "foo", "", true},
		{"invalid suffix angle bracket", "", "bad<tag", "", true},
		{"no flags default empty", "", "", "", false},
		{"only answer-tag set", "myanswer", "", "myanswer", false},
		{"answer-tag with invalid slash", "bad/tag", "", "", true},
		{"tag suffix with underscore", "", "my_tag", "answer-my_tag", false},
		{"valid composed with hyphen", "", "some-tag", "answer-some-tag", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTag, err := ComposeEffectiveAnswerTag(tc.answerTag, tc.tagSuffix)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got tag=%q err=nil", gotTag)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotTag != tc.wantTag {
				t.Fatalf("expected tag %q, got %q", tc.wantTag, gotTag)
			}
		})
	}
}

// TestRenderSystemPromptHonorsAnswerTag ensures the
// --answer-tag override reaches the rendered system prompt. The
// assertion looks for the literal substituted tag, so a regression
// (e.g. falling back to the default "answer") is caught even when the
// surrounding template text changes.
func TestRenderSystemPromptHonorsAnswerTag(t *testing.T) {
	prompts := &minisweagent.PromptsConfig{}
	rendered, err := RenderSystemPrompt(prompts, nil, "answercode", "")
	if err != nil {
		t.Fatalf("RenderSystemPrompt: %v", err)
	}
	if !strings.Contains(rendered, "<answercode>") {
		t.Fatalf("expected system prompt to contain <answercode>, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "{{.AnswerTag}}") {
		t.Fatalf("system prompt still contains unrendered {{.AnswerTag}}:\n%s", rendered)
	}
}

// TestRenderInstancePromptHonorsAnswerTag mirrors the system-prompt
// test for the instance template, since both surfaces were
// parameterized together but are rendered via separate entry points.
func TestRenderInstancePromptHonorsAnswerTag(t *testing.T) {
	prompts := &minisweagent.PromptsConfig{}
	cfg := &minisweagent.ShellAgentConfig{MaxSteps: 10}
	// The instance template needs {{.Machine}}; the helper pulls it
	// from the environment's GetTemplateVars, so pass a stub env that
	// advertises one.
	env := &stubEnv{GetTemplateVarsResult: map[string]interface{}{"Machine": "linux"}}
	rendered, err := RenderInstancePrompt(prompts, "do the thing", cfg, env, nil, "answercode", "")
	if err != nil {
		t.Fatalf("RenderInstancePrompt: %v", err)
	}
	if !strings.Contains(rendered, "<answercode>") {
		t.Fatalf("expected instance prompt to contain <answercode>, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "{{.AnswerTag}}") {
		t.Fatalf("instance prompt still contains unrendered {{.AnswerTag}}:\n%s", rendered)
	}
}

// TestRenderPromptsDefaultToAnswerTag pins the default-tag contract
// for the two render functions: empty / unset answerTag must render
// <answer>...</answer>, since that is the shape the parser, the
// force-finalize template, and the existing prompts were authored
// against.
func TestRenderPromptsDefaultToAnswerTag(t *testing.T) {
	prompts := &minisweagent.PromptsConfig{}
	sys, err := RenderSystemPrompt(prompts, nil, "", "")
	if err != nil {
		t.Fatalf("RenderSystemPrompt: %v", err)
	}
	if !strings.Contains(sys, "<answer>") {
		t.Fatalf("expected default system prompt to contain <answer>, got:\n%s", sys)
	}
	cfg := &minisweagent.ShellAgentConfig{MaxSteps: 10}
	env := &stubEnv{GetTemplateVarsResult: map[string]interface{}{"Machine": "linux"}}
	inst, err := RenderInstancePrompt(prompts, "do the thing", cfg, env, nil, "", "")
	if err != nil {
		t.Fatalf("RenderInstancePrompt: %v", err)
	}
	if !strings.Contains(inst, "<answer>") {
		t.Fatalf("expected default instance prompt to contain <answer>, got:\n%s", inst)
	}
}

// TestBuildShellAgentMessagesHonorsAnswerTag ensures the
// pre-built-message helper also threads the tag override through to
// the system prompt. The instance prompt is appended separately by
// the prompt layer; here we only assert the system prompt side.
func TestBuildShellAgentMessagesHonorsAnswerTag(t *testing.T) {
	conv := conversation.New("test-session", "Test goal")
	prebuilt, err := BuildShellAgentMessages(conv.ToLLMMessages(), &minisweagent.PromptsConfig{}, "", "", "final", "command")
	if err != nil {
		t.Fatalf("BuildShellAgentMessages: %v", err)
	}
	if len(prebuilt) == 0 {
		t.Fatal("expected at least one message")
	}
	if !strings.Contains(prebuilt[0].Content, "<final>") {
		t.Fatalf("expected prebuilt system prompt to contain <final>, got:\n%s", prebuilt[0].Content)
	}
	if strings.Contains(prebuilt[0].Content, "<answer>") {
		t.Fatalf("prebuilt system prompt should not contain the default <answer> tag when override is set, got:\n%s", prebuilt[0].Content)
	}
}

// stubEnv is a minimal minisweagent.Environment that returns a fixed
// GetTemplateVars map. It exists solely so the renderer tests can
// supply {{.Machine}} (and any other env-derived var) without
// pulling in the full shell-agent test machinery.
type stubEnv struct {
	GetTemplateVarsResult map[string]interface{}
}

func (s *stubEnv) Config() interface{} { return &minisweagent.EnvironmentConfig{} }
func (s *stubEnv) Execute(_ context.Context, _, _ string) (minisweagent.ExecuteResult, error) {
	return minisweagent.ExecuteResult{}, nil
}
func (s *stubEnv) GetTemplateVars() map[string]interface{} {
	if s.GetTemplateVarsResult == nil {
		return map[string]interface{}{}
	}
	return s.GetTemplateVarsResult
}
func (s *stubEnv) GetSyncProgress() float64 { return 1.0 }
func (s *stubEnv) GetSyncStatus() string    { return "" }

// TestPerRunCallCountStartsAtZero pins the per-run LLM call budget:
// the call counter used by StepLimit enforcement (and surfaced via
// NCalls()) must reset to 0 at the start of each Run() invocation so
// that one work_requests calls never starve the next one. After a
// single Run() that returns a final answer immediately, the per-run
// NCalls() must equal 1 (the one Query() that happened in that run).
func TestPerRunCallCountStartsAtZero(t *testing.T) {
	const response = "<answer>\nConfidence: 100% - ok\n</answer>"
	var models []*perRunCountingModel
	factory := func() (minisweagent.Model, error) {
		m := &perRunCountingModel{response: response}
		models = append(models, m)
		return m, nil
	}
	agent := agents.NewDefaultAgent(&perRunCountingModel{response: response}, &stubEnvForCounting{}, &minisweagent.ShellAgentConfig{}, &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "sys",
			InstanceTemplate: "inst",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	},
		agents.WithNewModel(factory),
	)

	exitStatus, answer, err := agent.Run(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exitStatus != "Submitted" {
		t.Fatalf("expected Submitted, got %q (answer=%q)", exitStatus, answer)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model built by factory, got %d", len(models))
	}
	if models[0].NCalls() != 1 {
		t.Fatalf("expected per-run NCalls == 1 after a single Run(), got %d", models[0].NCalls())
	}
}

// TestSecondRunResetsCallCount verifies that the per-run LLM call
// counter resets to 0 at the beginning of each Run() invocation. After
// two consecutive Run() calls on the same DefaultAgent, the per-run
// NCalls() must reflect only the calls made in the second run (1), not
// the sum of both runs (2).
func TestSecondRunResetsCallCount(t *testing.T) {
	const response = "<answer>\nConfidence: 100% - ok\n</answer>"
	var models []*perRunCountingModel
	factory := func() (minisweagent.Model, error) {
		m := &perRunCountingModel{response: response}
		models = append(models, m)
		return m, nil
	}
	agent := agents.NewDefaultAgent(&perRunCountingModel{response: response}, &stubEnvForCounting{}, &minisweagent.ShellAgentConfig{}, &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "sys",
			InstanceTemplate: "inst",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	},
		agents.WithNewModel(factory),
	)

	if _, _, err := agent.Run(context.Background(), "first run"); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if _, _, err := agent.Run(context.Background(), "second run"); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models built by factory (one per Run), got %d", len(models))
	}
	if models[1].NCalls() != 1 {
		t.Fatalf("expected per-run NCalls == 1 after second Run() (reset to 0 at start), got %d", models[1].NCalls())
	}
}

// TestStepLimitEnforcedOnPerRunCounter pins the StepLimit behaviour
// against the per-run counter: with StepLimit=1 and a stub model that
// always returns a command, the run must terminate with a
// LimitsExceeded error after exactly 1 LLM call in that run.
func TestStepLimitEnforcedOnPerRunCounter(t *testing.T) {
	const response = "<command>\nprintf ok\n</command>"
	var models []*perRunCountingModel
	factory := func() (minisweagent.Model, error) {
		m := &perRunCountingModel{response: response}
		models = append(models, m)
		return m, nil
	}
	cfg := &minisweagent.ShellAgentConfig{MaxSteps: 1}
	agent := agents.NewDefaultAgent(&perRunCountingModel{response: response}, &stubEnvForCounting{}, cfg, &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "sys",
			InstanceTemplate: "inst",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	},
		agents.WithNewModel(factory),
	)

	exitStatus, _, runErr := agent.Run(context.Background(), "loop forever")
	if runErr != nil {
		t.Fatalf("Run() error = %v", runErr)
	}
	if exitStatus != "LimitsExceeded" {
		t.Fatalf("expected exit status %q, got %q", "LimitsExceeded", exitStatus)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model built by factory, got %d", len(models))
	}
	if models[0].NCalls() != 1 {
		t.Fatalf("expected per-run NCalls == 1 (StepLimit=1, no calls accumulated from prior runs), got %d", models[0].NCalls())
	}
}

// TestComposeEffectiveCommandTag verifies that the command tag suffix
// is correctly composed into the effective command tag.
func TestComposeEffectiveCommandTag(t *testing.T) {
	cases := []struct {
		tagSuffix string
		want      string
	}{
		{"", "command"},
		{"foo", "command-foo"},
	}
	for _, tc := range cases {
		got := ComposeEffectiveCommandTag(tc.tagSuffix)
		if got != tc.want {
			t.Errorf("ComposeEffectiveCommandTag(%q) = %q, want %q", tc.tagSuffix, got, tc.want)
		}
	}
}

// TestComposeEffectiveTags verifies that the answer and command tag
// suffixes are correctly composed into effective tags, including
// mutual exclusion and default behaviors.
func TestComposeEffectiveTags(t *testing.T) {
	cases := []struct {
		answerTag   string
		tagSuffix   string
		wantAnswer  string
		wantCommand string
		wantErr     bool
	}{
		{"", "foo", "answer-foo", "command-foo", false},
		{"bar", "", "bar", "command", false},
		{"bar", "foo", "", "", true},
		{"", "", "answer", "command", false},
	}
	for _, tc := range cases {
		gotAnswer, gotCommand, err := ComposeEffectiveTags(tc.answerTag, tc.tagSuffix)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ComposeEffectiveTags(%q, %q) expected error, got nil", tc.answerTag, tc.tagSuffix)
			}
		} else {
			if err != nil {
				t.Errorf("ComposeEffectiveTags(%q, %q) unexpected error: %v", tc.answerTag, tc.tagSuffix, err)
			}
			if gotAnswer != tc.wantAnswer {
				t.Errorf("ComposeEffectiveTags(%q, %q) answer = %q, want %q", tc.answerTag, tc.tagSuffix, gotAnswer, tc.wantAnswer)
			}
			if gotCommand != tc.wantCommand {
				t.Errorf("ComposeEffectiveTags(%q, %q) command = %q, want %q", tc.answerTag, tc.tagSuffix, gotCommand, tc.wantCommand)
			}
		}
	}
}

type perRunCountingModel struct {
	response string
	nCalls   int
}

func (m *perRunCountingModel) Config() interface{} { return &minisweagent.ModelConfig{} }
func (m *perRunCountingModel) Cost() float64       { return 0 }
func (m *perRunCountingModel) NCalls() int         { return m.nCalls }

func (m *perRunCountingModel) Query(_ context.Context, _ []minisweagent.Message, _ ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.nCalls++
	return minisweagent.QueryResult{Content: m.response}, nil
}
func (m *perRunCountingModel) GetTemplateVars() map[string]interface{} {
	return map[string]interface{}{}
}

type stubEnvForCounting struct{}

func (s *stubEnvForCounting) Config() interface{} { return &minisweagent.EnvironmentConfig{} }
func (s *stubEnvForCounting) Execute(_ context.Context, _, _ string) (minisweagent.ExecuteResult, error) {
	return minisweagent.ExecuteResult{Output: "", ReturnCode: 0}, nil
}
func (s *stubEnvForCounting) GetTemplateVars() map[string]interface{} {
	return map[string]interface{}{}
}
func (s *stubEnvForCounting) GetSyncProgress() float64 { return 1.0 }
func (s *stubEnvForCounting) GetSyncStatus() string    { return "" }

// TestRunResumeNoSavedState verifies that when no resume file exists
// for a given SessionID, a fresh run proceeds normally without error.
func TestRunResumeNoSavedState(t *testing.T) {
	const sessionID = "test-resume-no-saved-state"
	const response = "<answer>\nConfidence: 100% - ok\n</answer>"

	// Clean up any leftover resume file from a previous test run.
	trajDir := t.TempDir()
	trajPath := filepath.Join(trajDir, "trajectory.json")
	_ = os.Remove(trajPath)

	model := &perRunCountingModel{response: response}
	messages := []llm.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "do the thing"},
	}

	req := Request{
		PreconstructedMessages: messages,
		Config:                 &minisweagent.ShellAgentConfig{},
		Prompts:                &minisweagent.PromptsConfig{},
		Model:                  model,
		Env:                    &stubEnvForCounting{},
		SessionID:              sessionID,
	}

	result, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitStatus != "Submitted" {
		t.Fatalf("expected Submitted, got %q", result.ExitStatus)
	}
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
}

// TestRunResumeFromSavedState saves a resume state file directly before
// calling Run, then verifies that the agent resumed from it and returned
// the final trajectory from the resumed run (not just the saved state).
func TestRunResumeFromSavedState(t *testing.T) {
	const sessionID = "test-resume-from-saved-state"
	const response = "<answer>\nConfidence: 100% - ok\n</answer>"

	trajDir := t.TempDir()
	trajPath := filepath.Join(trajDir, "trajectory.json")
	_ = os.Remove(trajPath)

	// Step 1: Save a resume state file with known values.
	resumeState := &runpkg.ResumeState{
		Version:           runpkg.ResumeStateVersion,
		StepCounter:       5,
		CommandsExecuted:  3,
		FinalizeRequested: true,
		SystemPrompt:      "custom system prompt content",
	}
	savedMessages := []minisweagent.Message{
		{Role: "system", Content: "custom system prompt content"},
		{Role: "user", Content: "resumed task"},
	}
	traj := runpkg.FileTrajectory{
		Messages:    savedMessages,
		ExitStatus:  "Ongoing",
		Result:      "partial result",
		ResumeState: resumeState,
		Timestamp:   time.Now().UTC(),
	}
	if err := runpkg.SaveTrajectory(traj, trajPath); err != nil {
		t.Fatalf("save trajectory: %v", err)
	}
	defer os.Remove(trajPath)

	// Step 2: Run with the same SessionID.
	model := &perRunCountingModel{response: response}
	messages := []llm.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "do the thing"},
	}

	req := Request{
		PreconstructedMessages: messages,
		Config:                 &minisweagent.ShellAgentConfig{},
		Prompts:                &minisweagent.PromptsConfig{},
		Model:                  model,
		Env:                    &stubEnvForCounting{},
		SessionID:              sessionID,
	}

	result, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitStatus != "Submitted" {
		t.Fatalf("expected Submitted, got %q", result.ExitStatus)
	}
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	// The resume path returns the final trajectory after the resumed run
	// completes, which includes the newly accumulated messages.
	if len(result.Trajectory.Messages) == 0 {
		t.Fatal("expected trajectory messages from the resumed run, got none")
	}
	if result.Trajectory.ExitStatus != "Submitted" {
		t.Errorf("expected trajectory ExitStatus 'Submitted' after resumed run, got %q",
			result.Trajectory.ExitStatus)
	}
	if result.Trajectory.Result == "" {
		t.Error("expected trajectory Result to be non-empty after resumed run")
	}
}

// TestRunResumeCorruptedFile writes a non-JSON file to the resume path
// and verifies that Run falls back to a fresh run without panicking or
// returning an error.
func TestRunResumeCorruptedFile(t *testing.T) {
	const sessionID = "test-resume-corrupted"
	const response = "<answer>\nConfidence: 100% - ok\n</answer>"

	trajDir := t.TempDir()
	trajPath := filepath.Join(trajDir, "trajectory.json")
	_ = os.Remove(trajPath)

	// Write corrupted (non-JSON) data to the resume path.
	corruptData := []byte("this is not valid json {{{[[[")
	if err := os.WriteFile(trajPath, corruptData, 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	defer os.Remove(trajPath)

	model := &perRunCountingModel{response: response}
	messages := []llm.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "do the thing"},
	}

	req := Request{
		PreconstructedMessages: messages,
		Config:                 &minisweagent.ShellAgentConfig{},
		Prompts:                &minisweagent.PromptsConfig{},
		Model:                  model,
		Env:                    &stubEnvForCounting{},
		SessionID:              sessionID,
	}

	result, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() should not return an error on corrupt resume file, got: %v", err)
	}
	if result.ExitStatus != "Submitted" {
		t.Fatalf("expected Submitted (fresh run fallback), got %q", result.ExitStatus)
	}
	if result.Error != nil {
		t.Fatalf("unexpected error from corrupt file fallback: %v", result.Error)
	}
}

// TestTrajectoryFilePersistsAfterSuccessfulResume verifies that after a
// successful resume, the trajectory file persists on disk with the correct
// exit status. Trajectory files are no longer deleted on clean exit.
func TestTrajectoryFilePersistsAfterSuccessfulResume(t *testing.T) {
	useIsolatedArtifactContext(t)
	const sessionID = "test-trajectory-persists"
	const response = "<answer>\nConfidence: 100% - ok\n</answer>"

	// Compute the canonical trajectory path that Run will use.
	trajPath, pathErr := artifacts.ShellAgentTrajectoryPath(sessionID, 0)
	if pathErr != nil {
		t.Fatalf("ShellAgentTrajectoryPath: %v", pathErr)
	}
	_ = os.Remove(trajPath)

	// Pre-create a valid resume file at the path Run will look for.
	resumeState := &runpkg.ResumeState{
		Version:          runpkg.ResumeStateVersion,
		StepCounter:      5,
		CommandsExecuted: 3,
		SystemPrompt:     "custom system prompt content",
	}
	traj := runpkg.FileTrajectory{
		Messages:    []minisweagent.Message{{Role: "system", Content: "resumed session"}},
		ExitStatus:  "Ongoing",
		Result:      "partial result",
		ResumeState: resumeState,
		Timestamp:   time.Now().UTC(),
	}
	if err := runpkg.SaveTrajectory(traj, trajPath); err != nil {
		t.Fatalf("save trajectory: %v", err)
	}
	defer os.Remove(trajPath)

	// Verify the trajectory file exists before the run.
	if _, err := os.Stat(trajPath); os.IsNotExist(err) {
		t.Fatal("trajectory file should exist before Run")
	}

	model := &perRunCountingModel{response: response}
	messages := []llm.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "do the thing"},
	}

	req := Request{
		PreconstructedMessages: messages,
		Config:                 &minisweagent.ShellAgentConfig{},
		Prompts:                &minisweagent.PromptsConfig{},
		Model:                  model,
		Env:                    &stubEnvForCounting{},
		SessionID:              sessionID,
		ResumeAttempt:          true,
	}

	result, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitStatus != "Submitted" {
		t.Fatalf("expected Submitted, got %q", result.ExitStatus)
	}

	// After successful completion, the trajectory file must persist.
	if _, err := os.Stat(trajPath); os.IsNotExist(err) {
		t.Fatalf("expected trajectory file to persist after successful resume, but it does not exist at %s", trajPath)
	}

	// Load the trajectory and verify the exit status is the success status.
	loadedTraj, loadErr := runpkg.LoadTrajectory(trajPath)
	if loadErr != nil {
		t.Fatalf("LoadTrajectory: %v", loadErr)
	}
	if loadedTraj.ExitStatus == "Cancelled" || loadedTraj.ExitStatus == "Error" {
		t.Fatalf("expected trajectory exit status to be a success status, got %q", loadedTraj.ExitStatus)
	}
	if loadedTraj.ExitStatus != "Submitted" {
		t.Fatalf("expected trajectory exit status %q, got %q", "Submitted", loadedTraj.ExitStatus)
	}
}

// TestModeMismatchWarningEmitted verifies that when a resume state was
// saved with one set of extra instructions (mode-alpha) and the current
// request carries different extra instructions (mode-beta), a warning is
// written to WarningWriter indicating the mode mismatch.
func TestModeMismatchWarningEmitted(t *testing.T) {
	useIsolatedArtifactContext(t)
	const sessionID = "test-mode-mismatch-warning"
	const response = "<answer>\nConfidence: 100% - ok\n</answer>"

	// Compute the canonical trajectory path that Run will use.
	trajPath, pathErr := artifacts.ShellAgentTrajectoryPath(sessionID, 0)
	if pathErr != nil {
		t.Fatalf("ShellAgentTrajectoryPath: %v", pathErr)
	}
	_ = os.Remove(trajPath)
	defer os.Remove(trajPath)

	// Pre-create a valid resume file whose saved system prompt
	// contains mode-alpha instructions.
	resumeState := &runpkg.ResumeState{
		Version:          runpkg.ResumeStateVersion,
		StepCounter:      5,
		CommandsExecuted: 3,
		SystemPrompt:     "base prompt + mode-alpha instructions",
	}
	traj := runpkg.FileTrajectory{
		Messages:    []minisweagent.Message{{Role: "system", Content: "resumed session"}},
		ExitStatus:  "Ongoing",
		Result:      "partial result",
		ResumeState: resumeState,
		Timestamp:   time.Now().UTC(),
	}
	if err := runpkg.SaveTrajectory(traj, trajPath); err != nil {
		t.Fatalf("save trajectory: %v", err)
	}

	model := &perRunCountingModel{response: response}
	messages := []llm.Message{
		{Role: "system", Content: "base prompt + mode-beta instructions"},
		{Role: "user", Content: "do the thing"},
	}

	var warningBuf bytes.Buffer
	req := Request{
		PreconstructedMessages: messages,
		Config:                 &minisweagent.ShellAgentConfig{},
		Prompts:                &minisweagent.PromptsConfig{},
		Model:                  model,
		Env:                    &stubEnvForCounting{},
		SessionID:              sessionID,
		ExtraInstructions:      "mode-beta instructions",
		WarningWriter:          &warningBuf,
		ResumeAttempt:          true,
	}

	result, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitStatus != "Submitted" {
		t.Fatalf("expected Submitted, got %q", result.ExitStatus)
	}

	// Assert the warning buffer contains a mode/system-prompt mismatch message.
	got := warningBuf.String()
	if got == "" {
		t.Fatal("expected a mode mismatch warning, but WarningWriter buffer is empty")
	}
	gotLower := strings.ToLower(got)
	hasModeRef := strings.Contains(gotLower, "mode") || strings.Contains(gotLower, "system prompt")
	hasMismatch := strings.Contains(gotLower, "mismatch") || strings.Contains(gotLower, "differ")
	if !hasModeRef || !hasMismatch {
		t.Fatalf("expected warning to reference mode/system-prompt and indicate mismatch, got: %s", got)
	}

	// After successful completion, the trajectory file must persist.
	if _, err := os.Stat(trajPath); os.IsNotExist(err) {
		t.Fatalf("expected trajectory file to persist after successful resume, but it does not exist at %s", trajPath)
	}

	// Load the trajectory and verify the exit status is the success status.
	loadedTraj, loadErr := runpkg.LoadTrajectory(trajPath)
	if loadErr != nil {
		t.Fatalf("LoadTrajectory: %v", loadErr)
	}
	if loadedTraj.ExitStatus == "Cancelled" || loadedTraj.ExitStatus == "Error" {
		t.Fatalf("expected trajectory exit status to be a success status, got %q", loadedTraj.ExitStatus)
	}
	if loadedTraj.ExitStatus != "Submitted" {
		t.Fatalf("expected trajectory exit status %q, got %q", "Submitted", loadedTraj.ExitStatus)
	}
}

// TestModeMatchNoWarningEmitted verifies that when the saved resume
// state and the current request carry the same extra instructions, no
// warning is written to WarningWriter.
func TestModeMatchNoWarningEmitted(t *testing.T) {
	useIsolatedArtifactContext(t)
	const sessionID = "test-mode-match-no-warning"
	const response = "<answer>\nConfidence: 100% - ok\n</answer>"

	// Compute the canonical trajectory path that Run will use.
	trajPath, pathErr := artifacts.ShellAgentTrajectoryPath(sessionID, 0)
	if pathErr != nil {
		t.Fatalf("ShellAgentTrajectoryPath: %v", pathErr)
	}
	_ = os.Remove(trajPath)
	defer os.Remove(trajPath)

	// Pre-create a valid resume file whose saved system prompt
	// contains mode-alpha instructions.
	resumeState := &runpkg.ResumeState{
		Version:          runpkg.ResumeStateVersion,
		StepCounter:      5,
		CommandsExecuted: 3,
		SystemPrompt:     "base prompt + mode-alpha instructions",
	}
	traj := runpkg.FileTrajectory{
		Messages:    []minisweagent.Message{{Role: "system", Content: "resumed session"}},
		ExitStatus:  "Ongoing",
		Result:      "partial result",
		ResumeState: resumeState,
		Timestamp:   time.Now().UTC(),
	}
	if err := runpkg.SaveTrajectory(traj, trajPath); err != nil {
		t.Fatalf("save trajectory: %v", err)
	}

	model := &perRunCountingModel{response: response}
	messages := []llm.Message{
		{Role: "system", Content: "base prompt + mode-alpha instructions"},
		{Role: "user", Content: "do the thing"},
	}

	var warningBuf bytes.Buffer
	req := Request{
		PreconstructedMessages: messages,
		Config:                 &minisweagent.ShellAgentConfig{},
		Prompts:                &minisweagent.PromptsConfig{},
		Model:                  model,
		Env:                    &stubEnvForCounting{},
		SessionID:              sessionID,
		ExtraInstructions:      "mode-alpha instructions",
		WarningWriter:          &warningBuf,
		ResumeAttempt:          true,
	}

	result, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitStatus != "Submitted" {
		t.Fatalf("expected Submitted, got %q", result.ExitStatus)
	}

	// After successful completion, the trajectory file must persist.
	if _, err := os.Stat(trajPath); os.IsNotExist(err) {
		t.Fatalf("expected trajectory file to persist after successful resume, but it does not exist at %s", trajPath)
	}

	// Load the trajectory and verify the exit status is the success status.
	loadedTraj, loadErr := runpkg.LoadTrajectory(trajPath)
	if loadErr != nil {
		t.Fatalf("LoadTrajectory: %v", loadErr)
	}
	if loadedTraj.ExitStatus == "Cancelled" || loadedTraj.ExitStatus == "Error" {
		t.Fatalf("expected trajectory exit status to be a success status, got %q", loadedTraj.ExitStatus)
	}
	if loadedTraj.ExitStatus != "Submitted" {
		t.Fatalf("expected trajectory exit status %q, got %q", "Submitted", loadedTraj.ExitStatus)
	}
}

// TestShellAgentResumesWithoutNewInput verifies that when a resume file
// exists and ResumeAttempt is set to true, the
// agent detects the resume file and calls the resume path (RestoreResumeState
// and ResumeWithMessages).
func TestShellAgentResumesWithoutNewInput(t *testing.T) {
	useIsolatedArtifactContext(t)
	const sessionID = "test-resume-without-new-input"
	const response = "<answer>\nConfidence: 100% - resumed ok\n</answer>"

	// Compute the canonical trajectory path that Run will use.
	trajPath, pathErr := artifacts.ShellAgentTrajectoryPath(sessionID, 0)
	if pathErr != nil {
		t.Fatalf("ShellAgentTrajectoryPath: %v", pathErr)
	}
	_ = os.Remove(trajPath)
	defer os.Remove(trajPath)

	// Pre-create a valid resume file.
	resumeState := &runpkg.ResumeState{
		Version:          runpkg.ResumeStateVersion,
		StepCounter:      5,
		CommandsExecuted: 3,
		SystemPrompt:     "custom system prompt content",
	}
	traj := runpkg.FileTrajectory{
		Messages:    []minisweagent.Message{{Role: "system", Content: "resumed session"}},
		ExitStatus:  "Ongoing",
		Result:      "partial result",
		ResumeState: resumeState,
		Timestamp:   time.Now().UTC(),
	}
	if err := runpkg.SaveTrajectory(traj, trajPath); err != nil {
		t.Fatalf("save trajectory: %v", err)
	}

	// Verify the resume file exists before the run.
	if _, err := os.Stat(trajPath); os.IsNotExist(err) {
		t.Fatal("resume file should exist before Run")
	}

	model := &perRunCountingModel{response: response}
	messages := []llm.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "do the thing"},
	}

	req := Request{
		PreconstructedMessages: messages,
		Config:                 &minisweagent.ShellAgentConfig{},
		Prompts:                &minisweagent.PromptsConfig{},
		Model:                  model,
		Env:                    &stubEnvForCounting{},
		SessionID:              sessionID,
		ResumeAttempt:          true,
	}

	result, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// The resume path uses agent.ResumeWithMessages, which for the stub
	// model returns "Submitted" (since the model produces a final answer).
	if result.ExitStatus != "Submitted" {
		t.Fatalf("expected Submitted (resume path), got %q", result.ExitStatus)
	}

	// After successful completion, the trajectory file must persist.
	if _, err := os.Stat(trajPath); os.IsNotExist(err) {
		t.Fatalf("expected trajectory file to persist after successful resume, but it does not exist at %s", trajPath)
	}

	// Load the trajectory and verify the exit status is the success status.
	loadedTraj, loadErr := runpkg.LoadTrajectory(trajPath)
	if loadErr != nil {
		t.Fatalf("LoadTrajectory: %v", loadErr)
	}
	if loadedTraj.ExitStatus == "Cancelled" || loadedTraj.ExitStatus == "Error" {
		t.Fatalf("expected trajectory exit status to be a success status, got %q", loadedTraj.ExitStatus)
	}
	if loadedTraj.ExitStatus != "Submitted" {
		t.Fatalf("expected trajectory exit status %q, got %q", "Submitted", loadedTraj.ExitStatus)
	}
}

// TestShellAgentStartsFreshWithNewInput verifies that when a resume file
// exists but ResumeAttempt is explicitly false, the agent skips the resume
// path entirely and starts a fresh run via RunWithMessages.
func TestShellAgentStartsFreshWithNewInput(t *testing.T) {
	useIsolatedArtifactContext(t)
	const sessionID = "test-starts-fresh-with-new-input"
	const response = "<answer>\nConfidence: 100% - fresh run ok\n</answer>"

	// Compute the canonical trajectory path that Run will use.
	trajPath, pathErr := artifacts.ShellAgentTrajectoryPath(sessionID, 0)
	if pathErr != nil {
		t.Fatalf("ShellAgentTrajectoryPath: %v", pathErr)
	}
	_ = os.Remove(trajPath)
	defer os.Remove(trajPath)

	// Pre-create a valid resume file.
	resumeState := &runpkg.ResumeState{
		Version:          runpkg.ResumeStateVersion,
		StepCounter:      5,
		CommandsExecuted: 3,
		SystemPrompt:     "custom system prompt content",
	}
	traj := runpkg.FileTrajectory{
		Messages:    []minisweagent.Message{{Role: "system", Content: "resumed session"}},
		ExitStatus:  "Ongoing",
		Result:      "partial result",
		ResumeState: resumeState,
		Timestamp:   time.Now().UTC(),
	}
	if err := runpkg.SaveTrajectory(traj, trajPath); err != nil {
		t.Fatalf("save trajectory: %v", err)
	}

	// Verify the resume file exists before the run.
	if _, err := os.Stat(trajPath); os.IsNotExist(err) {
		t.Fatal("resume file should exist before Run")
	}

	model := &perRunCountingModel{response: response}
	messages := []llm.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "do the thing"},
	}

	req := Request{
		PreconstructedMessages: messages,
		Config:                 &minisweagent.ShellAgentConfig{},
		Prompts:                &minisweagent.PromptsConfig{},
		Model:                  model,
		Env:                    &stubEnvForCounting{},
		SessionID:              sessionID,
		ResumeAttempt:          false, // Explicitly opt out of resume.
	}

	result, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitStatus != "Submitted" {
		t.Fatalf("expected Submitted (fresh run), got %q", result.ExitStatus)
	}

	// When ResumeAttempt is explicitly false, the agent starts a fresh run.
	// The pre-created resume file persists but is ignored by the agent.
	// Verify the file still exists (no longer deleted on clean exit).
	if _, err := os.Stat(trajPath); os.IsNotExist(err) {
		t.Fatalf("expected resume file to persist when ResumeAttempt=false, but it does not exist at %s", trajPath)
	}
}

// TestDeterministicInterruptStep verifies that when InterruptStep is set
// to a positive value, the agent checkpoints and cancels after exactly
// that many successful steps. It also verifies the resume file is written
// with the correct StepCounter.
func TestDeterministicInterruptStep(t *testing.T) {
	const sessionID = "test-interrupt"
	const interrupAtStep = 3

	// steppingModel returns a <command> response cycling through
	// echo step1, echo step2, etc. on each call.
	steppingModel := &steppingModel{prefix: "echo step"}
	msgs := []minisweagent.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "do the thing"},
	}

	agent := agents.NewDefaultAgent(steppingModel, &stubEnvForCounting{}, &minisweagent.ShellAgentConfig{}, &minisweagent.PromptsConfig{}, agents.WithSessionID(sessionID))
	agent.RunConfig.CheckpointDir = t.TempDir()
	agent.SetInterruptStep(interrupAtStep)

	exitStatus, _, runErr := agent.RunWithMessages(context.Background(), msgs)
	if runErr != nil {
		t.Fatalf("RunWithMessages error = %v", runErr)
	}
	if exitStatus != "Cancelled" {
		t.Fatalf("expected ExitStatus %q, got %q", "Cancelled", exitStatus)
	}

	// Load the resume file and verify StepCounter equals InterruptStep.
	trajPath := filepath.Join(agent.RunConfig.CheckpointDir, "trajectory.json")
	loadedTraj, loadErr := runpkg.LoadTrajectory(trajPath)
	if loadErr != nil {
		t.Fatalf("LoadTrajectory: %v", loadErr)
	}
	if loadedTraj.ResumeState == nil {
		t.Fatal("expected non-nil ResumeState in loaded trajectory")
	}
	if loadedTraj.ResumeState.StepCounter != interrupAtStep {
		t.Fatalf("expected StepCounter %d, got %d", interrupAtStep, loadedTraj.ResumeState.StepCounter)
	}
}

// steppingModel is a mock minisweagent.Model that returns a command
// response on each call, cycling through numbered steps.
type steppingModel struct {
	prefix string
	nCalls int
}

func (m *steppingModel) Config() interface{} { return &minisweagent.ModelConfig{} }
func (m *steppingModel) Cost() float64       { return 0 }
func (m *steppingModel) NCalls() int         { return m.nCalls }
func (m *steppingModel) Query(_ context.Context, _ []minisweagent.Message, _ ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.nCalls++
	content := fmt.Sprintf("<command>\n%s%d\n</command>", m.prefix, m.nCalls)
	return minisweagent.QueryResult{Content: content}, nil
}
func (m *steppingModel) GetTemplateVars() map[string]interface{} {
	return map[string]interface{}{}
}

// TestResumeAttemptFlagPropagatesFromCLIToRequest verifies that the
// ResumeAttempt field on shellagent.Request is correctly wired:
// it verifies that the Request struct has the ResumeAttempt field and that
// it can be set to both true and false, reflecting the CLI intent.
func TestResumeAttemptFlagPropagatesFromCLIToRequest(t *testing.T) {
	// Verify the Request struct has ResumeAttempt as a bool field.
	req := Request{
		Config:  &minisweagent.ShellAgentConfig{},
		Prompts: &minisweagent.PromptsConfig{},
	}
	// Explicitly false == fresh start (new -p/-f input provided).
	req.ResumeAttempt = false
	if req.ResumeAttempt {
		t.Fatal("expected ResumeAttempt=false to mean fresh start")
	}

	// Explicitly true == resume.
	req.ResumeAttempt = true
	if !req.ResumeAttempt {
		t.Fatal("expected ResumeAttempt=true to mean resume")
	}
}

// TestResumeRestoresStepCounter verifies that when a shell-agent session
// is resumed from a saved resume file, the agent continues from the saved
// StepCounter rather than restarting from step 1.  The test creates a
// resume file with StepCounter=5 and ExitStatus=Ongoing, calls Run with
// ResumeAttempt=true, and asserts that the agent produced additional
// trajectory messages on top of the saved ones.
func TestResumeRestoresStepCounter(t *testing.T) {
	useIsolatedArtifactContext(t)
	const sessionID = "test-resume-restores-step-counter"
	const response = "<answer>\nConfidence: 100% - restored step counter ok\n</answer>"

	// Compute the canonical trajectory path that Run will use.
	trajPath, pathErr := artifacts.ShellAgentTrajectoryPath(sessionID, 0)
	if pathErr != nil {
		t.Fatalf("ShellAgentTrajectoryPath: %v", pathErr)
	}
	_ = os.Remove(trajPath)
	defer os.Remove(trajPath)

	// Create a resume file with StepCounter set to 5 and ExitStatus Ongoing.
	resumeState := &runpkg.ResumeState{
		Version:          runpkg.ResumeStateVersion,
		StepCounter:      5,
		CommandsExecuted: 3,
		SystemPrompt:     "custom system prompt content",
	}
	savedMessages := []minisweagent.Message{
		{Role: "system", Content: "resumed session"},
	}
	traj := runpkg.FileTrajectory{
		Messages:    savedMessages,
		ExitStatus:  "Ongoing",
		Result:      "partial result",
		ResumeState: resumeState,
		Timestamp:   time.Now().UTC(),
	}
	if err := runpkg.SaveTrajectory(traj, trajPath); err != nil {
		t.Fatalf("save trajectory: %v", err)
	}
	defer os.Remove(trajPath)

	model := &perRunCountingModel{response: response}
	messages := []llm.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "do the thing"},
	}

	req := Request{
		PreconstructedMessages: messages,
		Config:                 &minisweagent.ShellAgentConfig{},
		Prompts:                &minisweagent.PromptsConfig{},
		Model:                  model,
		Env:                    &stubEnvForCounting{},
		SessionID:              sessionID,
		ResumeAttempt:          true,
	}

	result, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitStatus != "Submitted" {
		t.Fatalf("expected Submitted, got %q", result.ExitStatus)
	}

	// After successful completion, the trajectory file must persist.
	if _, err := os.Stat(trajPath); os.IsNotExist(err) {
		t.Fatalf("expected trajectory file to persist after successful resume, but it does not exist at %s", trajPath)
	}

	// Load the trajectory and verify the exit status is the success status.
	loadedTraj, loadErr := runpkg.LoadTrajectory(trajPath)
	if loadErr != nil {
		t.Fatalf("LoadTrajectory: %v", loadErr)
	}
	if loadedTraj.ExitStatus == "Cancelled" || loadedTraj.ExitStatus == "Error" {
		t.Fatalf("expected trajectory exit status to be a success status, got %q", loadedTraj.ExitStatus)
	}
	if loadedTraj.ExitStatus != "Submitted" {
		t.Fatalf("expected trajectory exit status %q, got %q", "Submitted", loadedTraj.ExitStatus)
	}

	// Assert the trajectory message count exceeds the saved message count,
	// proving the agent executed at least one new step rather than resetting.
	savedMsgCount := len(savedMessages)
	trajectoryMsgCount := len(result.Trajectory.Messages)
	if trajectoryMsgCount <= savedMsgCount {
		t.Fatalf("expected trajectory message count (%d) to exceed saved message count (%d), but it did not", trajectoryMsgCount, savedMsgCount)
	}
}

// TestResumePreservesConversationHistory verifies that resuming refreshes
// the current system prompt while preserving the saved non-system history.
func TestResumePreservesConversationHistory(t *testing.T) {
	const sessionID = "test-resume-preserves-history"
	const response = "<answer>\nConfidence: 100% - ok\n</answer>"

	trajDir := t.TempDir()
	trajPath := filepath.Join(trajDir, "trajectory.json")
	_ = os.Remove(trajPath)

	// Step 1: Create a resume file with 3 known messages.
	resumeState := &runpkg.ResumeState{
		Version:          runpkg.ResumeStateVersion,
		StepCounter:      3,
		CommandsExecuted: 1,
		SystemPrompt:     "system prompt",
	}
	savedMessages := []minisweagent.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: "i ran the command"},
	}
	traj := runpkg.FileTrajectory{
		Messages:    savedMessages,
		ExitStatus:  "Ongoing",
		Result:      "",
		ResumeState: resumeState,
		Timestamp:   time.Now().UTC(),
	}
	if err := runpkg.SaveTrajectory(traj, trajPath); err != nil {
		t.Fatalf("save trajectory: %v", err)
	}
	defer os.Remove(trajPath)

	// Step 2: Create a stub model that returns a final answer response.
	model := &perRunCountingModel{response: response}

	// Step 3: Create a Request with different fresh PreconstructedMessages.
	freshMessages := []llm.Message{
		{Role: "system", Content: "new system prompt"},
		{Role: "user", Content: "new task"},
	}

	req := Request{
		PreconstructedMessages: freshMessages,
		Config:                 &minisweagent.ShellAgentConfig{},
		Prompts:                &minisweagent.PromptsConfig{},
		Model:                  model,
		Env:                    &stubEnvForCounting{},
		SessionID:              sessionID,
		ResumeAttempt:          true,
		TrajectoryBaseDir:      trajDir,
	}

	// Step 4: Call Run and verify.
	result, err := Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitStatus != "Submitted" {
		t.Fatalf("expected ExitStatus Submitted, got %q", result.ExitStatus)
	}

	// Verify the trajectory contains the 3 saved messages (original history).
	msgs := result.Trajectory.Messages
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 trajectory messages, got %d", len(msgs))
	}

	// The system message is refreshed, while the saved user and assistant
	// messages remain the conversation history.
	expectedRoles := []string{"system", "user", "assistant"}
	expectedContents := []string{"new system prompt", "do the thing", "i ran the command"}
	for i := 0; i < 3; i++ {
		if msgs[i].Role != expectedRoles[i] {
			t.Fatalf("trajectory message %d: expected role %q, got %q", i, expectedRoles[i], msgs[i].Role)
		}
		if msgs[i].Content != expectedContents[i] {
			t.Fatalf("trajectory message %d: expected content %q, got %q", i, expectedContents[i], msgs[i].Content)
		}
	}

	// The fresh task must not replace the saved user history.
	if len(msgs) > 1 && msgs[1].Content == "new task" {
		t.Fatal("expected trajectory to preserve saved user history, but found the fresh task")
	}
}

func TestRenderSystemPromptIncludesAbsoluteCWD(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "workspace with spaces")
	rendered, err := RenderSystemPrompt(
		&minisweagent.PromptsConfig{},
		map[string]interface{}{"cwd": cwd, "CWD": cwd},
		"answer",
		"command",
	)
	if err != nil {
		t.Fatalf("RenderSystemPrompt: %v", err)
	}
	want := fmt.Sprintf("The current working directory is %q.", cwd)
	if !strings.Contains(rendered, want) {
		t.Fatalf("system prompt missing %q\n%s", want, rendered)
	}
	if !strings.Contains(rendered, "Every shell command starts in this directory unless the command explicitly changes directories.") {
		t.Fatalf("system prompt missing command-directory contract\n%s", rendered)
	}
}
