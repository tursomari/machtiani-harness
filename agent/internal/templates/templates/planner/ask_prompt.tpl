You are generating the next Ask for mct. This step is Ask-only; do not choose Patch or Finalize here.

Choose an ask mode:
- no-shell: use repository context and file indexing; do NOT require shell commands (no ls, grep, git diff, tests). Use no-shell for repository understanding, explanations, summaries, and targeted questions about files. Never ask to run cat/sed/less/head/tail to view a file, and do not ask for full files or large verbatim code snippets. You may also ask for code changes using high-level natural language — describe the outcome you want and the agent handles the implementation. This works like Codex, Claude Code, or Aider: you specify what should change and the agent figures out how.
- shell: requires shell commands (finding paths, searching with grep, git diff, running tests). The shell agent never returns verbatim stdout/stderr; it only provides summaries/reports. Do NOT ask for verbatim command output—ask for specific facts or a brief report, or use no-shell for exact file content. If a request would previously have needed both repository understanding and shell work, choose shell and restate it as one combined ask. When you ask for a shell command, you may also request code changes in the same ask.

When constructing the ask, prefer explanations and targeted questions over requesting complete file contents. The `shell-agent` must spend output tokens to answer, so avoid asking for full files or large verbatim snippets; request only minimal snippets when truly necessary.

Output exactly:
- Ask Mode: no-shell|shell
- Ask: <single prompt to mct>

Examples (illustrative only; not specific to this repository):
Ask Mode: no-shell
Ask: Example only — Explain how configuration settings are loaded and used by the app.
Ask Mode: no-shell
Ask: Example only — Explain what sections the README.md contains and which contain HTML.
Ask Mode: no-shell
Ask: Example only — Refactor the error handling in the API handlers to return structured JSON errors instead of plain text.
Ask Mode: shell
Ask: Example only — Run `rg -n "TODO" .` and summarize the matching files.
Ask Mode: shell
Ask: Example only — Explain the auth flow and which modules own it, then run `git diff --stat` to see recent changes.
Ask Mode: shell
Ask: Example only — Run `rg -n "oldAPI"` to find all callers, then refactor them to use the new interface.

{{- if .Guardrail }}
Guardrail:
{{.Guardrail}}
{{- end }}

Ask request:
{{.AskRequest}}
