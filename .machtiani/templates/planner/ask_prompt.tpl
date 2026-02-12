You are generating the next Ask for mct. This step is Ask-only; do not choose Patch or Finalize here.
Ask is for questions, file reads, or repository understanding. Ask is NOT for changing, updating, deleting, or patching files. If edits are needed, you will have another chance to choose Patch after this ask.

Choose an ask mode:
- no-shell: use repository context and file indexing; do NOT require shell commands (no ls, grep, git diff, tests). Use no-shell for file reads (e.g., "show full README.md"). Never ask to run cat/sed/less/head/tail to view a file.
- shell: requires shell commands (finding paths, searching with grep, git diff, running tests). Shell results are summarized; do NOT ask for verbatim command output—ask for specific facts or a brief report.
- both: only if you need two separate asks; you must restate as two explicit lines.

Output exactly:
- If Ask Mode is no-shell or shell:
  Ask Mode: no-shell|shell
  Ask: <single prompt to mct>
- If Ask Mode is both:
  Ask Mode: both
  No-shell: <prompt to mct that requires no shell commands>
  Shell: <prompt to mct that requires shell commands>

Examples (illustrative only; not specific to this repository):
Ask Mode: no-shell
Ask: Example only — Explain how configuration settings are loaded and used by the app.
Ask Mode: no-shell
Ask: Example only — Show the full README.md and summarize which sections contain HTML.
Ask Mode: shell
Ask: Example only — Run `grep -n "TODO" -r .` and summarize the matching files.
Ask Mode: both
No-shell: Example only — Explain the auth flow and which modules own it.
Shell: Example only — Run `git diff --stat` to see recent changes.

{{- if .Guardrail }}
Guardrail:
{{.Guardrail}}
{{- end }}

Ask request:
{{.AskRequest}}
