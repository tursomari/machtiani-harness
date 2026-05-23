package templates

import (
	"strings"
	"testing"
)

func TestGetEmbeddedTemplate_KnownKeysLoad(t *testing.T) {
	keys := []string{
		"planner.plan_prompt",
		"planner.plan_system",
		"planner.ask_prompt",
		"planner.ask_mixed_monitor",
		"planner.finalize_prompt",
		"shell_agent.system_template",
		"shell_agent.instance_template",
		"shell_agent.lightweight_system_template",
		"shell_agent.lightweight_intent_template",
		"mct.header_user",
		"mct.conversation_history_template",
		"file_discovery.system_prompt_template",
	}
	for _, key := range keys {
		key := key
		t.Run(key, func(t *testing.T) {
			got, err := GetEmbeddedTemplate(key)
			if err != nil {
				t.Fatalf("GetEmbeddedTemplate(%q) error: %v", key, err)
			}
			if got == "" {
				t.Fatalf("GetEmbeddedTemplate(%q) returned empty", key)
			}
		})
	}
}

func TestGetEmbeddedTemplate_UnknownKeyErrors(t *testing.T) {
	if _, err := GetEmbeddedTemplate("nope"); err == nil {
		t.Fatalf("expected error")
	}
}

func TestShellAgentSystemTemplateEncouragesProactiveFinalization(t *testing.T) {
	got, err := GetEmbeddedTemplate("shell_agent.system_template")
	if err != nil {
		t.Fatalf("GetEmbeddedTemplate(shell_agent.system_template) error: %v", err)
	}
	checks := []string{
		"Before each step, explicitly assess what the task is asking for",
		"additional searching is unlikely to change the answer in a meaningful way",
		"Do not wait for forced finalization if the answer is already sufficient.",
		"Present the answer as a short list of substantive claims.",
		"Confidence: <0-100>% - ",
		"Do not provide a single overall confidence score",
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("shell_agent.system_template missing %q\n%s", check, got)
		}
	}
}

func TestShellAgentInternalPlannerSystemTemplateEncouragesProactiveFinalization(t *testing.T) {
	got, err := GetEmbeddedTemplate("shell_agent.internal_planner_system")
	if err != nil {
		t.Fatalf("GetEmbeddedTemplate(shell_agent.internal_planner_system) error: %v", err)
	}
	checks := []string{
		"Before each step, explicitly assess what the task is asking for",
		"additional searching is unlikely to change the answer in a meaningful way",
		"Do not wait for forced finalization if the answer is already sufficient.",
		"Present the answer as a short list of substantive claims.",
		"Confidence: <0-100>% - ",
		"Do not provide a single overall confidence score",
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("shell_agent.internal_planner_system missing %q\n%s", check, got)
		}
	}
}

func TestPlannerInstanceTemplateEncouragesStopContinueDecision(t *testing.T) {
	got, err := GetEmbeddedTemplate("planner.instance_template")
	if err != nil {
		t.Fatalf("GetEmbeddedTemplate(planner.instance_template) error: %v", err)
	}
	checks := []string{
		"Conclude now if the task is already answerable",
		"Run one concrete command if that command is likely to materially reduce a specific uncertainty.",
		"Conclude with partial findings if further commands are unlikely to add meaningful new evidence.",
		"Avoid exploratory commands with no clear hypothesis",
		"Confidence: <0-100>% - ",
		"do not use a single overall confidence score",
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("planner.instance_template missing %q\n%s", check, got)
		}
	}
}

func TestShellAgentObservationTemplateMentionsDiminishingReturns(t *testing.T) {
	got, err := GetEmbeddedTemplate("shell_agent.action_observation_template")
	if err != nil {
		t.Fatalf("GetEmbeddedTemplate(shell_agent.action_observation_template) error: %v", err)
	}
	checks := []string{
		"Repeated empty outputs",
		"repeated errors",
		"repeated searches that do not produce new evidence",
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("shell_agent.action_observation_template missing %q\n%s", check, got)
		}
	}
}

func TestShellAgentFinalAnswerTemplatesRequireConfidenceScore(t *testing.T) {
	checks := map[string]string{
		"shell_agent.instance_template":           "Confidence: <0-100>% - ",
		"shell_agent.lightweight_system_template": "Confidence: <0-100>% - ",
		"shell_agent.lightweight_intent_template": "Confidence: <0-100>% - ",
	}
	for key, check := range checks {
		got, err := GetEmbeddedTemplate(key)
		if err != nil {
			t.Fatalf("GetEmbeddedTemplate(%s) error: %v", key, err)
		}
		if !strings.Contains(got, check) {
			t.Fatalf("%s missing %q\n%s", key, check, got)
		}
	}
}

func TestShellAgentInternalPlannerSystemDoNotHardcodeReadOnlyPolicy(t *testing.T) {
	banned := map[string]string{
		"shell_agent.internal_planner_system":                 "Assume read-only intent unless the task clearly authorises a write, and keep writes minimal.",
		"shell_agent.lightweight_system_template": "Prefer read-only commands. When the instruction explicitly requests a write, touch only the files mentioned.",
	}
	for key, text := range banned {
		got, err := GetEmbeddedTemplate(key)
		if err != nil {
			t.Fatalf("GetEmbeddedTemplate(%s) error: %v", key, err)
		}
		if strings.Contains(got, text) {
			t.Fatalf("%s still contains retired hardcoded write policy %q\n%s", key, text, got)
		}
	}
}

// TestShellAgentTemplatesDoNotUseFencedBlockCommandFormat is a regression check
// introduced after commit 46d5e8c. That commit moved shell-agent command
// formatting from fenced-block syntax (```bash) to XML command tags,
// but the old format persisted in an overlooked internal planner prompt.
// This test verifies that the fenced-block format has not been accidentally
// reintroduced in any of the three shell-agent system prompts.
func TestShellAgentTemplatesDoNotUseFencedBlockCommandFormat(t *testing.T) {
	keys := []string{
		"shell_agent.system_template",
		"shell_agent.lightweight_system_template",
		"shell_agent.internal_planner_system",
	}
	for _, key := range keys {
		got, err := GetEmbeddedTemplate(key)
		if err != nil {
			t.Fatalf("GetEmbeddedTemplate(%s) error: %v", key, err)
		}
		if strings.Contains(got, "fenced Bash") {
			t.Fatalf("%s contains banned substring %q\n%s", key, "fenced Bash", got)
		}
		if strings.Contains(got, "```bash") {
			t.Fatalf("%s contains banned substring %q\n%s", key, "```bash", got)
		}
	}
}
