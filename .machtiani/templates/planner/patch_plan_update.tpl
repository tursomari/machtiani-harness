You are updating a patch plan after a file was successfully edited.

The file just patched: {{.LastPatchedFile}}

Current plan:
{{.ExistingPlanJSON}}

Goal: {{.Goal}}

Recent transcript:
{{.RecentTranscript}}

Update the plan:
1. Mark any items as complete if they match the patched file
2. Add new items if the transcript reveals additional needed changes
3. Remove items that are no longer relevant
4. Keep the existing goal, refining it if necessary

Output ONLY valid JSON in the same format (no markdown fences):
{
  "goal": "<intro statement>",
  "items": [
    {"description": "<description>", "complete": <true|false>}
  ]
}
