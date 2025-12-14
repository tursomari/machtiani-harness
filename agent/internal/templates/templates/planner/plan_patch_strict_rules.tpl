If patch, immediately follow with a single standalone JSON object ONLY (no commentary, no markdown fences).

Patch JSON schema (when Decision: patch):
{
  "edits": [
    { "path": string, "mode": "patch", "start_line": int, "end_line": int, "new_content": string }
  ],
  "metadata": { "description": string (optional) }
}
Patch mode rules:
  * Provide exactly one edit per request; queue further changes for later turns.
  * use 1-based start_line; end_line = start_line for replacements, end_line = 0 for pure insertions.
  * Delete lines by setting end_line >= start_line and leaving new_content empty.
  * Preserve indentation and include any trailing newline you expect in new_content.
  * Do not mutate other files in the same instruction.

Strict patch planner flow:
  1. Prompt the planner LLM with the goal/context to select the exact repo-relative file path that needs editing; parse that path from the reply.
  2. Load that file directly from disk using the resolved path so you have the authoritative contents with line numbers.
  3. Modify the in-memory copy with the lines you want inserted, removed, or rewritten.
  4. Populate the JSON schema with start_line/end_line and new_content that reflect those concrete lines.
  5. If the file changes later in the run, reload it from disk before producing the final patch.

Minimal example (do not include this text in output):
Decision: patch
{
  "edits": [
    { "path": "docs/guide.md", "mode": "patch", "start_line": 12, "end_line": 12, "new_content": "This feature is experimental.\n" }
  ],
  "metadata": { "description": "Fix typo" }
}

When intentionally re-editing a file already updated this session, reload it from disk first and set metadata.force_repatch to true.
