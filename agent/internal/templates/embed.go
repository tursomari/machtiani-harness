package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed templates/planner/* templates/shell-agent/* templates/mct/* templates/file-discovery/*
var embeddedTemplates embed.FS

var templateMap = map[string]string{
	// Planner
	"planner.system_template":            "templates/planner/system_template.tpl",
	"planner.instance_template":          "templates/planner/instance_template.tpl",
	"planner.timeout_template":           "templates/planner/timeout_template.tpl",
	"planner.format_error_template":      "templates/planner/format_error_template.tpl",
	"planner.plan_system":                "templates/planner/plan_system.tpl",
	"planner.plan_prompt":                "templates/planner/plan_prompt.tpl",
	"planner.ask_prompt":                 "templates/planner/ask_prompt.tpl",
	"planner.ask_mixed_monitor":          "templates/planner/ask_mixed_monitor.tpl",
	"planner.ask_user_directed_monitor":  "templates/planner/ask_user_directed_monitor.tpl",
	"planner.ask_user_directed_purifier": "templates/planner/ask_user_directed_purifier.tpl",
	"planner.finalize_prompt":            "templates/planner/finalize_prompt.tpl",
	"planner.review_prompt":              "templates/planner/review_prompt.tpl",

	// Shell-agent
	"shell_agent.system_template":             "templates/shell-agent/system_template.tpl",
	"shell_agent.instance_template":           "templates/shell-agent/instance_template.tpl",
	"shell_agent.timeout_template":            "templates/shell-agent/timeout_template.tpl",
	"shell_agent.format_error_template":       "templates/shell-agent/format_error_template.tpl",
	"shell_agent.action_observation_template": "templates/shell-agent/action_observation_template.txt",
	"shell_agent.lightweight_system_template": "templates/shell-agent/lightweight_system_template.txt",
	"shell_agent.lightweight_intent_template": "templates/shell-agent/lightweight_intent_template.txt",
	"shell_agent.lightweight_error_template":  "templates/shell-agent/lightweight_error_template.txt",

	// MCT
	"mct.header_user":                   "templates/mct/header_user_template.tpl",
	"mct.header_existing":               "templates/mct/header_existing_template.tpl",
	"mct.shell_agent_context_prefix":    "templates/mct/shell_agent_context_prefix.tpl",
	"mct.shell_agent_context_template":  "templates/mct/shell_agent_context_template.tpl",
	"mct.shell_agent_prompt_notice":     "templates/mct/shell_agent_prompt_notice.tpl",
	"mct.conversation_history_template": "templates/mct/conversation_history_template.tpl",
	"mct.readme_system_template":        "templates/mct/readme_system_template.tpl",

	// File discovery
	"file_discovery.system_prompt_template": "templates/file-discovery/system_prompt_template.tpl",
}

func GetEmbeddedTemplate(key string) (string, error) {
	path, ok := templateMap[key]
	if !ok {
		return "", fmt.Errorf("unknown embedded template key: %s", key)
	}

	data, err := fs.ReadFile(embeddedTemplates, path)
	if err != nil {
		return "", fmt.Errorf("read embedded template %s (%s): %w", key, path, err)
	}
	return strings.TrimSpace(string(data)), nil
}
