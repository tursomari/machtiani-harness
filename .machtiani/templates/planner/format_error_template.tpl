Please respond with the planner format:
{{- if and .PatchEnabled .AllowFinalize }}
Decision: ask|patch|finalize
{{- else if .PatchEnabled }}
Decision: ask|patch
{{- else if .AllowFinalize }}
Decision: ask|finalize
{{- else }}
Decision: ask
{{- end }}
If Decision is ask, add exactly one follow-up line beginning with Question:, Instruction:, or Message: followed by the prompt.
