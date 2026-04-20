Use the conversation above to choose the next planner action.
{{- if .PatchIntro }}
{{.PatchIntro}}
{{- end }}
Decision menu (choose exactly one):
- Ask: ask questions, request file reads, or request commands (e.g., `git diff`, `grep`). Ask does NOT change files.
- Patch: change or update files.
- Finalize: only when the goal is complete.

Patch bias: If the goal clearly requires edits and the transcript already includes the relevant file content (full file or snippets), choose Patch rather than another Ask.

Examples (illustrative only; not specific to this repository):
- Example only — Ask: "Show where configuration settings are loaded and summarize the flow."
- Example only — Ask (shell): "Run `grep -n \"TODO\" -r .` and report the matching files."
- Example only — Patch: "Update error handling in path/to/file.go."
- Example only — Finalize: "Summarize findings and remaining risks."

Output format (exactly one line, no extra text):
Begin your reply immediately with `Decision:` — no leading commentary.
{{- if and .PatchEnabled .AllowFinalize }}
Decision: ask|patch|finalize
{{- else if .PatchEnabled }}
Decision: ask|patch
{{- else if .AllowFinalize }}
Decision: ask|finalize
{{- else }}
Decision: ask
{{- end }}

This step is decision-only; do NOT include the ask prompt here.

{{- if and .PatchEnabled .ForceRepatch }}

<repatch>
  <trigger>If the guard reports `Skipping patch because all target files were already updated earlier this session`</trigger>
  <instruction>Include `metadata.force_repatch: true` on the next patch.</instruction>
</repatch>

{{- end }}
Step {{.Step}} of {{.MaxSteps}}. Decide.
