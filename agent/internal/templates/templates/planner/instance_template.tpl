Task: {{.Task}}

Machine: {{.Machine}}
Available steps: {{.StepLimit}}

Before choosing the next step, decide which of these modes applies:
1. Conclude now if the task is already answerable from the evidence gathered so far.
2. Run one concrete command if that command is likely to materially reduce a specific uncertainty.
3. Conclude with partial findings if further commands are unlikely to add meaningful new evidence.

Return exactly one fenced Bash command (```bash ... ```) that the worker can execute without additional interpretation, unless you are concluding.
The command must be a single line.
If you need to run in a subdirectory, chain it explicitly (cd path/to/dir && <command>) because each step starts in the project root.
If a safe single command is impossible, emit a fenced echo/printf that explains the limitation instead of inventing extra steps.
Avoid exploratory commands with no clear hypothesis, repeating similar searches without new information, or continuing after the task has already been sufficiently answered.
When you intend to conclude, respond with the final answer directly starting with "## Answer" and present it as claim-by-claim findings where each substantive claim begins with "Confidence: <0-100>% - "; do not use a single overall confidence score.
