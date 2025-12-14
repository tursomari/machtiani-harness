{{- if .IncludeHistory -}}
Conversation History:
{{- range .History }}
{{.Index}}. {{.DisplayRole}}{{if .Files}} (Files: {{join .Files ", "}}){{end}}:
{{.Content}}
{{- end}}
Current Request:
{{.UserPrompt}}
{{- else -}}
{{.UserPrompt}}
{{- end }}
