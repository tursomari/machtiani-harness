# patcher

A standalone Go binary that generates git-compatible patches from precise edit instructions. It never modifies the working tree or index.

Usage:

- Build: `go build ./cmd/patcher` (produces `./patcher` in this dir)
- Add to PATH: `export PATH="$PWD:$PATH"`
- Run: `patcher --repo <path> --session <id> --input <file|-> [--out-dir <path>] [--verbose]`

Behavior:

- Reads JSON instructions describing edits (replace/rewrite/create/delete).
- Applies edits in-memory to compute final contents.
- Writes changed files to a temporary mirror directory.
- Generates a patch with `git diff --no-index --binary --relative <repo> <mirror>`.
- Saves the patch to `<repo>/.mct/patches/<session>/<timestamp>-<rand>.patch` by default.
- Verifies applicability via `git apply --check --unsafe-paths` without modifying files.
- Prints a single JSON object to stdout with patch path and basic stats.

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
