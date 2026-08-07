package shellagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/agents"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func TestAgentCommandReviewerPersistsAndContinuesHistory(t *testing.T) {
	model := &supervisorScriptedModel{responses: []string{
		`<answer>{"disposition":"continue","summary":"build output is advancing"}</answer>`,
		`<answer>{"disposition":"cancel","summary":"daemon is wedged"}</answer>`,
	}}
	root := t.TempDir()
	reviewer := newAgentCommandReviewer(Request{
		Config:    &minisweagent.ShellAgentConfig{CommandSupervisorMaxSteps: 20},
		Prompts:   &minisweagent.PromptsConfig{},
		Model:     model,
		Env:       &stubEnvForCounting{},
		SessionID: "parent-session",
	}, root)

	first := supervisorReviewRequest(1)
	decision, err := reviewer.Review(context.Background(), first)
	if err != nil {
		t.Fatalf("first Review: %v", err)
	}
	if decision.Disposition != agents.CommandDispositionContinue {
		t.Fatalf("first decision = %+v", decision)
	}

	second := supervisorReviewRequest(2)
	second.Output.Output = "more output"
	decision, err = reviewer.Review(context.Background(), second)
	if err != nil {
		t.Fatalf("second Review: %v", err)
	}
	if decision.Disposition != agents.CommandDispositionCancel {
		t.Fatalf("second decision = %+v", decision)
	}
	if len(model.captured) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.captured))
	}
	secondMessages := model.captured[1]
	if !messagesContain(secondMessages, `"disposition":"continue"`) {
		t.Fatal("second review did not retain the first review decision")
	}
	if !messagesContain(secondMessages, "more output") {
		t.Fatal("second review did not append current command state")
	}
	trajectory := filepath.Join(root, "command-supervisors", "3", "trajectory.json")
	if _, err := os.Stat(trajectory); err != nil {
		t.Fatalf("supervisor trajectory %s: %v", trajectory, err)
	}
}

func TestAgentCommandReviewerRejectsMalformedDisposition(t *testing.T) {
	model := &supervisorScriptedModel{responses: []string{`<answer>{"summary":"forgot the decision"}</answer>`}}
	reviewer := newAgentCommandReviewer(Request{
		Config:  &minisweagent.ShellAgentConfig{CommandSupervisorMaxSteps: 20},
		Prompts: &minisweagent.PromptsConfig{},
		Model:   model,
		Env:     &stubEnvForCounting{},
	}, t.TempDir())

	if _, err := reviewer.Review(context.Background(), supervisorReviewRequest(1)); err == nil {
		t.Fatal("malformed supervisor answer should fail the review")
	}
}

func TestCommandReviewPromptIncludesProcessDeadlineAndAuthority(t *testing.T) {
	request := supervisorReviewRequest(4)
	request.Reason = agents.CommandReviewDeadline
	prompt := commandReviewPrompt(request)
	for _, expected := range []string{
		"PID: 4242",
		"process group ID: 4242",
		request.Deadline.Format(time.RFC3339),
		"15m0s",
		"captured bytes: 7",
		"total bytes: 11",
		"You may inspect, repair, signal, or gracefully stop",
		"will be killed at the absolute deadline",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("review prompt missing %q:\n%s", expected, prompt)
		}
	}
}

func supervisorReviewRequest(review int) agents.CommandReviewRequest {
	started := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	return agents.CommandReviewRequest{
		Command:        "go test ./...",
		CommandNumber:  3,
		ReviewNumber:   review,
		Reason:         agents.CommandReviewRegular,
		PID:            4242,
		ProcessGroupID: 4242,
		StartedAt:      started,
		Deadline:       started.Add(24 * time.Hour),
		Remaining:      15 * time.Minute,
		Output: minisweagent.CommandOutputSnapshot{
			Output:        "working",
			CapturedBytes: 7,
			TotalBytes:    11,
			OverflowBytes: 4,
			UpdatedAt:     started.Add(time.Minute),
		},
	}
}

type supervisorScriptedModel struct {
	responses []string
	captured  [][]minisweagent.Message
}

func (m *supervisorScriptedModel) Config() interface{} { return &minisweagent.ModelConfig{} }
func (m *supervisorScriptedModel) Cost() float64       { return 0 }
func (m *supervisorScriptedModel) NCalls() int         { return len(m.captured) }
func (m *supervisorScriptedModel) GetTemplateVars() map[string]interface{} {
	return map[string]interface{}{}
}
func (m *supervisorScriptedModel) Query(_ context.Context, messages []minisweagent.Message, _ ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.captured = append(m.captured, append([]minisweagent.Message(nil), messages...))
	return minisweagent.QueryResult{Content: m.responses[len(m.captured)-1]}, nil
}

func messagesContain(messages []minisweagent.Message, needle string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}
