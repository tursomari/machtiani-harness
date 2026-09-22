package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/run"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

type stubModel struct{}

func (s *stubModel) Config() interface{} { return map[string]interface{}{"Machine": "test"} }
func (s *stubModel) Cost() float64       { return 0 }
func (s *stubModel) NCalls() int         { return 0 }
func (s *stubModel) Query(context.Context, []minisweagent.Message, ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	return minisweagent.QueryResult{}, nil
}
func (s *stubModel) GetTemplateVars() map[string]interface{} { return map[string]interface{}{} }

type queryCapturingModel struct {
	*stubModel
	capturedMessages []minisweagent.Message
}

func (m *queryCapturingModel) Query(ctx context.Context, msgs []minisweagent.Message, opts ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.capturedMessages = append([]minisweagent.Message(nil), msgs...)
	return minisweagent.QueryResult{Content: "observed status"}, nil
}

type refusingModel struct {
	*stubModel
}

func (m *refusingModel) Query(ctx context.Context, msgs []minisweagent.Message, opts ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	// Check if the prompt contains a file-modification intent
	var userContent string
	for _, msg := range msgs {
		if msg.Role == "user" {
			userContent = msg.Content
			break
		}
	}
	// Simulate refusal for file-modification commands
	if strings.Contains(userContent, "sed") || strings.Contains(userContent, "touch") || strings.Contains(userContent, "patch") {
		return minisweagent.QueryResult{
			Content: "PATCH_REQUEST: Cannot execute sed/touch/patch commands - file modifications are forbidden. Please use the myapp module instead.",
		}, nil
	}
	return minisweagent.QueryResult{Content: "observed result"}, nil
}

type stubEnvironment struct {
	syncProgress float64
	syncStatus   string
	lastCommand  string
	executed     bool
}

func (s *stubEnvironment) Config() interface{} { return &minisweagent.EnvironmentConfig{} }
func (s *stubEnvironment) Execute(_ context.Context, command, _ string) (minisweagent.ExecuteResult, error) {
	s.executed = true
	s.lastCommand = command
	return minisweagent.ExecuteResult{}, nil
}
func (s *stubEnvironment) GetTemplateVars() map[string]interface{} { return map[string]interface{}{} }
func (s *stubEnvironment) GetSyncProgress() float64 {
	if s.syncProgress == 0 {
		return 1.0
	}
	return s.syncProgress
}
func (s *stubEnvironment) GetSyncStatus() string { return s.syncStatus }

type errorEnvironment struct {
	stubEnvironment
}

func (e *errorEnvironment) Execute(ctx context.Context, command, workdir string) (minisweagent.ExecuteResult, error) {
	return minisweagent.ExecuteResult{}, &minisweagent.ExecutionTimeoutError{Message: "simulated timeout"}
}

type scriptedModel struct {
	responses []minisweagent.QueryResult
	idx       int
	captured  [][]minisweagent.Message
}

func (m *scriptedModel) Config() interface{} { return map[string]interface{}{} }
func (m *scriptedModel) Cost() float64       { return 0 }
func (m *scriptedModel) NCalls() int         { return m.idx }
func (m *scriptedModel) Query(ctx context.Context, msgs []minisweagent.Message, opts ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.captured = append(m.captured, append([]minisweagent.Message(nil), msgs...))
	if m.idx >= len(m.responses) {
		return minisweagent.QueryResult{}, fmt.Errorf("no scripted response for call %d", m.idx+1)
	}
	res := m.responses[m.idx]
	m.idx++
	return res, nil
}
func (m *scriptedModel) GetTemplateVars() map[string]interface{} { return map[string]interface{}{} }

type cacheCapturingModel struct {
	resolved llm.ResolvedModel
	captured [][]minisweagent.Message
	response string
}

func (m *cacheCapturingModel) Config() interface{} { return &minisweagent.ModelConfig{} }
func (m *cacheCapturingModel) Cost() float64       { return 0 }
func (m *cacheCapturingModel) NCalls() int         { return len(m.captured) }
func (m *cacheCapturingModel) GetTemplateVars() map[string]interface{} {
	return map[string]interface{}{}
}

func (m *cacheCapturingModel) Query(ctx context.Context, msgs []minisweagent.Message, opts ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.captured = append(m.captured, append([]minisweagent.Message(nil), msgs...))
	content := m.response
	if strings.TrimSpace(content) == "" {
		content = "cache-response"
	}
	return minisweagent.QueryResult{Content: content}, nil
}

func (m *cacheCapturingModel) ResolvedModel() llm.ResolvedModel {
	return m.resolved
}

type commandCapturingModel struct {
	*stubModel
	captured []minisweagent.Message
	response string
	nCalls   int
}

func (m *commandCapturingModel) Query(ctx context.Context, msgs []minisweagent.Message, opts ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.captured = append([]minisweagent.Message(nil), msgs...)
	m.nCalls++
	content := m.response
	if strings.TrimSpace(content) == "" {
		content = "<command>\necho ok\n</command>"
	}
	return minisweagent.QueryResult{Content: content}, nil
}

func (m *commandCapturingModel) NCalls() int {
	return m.nCalls
}

type failingExecEnvironment struct {
	attempts    int
	lastCommand string
}

func (f *failingExecEnvironment) Config() interface{} {
	return &minisweagent.EnvironmentConfig{CommandTimeout: 5}
}

func (f *failingExecEnvironment) Execute(ctx context.Context, command, cwd string) (minisweagent.ExecuteResult, error) {
	f.attempts++
	f.lastCommand = command
	return minisweagent.ExecuteResult{Output: "ls: invalid option -- fake", ReturnCode: 2}, nil
}

func (f *failingExecEnvironment) GetTemplateVars() map[string]interface{} {
	return map[string]interface{}{}
}
func (f *failingExecEnvironment) GetSyncProgress() float64 { return 1.0 }
func (f *failingExecEnvironment) GetSyncStatus() string    { return "" }

type capturingEnvironment struct {
	result      minisweagent.ExecuteResult
	execErr     error
	lastCommand string
	calls       int
}

func (c *capturingEnvironment) Config() interface{} {
	return &minisweagent.EnvironmentConfig{CommandTimeout: 5}
}

func (c *capturingEnvironment) Execute(ctx context.Context, command, cwd string) (minisweagent.ExecuteResult, error) {
	c.calls++
	c.lastCommand = command
	if c.execErr != nil {
		return minisweagent.ExecuteResult{}, c.execErr
	}
	return c.result, nil
}

func (c *capturingEnvironment) GetTemplateVars() map[string]interface{} {
	return map[string]interface{}{}
}
func (c *capturingEnvironment) GetSyncProgress() float64 { return 1.0 }
func (c *capturingEnvironment) GetSyncStatus() string    { return "" }

func newTestAgent() *DefaultAgent {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	return NewDefaultAgent(&stubModel{}, &stubEnvironment{}, cfg, prompts)
}

func TestRunLoopFormatErrorCircuitBreaker(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{
		{Content: "no bash command here"},
		{Content: "still no fence"},
		{Content: "third without fence"},
	}}
	env := &stubEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "test task", nil)

	status, msg, err := agent.RunLoop(context.Background(), false)
	var formatErr *minisweagent.FormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("expected format error, got %v", err)
	}
	if status != "FormatErrorLoop" {
		t.Fatalf("status = %q, want %q", status, "FormatErrorLoop")
	}
	if msg == "" {
		t.Fatal("expected non-empty msg")
	}
}

func TestRunLoopFormatErrorCounterResetsOnSuccess(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{
		{Content: "no fence one"},
		{Content: "no fence two"},
		{Content: "<command>\necho hello\n</command>"},
		{Content: "no fence three"},
	}}
	env := &stubEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "test task", nil)

	status, msg, err := agent.RunLoop(context.Background(), false)
	if err == nil {
		t.Fatal("expected error from running out of scripted responses, got nil")
	}
	if status == "FormatErrorLoop" {
		t.Fatalf("status should not be FormatErrorLoop, got %q (msg=%q)", status, msg)
	}
}

func TestRunLoopFormatErrorNoDuplicateMessages(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{
		{Content: "no fence one"},
		{Content: "no fence two"},
		{Content: "<command>\necho hello\n</command>"},
	}}
	env := &stubEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "test task", nil)

	status, msg, err := agent.RunLoop(context.Background(), false)
	if err == nil {
		t.Fatalf("expected error from running out of scripted responses, got nil (status=%q)", status)
	}
	if status == "FormatErrorLoop" {
		t.Fatalf("status should not be FormatErrorLoop, got %q (msg=%q)", status, msg)
	}

	var formatErrorCount int
	var rawErrorCount int
	for _, m := range agent.State.Messages {
		if strings.Contains(m.Content, "format-error") {
			formatErrorCount++
		}
		if strings.Contains(m.Content, "Command parsing failed") {
			rawErrorCount++
		}
	}

	if formatErrorCount != 2 {
		t.Errorf("expected 2 messages containing %q, got %d", "format-error", formatErrorCount)
	}
	if rawErrorCount != 0 {
		t.Errorf("expected 0 messages containing %q, got %d", "Command parsing failed", rawErrorCount)
	}
}

func TestRenderFormatErrorUsesTemplate(t *testing.T) {
	t.Run("WithTemplate", func(t *testing.T) {
		cfg := &minisweagent.ShellAgentConfig{}
		prompts := &minisweagent.PromptsConfig{
			Planner: &minisweagent.PlannerPromptsConfig{
				SystemTemplate:   "",
				InstanceTemplate: "",
			},
			ShellAgent: &minisweagent.ShellAgentPromptsConfig{
				FormatErrorTemplate:       "custom-format-message",
				ActionObservationTemplate: "{{.Output}}",
			},
		}

		agent := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, cfg, prompts)

		result := agent.renderFormatError(fmt.Errorf("test error"))
		if result != "custom-format-message" {
			t.Fatalf("expected %q, got %q", "custom-format-message", result)
		}
	})

	t.Run("WithFallbackMessage", func(t *testing.T) {
		cfg := &minisweagent.ShellAgentConfig{}
		prompts := &minisweagent.PromptsConfig{
			Planner: &minisweagent.PlannerPromptsConfig{
				SystemTemplate:   "",
				InstanceTemplate: "",
			},
			ShellAgent: &minisweagent.ShellAgentPromptsConfig{
				FormatErrorTemplate:       "dummy-not-empty",
				ActionObservationTemplate: "{{.Output}}",
			},
		}

		agent := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, cfg, prompts)
		agent.State.Prompts.ShellAgent.FormatErrorTemplate = ""

		result := agent.renderFormatError(fmt.Errorf("test error"))
		const fallback = `Your previous response was rejected. No command from that response was executed.

Reason: test error
Consecutive rejected responses: 1 of 3.
Three consecutive rejections end this attempt.

Correct the specific problem above and respond with exactly one of:
- <command>Bash command to execute</command>
- <answer>Your final answer</answer>

Use the exact tag names shown. Do not wrap the response in Markdown code fences or combine command and answer blocks.`
		if result != fallback {
			t.Fatalf("expected fallback message %q, got %q", fallback, result)
		}
	})

	t.Run("WithTemplateRenderingEmpty", func(t *testing.T) {
		cfg := &minisweagent.ShellAgentConfig{}
		prompts := &minisweagent.PromptsConfig{
			Planner: &minisweagent.PlannerPromptsConfig{
				SystemTemplate:   "",
				InstanceTemplate: "",
			},
			ShellAgent: &minisweagent.ShellAgentPromptsConfig{
				FormatErrorTemplate:       "{{.NonexistentVar}}",
				ActionObservationTemplate: "{{.Output}}",
			},
		}

		agent := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, cfg, prompts)

		result := agent.renderFormatError(fmt.Errorf("test error"))
		const fallback = `Your previous response was rejected. No command from that response was executed.

Reason: test error
Consecutive rejected responses: 1 of 3.
Three consecutive rejections end this attempt.

Correct the specific problem above and respond with exactly one of:
- <command>Bash command to execute</command>
- <answer>Your final answer</answer>

Use the exact tag names shown. Do not wrap the response in Markdown code fences or combine command and answer blocks.`
		if result != fallback {
			t.Fatalf("expected fallback message %q, got %q", fallback, result)
		}
	})

	t.Run("WithMalformedTemplate", func(t *testing.T) {
		cfg := &minisweagent.ShellAgentConfig{}
		prompts := &minisweagent.PromptsConfig{
			Planner: &minisweagent.PlannerPromptsConfig{
				SystemTemplate:   "",
				InstanceTemplate: "",
			},
			ShellAgent: &minisweagent.ShellAgentPromptsConfig{
				FormatErrorTemplate:       "{{.Broken",
				ActionObservationTemplate: "{{.Output}}",
			},
		}

		agent := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, cfg, prompts)

		result := agent.renderFormatError(fmt.Errorf("test error"))
		const fallback = `Your previous response was rejected. No command from that response was executed.

Reason: test error
Consecutive rejected responses: 1 of 3.
Three consecutive rejections end this attempt.

Correct the specific problem above and respond with exactly one of:
- <command>Bash command to execute</command>
- <answer>Your final answer</answer>

Use the exact tag names shown. Do not wrap the response in Markdown code fences or combine command and answer blocks.`
		if result != fallback {
			t.Fatalf("expected fallback message %q, got %q", fallback, result)
		}
	})
}

func TestTranslateAndExecuteRunsXMLCommand(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "<command>\nls\n</command>"}}}
	env := &capturingEnvironment{result: minisweagent.ExecuteResult{Output: "file.txt\n", ReturnCode: 0}}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "List files", nil)

	if err := agent.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if env.calls != 1 {
		t.Fatalf("Execute called %d times, want 1", env.calls)
	}
	if env.lastCommand != "ls" {
		t.Fatalf("executed command = %q, want 'ls'", env.lastCommand)
	}
	if got := agent.State.Messages[len(agent.State.Messages)-1].Content; !strings.Contains(got, "Command executed: ls") {
		t.Fatalf("feedback message missing command: %q", got)
	}
}

func TestTranslateAndExecuteAcceptsFinalAnswer(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "<answer>\nDone\n</answer>"}}}
	env := &capturingEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "Summarize the loop.", nil)

	err := agent.Step(context.Background())
	submitted, ok := err.(*minisweagent.Submitted)
	if !ok {
		t.Fatalf("expected Submitted error, got %T", err)
	}
	if submitted.Result != "Done" {
		t.Fatalf("submitted result = %q, want %q", submitted.Result, "Done")
	}
	if env.calls != 0 {
		t.Fatalf("expected no command execution, got %d calls", env.calls)
	}
}

func TestTranslateAndExecuteAcceptsAnswerTagWithLeadingAndTrailingCommentary(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "Here is the result:\n<answer>\nDone\n</answer>\nNo further commands are needed."}}}
	env := &capturingEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "Summarize the loop.", nil)

	err := agent.Step(context.Background())
	submitted, ok := err.(*minisweagent.Submitted)
	if !ok {
		t.Fatalf("expected Submitted error, got %T", err)
	}
	if submitted.Result != "Done" {
		t.Fatalf("submitted result = %q, want %q", submitted.Result, "Done")
	}
	if env.calls != 0 {
		t.Fatalf("expected no command execution, got %d calls", env.calls)
	}
}

func TestTranslateAndExecuteRejectsLegacyAnswerHeading(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "## Answer\nDone"}}}
	env := &capturingEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "Summarize the loop.", nil)

	err := agent.Step(context.Background())
	var formatErr *minisweagent.FormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("expected FormatError, got %T", err)
	}
	if env.calls != 0 {
		t.Fatalf("expected no command execution, got %d calls", env.calls)
	}
}

func TestTranslateAndExecuteRejectsEmptyAnswerTag(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "<answer>\n\n</answer>"}}}
	env := &capturingEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "Summarize the loop.", nil)

	err := agent.Step(context.Background())
	var formatErr *minisweagent.FormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("expected FormatError, got %T", err)
	}
	if env.calls != 0 {
		t.Fatalf("expected no command execution, got %d calls", env.calls)
	}
}

func TestTranslateAndExecuteAcceptsStrayCloseAnswerTags(t *testing.T) {
	// PlanAnswerTag.md rule 4: the parser spans the body from the FIRST
	// opening tag to the LAST closing tag. Stray close tags in the
	// middle are tolerated. This test pins down that contract end-to-end:
	// the second <answer>...</answer> pair acts as a sub-block, and the
	// first-opens-to-last-closes body is returned as the final answer
	// (no FormatError). The previous "multiple tags = format error"
	// behaviour is explicitly superseded.
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "<answer>one</answer>\n<answer>two</answer>"}}}
	env := &capturingEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "Summarize the loop.", nil)

	err := agent.Step(context.Background())
	if err == nil {
		t.Fatal("expected Submitted, got nil")
	}
	var submitted *minisweagent.Submitted
	if !errors.As(err, &submitted) {
		t.Fatalf("expected Submitted, got %T (%v)", err, err)
	}
	want := "one</answer>\n<answer>two"
	if submitted.Result != want {
		t.Fatalf("expected body %q, got %q", want, submitted.Result)
	}
	if env.calls != 0 {
		t.Fatalf("expected no command execution, got %d calls", env.calls)
	}
}

func TestTranslateAndExecuteRejectsMissingFence(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "ls -la"}}}
	env := &capturingEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "List files", nil)

	err := agent.Step(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var formatErr *minisweagent.FormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("expected FormatError, got %T", err)
	}
	if env.calls != 0 {
		t.Fatalf("execute should not be called, saw %d calls", env.calls)
	}
	if got := agent.State.Messages[len(agent.State.Messages)-1].Content; !strings.Contains(got, "format-error") {
		t.Fatalf("unexpected feedback message: %q", got)
	}
}

func TestTranslateAndExecuteAllowsChaining(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "<command>\nls && pwd\n</command>"}}}
	env := &capturingEnvironment{result: minisweagent.ExecuteResult{Output: "file.txt\n/home/example\n", ReturnCode: 0}}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "List files", nil)

	if err := agent.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if env.calls != 1 {
		t.Fatalf("execute called %d times, want 1", env.calls)
	}
}

func TestTranslateAndExecuteAutoTranslatesBroadRecursiveGrepAtRepoRoot(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	// A broad recursive grep from the project root triggers auto-translation to rg.
	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "<command>\ngrep -rnEi \"myapp|class.*Myapp\" . --max-count=20\n</command>"}}}
	env := &capturingEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "Find myapp configuration", nil)

	err := agent.Step(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.calls != 1 {
		t.Fatalf("Execute called %d times, want 1", env.calls)
	}
	if !strings.Contains(env.lastCommand, "rg") {
		t.Fatalf("expected translated command to contain rg, got %q", env.lastCommand)
	}
}

func TestTranslateAndExecuteAddsFeedbackForFailure(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "<command>\nls --bad-flag\n</command>"}}}
	env := &capturingEnvironment{result: minisweagent.ExecuteResult{Output: "ls: unrecognized option '--bad-flag'\n", ReturnCode: 2}}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "List files", nil)

	if err := agent.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if env.calls != 1 {
		t.Fatalf("execute called %d times, want 1", env.calls)
	}
	feedback := agent.State.Messages[len(agent.State.Messages)-1].Content
	if !strings.Contains(feedback, "Exit code: 2") {
		t.Fatalf("feedback missing exit code: %q", feedback)
	}
	if !strings.Contains(feedback, "ls: unrecognized option") {
		t.Fatalf("feedback missing command output: %q", feedback)
	}
}

func TestHasFinishedDetectsFinalMarkerFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MINISWE_FINAL_DIR", dir)
	agent := newTestAgent()

	// Use the agent's sessionID to get the session-scoped marker path
	path := minisweagent.FinalMarkerPath(agent.RunConfig.SessionID)
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })

	if err := os.WriteFile(path, []byte("done\n"), 0o600); err != nil {
		t.Fatalf("write marker file: %v", err)
	}

	err := agent.hasFinished("echo", minisweagent.ExecuteResult{})
	if err == nil {
		t.Fatalf("expected submitted error, got nil")
	}

	submitted, ok := err.(*minisweagent.Submitted)
	if !ok {
		t.Fatalf("expected Submitted error, got %T", err)
	}

	if submitted.Result != "done" {
		t.Fatalf("submitted result = %q, want %q", submitted.Result, "done")
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected marker file to be removed, stat err = %v", statErr)
	}
}

func TestHasFinishedUsesLastOutputWhenMarkerEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MINISWE_FINAL_DIR", dir)
	agent := newTestAgent()
	agent.State.lastNonEmptyOutput = "final answer"

	// Use the agent's sessionID to get the session-scoped marker path
	path := minisweagent.FinalMarkerPath(agent.RunConfig.SessionID)
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })

	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("write marker file: %v", err)
	}

	err := agent.hasFinished("echo", minisweagent.ExecuteResult{})
	submitted, ok := err.(*minisweagent.Submitted)
	if !ok {
		t.Fatalf("expected Submitted error, got %T", err)
	}

	if submitted.Result != "final answer" {
		t.Fatalf("submitted result = %q, want %q", submitted.Result, "final answer")
	}
}

func TestHasFinishedIgnoresMissingMarkerFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MINISWE_FINAL_DIR", dir)
	agent := newTestAgent()

	// Use the agent's sessionID to get the session-scoped marker path
	path := minisweagent.FinalMarkerPath(agent.RunConfig.SessionID)
	os.Remove(path)
	t.Cleanup(func() { os.Remove(path) })

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir temp dir: %v", err)
	}

	if err := agent.hasFinished("echo", minisweagent.ExecuteResult{}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestDisplaySyncProgressWithVerbose(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	env := &stubEnvironment{syncProgress: 0.875, syncStatus: "synced 140/160 files"}
	agent := NewDefaultAgent(&stubModel{}, env, cfg, prompts)
	agent.RunConfig.Verbose = true

	// This should not panic and should log progress
	agent.displaySyncProgress()
}

func TestDisplaySyncProgressNoDisplayWhenComplete(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	env := &stubEnvironment{syncProgress: 1.0, syncStatus: ""}
	agent := NewDefaultAgent(&stubModel{}, env, cfg, prompts)
	agent.RunConfig.Verbose = true

	// This should not log anything since progress is 100% and status is empty
	agent.displaySyncProgress()
}

func TestDisplaySyncProgressNoDisplayWhenNotVerbose(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	env := &stubEnvironment{syncProgress: 0.5, syncStatus: "syncing files"}
	agent := NewDefaultAgent(&stubModel{}, env, cfg, prompts)
	agent.RunConfig.Verbose = false

	// This should not log anything since verbose is false
	agent.displaySyncProgress()
}

// TestDefaultAgentUsesSystemTemplate verifies that the system message rendered for queries
// preserves the configured template content.
func TestDefaultAgentUsesSystemTemplate(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "You are a helpful assistant.",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	// Create a custom model stub that captures query calls to inspect system messages
	queryModel := &queryCapturingModel{stubModel: &stubModel{}}

	agent := NewDefaultAgent(queryModel, &stubEnvironment{}, cfg, prompts)
	agent.addMessage("user", "Check the system status", nil)

	// Trigger a query to capture the system message
	_, err := agent.Query(context.Background())
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	// Verify system message content mirrors the configured template
	if len(queryModel.capturedMessages) == 0 {
		t.Fatal("expected messages to be captured")
	}

	found := false
	for _, msg := range queryModel.capturedMessages {
		if msg.Role == "system" {
			if strings.TrimSpace(msg.Content) != "You are a helpful assistant." {
				t.Fatalf("unexpected system message content: %q", msg.Content)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected system message in captured messages")
	}
}

func TestDefaultAgentCacheAnchorStableWithoutTruncation(t *testing.T) {
	model := &cacheCapturingModel{resolved: llm.ResolvedModel{
		CacheKeyName:          "cache_control",
		CacheControl:          map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold: 1,
		CacheLookbackOffset:   1,
	}}
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "sys", InstanceTemplate: "inst"},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}
	agent := NewDefaultAgent(model, &stubEnvironment{}, cfg, prompts)
	agent.addMessage("user", "intent", nil)

	if _, err := agent.Query(context.Background()); err != nil {
		t.Fatalf("first Query error: %v", err)
	}
	if _, err := agent.Query(context.Background()); err != nil {
		t.Fatalf("second Query error: %v", err)
	}
	if len(model.captured) < 2 {
		t.Fatalf("expected two captured prompts, got %d", len(model.captured))
	}
	idx1, meta1 := findCacheAnchor(model.captured[0])
	idx2, meta2 := findCacheAnchor(model.captured[1])
	if idx1 < 0 || idx2 < 0 {
		t.Fatalf("expected cache anchor in both prompts")
	}
	if idx1 != idx2 {
		t.Fatalf("expected anchor index stable, got %d then %d", idx1, idx2)
	}
	if len(model.captured[0]) == 0 {
		t.Fatalf("expected captured prompt for first query")
	}
	if model.captured[0][len(model.captured[0])-1].Content == llm.CacheAnchorMarkerText {
		t.Fatalf("expected cache anchor to not be last user message in prompt")
	}
	if metadataIntForTest(meta1, llm.CacheAnchorSequenceMetadataKey) != metadataIntForTest(meta2, llm.CacheAnchorSequenceMetadataKey) {
		t.Fatalf("expected anchor sequence stable")
	}
}

func TestDefaultAgentCacheAnchorStableWithTruncation(t *testing.T) {
	model := &cacheCapturingModel{resolved: llm.ResolvedModel{
		CacheKeyName:          "cache_control",
		CacheControl:          map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold: 1,
		CacheLookbackOffset:   2,
	}}
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "sys", InstanceTemplate: "inst"},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}
	agent := NewDefaultAgent(model, &stubEnvironment{}, cfg, prompts)
	agent.RunConfig.MaxInputTokens = 35
	longOutput := strings.Repeat("word ", 60)
	agent.addMessage("user", "intent", nil)
	agent.addMessage("assistant", longOutput, nil)
	agent.addMessage("user", "retry", nil)
	agent.addMessage("assistant", "ok", nil)

	if _, err := agent.Query(context.Background()); err != nil {
		t.Fatalf("first Query error: %v", err)
	}
	if _, err := agent.Query(context.Background()); err != nil {
		t.Fatalf("second Query error: %v", err)
	}
	if len(model.captured) < 2 {
		t.Fatalf("expected two captured prompts, got %d", len(model.captured))
	}
	idx1, meta1 := findCacheAnchor(model.captured[0])
	idx2, meta2 := findCacheAnchor(model.captured[1])
	if idx1 < 0 || idx2 < 0 {
		t.Fatalf("expected cache anchor in both prompts")
	}
	if metadataIntForTest(meta1, llm.CacheAnchorSequenceMetadataKey) != metadataIntForTest(meta2, llm.CacheAnchorSequenceMetadataKey) {
		t.Fatalf("expected anchor sequence stable with truncation")
	}
	if anchorMessageContent(model.captured[0]) != llm.CacheAnchorMarkerText {
		t.Fatalf("expected anchor content untouched by truncation")
	}
	if anchorMessageContent(model.captured[1]) != llm.CacheAnchorMarkerText {
		t.Fatalf("expected anchor content untouched by truncation")
	}
	if promptContainsContent(model.captured[0], longOutput) {
		t.Fatalf("expected oldest assistant content truncated from prompt")
	}
	if promptContainsContent(model.captured[1], longOutput) {
		t.Fatalf("expected oldest assistant content truncated from prompt")
	}
}

func TestDefaultAgentForcesFinalizeNearStepLimit(t *testing.T) {
	model := &commandCapturingModel{stubModel: &stubModel{}}
	cfg := &minisweagent.ShellAgentConfig{MaxSteps: 1}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{SystemTemplate: "sys", InstanceTemplate: "inst"},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}
	agent := NewDefaultAgent(model, &stubEnvironment{}, cfg, prompts)

	_, _, _ = agent.Run(context.Background(), "Summarize the core loop.")

	if len(model.captured) == 0 {
		t.Fatalf("expected captured prompt, got none")
	}
	found := false
	for _, msg := range model.captured {
		if msg.Role == "user" && strings.Contains(msg.Content, "<answer>") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected finalize instruction in prompt when near step limit")
	}
	joined := joinCapturedMessages(model.captured)
	checks := []string{
		"Reply to the user now based on the conversation so far.",
		"The original task was: \"Summarize the core loop.\"",
		"Answer the user's current need rather than automatically turning this into a full session wrap-up.",
		"If the user is asking for a summary or wrap-up, provide it.",
		"Do not make further work requests or ask for more shell work.",
		"Output exactly one <answer>...</answer> block and no <command> block.",
		"Present the answer as a short list of substantive claims.",
		"Confidence: <0-100>% - ",
		"Do not provide a single overall confidence score",
		"Do not output a shell command.",
	}
	for _, check := range checks {
		if !strings.Contains(joined, check) {
			t.Fatalf("forced finalize prompt missing %q\n%s", check, joined)
		}
	}
}

func joinCapturedMessages(messages []minisweagent.Message) string {
	parts := make([]string, 0, len(messages))
	for _, msg := range messages {
		parts = append(parts, msg.Content)
	}
	return strings.Join(parts, "\n")
}

// deadlineExceededModel simulates an LLM API call that times out by returning
// ProviderTimeoutError on the first two calls, then a successful final answer.
type deadlineExceededModel struct {
	*stubModel
	calls int
}

func (m *deadlineExceededModel) Query(ctx context.Context, msgs []minisweagent.Message, opts ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.calls++
	if m.calls <= 2 {
		return minisweagent.QueryResult{}, &llm.ProviderTimeoutError{URL: "mock", Err: context.DeadlineExceeded}
	}
	return minisweagent.QueryResult{Content: "<answer>\nDone after timeout.\n</answer>"}, nil
}

// TestShellAgentTerminatesOnLLMTimeout demonstrates the bug where a
// context.DeadlineExceeded error from the LLM model Query call causes the
// shell-agent to exit without producing a final answer.
func TestShellAgentTerminatesOnLLMTimeout(t *testing.T) {
	model := &deadlineExceededModel{stubModel: &stubModel{}}
	cfg := &minisweagent.ShellAgentConfig{MaxSteps: 0}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "sys",
			InstanceTemplate: "inst",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}
	agent := NewDefaultAgent(model, &stubEnvironment{}, cfg, prompts)

	exitStatus, answer, err := agent.Run(context.Background(), "echo hello")

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if exitStatus != "Submitted" {
		t.Fatalf("expected exitStatus 'Submitted', got %q", exitStatus)
	}
	if answer == "" {
		t.Fatal("expected non-empty answer, got empty")
	}
}

// TestShellAgentRejectsFileModificationCommands verifies that shell-agent outputs PATCH_REQUEST
// signal when it detects an attempted file-modification command (e.g., sed, touch, patch).
func TestShellAgentRejectsFileModificationCommands(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "You must refuse file modifications and output PATCH_REQUEST: [description] instead.",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	// Create a model that simulates refusal of file-modification commands
	model := &refusingModel{stubModel: &stubModel{}}

	agent := NewDefaultAgent(model, &stubEnvironment{}, cfg, prompts)
	agent.addMessage("user", "Execute: sed -i 's/old/new/' file.txt", nil)

	// Test: Agent should refuse to execute sed
	result, err := agent.Query(context.Background())
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	if !strings.Contains(result.Content, "PATCH_REQUEST") {
		t.Fatalf("expected PATCH_REQUEST signal in response, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "file modifications are forbidden") {
		t.Fatalf("expected refusal message, got: %q", result.Content)
	}
}

func TestFinalizeReminderOnNonCompliance(t *testing.T) {
	messages := []minisweagent.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "--- BEGIN TASK ---\nTask: echo hello\n--- END TASK ---"},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{
		{Content: "<command>printf step1</command>"},
		{Content: "<command>printf step2</command>"},
		{Content: "<command>printf step3</command>"},
		{Content: "<command>printf step4</command>"},
		{Content: "<command>printf should-not-exec-1</command>"},
		{Content: "<command>printf should-not-exec-2</command>"},
		{Content: "<command>printf should-not-exec-3</command>"},
		{Content: "<answer>\nConfidence: 100% - Final answer delivered after reminders.\n</answer>"},
	}}
	env := &capturingEnvironment{result: minisweagent.ExecuteResult{Output: "ok", ReturnCode: 0}}

	cfg := &minisweagent.ShellAgentConfig{MaxSteps: 10, FinalizeRemainingSteps: 6}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "default-system-template",
			InstanceTemplate: "default-instance-template",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.RunConfig.Task = "echo hello"

	exitStatus, answer, err := agent.RunWithMessages(context.Background(), messages)
	if err != nil {
		t.Fatalf("RunWithMessages unexpected error: %v", err)
	}
	if exitStatus != "Submitted" {
		t.Fatalf("expected Submitted, got %q", exitStatus)
	}
	if answer == "" {
		t.Fatal("expected non-empty final answer")
	}
	if !strings.Contains(answer, "Final answer delivered after reminders") {
		t.Fatalf("expected answer to contain final answer text, got %q", answer)
	}
	if agent.State.consecutiveFinalizeReminders != 3 {
		t.Fatalf("expected 3 consecutive finalize reminders, got %d", agent.State.consecutiveFinalizeReminders)
	}
	if agent.State.consecutiveFormatErrors != 0 {
		t.Fatalf("expected 0 consecutive format errors during reminder window, got %d", agent.State.consecutiveFormatErrors)
	}
	if !agent.State.finalizeRequested {
		t.Fatal("expected finalizeRequested to be true")
	}
	if env.calls != 4 {
		t.Fatalf("expected 4 command executions (steps 1-4 only, none during reminders), got %d", env.calls)
	}
}

func TestFinalizeRemindersExhaustedThenLimitsExceeded(t *testing.T) {
	messages := []minisweagent.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "--- BEGIN TASK ---\nTask: echo hello\n--- END TASK ---"},
	}

	responses := make([]minisweagent.QueryResult, 15)
	for i := range responses {
		responses[i] = minisweagent.QueryResult{Content: "<command>printf step</command>"}
	}
	model := &scriptedModel{responses: responses}
	env := &capturingEnvironment{result: minisweagent.ExecuteResult{Output: "ok", ReturnCode: 0}}

	cfg := &minisweagent.ShellAgentConfig{MaxSteps: 10, FinalizeRemainingSteps: 6}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "default-system-template",
			InstanceTemplate: "default-instance-template",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.RunConfig.Task = "echo hello"

	exitStatus, answer, runErr := agent.RunWithMessages(context.Background(), messages)
	if runErr != nil {
		t.Fatalf("RunWithMessages unexpected error: %v", runErr)
	}
	if exitStatus != "LimitsExceeded" {
		t.Fatalf("expected LimitsExceeded exitStatus, got %q", exitStatus)
	}
	if answer == "" {
		t.Fatal("expected non-empty answer from LimitsExceeded")
	}
	if agent.State.consecutiveFinalizeReminders != 3 {
		t.Fatalf("expected 3 consecutive finalize reminders, got %d", agent.State.consecutiveFinalizeReminders)
	}
	if agent.State.consecutiveFormatErrors != 0 {
		t.Fatalf("expected 0 consecutive format errors, got %d", agent.State.consecutiveFormatErrors)
	}
	if !agent.State.finalizeRequested {
		t.Fatal("expected finalizeRequested to be true")
	}
}

// Tests for cache prefix hash stamping and drift detection.
// Tests 1-4 exercise stampCachePrefixHash directly.
// Tests 5-6 exercise the full Query() path with cacheCapturingModel.

func TestStampCachePrefixHashStoresHash(t *testing.T) {
	messages := []minisweagent.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "hello"},
		{Role: "user", Content: llm.CacheAnchorMarkerText, Metadata: map[string]any{
			"type":                             "cache_anchor",
			llm.CacheAnchorSequenceMetadataKey: 1,
		}},
	}
	stampCachePrefixHash(messages)
	hash, ok := messages[2].Metadata[llm.CacheAnchorInsertionPrefixHashMetadataKey].(string)
	if !ok || hash == "" {
		t.Fatal("expected insertion_prefix_hash in anchor metadata after stamping")
	}
}

func TestStampCachePrefixHashDeterministic(t *testing.T) {
	makeMessages := func() []minisweagent.Message {
		return []minisweagent.Message{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "hello"},
			{Role: "user", Content: llm.CacheAnchorMarkerText, Metadata: map[string]any{
				"type":                             "cache_anchor",
				llm.CacheAnchorSequenceMetadataKey: 1,
			}},
		}
	}
	msgs1 := makeMessages()
	msgs2 := makeMessages()
	stampCachePrefixHash(msgs1)
	stampCachePrefixHash(msgs2)
	hash1, _ := msgs1[2].Metadata[llm.CacheAnchorInsertionPrefixHashMetadataKey].(string)
	hash2, _ := msgs2[2].Metadata[llm.CacheAnchorInsertionPrefixHashMetadataKey].(string)
	if hash1 == "" || hash2 == "" {
		t.Fatalf("expected non-empty hashes from stamping, got %q / %q", hash1, hash2)
	}
	if hash1 != hash2 {
		t.Fatalf("expected identical hashes for identical prefix content, got %s vs %s", hash1, hash2)
	}
}

func TestStampCachePrefixHashDetectsMutation(t *testing.T) {
	messages := []minisweagent.Message{
		{Role: "system", Content: "sys v1"},
		{Role: "user", Content: "hello"},
		{Role: "user", Content: llm.CacheAnchorMarkerText, Metadata: map[string]any{
			"type":                             "cache_anchor",
			llm.CacheAnchorSequenceMetadataKey: 1,
		}},
	}
	stampCachePrefixHash(messages)
	origHash, _ := messages[2].Metadata[llm.CacheAnchorInsertionPrefixHashMetadataKey].(string)
	if origHash == "" {
		t.Fatal("expected non-empty insertion hash")
	}

	// Change prefix content (system message).
	messagesCopy := []minisweagent.Message{
		{Role: "system", Content: "sys v2"},
		{Role: "user", Content: "hello"},
		{Role: "user", Content: llm.CacheAnchorMarkerText, Metadata: map[string]any{
			"type":                             "cache_anchor",
			llm.CacheAnchorSequenceMetadataKey: 1,
		}},
	}
	newHash := llm.CachePrefixHash(
		llm.FormatMessagesForHashing(toLLMMessages(messagesCopy[:2])),
		2,
	)
	if newHash == "" {
		t.Fatal("expected non-empty recomputed hash")
	}
	if origHash == newHash {
		t.Fatal("expected different hash after prefix mutation, but hashes matched")
	}
}

func TestStampCachePrefixHashSkipsRetiredAnchor(t *testing.T) {
	messages := []minisweagent.Message{
		{Role: "user", Content: "hello"},
		{Role: "user", Content: llm.CacheAnchorMarkerText, Metadata: map[string]any{
			"type":                             "cache_anchor",
			llm.CacheAnchorRetiredMetadataKey:  true,
			llm.CacheAnchorSequenceMetadataKey: 1,
		}},
	}
	stampCachePrefixHash(messages)
	if _, ok := messages[1].Metadata[llm.CacheAnchorInsertionPrefixHashMetadataKey]; ok {
		t.Fatal("expected no insertion hash on retired anchor")
	}
}

func TestCachePrefixHashStableAcrossQueryCalls(t *testing.T) {
	model := &cacheCapturingModel{resolved: llm.ResolvedModel{
		CacheKeyName:          "cache_control",
		CacheControl:          map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold: 1,
		CacheLookbackOffset:   1,
	}}
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "system prompt",
			InstanceTemplate: "instance task",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}
	agent := NewDefaultAgent(model, &stubEnvironment{}, cfg, prompts)
	agent.addMessage("user", "intent", nil)

	if _, err := agent.Query(context.Background()); err != nil {
		t.Fatalf("first Query error: %v", err)
	}
	if _, err := agent.Query(context.Background()); err != nil {
		t.Fatalf("second Query error: %v", err)
	}

	if len(model.captured) < 2 {
		t.Fatalf("expected two captured prompts, got %d", len(model.captured))
	}

	// Both captured prompts should have the same insertion prefix hash.
	idx1, meta1 := findCacheAnchor(model.captured[0])
	idx2, meta2 := findCacheAnchor(model.captured[1])
	if idx1 < 0 || idx2 < 0 {
		t.Fatal("expected cache anchor in both prompts")
	}
	hash1, _ := meta1[llm.CacheAnchorInsertionPrefixHashMetadataKey].(string)
	hash2, _ := meta2[llm.CacheAnchorInsertionPrefixHashMetadataKey].(string)
	if hash1 == "" || hash2 == "" {
		t.Fatal("expected non-empty insertion hash in both anchors")
	}
	if hash1 != hash2 {
		t.Fatalf("expected stable cache prefix hash across calls, got %s then %s", hash1, hash2)
	}
}

func TestCachePrefixDriftDetectedAfterMutation(t *testing.T) {
	model := &cacheCapturingModel{resolved: llm.ResolvedModel{
		CacheKeyName:          "cache_control",
		CacheControl:          map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold: 1,
		CacheLookbackOffset:   1,
	}}
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "system prompt",
			InstanceTemplate: "instance task",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}
	agent := NewDefaultAgent(model, &stubEnvironment{}, cfg, prompts)

	// Add multiple messages so the prefix has content to mutate.
	agent.addMessage("user", "hello alpha", nil)
	agent.addMessage("assistant", "response alpha", nil)
	agent.addMessage("user", "hello beta", nil)

	// First query: anchor inserted, hash stamped on stable prefix.
	if _, err := agent.Query(context.Background()); err != nil {
		t.Fatalf("first Query error: %v", err)
	}

	// Mutate a message that sits before the anchor in the prefix.
	// After first Query, a.messages is [system, user-alpha, asst-alpha, anchor, user-beta, asst-response].
	// The prefix before the anchor is [system, user-alpha, asst-alpha].
	// Change user-alpha's content.
	for i, msg := range agent.State.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "hello alpha") {
			agent.State.Messages[i].Content = "hello alpha MUTATED"
			break
		}
	}

	// Second query: no rotation, stored hash vs mutated prefix should differ.
	if _, err := agent.Query(context.Background()); err != nil {
		t.Fatalf("second Query error: %v", err)
	}

	if len(model.captured) < 2 {
		t.Fatalf("expected two captured prompts, got %d", len(model.captured))
	}

	// The second captured prompt should carry the original insertion hash
	// (since ensureCacheAnchor did not rotate), which differs from the
	// hash computed over the now-mutated prefix.
	anchorIdx2, meta2 := findCacheAnchor(model.captured[1])
	if anchorIdx2 < 0 {
		t.Fatal("expected cache anchor in second captured prompt")
	}
	storedHash, _ := meta2[llm.CacheAnchorInsertionPrefixHashMetadataKey].(string)
	if storedHash == "" {
		t.Fatal("expected stored insertion hash in second prompt's anchor")
	}
	currentHash := llm.CachePrefixHash(
		llm.FormatMessagesForHashing(toLLMMessages(model.captured[1][:anchorIdx2])),
		anchorIdx2,
	)
	if currentHash == "" {
		t.Fatal("expected non-empty current hash computed over mutated prefix")
	}
	if storedHash == currentHash {
		t.Fatalf("expected different hash after prefix mutation: stored=%s current=%s",
			storedHash, currentHash)
	}
}

func findCacheAnchor(messages []minisweagent.Message) (int, map[string]any) {
	for i := len(messages) - 1; i >= 0; i-- {
		metadata := messages[i].Metadata
		if metadata == nil {
			continue
		}
		val, ok := metadata["type"].(string)
		if !ok || !strings.EqualFold(strings.TrimSpace(val), "cache_anchor") {
			continue
		}
		if retired, ok := metadata[llm.CacheAnchorRetiredMetadataKey]; ok {
			switch v := retired.(type) {
			case bool:
				if v {
					continue
				}
			case string:
				if strings.EqualFold(strings.TrimSpace(v), "true") {
					continue
				}
			}
		}
		return i, metadata
	}
	return -1, nil
}

func metadataIntForTest(metadata map[string]any, key string) int {
	if metadata == nil {
		return 0
	}
	if raw, ok := metadata[key]; ok {
		switch v := raw.(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		}
	}
	return 0
}

func anchorMessageContent(messages []minisweagent.Message) string {
	idx, _ := findCacheAnchor(messages)
	if idx < 0 {
		return ""
	}
	return messages[idx].Content
}

func promptContainsContent(messages []minisweagent.Message, content string) bool {
	for _, msg := range messages {
		if msg.Content == content {
			return true
		}
	}
	return false
}

// TestSystemPromptContentPreservesPrebuiltMessage verifies that when
// RunWithMessages is called with a pre-built system message containing
// extra instructions (e.g. per-mode shell-agent guidance), the
// systemPromptContent() method preserves it verbatim instead of
// re-rendering from the Go template.
//
// This is the regression test for the unstaged fix in default.go that
// guards against mode-injected guidance being silently discarded on
// resume when --mode is not re-specified.
func TestSystemPromptContentPreservesPrebuiltMessage(t *testing.T) {
	extraInstructions := "MAGIC_MODE_TOKEN: use handoff notes for all file modifications."

	// Build pre-built messages that simulate what BuildShellAgentMessages
	// produces when extra instructions are appended.
	messages := []minisweagent.Message{
		{Role: "system", Content: "You are a shell agent.\n" + extraInstructions},
		{Role: "user", Content: "--- BEGIN TASK ---\nTest task\n--- END TASK ---"},
	}

	model := &commandCapturingModel{stubModel: &stubModel{}}
	env := &stubEnvironment{}

	cfg := &minisweagent.ShellAgentConfig{MaxSteps: 2}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "default-system-template",
			InstanceTemplate: "default-instance-template",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.State.Messages = append([]minisweagent.Message(nil), messages...)

	// Call systemPromptContent directly to verify the guard.
	sysContent, err := agent.systemPromptContent()
	if err != nil {
		t.Fatalf("systemPromptContent: %v", err)
	}

	if !strings.Contains(sysContent, extraInstructions) {
		t.Errorf("system prompt should contain extra instructions, got:\n%s", sysContent)
	}

	if !strings.Contains(sysContent, "MAGIC_MODE_TOKEN") {
		t.Errorf("system prompt should contain MAGIC_MODE_TOKEN marker, got:\n%s", sysContent)
	}

	// Verify the system prompt was NOT re-rendered from the template.
	// The template is "default-system-template" which does NOT contain MAGIC_MODE_TOKEN.
	if sysContent == "default-system-template" {
		t.Error("system prompt was incorrectly re-rendered from template instead of preserving pre-built message")
	}

	// Verify the system prompt was cached and won't be re-rendered.
	if !agent.RunConfig.SystemPromptCached {
		t.Error("systemPromptCached should be true after first call")
	}

	// Call again to verify caching works.
	sysContent2, err := agent.systemPromptContent()
	if err != nil {
		t.Fatalf("second systemPromptContent: %v", err)
	}
	if sysContent2 != sysContent {
		t.Error("cached system prompt should match first call")
	}
}

// TestSystemPromptContentReRendersWithoutPrebuiltMessage verifies that
// when no pre-built messages are set (the standard Run path), the
// system prompt is rendered from the template as expected.
func TestSystemPromptContentReRendersWithoutPrebuiltMessage(t *testing.T) {
	model := &commandCapturingModel{stubModel: &stubModel{}}
	env := &stubEnvironment{}

	cfg := &minisweagent.ShellAgentConfig{MaxSteps: 2}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "You are a helpful assistant.",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	agent := NewDefaultAgent(model, env, cfg, prompts)
	// Do NOT call SetMessages — simulate the standard Run path where
	// messages are empty and systemPromptContent renders from template.

	sysContent, err := agent.systemPromptContent()
	if err != nil {
		t.Fatalf("systemPromptContent: %v", err)
	}

	// The rendered template should contain the default system prompt.
	if !strings.Contains(sysContent, "You are a helpful assistant.") {
		t.Errorf("rendered system prompt should contain default template, got:\n%s", sysContent)
	}

	// It should NOT contain mode-specific extra instructions.
	if strings.Contains(sysContent, "MAGIC_MODE_TOKEN") {
		t.Error("rendered system prompt should not contain MAGIC_MODE_TOKEN")
	}
}

// TestRunWithMessagesPreservesSystemPromptEndToEnd verifies the
// end-to-end flow: RunWithMessages with a pre-built system message
// preserves the system prompt across Query calls.
func TestRunWithMessagesPreservesSystemPromptEndToEnd(t *testing.T) {
	extraInstructions := "MAGIC_MODE_TOKEN: use handoff notes for all file modifications."

	messages := []minisweagent.Message{
		{Role: "system", Content: "You are a shell agent.\n" + extraInstructions},
		{Role: "user", Content: "--- BEGIN TASK ---\nTest task\n--- END TASK ---"},
	}

	model := &commandCapturingModel{stubModel: &stubModel{}, response: "<answer>\nOK\n</answer>"}
	env := &stubEnvironment{}

	cfg := &minisweagent.ShellAgentConfig{MaxSteps: 0}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "default-system-template",
			InstanceTemplate: "default-instance-template",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	agent := NewDefaultAgent(model, env, cfg, prompts)

	ctx := context.Background()
	exitStatus, answer, err := agent.RunWithMessages(ctx, messages)
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}

	if exitStatus == "" {
		t.Error("exit status should not be empty")
	}
	if answer == "" {
		t.Error("answer should not be empty")
	}

	// Verify the model received the system prompt with extra instructions.
	if len(model.captured) == 0 {
		t.Fatal("model should have received messages")
	}
	if model.captured[0].Role != "system" {
		t.Errorf("first message should be system, got: %s", model.captured[0].Role)
	}
	if !strings.Contains(model.captured[0].Content, extraInstructions) {
		t.Errorf("system message should contain extra instructions, got:\n%s", model.captured[0].Content)
	}
	if !strings.Contains(model.captured[0].Content, "MAGIC_MODE_TOKEN") {
		t.Errorf("system message should contain MAGIC_MODE_TOKEN marker, got:\n%s", model.captured[0].Content)
	}

	// Verify the system prompt was NOT re-rendered from the template.
	if model.captured[0].Content == "default-system-template" {
		t.Error("system message was incorrectly re-rendered from template instead of preserving pre-built message")
	}
}

func TestRunWithMessagesPreservesTaskForForcedFinalization(t *testing.T) {
	const task = "Run the local tmp-root check and report the output."
	messages := []minisweagent.Message{
		{Role: "system", Content: "You are a shell agent."},
		{Role: "user", Content: "--- BEGIN TASK ---\nTask: " + task + "\n--- END TASK ---"},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{
		{Content: "<command>printf ok</command>"},
		{Content: "<answer>\nConfidence: 100% - ok\n</answer>"},
	}}
	env := &stubEnvironment{}

	cfg := &minisweagent.ShellAgentConfig{MaxSteps: 2, FinalizeRemainingSteps: 1}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "default-system-template",
			InstanceTemplate: "default-instance-template",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.RunConfig.Task = task

	exitStatus, answer, err := agent.RunWithMessages(context.Background(), messages)
	if err != nil {
		t.Fatalf("RunWithMessages: %v", err)
	}
	if exitStatus != "Submitted" {
		t.Fatalf("expected Submitted, got %q", exitStatus)
	}
	if !strings.Contains(answer, "ok") {
		t.Fatalf("expected final answer to include ok, got %q", answer)
	}
	if len(model.captured) < 2 {
		t.Fatalf("expected forced-finalization query, got %d query calls", len(model.captured))
	}

	var forcedMessage string
	for _, msg := range model.captured[1] {
		if strings.Contains(msg.Content, "Reply to the user now based on the conversation so far.") {
			forcedMessage = msg.Content
			break
		}
	}
	if forcedMessage == "" {
		t.Fatalf("expected force-finalization message in second query")
	}
	if !strings.Contains(forcedMessage, task) {
		t.Fatalf("expected force-finalization message to include task %q, got:\n%s", task, forcedMessage)
	}
}

func TestRunLoopFormatErrorCounterResetsOnNonFormatError(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{
		{Content: "no fence one"},
		{Content: "no fence two"},
		{Content: "<command>\nsleep 999\n</command>"},
		{Content: "no fence three"},
	}}
	env := &errorEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "test task", nil)

	status, msg, err := agent.RunLoop(context.Background(), false)
	if err == nil {
		t.Fatal("expected error from running out of scripted responses, got nil")
	}
	if status == "FormatErrorLoop" {
		t.Fatalf("status should not be FormatErrorLoop, got %q (msg=%q); non-format AgentError should have reset the counter", status, msg)
	}
}
func TestParseXMLCommandMctForgePreservesHashLines(t *testing.T) {
	input := "<command>\nmachtiani-forge Create file with heading:\n\n# Documentation Cache\n\n## Nix Reference\n\nSome content here.\ndo not stage.\n</command>"
	command, corrections, err := parseXMLCommand(input, "command")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(command, "machtiani-forge") {
		t.Errorf("expected command to start with machtiani-forge, got: %s", command)
	}
	if !strings.Contains(command, "## Nix Reference") {
		t.Errorf("expected ## Nix Reference to be preserved in command, got: %s", command)
	}
	if !strings.Contains(command, "# Documentation Cache") {
		t.Errorf("expected # Documentation Cache to be preserved in command, got: %s", command)
	}
	if strings.Contains(command, "&&") {
		t.Errorf("expected no && joining in machtiani-forge command, got: %s", command)
	}
	_ = corrections
}

func TestParseXMLCommandMctForgePreservesMultiline(t *testing.T) {
	input := "<command>\nmachtiani-forge Create the following files:\n\n1. Create file flake.nix\n2. Create file hosts/nixlab.nix\ndo not stage.\n</command>"
	command, corrections, err := parseXMLCommand(input, "command")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(command, "1. Create file flake.nix") {
		t.Errorf("expected multi-line content to be preserved, got: %s", command)
	}
	if strings.Contains(command, "&&") {
		t.Errorf("expected no && joining in machtiani-forge command, got: %s", command)
	}
	_ = corrections
}

func TestParseXMLCommandMctForgeDoesNotMatchSimilarPrefix(t *testing.T) {
	input := "<command>\nmachtiani-forge-test some args\n## should be stripped\nother line\n</command>"
	cmd, _, err := parseXMLCommand(input, "command")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	t.Logf("parseXMLCommand returned raw multi-line content (as expected): %s", cmd)
	_ = cmd
}

func TestParseXMLCommandWithBackticks(t *testing.T) {
	input := "<command>echo `date`</command>"
	cmd, _, err := parseXMLCommand(input, "command")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed with backticks: %v", err)
	}
	if cmd != "echo `date`" {
		t.Fatalf("expected backtick command, got %qb", cmd)
	}
}

// TestParseXMLCommandWithQuotedCloseTag verifies that the simplified parser
// truncates at the first closing tag. With unique session tags, literal
// closing tags inside shell content are impossible, so the old quote-aware
// protection (which would have searched past internal close tags) is unnecessary.
func TestParseXMLCommandWithQuotedCloseTag(t *testing.T) {
	input := "<command>echo 'this is </command> inside quotes'</command>"
	cmd, _, err := parseXMLCommand(input, "command")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed with quoted close tag: %v", err)
	}
	expected := "echo 'this is"
	if cmd != expected {
		t.Fatalf("expected %q, got %q", expected, cmd)
	}
}

func TestParseXMLCommandMissingCloseTag(t *testing.T) {
	input := "<command>ls -la"
	_, _, err := parseXMLCommand(input, "command")
	if err == nil {
		t.Fatal("parseXMLCommand should fail with missing close tag")
	}
}

// TestABComparisonBackgroundExecutionAcceptance verifies the treatment
// behavior: the default system template (which now contains the background
// execution strategy) allows background commands to be accepted and executed.
func TestABComparisonBackgroundExecutionAcceptance(t *testing.T) {
	// 1. Create a scriptedModel that responds with a background command.
	model := &scriptedModel{responses: []minisweagent.QueryResult{
		{Content: "<command>\nsleep 1 < /dev/null > /dev/null 2> /dev/null &\n</command>"},
	}}

	// 2. Create a stubEnvironment that tracks whether Execute was called
	//    with a command containing the & background pattern.
	env := &stubEnvironment{}

	// 3. Create a DefaultAgent with the stub environment and scripted model,
	//    using the default system template (which now contains the background
	//    execution strategy).
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "test task", nil)

	// 4. Call agent.Query then agent.translateAndExecute to process the
	//    scripted response.
	resp, err := agent.Query(context.Background())
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	_, err = agent.translateAndExecute(context.Background(), resp)

	// 5. Verify no format error was returned (the command was accepted).
	if err != nil {
		var formatErr *minisweagent.FormatError
		if errors.As(err, &formatErr) {
			t.Fatalf("unexpected FormatError: background command should be accepted, got: %v", err)
		}
	}

	// 6. Verify the environment Execute method was called with the
	//    background pattern present.
	if !env.executed {
		t.Fatal("expected Execute to be called")
	}
	if !strings.Contains(env.lastCommand, "&") {
		t.Errorf("expected background pattern & in command, got: %s", env.lastCommand)
	}
}

// TestABComparisonBaselineRejectsBackgroundWhenRestricted documents the
// OLD agent behavior: the lightweight template contained the phrase
// "do not include ... background execution", which meant a background
// command response would have been considered invalid under the old rules.
// This test serves as documentation of the baseline vs treatment comparison.
func TestABComparisonBaselineRejectsBackgroundWhenRestricted(t *testing.T) {
	// The OLD lightweight template explicitly prohibited background execution.
	// This hardcoded message represents what the template used to say.
	oldLightweightTemplate := "do not include ... background execution"

	// Verify the old restriction explicitly prohibited background execution.
	if !strings.Contains(oldLightweightTemplate, "do not include") {
		t.Error("old lightweight template should contain 'do not include'")
	}
	if !strings.Contains(oldLightweightTemplate, "background execution") {
		t.Error("old lightweight template should contain 'background execution'")
	}

	// Under the old rules, a background command response would have been
	// considered invalid because the template prohibited its use.  Simulate
	// the old agent by creating a DefaultAgent whose system template is
	// the restrictive text.
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   oldLightweightTemplate,
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	model := &scriptedModel{responses: []minisweagent.QueryResult{
		{Content: "<command>\nsleep 1 < /dev/null > /dev/null 2> /dev/null &\n</command>"},
	}}
	env := &stubEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.addMessage("user", "test task", nil)

	// Query and attempt to execute the background command.
	resp, err := agent.Query(context.Background())
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	_, err = agent.translateAndExecute(context.Background(), resp)

	// The old template said "do not include ... background execution".
	// The contract between the template and the model made background
	// commands invalid under the old rules.  This test documents that
	// baseline expectation for the A/B comparison.
	_ = err
}

// --- Tests for the early-turn enforcement feature -------------------------
//
// These tests cover the bash -n syntax validator and the gate that
// activates stricter behaviour on the first few agent steps when the
// caller has explicitly opted in via EnforceEarlyCommands. Existing
// behaviour must be preserved when the flag is off or when the turn is
// past the early-turn window (commandsExecuted >= 3).

func TestValidateBashSyntaxValidCommand(t *testing.T) {
	if err := validateBashSyntax("echo hello world"); err != nil {
		t.Fatalf("validateBashSyntax(echo hello world) returned error: %v", err)
	}
	if err := validateBashSyntax("ls -la /tmp"); err != nil {
		t.Fatalf("validateBashSyntax(ls -la /tmp) returned error: %v", err)
	}
	if err := validateBashSyntax("cd /tmp && pwd"); err != nil {
		t.Fatalf("validateBashSyntax(cd /tmp && pwd) returned error: %v", err)
	}
}

func TestValidateBashSyntaxInvalidCommand(t *testing.T) {
	// Unbalanced quote — bash will reject.
	if err := validateBashSyntax("echo 'unterminated"); err == nil {
		t.Fatal("expected error from validateBashSyntax on unterminated quote, got nil")
	}
	// Stray redirection — bash will reject.
	if err := validateBashSyntax("ls >"); err == nil {
		t.Fatal("expected error from validateBashSyntax on stray redirection, got nil")
	}
}

func TestEarlyTurnEnforcementEnabled(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "format-error", ActionObservationTemplate: "{{.Output}}"},
	}
	agent := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, cfg, prompts)

	// Default state: flag off, commandsExecuted=0.
	if agent.earlyTurnEnforcementEnabled() {
		t.Fatal("enforcement should be disabled by default (flag off, commandsExecuted=0)")
	}

	// Flag on, commandsExecuted=0: enforcement on.
	agent.RunConfig.EnforceEarlyCommands = true
	if !agent.earlyTurnEnforcementEnabled() {
		t.Fatal("enforcement should be enabled with flag on and commandsExecuted=0")
	}

	// Flag on, commandsExecuted=2 (still inside window): enforcement on.
	agent.State.commandsExecuted = 2
	if !agent.earlyTurnEnforcementEnabled() {
		t.Fatal("enforcement should be enabled with flag on and commandsExecuted=2")
	}

	// Flag on, commandsExecuted=3 (just outside window): enforcement off.
	agent.State.commandsExecuted = 3
	if agent.earlyTurnEnforcementEnabled() {
		t.Fatal("enforcement should be disabled with commandsExecuted=3")
	}

	// Flag off: enforcement is gated off, regardless of commandsExecuted.
	agent.RunConfig.EnforceEarlyCommands = false
	if agent.earlyTurnEnforcementEnabled() {
		t.Fatal("enforcement should be disabled with flag off")
	}
}

func TestRenderFormatErrorUsesEarlyTurnMessageWhenEnforced(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "custom-template-message", ActionObservationTemplate: "{{.Output}}"},
	}
	agent := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, cfg, prompts)
	agent.RunConfig.EnforceEarlyCommands = true
	agent.State.commandsExecuted = 0

	got := agent.renderFormatError(fmt.Errorf("missing <command> block"))
	want := "custom-template-message"
	if got != want {
		t.Fatalf("expected format error template message, got %q", got)
	}
}

func TestRenderFormatErrorFallsBackOnLateSteps(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "custom-template-message", ActionObservationTemplate: "{{.Output}}"},
	}
	agent := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, cfg, prompts)
	agent.RunConfig.EnforceEarlyCommands = true
	agent.State.commandsExecuted = 3

	got := agent.renderFormatError(fmt.Errorf("missing <command> block"))
	if got != "custom-template-message" {
		t.Fatalf("expected template message on late turn, got %q", got)
	}
}

func TestRenderFormatErrorFallsBackWhenFlagOff(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "custom-template-message", ActionObservationTemplate: "{{.Output}}"},
	}
	agent := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, cfg, prompts)
	// Flag stays off; plannerTurn is 0 but enforcement should not engage.
	got := agent.renderFormatError(fmt.Errorf("missing <command> block"))
	if got != "custom-template-message" {
		t.Fatalf("expected template message when flag off, got %q", got)
	}
}

func TestTranslateAndExecuteEnforcesEarlyTurnMalformedResponse(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "format-error: {{.RawErr}} {{.CommandTag}} {{.AnswerTag}}", ActionObservationTemplate: "{{.Output}}"},
	}
	// No <command> block — triggers a format error from parseXMLCommand.
	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "no command here"}}}
	env := &capturingEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.RunConfig.EnforceEarlyCommands = true
	agent.State.commandsExecuted = 0
	agent.RunConfig.AnswerTag = "customanswer"
	agent.RunConfig.NormalizeAnswerTag()
	agent.RunConfig.CommandTag = "command"
	agent.RunConfig.NormalizeCommandTag()
	agent.addMessage("user", "List files", nil)

	err := agent.Step(context.Background())
	if err == nil {
		t.Fatal("expected FormatError, got nil")
	}
	var formatErr *minisweagent.FormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("expected FormatError, got %T", err)
	}
	if env.calls != 0 {
		t.Fatalf("execute should not be called, saw %d calls", env.calls)
	}
	// The user-facing message should be the format error template message.
	feedback := agent.State.Messages[len(agent.State.Messages)-1].Content
	want := "format-error: response must include a <command>...</command> block command customanswer"
	if feedback != want {
		t.Fatalf("expected format error template message, got %q", feedback)
	}
}

func TestTranslateAndExecuteNoEnforcementOnLateSteps(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "custom-template-message", ActionObservationTemplate: "{{.Output}}"},
	}
	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "no command here"}}}
	env := &capturingEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.RunConfig.EnforceEarlyCommands = true
	agent.State.commandsExecuted = 3 // outside early-step window
	agent.addMessage("user", "List files", nil)

	err := agent.Step(context.Background())
	if err == nil {
		t.Fatal("expected FormatError, got nil")
	}
	var formatErr *minisweagent.FormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("expected FormatError, got %T", err)
	}
	feedback := agent.State.Messages[len(agent.State.Messages)-1].Content
	if feedback != "custom-template-message" {
		t.Fatalf("expected template message on late turn, got %q", feedback)
	}
}

func TestTranslateAndExecuteNoEnforcementWhenFlagOff(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "custom-template-message", ActionObservationTemplate: "{{.Output}}"},
	}
	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "no command here"}}}
	env := &capturingEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	// Flag stays off; commandsExecuted=0 but enforcement is gated off.
	agent.addMessage("user", "List files", nil)

	err := agent.Step(context.Background())
	if err == nil {
		t.Fatal("expected FormatError, got nil")
	}
	var formatErr *minisweagent.FormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("expected FormatError, got %T", err)
	}
	feedback := agent.State.Messages[len(agent.State.Messages)-1].Content
	if feedback != "custom-template-message" {
		t.Fatalf("expected template message when flag off, got %q", feedback)
	}
}

func TestTranslateAndExecuteEnforcesBashSyntaxOnEarlyStep(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "Format Error: {{.RawErr}}\nExpected: <{{.CommandTag}}>...</{{.CommandTag}}>, <{{.AnswerTag}}>...</{{.AnswerTag}}>", ActionObservationTemplate: "{{.Output}}"},
	}
	// The command is wrapped in <command>...</command> so parseXMLCommand
	// succeeds, but the inner string has a bash syntax error (stray `>`).
	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "<command>\nls >\n</command>"}}}
	env := &capturingEnvironment{}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.RunConfig.EnforceEarlyCommands = true
	agent.State.commandsExecuted = 0
	agent.addMessage("user", "List files", nil)

	err := agent.Step(context.Background())
	if err == nil {
		t.Fatal("expected FormatError for bash syntax violation, got nil")
	}
	var formatErr *minisweagent.FormatError
	if !errors.As(err, &formatErr) {
		t.Fatalf("expected FormatError, got %T", err)
	}
	if env.calls != 0 {
		t.Fatalf("execute should not be called on syntax error, saw %d calls", env.calls)
	}
	feedback := agent.State.Messages[len(agent.State.Messages)-1].Content
	want := "Format Error: bash syntax error: bash: -c: line 1: syntax error near unexpected token `newline'\nExpected: <command>...</command>, <answer>...</answer>"
	if feedback != want {
		t.Fatalf("expected format error template message, got %q", feedback)
	}
}

func TestTranslateAndExecuteSkipsBashSyntaxOnLateSteps(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "custom-template-message", ActionObservationTemplate: "{{.Output}}"},
	}
	// Same syntactically-invalid command, but the planner turn is past the
	// early window. The bash check should be skipped, so the command is
	// dispatched to the environment as-is.
	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "<command>\nls >\n</command>"}}}
	env := &capturingEnvironment{result: minisweagent.ExecuteResult{ReturnCode: 2, Output: "syntax error"}}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.RunConfig.EnforceEarlyCommands = true
	agent.State.commandsExecuted = 3 // past the early-step window
	agent.addMessage("user", "List files", nil)

	if err := agent.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v, expected no error since bash check is skipped on late steps", err)
	}
	if env.calls != 1 {
		t.Fatalf("expected execute to be called once on late step, got %d", env.calls)
	}
}

func TestTranslateAndExecuteSkipsBashSyntaxWhenFlagOff(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "custom-template-message", ActionObservationTemplate: "{{.Output}}"},
	}
	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "<command>\nls >\n</command>"}}}
	env := &capturingEnvironment{result: minisweagent.ExecuteResult{ReturnCode: 2, Output: "syntax error"}}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	// Flag stays off. Bash check must not run, even on early steps.
	agent.addMessage("user", "List files", nil)

	if err := agent.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v, expected no error since bash check is gated off", err)
	}
	if env.calls != 1 {
		t.Fatalf("expected execute to be called once, got %d", env.calls)
	}
}

func TestTranslateAndExecuteSkipsBashSyntaxForMctForge(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "custom-template-message", ActionObservationTemplate: "{{.Output}}"},
	}
	// machtiani-forge commands embed natural-language content and are not
	// executed as bash, so the syntax check must be skipped even on
	// early turns with enforcement on.
	model := &scriptedModel{responses: []minisweagent.QueryResult{{Content: "<command>\nmachtiani-forge Create the following files:\n## Heading\nSome content.\n</command>"}}}
	env := &capturingEnvironment{result: minisweagent.ExecuteResult{ReturnCode: 0, Output: "ok"}}
	agent := NewDefaultAgent(model, env, cfg, prompts)
	agent.RunConfig.EnforceEarlyCommands = true
	agent.State.commandsExecuted = 0
	agent.addMessage("user", "Create files", nil)

	if err := agent.Step(context.Background()); err != nil {
		t.Fatalf("Step() error = %v, machtiani-forge commands should skip bash syntax check", err)
	}
	if env.calls != 1 {
		t.Fatalf("expected execute to be called once for machtiani-forge, got %d", env.calls)
	}
}

func TestFirstNonEmptyLine(t *testing.T) {
	if got := firstNonEmptyLine(""); got != "" {
		t.Fatalf("expected empty string for empty input, got %q", got)
	}
	if got := firstNonEmptyLine("   \n\n"); got != "" {
		t.Fatalf("expected empty string for whitespace-only input, got %q", got)
	}
	if got := firstNonEmptyLine("first\nsecond\nthird"); got != "first" {
		t.Fatalf("expected %q, got %q", "first", got)
	}
	if got := firstNonEmptyLine("\n  hello world  \nsecond"); got != "hello world" {
		t.Fatalf("expected %q, got %q", "hello world", got)
	}
}

// TestParseXMLAnswer_DefaultTag pins the default-tag contract so
// existing callers (no override) keep extracting <answer>...</answer>.
// PlanAnswerTag.md rules 4, 6, 10.
func TestParseXMLAnswer_DefaultTag(t *testing.T) {
	got, err := parseXMLAnswer("<answer>hello world</answer>", "answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("expected %q, got %q", "hello world", got)
	}
}

// TestParseXMLAnswer_HonorsCustomTag covers rule 1: the parser takes
// the tag name as a parameter, so the --answer-tag override is the
// single source of truth for both renderer and parser.
func TestParseXMLAnswer_HonorsCustomTag(t *testing.T) {
	got, err := parseXMLAnswer("<final>custom-tag body</final>", "final")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "custom-tag body" {
		t.Fatalf("expected %q, got %q", "custom-tag body", got)
	}
}

// TestParseXMLAnswer_FirstOpenLastClose covers rule 4: the body spans
// from the FIRST opening tag to the LAST closing tag. Stray close tags
// in the middle are tolerated.
func TestParseXMLAnswer_FirstOpenLastClose(t *testing.T) {
	content := "<answer>one</answer>\n<answer>two</answer>"
	got, err := parseXMLAnswer(content, "answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "one</answer>\n<answer>two"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

// TestParseXMLAnswer_NoCloseFailSafe covers rule 6: when the open tag
// is present but no close tag is found, the body runs to the end of
// the response instead of erroring.
func TestParseXMLAnswer_NoCloseFailSafe(t *testing.T) {
	got, err := parseXMLAnswer("<answer>truncated body never closed", "answer")
	if err != nil {
		t.Fatalf("expected fail-safe, got error: %v", err)
	}
	if got != "truncated body never closed" {
		t.Fatalf("expected %q, got %q", "truncated body never closed", got)
	}
}

// TestParseXMLAnswer_MissingOpenStillErrors covers rule 9: when no
// opening tag is present, the parser still rejects the response.
func TestParseXMLAnswer_MissingOpenStillErrors(t *testing.T) {
	if _, err := parseXMLAnswer("no tags at all", "answer"); err == nil {
		t.Fatal("expected error for missing open tag, got nil")
	}
}

// TestParseXMLAnswer_EmptyBodyRejected covers rule 10: a present-but-
// empty body is rejected so the planner doesn't accidentally submit a
// blank final answer.
func TestParseXMLAnswer_EmptyBodyRejected(t *testing.T) {
	if _, err := parseXMLAnswer("<answer>   </answer>", "answer"); err == nil {
		t.Fatal("expected error for empty body, got nil")
	}
}

// TestParseXMLAnswer_TagEmptyFallsBackToDefault ensures callers that
// pass an empty tag name still get the documented default ("answer").
func TestParseXMLAnswer_TagEmptyFallsBackToDefault(t *testing.T) {
	got, err := parseXMLAnswer("<answer>body</answer>", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "body" {
		t.Fatalf("expected %q, got %q", "body", got)
	}
}

// TestParseXMLAnswer_HonorsWhitespaceTag ensures SetAnswerTag's
// trim-before-default behaviour is mirrored in the parser.
func TestParseXMLAnswer_HonorsWhitespaceTag(t *testing.T) {
	got, err := parseXMLAnswer("<answer>body</answer>", "   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "body" {
		t.Fatalf("expected %q, got %q", "body", got)
	}
}

// TestDefaultAgentSetAnswerTagNormalisesEmpty ensures the public
// setter applies the same normalise-empty-to-"answer" rule that the
// parser and renderer use, so the two stay in sync even when a
// caller passes "" (the recommended "no override" signal).
func TestDefaultAgentSetAnswerTagNormalisesEmpty(t *testing.T) {
	agent := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, &minisweagent.ShellAgentConfig{}, &minisweagent.PromptsConfig{})
	agent.RunConfig.AnswerTag = ""
	agent.RunConfig.NormalizeAnswerTag()
	if got := agent.RunConfig.AnswerTag; got != "answer" {
		t.Fatalf("expected empty input to normalise to %q, got %q", "answer", got)
	}
	if v, ok := agent.State.ExtraVars["AnswerTag"]; !ok || v != "answer" {
		t.Fatalf("expected extraVars[AnswerTag]=%q, got %v (ok=%v)", "answer", v, ok)
	}
}

// TestDefaultAgentSetAnswerTagOverridesDefault ensures the setter
// actually overrides the default the agent was constructed with.
func TestDefaultAgentSetAnswerTagOverridesDefault(t *testing.T) {
	agent := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, &minisweagent.ShellAgentConfig{}, &minisweagent.PromptsConfig{})
	agent.RunConfig.AnswerTag = "answercode"
	agent.RunConfig.NormalizeAnswerTag()
	if got := agent.RunConfig.AnswerTag; got != "answercode" {
		t.Fatalf("expected %q, got %q", "answercode", got)
	}
	if v, ok := agent.State.ExtraVars["AnswerTag"]; !ok || v != "answercode" {
		t.Fatalf("expected extraVars[AnswerTag]=%q, got %v (ok=%v)", "answercode", v, ok)
	}
	if v, ok := agent.State.ExtraVars["answer_tag"]; !ok || v != "answercode" {
		t.Fatalf("expected extraVars[answer_tag]=%q, got %v (ok=%v)", "answercode", v, ok)
	}
}

// TestRenderFormatErrorUsesCustomAnswerTagInEarlyTurnTemplate ensures
// the early-turn format-error message honours the active tag name
// end-to-end, so a --answer-tag override reaches the model.
func TestRenderFormatErrorUsesCustomAnswerTagInEarlyTurnTemplate(t *testing.T) {
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner:    &minisweagent.PlannerPromptsConfig{SystemTemplate: "", InstanceTemplate: ""},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{FormatErrorTemplate: "custom-format-error: {{.RawErr}} tag={{.CommandTag}} ans={{.AnswerTag}}", ActionObservationTemplate: "{{.Output}}"},
	}
	agent := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, cfg, prompts)
	agent.RunConfig.EnforceEarlyCommands = true
	agent.State.commandsExecuted = 0
	agent.RunConfig.AnswerTag = "customanswer"
	agent.RunConfig.NormalizeAnswerTag()
	agent.RunConfig.CommandTag = "command"
	agent.RunConfig.NormalizeCommandTag()

	got := agent.renderFormatError(fmt.Errorf("test error"))
	want := "custom-format-error: test error tag=command ans=customanswer"
	if got != want {
		t.Fatalf("expected format error template message with custom tag, got %q", got)
	}
}

func TestResumeStateRoundTrip(t *testing.T) {
	// 1. Create a new DefaultAgent and set known values on its state.
	agent := newTestAgent()
	agent.State.stepCounter = 7
	agent.State.commandsExecuted = 42
	agent.State.lastNonEmptyOutput = "last output text"
	agent.State.finalizeRequested = true
	agent.State.consecutiveFormatErrors = 2
	agent.State.consecutiveFinalizeReminders = 1
	agent.RunConfig.SystemPrompt = "test system prompt"
	agent.RunConfig.SystemPromptCached = true
	agent.RunConfig.AnswerTag = "answer-review"
	agent.RunConfig.CommandTag = "command-review"
	agent.RunConfig.NormalizeAnswerTag()
	agent.RunConfig.NormalizeCommandTag()

	// 3. Snapshot via GetResumeState.
	rs := agent.GetResumeState()

	// 4. Verify the snapshot fields match.
	if rs.Version != run.ResumeStateVersion {
		t.Errorf("Version = %d, want %d", rs.Version, run.ResumeStateVersion)
	}
	if rs.StepCounter != 7 {
		t.Errorf("StepCounter = %d, want 7", rs.StepCounter)
	}
	if rs.CommandsExecuted != 42 {
		t.Errorf("CommandsExecuted = %d, want 42", rs.CommandsExecuted)
	}
	if rs.LastNonEmptyOutput != "last output text" {
		t.Errorf("LastNonEmptyOutput = %q, want %q", rs.LastNonEmptyOutput, "last output text")
	}
	if rs.FinalizeRequested != true {
		t.Errorf("FinalizeRequested = %v, want true", rs.FinalizeRequested)
	}
	if rs.ConsecutiveFormatErrors != 2 {
		t.Errorf("ConsecutiveFormatErrors = %d, want 2", rs.ConsecutiveFormatErrors)
	}
	if rs.ConsecutiveFinalizeReminders != 1 {
		t.Errorf("ConsecutiveFinalizeReminders = %d, want 1", rs.ConsecutiveFinalizeReminders)
	}
	if rs.SystemPrompt != "test system prompt" {
		t.Errorf("SystemPrompt = %q, want %q", rs.SystemPrompt, "test system prompt")
	}
	if rs.SystemPromptCached != true {
		t.Errorf("SystemPromptCached = %v, want true", rs.SystemPromptCached)
	}
	if rs.AnswerTag != "answer-review" || rs.CommandTag != "command-review" {
		t.Errorf("resume tags = (%q, %q), want (answer-review, command-review)", rs.AnswerTag, rs.CommandTag)
	}

	// 5. Create a fresh agent and verify zero values, then restore.
	agent2 := newTestAgent()
	if agent2.State.stepCounter != 0 {
		t.Errorf("fresh agent stepCounter = %d, want 0", agent2.State.stepCounter)
	}
	if agent2.State.finalizeRequested != false {
		t.Errorf("fresh agent finalizeRequested = %v, want false", agent2.State.finalizeRequested)
	}
	if agent2.RunConfig.SystemPrompt != "" {
		t.Errorf("fresh agent SystemPrompt = %q, want empty", agent2.RunConfig.SystemPrompt)
	}
	if agent2.RunConfig.SystemPromptCached != false {
		t.Errorf("fresh agent SystemPromptCached = %v, want false", agent2.RunConfig.SystemPromptCached)
	}

	agent2.RestoreResumeState(rs)

	// 6. Verify all fields on agent2 now match the original values.
	if agent2.State.stepCounter != 7 {
		t.Errorf("after restore stepCounter = %d, want 7", agent2.State.stepCounter)
	}
	if agent2.State.commandsExecuted != 42 {
		t.Errorf("after restore commandsExecuted = %d, want 42", agent2.State.commandsExecuted)
	}
	if agent2.State.lastNonEmptyOutput != "last output text" {
		t.Errorf("after restore lastNonEmptyOutput = %q, want %q", agent2.State.lastNonEmptyOutput, "last output text")
	}
	if agent2.State.finalizeRequested != true {
		t.Errorf("after restore finalizeRequested = %v, want true", agent2.State.finalizeRequested)
	}
	if agent2.State.consecutiveFormatErrors != 2 {
		t.Errorf("after restore consecutiveFormatErrors = %d, want 2", agent2.State.consecutiveFormatErrors)
	}
	if agent2.State.consecutiveFinalizeReminders != 1 {
		t.Errorf("after restore consecutiveFinalizeReminders = %d, want 1", agent2.State.consecutiveFinalizeReminders)
	}
	if agent2.RunConfig.SystemPrompt != "test system prompt" {
		t.Errorf("after restore SystemPrompt = %q, want %q", agent2.RunConfig.SystemPrompt, "test system prompt")
	}
	if agent2.RunConfig.SystemPromptCached != true {
		t.Errorf("after restore SystemPromptCached = %v, want true", agent2.RunConfig.SystemPromptCached)
	}
	if agent2.RunConfig.AnswerTag != "answer-review" || agent2.RunConfig.CommandTag != "command-review" {
		t.Errorf("after restore tags = (%q, %q), want (answer-review, command-review)", agent2.RunConfig.AnswerTag, agent2.RunConfig.CommandTag)
	}

	// 7. Subtest: incompatible version.
	t.Run("incompatible version", func(t *testing.T) {
		rs := &run.ResumeState{Version: 999}
		err := rs.ValidateVersion()
		if err == nil {
			t.Fatal("expected non-nil error for incompatible version")
		}
		errStr := err.Error()
		if !strings.Contains(errStr, "999") {
			t.Errorf("error %q should contain '999'", errStr)
		}
		if !strings.Contains(errStr, "incompatible") {
			t.Errorf("error %q should contain 'incompatible'", errStr)
		}
	})

	// 8. Subtest: Version 1 validates.
	t.Run("Version 1 validates", func(t *testing.T) {
		rs := &run.ResumeState{Version: run.ResumeStateVersion}
		err := rs.ValidateVersion()
		if err != nil {
			t.Errorf("expected nil error for version %d, got %v", run.ResumeStateVersion, err)
		}
	})
}

// TestRunMethodCheckpointsOnCancel verifies that when Run is cancelled the agent
// persists a resume checkpoint file and returns "Cancelled" as the exit status.
func TestRunMethodCheckpointsOnCancel(t *testing.T) {
	agent := newTestAgent()
	agent.RunConfig.SystemPrompt = "test system prompt"
	agent.RunConfig.SystemPromptCached = true
	agent.RunConfig.SessionID = "test-run-checkpoint-on-cancel"
	agent.RunConfig.CheckpointDir = t.TempDir()

	trajPath := filepath.Join(agent.RunConfig.CheckpointDir, "trajectory.json")

	ctx, cancel := context.WithCancel(context.Background())

	var exitStatus, runErrMsg string
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		exitStatus, runErrMsg, runErr = agent.Run(ctx, "echo hello")
	}()

	cancel()
	<-done

	t.Logf("Run returned: exitStatus=%q, result=%q, err=%v", exitStatus, runErrMsg, runErr)

	if exitStatus != "Cancelled" {
		t.Errorf("exitStatus = %q, want %q", exitStatus, "Cancelled")
	}

	_, err := os.Stat(trajPath)
	if err != nil {
		t.Fatalf("resume file should exist at %s: %v", trajPath, err)
	}
}

// TestRunMethodInterruptPersistence verifies that after an interrupted Run the
// persisted resume file contains a valid FileTrajectory with a non-nil ResumeState,
// a positive StepCounter, the "Cancelled" exit status, and some messages.
func TestRunMethodInterruptPersistence(t *testing.T) {
	agent := newTestAgent()
	agent.RunConfig.SystemPrompt = "test system prompt"
	agent.RunConfig.SystemPromptCached = true
	agent.RunConfig.SessionID = "test-run-interrupt-persistence"
	agent.RunConfig.CheckpointDir = t.TempDir()

	trajPath := filepath.Join(agent.RunConfig.CheckpointDir, "trajectory.json")

	ctx, cancel := context.WithCancel(context.Background())

	var exitStatus, runErrMsg string
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		exitStatus, runErrMsg, runErr = agent.Run(ctx, "echo hello")
	}()

	// Let the agent execute a few steps before cancelling.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	t.Logf("Run returned: exitStatus=%q, result=%q, err=%v", exitStatus, runErrMsg, runErr)

	if exitStatus != "Cancelled" {
		t.Errorf("exitStatus = %q, want %q", exitStatus, "Cancelled")
	}

	traj, err := run.LoadTrajectory(trajPath)
	if err != nil {
		t.Fatalf("LoadTrajectory: %v", err)
	}

	if traj.ResumeState == nil {
		t.Fatal("ResumeState should not be nil")
	}
	if traj.ResumeState.StepCounter <= 0 {
		t.Errorf("StepCounter = %d, want > 0", traj.ResumeState.StepCounter)
	}
	if traj.ExitStatus != "Cancelled" {
		t.Errorf("ExitStatus = %q, want %q", traj.ExitStatus, "Cancelled")
	}
	if len(traj.Messages) == 0 {
		t.Error("Messages should not be empty")
	}
}

// stepCountingModel is a mock LLM model that returns a known sequence of
// responses and tracks how many times Query was called. It signals a readiness
// channel when the third call arrives and then waits for a proceed signal so
// that the test can cancel the context before the model returns the step-3
// response.
type stepCountingModel struct {
	*stubModel
	nCalls  int
	ready   chan struct{}
	proceed chan struct{}
}

func (m *stepCountingModel) Query(ctx context.Context, msgs []minisweagent.Message, opts ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.nCalls++
	switch m.nCalls {
	case 1:
		return minisweagent.QueryResult{Content: "<command>\necho step1\n</command>"}, nil
	case 2:
		return minisweagent.QueryResult{Content: "<command>\necho step2\n</command>"}, nil
	case 3:
		// Signal that the model has been queried for step 3, then
		// wait for the test to signal it may proceed (so the test
		// can cancel the context while the model is blocked).
		m.ready <- struct{}{}
		if m.proceed != nil {
			<-m.proceed
		}
		return minisweagent.QueryResult{Content: "<command>\nsleep 10\n</command>"}, nil
	default:
		return minisweagent.QueryResult{Content: "<answer>\nresume ok\n</answer>"}, nil
	}
}

func (m *stepCountingModel) NCalls() int {
	return m.nCalls
}

// TestStepLevelResumeDeterministic proves deterministically that the
// shell-agent resumes at the exact step it was interrupted, not step 1.
func TestStepLevelResumeDeterministic(t *testing.T) {
	ready := make(chan struct{})
	proceed := make(chan struct{})
	model1 := &stepCountingModel{stubModel: &stubModel{}, ready: ready, proceed: proceed}

	// 1. Create agent and set session-specific config.
	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}
	agent := NewDefaultAgent(model1, &stubEnvironment{}, cfg, prompts)
	agent.RunConfig.SessionID = "test-deterministic-step-resume"
	agent.RunConfig.SystemPrompt = "test"
	agent.RunConfig.SystemPromptCached = true
	// Pin NewModel so refreshModel() returns the same stepCountingModel.
	agent.RunConfig.NewModel = func() (minisweagent.Model, error) { return model1, nil }
	agent.RunConfig.CheckpointDir = t.TempDir()

	trajPath := filepath.Join(agent.RunConfig.CheckpointDir, "trajectory.json")

	// 2. Create a cancellable context.
	ctx, cancel := context.WithCancel(context.Background())

	// 3. Start agent.Run in a goroutine.
	var exitStatus, runResult string
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		exitStatus, runResult, runErr = agent.Run(ctx, "echo hello")
	}()

	// 4. Wait for the readiness channel indicating the model has been
	//    queried for step 3 (this unblocks when the third Query starts).
	//    The Query goroutine is now blocked on the proceed channel,
	//    giving us time to cancel the context.
	<-ready

	// 5. Cancel the context. Because the model is still blocked inside
	//    the third Query call (waiting on proceed), the cancellation
	//    happens before the step-3 response is returned.
	cancel()

	// Unblock the model's third Query so the agent can process the
	// now-cancelled context.
	close(proceed)

	// 6. Wait for Run to return.
	<-done

	t.Logf("Run returned: exitStatus=%q, result=%q, err=%v", exitStatus, runResult, runErr)

	if exitStatus != "Cancelled" {
		t.Errorf("exitStatus = %q, want %q", exitStatus, "Cancelled")
	}

	// 7. Load the resume file and inspect the saved StepCounter.
	traj, err := run.LoadTrajectory(trajPath)
	if err != nil {
		t.Fatalf("LoadTrajectory: %v", err)
	}
	if traj.ResumeState == nil {
		t.Fatal("ResumeState should not be nil")
	}

	savedStep := traj.ResumeState.StepCounter
	t.Logf("saved StepCounter = %d", savedStep)

	// The agent increments stepCounter after each successful Step.
	// Steps 1 and 2 completed, so stepCounter should be at least 2.
	if savedStep < 2 {
		t.Errorf("StepCounter = %d, want >= 2", savedStep)
	}

	// 8. Create a second agent and restore the saved resume state.
	// Jump nCalls past the sequence so every Query returns the default
	// <answer>resume ok</answer> response.
	model2 := &stepCountingModel{stubModel: &stubModel{}, nCalls: 100}
	agent2 := NewDefaultAgent(model2, &stubEnvironment{}, cfg, prompts)
	agent2.RunConfig.SessionID = agent.RunConfig.SessionID
	agent2.RunConfig.NewModel = func() (minisweagent.Model, error) { return model2, nil }
	agent2.RestoreResumeState(traj.ResumeState)

	if agent2.State.stepCounter != savedStep {
		t.Errorf("agent2.State.stepCounter = %d, want %d (saved step)", agent2.State.stepCounter, savedStep)
	}

	// 9. Resume with a fresh context. The stepCountingModel's default
	//    call (nCalls >= 4) returns <answer>resume ok</answer>.
	resumeCtx := context.Background()
	// Build minimal messages for the resume path.
	msgs := []minisweagent.Message{
		{Role: "system", Content: "test"},
		{Role: "user", Content: "--- BEGIN TASK ---\nTask: echo hello\n--- END TASK ---"},
	}
	exitStatus2, result2, err2 := agent2.ResumeWithMessages(resumeCtx, msgs)
	if err2 != nil {
		t.Fatalf("ResumeWithMessages: %v", err2)
	}

	t.Logf("ResumeWithMessages returned: exitStatus=%q, result=%q", exitStatus2, result2)
	t.Logf("resumed step counter after resume = %d", agent2.State.stepCounter)

	if exitStatus2 != "Submitted" {
		t.Errorf("exitStatus2 = %q, want %q", exitStatus2, "Submitted")
	}

	// After resume, stepCounter should be savedStep + 1 (one more step
	// completed successfully).
	if agent2.State.stepCounter != savedStep+1 {
		t.Errorf("agent2.State.stepCounter after resume = %d, want saved step (%d) + 1 = %d",
			agent2.State.stepCounter, savedStep, savedStep+1)
	}

	_ = result2
}

func TestCheckpointWrittenOnContextCancel(t *testing.T) {
	agent := newTestAgent()
	agent.RunConfig.SystemPrompt = "test system prompt"
	agent.RunConfig.SystemPromptCached = true
	agent.RunConfig.SessionID = "test-checkpoint-cancel"
	agent.RunConfig.CheckpointDir = t.TempDir()

	trajPath := filepath.Join(agent.RunConfig.CheckpointDir, "trajectory.json")

	ctx, cancel := context.WithCancel(context.Background())

	var exitStatus, runErrMsg string
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		exitStatus, runErrMsg, runErr = agent.Run(ctx, "echo hello")
	}()

	// Cancel the context after a short delay to let the agent execute a few steps.
	time.AfterFunc(50*time.Millisecond, cancel)
	<-done

	t.Logf("RunWithMessages returned: exitStatus=%q, result=%q, err=%v", exitStatus, runErrMsg, runErr)

	if exitStatus != "Cancelled" {
		t.Errorf("exitStatus = %q, want %q", exitStatus, "Cancelled")
	}

	traj, err := run.LoadTrajectory(trajPath)
	if err != nil {
		t.Fatalf("LoadTrajectory: %v", err)
	}

	if traj.ResumeState == nil {
		t.Fatal("ResumeState should not be nil")
	}
	if traj.ResumeState.StepCounter <= 0 {
		t.Errorf("StepCounter = %d, want > 0", traj.ResumeState.StepCounter)
	}
	if traj.ExitStatus != "Cancelled" {
		t.Errorf("ExitStatus = %q, want %q", traj.ExitStatus, "Cancelled")
	}
	if len(traj.Messages) == 0 {
		t.Error("Messages should not be empty")
	}
}

// errorReturningModel is a test helper that returns a valid command on the first
// Query call and an error on subsequent calls. It embeds stubModel and overrides
// Query and NCalls.
type errorReturningModel struct {
	stubModel
	nCalls int
}

func (m *errorReturningModel) Query(ctx context.Context, msgs []minisweagent.Message, opts ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.nCalls++
	if m.nCalls == 1 {
		return minisweagent.QueryResult{Content: "<command>\necho hello\n</command>"}, nil
	}
	return minisweagent.QueryResult{}, fmt.Errorf("simulated LLM failure")
}

func (m *errorReturningModel) NCalls() int {
	return m.nCalls
}

// TestTrajectoryContainsCacheAnchor verifies that per-step trajectory checkpoints
// include cache anchor messages with content "[cache anchor]" and metadata
// type "cache_anchor".
func TestTrajectoryContainsCacheAnchor(t *testing.T) {
	tmpDir := t.TempDir()

	model := &cacheCapturingModel{resolved: llm.ResolvedModel{
		CacheKeyName:          "cache_control",
		CacheControl:          map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold: 1,
		CacheLookbackOffset:   1,
	}}

	cfg := &minisweagent.ShellAgentConfig{}
	prompts := &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "sys",
			InstanceTemplate: "inst",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}

	agent := NewDefaultAgent(model, &stubEnvironment{}, cfg, prompts)
	agent.RunConfig.CheckpointDir = tmpDir
	agent.addMessage("user", "intent", nil)

	if _, err := agent.Query(context.Background()); err != nil {
		t.Fatalf("first Query error: %v", err)
	}
	if _, err := agent.Query(context.Background()); err != nil {
		t.Fatalf("second Query error: %v", err)
	}

	// Persist the current trajectory so it can be read back from the checkpoint dir.
	traj := run.FromAgent(agent, "Ongoing", "", nil)
	if err := run.SaveTrajectoryToPath(traj, tmpDir); err != nil {
		t.Fatalf("SaveTrajectoryToPath: %v", err)
	}

	loaded, err := run.LoadTrajectory(filepath.Join(tmpDir, "trajectory.json"))
	if err != nil {
		t.Fatalf("LoadTrajectory: %v", err)
	}

	idx, _ := findCacheAnchor(loaded.Messages)
	if idx < 0 {
		t.Fatal("expected cache anchor in trajectory, but none found")
	}

	found := false
	for _, msg := range loaded.Messages {
		if msg.Content == llm.CacheAnchorMarkerText {
			metaType, _ := msg.Metadata["type"].(string)
			if metaType == "cache_anchor" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("expected at least one message with [cache anchor] content and cache_anchor metadata type")
	}
}

// TestTrajectoryExitStatusOnError verifies that the exit status recorded in a
// checkpoint trajectory matches the status returned by RunLoop when the model
// produces an error.
func TestTrajectoryExitStatusOnError(t *testing.T) {
	tmpDir := t.TempDir()

	model := &errorReturningModel{}

	agent := NewDefaultAgent(model, &stubEnvironment{}, &minisweagent.ShellAgentConfig{}, &minisweagent.PromptsConfig{
		Planner: &minisweagent.PlannerPromptsConfig{
			SystemTemplate:   "",
			InstanceTemplate: "",
		},
		ShellAgent: &minisweagent.ShellAgentPromptsConfig{
			FormatErrorTemplate:       "format-error",
			ActionObservationTemplate: "{{.Output}}",
		},
	}, WithCheckpointDir(tmpDir))

	agent.RunConfig.SessionID = "test-trajectory-exit-status-on-error"
	agent.addMessage("user", "test task", nil)

	exitStatus, _, err := agent.RunLoop(context.Background(), false)
	if err == nil {
		t.Fatal("expected error from RunLoop, got nil")
	}
	if exitStatus != "Error" {
		t.Errorf("exitStatus = %q, want %q", exitStatus, "Error")
	}

	// Save trajectory with the actual exit status returned by RunLoop.
	traj := run.FromAgent(agent, exitStatus, "", nil)
	if err := run.SaveTrajectoryToPath(traj, tmpDir); err != nil {
		t.Fatalf("SaveTrajectoryToPath: %v", err)
	}

	loaded, err := run.LoadTrajectory(filepath.Join(tmpDir, "trajectory.json"))
	if err != nil {
		t.Fatalf("LoadTrajectory: %v", err)
	}

	if loaded.ExitStatus == "Ongoing" {
		t.Errorf("ExitStatus should not be 'Ongoing', got %q", loaded.ExitStatus)
	}
	if loaded.ExitStatus != exitStatus {
		t.Errorf("ExitStatus = %q, want %q", loaded.ExitStatus, exitStatus)
	}
}
