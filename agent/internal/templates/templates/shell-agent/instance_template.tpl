--- BEGIN TASK ---
Task: {{.Task}}

Machine: {{.Machine}}
Available steps: {{.StepLimit}}
--- END TASK ---

Return exactly one natural-language action. If you intend to conclude, output only the final answer. The first non-whitespace characters of your response must be exactly "## Answer". Do not include any rationale, transition sentence, analysis, or preamble before it. Unless you are concluding, do not begin your response with "## Answer". After "## Answer", present the answer as claim-by-claim findings where each substantive claim begins with "Confidence: <0-100>% - "; do not use a single overall confidence score.
