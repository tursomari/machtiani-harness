package templates

import "testing"

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
