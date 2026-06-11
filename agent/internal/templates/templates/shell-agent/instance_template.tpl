--- BEGIN TASK ---
Task: {{.Task}}

Machine: {{.Machine}}
Available steps: {{.StepLimit}}
--- END TASK ---

Respond with exactly one Bash command wrapped in <command>...</command> tags, unless you are concluding with <{{.AnswerTag}}>...</{{.AnswerTag}}>.
You may precede the command with a single sentence of natural-language description explaining what the command will do.  Example:

Let me check which files are in the current directory.
<command>ls -la</command>

Do NOT output high-level instructions, strategy, or any text that could be mistaken for a planner's guidance.  If you emit anything other than a <command> block (or a final <{{.AnswerTag}}> block) you have failed.
If you intend to conclude, output exactly one <{{.AnswerTag}}>...</{{.AnswerTag}}> block and no <command> block. Put the entire final answer inside the <{{.AnswerTag}}> tags. The answer content may be Markdown/plain text. Do not include final-answer content outside the tags. Inside <{{.AnswerTag}}>, present the answer as claim-by-claim findings where each substantive claim begins with "Confidence: <0-100>% - "; do not use a single overall confidence score.
{{if .ShowFewShot}}
Before you respond, here are examples of valid responses:

Example 1:
I will list the files in the current directory.
<command>ls -la</command>

Example 2:
Let me check the git status.
<command>git status</command>

Example 3:
I need to read the log file.
<command>cat stdout.log</command>

Example 4:
I have enough evidence to answer.
<{{.AnswerTag}}>
Confidence: 90% - First substantive claim drawn from the evidence gathered.
Confidence: 75% - Second claim with appropriate uncertainty because the source was indirect.
Confidence: 60% - Third claim where the evidence is partial, and additional verification would be needed to raise confidence.
</{{.AnswerTag}}>

{{end}}
