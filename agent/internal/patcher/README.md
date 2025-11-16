# patcher

A standalone Go binary that generates git-compatible patches from precise edit instructions. It never modifies the working tree or index.

Usage:

- Install (recommended): `./scripts/install.sh --install-peripherals` from the repo root (puts `patcher` under `~/.local/bin` by default; manual commands live in the root `README.md` under Quick Install → Optional CLIs)
- Run: `patcher --repo <path> --session <id> --input <file|-> [--out-dir <path>] [--verbose]`
- Inspect build metadata: `patcher --version`

Behavior:

- Reads JSON instructions describing edits (replace/rewrite/create/delete).
- Applies edits in-memory to compute final contents.
- Writes changed files to a temporary mirror directory.
- Generates a patch with `git diff --no-index --binary --relative <repo> <mirror>`.
- Saves the patch to `<repo>/.machtiani/sessions/<session>/artifacts/patches/<timestamp>-<rand>.patch` by default.
- Verifies applicability via `git apply --check --unsafe-paths` without modifying files.
- Prints a single JSON object to stdout with patch path and basic stats.

### Success Signaling

When a strict patch applies cleanly, the session runner now records a concise banner ahead of the detailed patch diagnostics:

```
✅ STRICT PATCH SUCCESS: update widgets
Updated files: lib/widget/config.go
Changes: +3 / -1 (applied to workspace)
```

This header appears in the transcript before the legacy stats block so downstream planners can quickly spot which files already changed. The patch question line is also prefixed with `Patcher: [SUCCESS] …` and the telemetry payload includes `success_files` to mirror the updated list.

Exit codes:

- 0: Success; patch written and applies cleanly
- 2: Invalid input (schema/validation)
- 3: Could not generate patch
- 4: Patch does not apply cleanly
- 1: Unexpected internal error

Notes:

- Requires `git` in PATH.
- Only exact string replacements are supported for replace mode.
- All paths are validated to remain under the repo root.

## License

MIT


## Strict Patch Snippet Source

When instructions use `mode: "patch"`, each hunk may now include an optional `snippet_source` object:

```json
{
  "old_start": 42,
  "old_count": 2,
  "new_start": 42,
  "new_count": 3,
  "context_before": ["func example()"],
  "deletions": ["return foo"],
  "additions": ["return bar"],
  "context_after": ["}"],
  "snippet_source": {
    "start_line": 42,
    "end_line": 43
  }
}
```

- `start_line`/`end_line` are 1-based and inclusive, describing the exact range of lines that supplied the "before" snippet.
- The patcher reads those lines directly from disk during validation and application, eliminating ambiguity when identical blocks appear multiple times.
- Omit `filepath` to use the surrounding edit path; non-empty values must remain within the repository root.
- `old_count` must match the number of lines in the range; use `end_line = start_line - 1` for pure insertions (`old_count = 0`).

Legacy instructions without `snippet_source` continue to fall back to context matching.
