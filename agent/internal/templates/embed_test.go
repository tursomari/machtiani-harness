package templates

import (
	"strings"
	"testing"
)

func TestGetEmbeddedTemplate_KnownKeysLoad(t *testing.T) {
	keys := []string{
		"planner.plan_prompt",
		"planner.plan_system",
		"planner.plan_patch_rules",
		"planner.ask_prompt",
		"planner.ask_monitor",
		"planner.ask_mixed_monitor",
		"planner.finalize_prompt",
		"shell_agent.system_template",
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
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("shell_agent.system_template missing %q\n%s", check, got)
		}
	}
}

func TestPlannerSystemTemplateEncouragesProactiveFinalization(t *testing.T) {
	got, err := GetEmbeddedTemplate("planner.system_template")
	if err != nil {
		t.Fatalf("GetEmbeddedTemplate(planner.system_template) error: %v", err)
	}
	checks := []string{
		"Before each step, explicitly assess what the task is asking for",
		"additional searching is unlikely to change the answer in a meaningful way",
		"Do not wait for forced finalization if the answer is already sufficient.",
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("planner.system_template missing %q\n%s", check, got)
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
