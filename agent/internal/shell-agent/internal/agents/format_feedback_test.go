package agents

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func TestDefaultAndFallbackFormatFeedbackAreIdentical(t *testing.T) {
	a := NewDefaultAgent(&stubModel{}, &stubEnvironment{}, &minisweagent.ShellAgentConfig{}, &minisweagent.PromptsConfig{}, WithCommandTag("command-test"), WithAnswerTag("answer-test"))
	defaultTemplate := a.State.Prompts.ShellAgent.FormatErrorTemplate
	for count := 0; count < 3; count++ {
		a.State.consecutiveFormatErrors = count
		a.State.Prompts.ShellAgent.FormatErrorTemplate = defaultTemplate
		want := a.renderFormatError(fmt.Errorf("specific rejection reason"))
		for _, tmpl := range []string{"", "{{.Missing}}", "{{.Broken", "  "} {
			a.State.Prompts.ShellAgent.FormatErrorTemplate = tmpl
			if got := a.renderFormatError(fmt.Errorf("specific rejection reason")); got != want {
				t.Fatalf("fallback %q differs from default:\n%s\n%s", tmpl, got, want)
			}
		}
		for _, part := range []string{"specific rejection reason", fmt.Sprintf("%d of 3", count+1), "<command-test>", "<answer-test>", "No command from that response was executed."} {
			if !strings.Contains(want, part) {
				t.Fatalf("feedback missing %q: %s", part, want)
			}
		}
	}
}

func TestFormatFeedbackCountsRejectionsAndResetsAfterCommand(t *testing.T) {
	m := &scriptedModel{responses: []minisweagent.QueryResult{
		{Content: "bad"}, {Content: "bad"}, {Content: "<command>echo ok</command>"},
		{Content: "bad"}, {Content: "bad"}, {Content: "bad"},
	}}
	e := &capturingEnvironment{}
	a := NewDefaultAgent(m, e, &minisweagent.ShellAgentConfig{}, &minisweagent.PromptsConfig{})
	status, _, err := a.RunLoop(context.Background(), false)
	if status != "FormatErrorLoop" || err == nil || e.calls != 1 {
		t.Fatalf("status = %s, err = %v, executions = %d", status, err, e.calls)
	}
	wantCounts := []int{1, 2, 1, 2, 3}
	var feedback []string
	for _, msg := range a.Messages() {
		if msg.Role == "user" && strings.HasPrefix(msg.Content, "Your previous response was rejected.") {
			feedback = append(feedback, msg.Content)
		}
	}
	if len(feedback) != len(wantCounts) {
		t.Fatalf("feedback = %v", feedback)
	}
	for i, count := range wantCounts {
		if !strings.Contains(feedback[i], fmt.Sprintf("%d of 3", count)) {
			t.Fatalf("feedback %d: %s", i, feedback[i])
		}
	}
}
