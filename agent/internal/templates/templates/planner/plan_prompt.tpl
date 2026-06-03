Use the conversation above to choose the next planner action.
Decision menu (choose exactly one):
- AskWorker: delegate a sub-task to a worker agent for investigation or execution.
- AskUser: direct a question to the user when input or clarification is needed.
- AnswerUser: provide the final answer to the user, only when the goal is fully complete.

Examples (illustrative only; not specific to this repository):
- Example only — AskWorker: "Show where configuration settings are loaded and summarize the flow."
- Example only — AskWorker: "Run `grep -n \"TODO\" -r .` and report the matching files."
- Example only — AnswerUser: "Summarize findings and remaining risks."

Output format (exactly one line, no extra text):
Begin your reply immediately with `Decision:` — no leading commentary.
{{- if .AllowFinalize }}
Decision: ask_worker|ask_user|answer_user
{{- else }}
Decision: ask_worker
{{- end -}}
This step is decision-only; do NOT include the ask prompt here.

Step {{.Step}} of {{.MaxSteps}}. Decide.