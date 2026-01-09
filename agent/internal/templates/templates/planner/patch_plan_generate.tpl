You are analyzing a coding task to create a patch plan. Based on the goal and transcript, produce a JSON list of discrete file edits needed to complete this task.

Goal: {{.Goal}}

Transcript context:
{{.Transcript}}

Output ONLY valid JSON in this exact format (no markdown fences, no commentary):
{
  "items": [
    {"description": "<file path>: <brief description of change>", "complete": false}
  ]
}

Each item should describe one logical file change. Be specific about file paths.
