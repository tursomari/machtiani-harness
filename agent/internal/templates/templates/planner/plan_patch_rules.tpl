If patch, immediately follow with a single standalone JSON object ONLY (no commentary, no markdown fences).

Patch JSON schema (when Decision: patch):
{
  "edits": [
    { "path": string (repo-relative), "mode": one of replace|rewrite|create|delete,
      "before": string (replace only), "after": string (replace only), "occurrence": number (1-based, optional),
      "new_content": string (rewrite/create only) }
  ],
  "metadata": { "description": string (optional) }
}
Rules: use forward slashes; paths must be under repo root;
replace requires before+after and file exists; rewrite requires new_content and file exists;
create requires new_content and file must not exist; delete requires file exists.

Minimal example (do not include this text in output):
Decision: patch
{
  "edits": [
    { "path": "README.md", "mode": "replace", "before": "teh", "after": "the", "occurrence": 1 }
  ],
  "metadata": { "description": "Fix README typo" }
}

When intentionally re-editing a file already updated this session, reload it from disk first and set metadata.force_repatch to true.
