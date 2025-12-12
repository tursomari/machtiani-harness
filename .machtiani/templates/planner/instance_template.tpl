Task: {{.Task}}

Machine: {{.Machine}}
Available steps: {{.StepLimit}}

Return exactly one natural-language action that maps to a single Bash command the worker can execute without additional interpretation.
Mention the working directory and any relevant files or filters if they matter.
If a safe single command is impossible, instruct the worker to explain that limitation instead of inventing extra steps.
If you intend to conclude, describe the submission action so the worker can write the final answer to {{.FinalMarkerPath}} and finish.
