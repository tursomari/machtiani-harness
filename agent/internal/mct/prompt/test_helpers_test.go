package prompt

import "github.com/tursomari/machtiani/agent/internal/llm"

func testPromptsConfig() *llm.MCTPromptsConfig {
	return &llm.MCTPromptsConfig{
		ShellAgentContextPrefix: "Here is possibly relevant information from the shell agent.",
		HeaderUserTemplate:      "# User\n\n{{.Combined}}\n\n# Assistant\n\n",
		HeaderExistingTemplate:  "{{.Combined}}\n# Assistant\n\n",
		ConversationHistoryTemplate: `{{- if .IncludeHistory -}}
Conversation History:
{{- range .History }}
{{.Index}}. {{.DisplayRole}}{{if .Files}} (Files: {{join .Files ", "}}){{end}}:
{{.Content}}{{- end}}
Current Request:
{{.UserPrompt}}
{{- else -}}
{{.UserPrompt}}
{{- end }}`,
		ShellAgentContextTemplate: `{{.Prefix}}{{if .HasStdout}}

{{.Stdout}}{{end}}{{if .HasStderr}}{{if .HasStdout}}

{{end}}[stderr]
{{.Stderr}}{{end}}`,
		ReadmeSystemTemplate: "Test README system prompt.",
	}
}
