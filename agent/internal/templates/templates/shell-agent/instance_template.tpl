--- BEGIN TASK ---
Task: {{.Task}}

Machine: {{.Machine}}
Available steps: {{.StepLimit}}
--- END TASK ---

Respond with exactly one Bash command wrapped in <command>...</command> tags.
You may precede the command with a single sentence of natural-language description explaining what the command will do.  Example:

Let me check which files are in the current directory.
<command>ls -la</command>

Do NOT output high-level instructions, strategy, or any text that could be mistaken for a planner's guidance.  If you emit anything other than a <command> block (or a final answer) you have failed.
If you intend to conclude, output only the final answer.  The first non-whitespace characters must be exactly "## Answer". Do not include any rationale, transition sentence, analysis, or preamble before it. Unless you are concluding, do not begin your response with "## Answer". After "## Answer", present the answer as claim-by-claim findings where each substantive claim begins with "Confidence: <0-100>% - "; do not use a single overall confidence score.
