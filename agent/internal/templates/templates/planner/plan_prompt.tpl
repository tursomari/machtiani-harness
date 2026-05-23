Use the conversation above to choose the next planner action.
Decision menu (choose exactly one):
- Ask: ask questions, request file reads, or request commands (e.g., `git diff`, `grep`). Ask does NOT change files.
- Finalize: only when the goal is complete.

Examples (illustrative only; not specific to this repository):
- Example only — Ask: "Show where configuration settings are loaded and summarize the flow."
- Example only — Ask (shell): "Run `grep -n \"TODO\" -r .` and report the matching files."
- Example only — Finalize: "Summarize findings and remaining risks."

Output format (exactly one line, no extra text):
Begin your reply immediately with `Decision:` — no leading commentary.
{{- if .AllowFinalize }}
Decision: ask|finalize
{{- else }}
Decision: ask
{{- end }}

This step is decision-only; do NOT include the ask prompt here.

Step {{.Step}} of {{.MaxSteps}}. Decide.