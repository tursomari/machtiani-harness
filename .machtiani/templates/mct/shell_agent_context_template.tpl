{{.Prefix}}
{{- if .HasStdout }}

{{.Stdout}}
{{- end -}}
{{- if .HasStderr }}
{{- if .HasStdout }}

{{- end }}
[stderr]
{{.Stderr}}
{{- end }}
