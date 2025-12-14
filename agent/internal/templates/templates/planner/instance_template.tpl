Task: {{.Task}}

Machine: {{.Machine}}
Available steps: {{.StepLimit}}

Return exactly one fenced Bash command (```bash ... ```) that the worker can execute without additional interpretation.
The command must be a single line.
If you need to run in a subdirectory, chain it explicitly (cd path/to/dir && <command>) because each step starts in the project root.
If a safe single command is impossible, emit a fenced echo/printf that explains the limitation instead of inventing extra steps.
When you intend to conclude, emit the exact command that writes the final answer to {{.FinalMarkerPath}} and finishes.
