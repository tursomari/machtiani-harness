# file-discovery — rooted native file discovery

A Go CLI that helps an LLM discover relevant repository files through three
read-only operations implemented in this Go module. The historical
`RG_OUT`/`SED_OUT`/`LS_OUT` marker names remain part of the model protocol, but
sync does not launch `rg`, `sed`, `ls`, or a shell.

## Highlights
- Model-driven, minimal tool surface: `file_search`, `read_file`, and `list_dir`
- Opens one read-only `os.Root` for the sync workspace and performs all three
  operations relative to it
- Reconciles in-workspace symlinks to their targets while preserving logical
  paths; broken, cyclic, and out-of-workspace links are skipped or rejected
- Applies rooted ignore files and hard excludes, deterministic sorting, Go RE2
  filtering, and bounded output
- Enforces strict protocol, per-request token fitting, emergency initial-input limits, and final block validation
- All logs to stderr; prints exactly one final block to stdout on success

## Requirements
- Go 1.26.5
- For full loop: OpenAI-compatible Chat Completions endpoint
  - `OPENAI_API_KEY` (required)
  - `OPENAI_BASE_URL` (required)
  - `OPENAI_MODEL` (required)

## Install
`file-discovery` is a developer peripheral and is not part of the default Nix package. The commands below compile it directly from this directory.
```bash
# In some sandboxes, Go’s default cache is not writable; use a local cache:
mkdir -p .gocache
GOCACHE=$(pwd)/.gocache go build -o file-discovery ./cmd/file-discovery
# Optionally inject version metadata:
# GOCACHE=$(pwd)/.gocache go build -ldflags "-X main.version=$(git rev-parse --short HEAD)" -o file-discovery ./cmd/file-discovery
```

## Usage
Pipe your issue conversation into `file-discovery`:
```bash
cat issue.txt | ./file-discovery
```

Authentication and model:
- `-api-key`/`--openai-api-key` or `OPENAI_API_KEY`: API key. When the value includes `provider:key`, it overrides the matching provider in the shared config for this run (flag is repeatable).
- `-base-url`/`--openai-base-url` or `OPENAI_BASE_URL`: OpenAI-compatible base URL (required)
- `-model`/`--openai-model` or `OPENAI_MODEL`: Model name (required)
  - Precedence: CLI flags override the corresponding `OPENAI_*` env vars.

Flags:
- `-max-rounds` (default 20)
- `-cmd-timeout` seconds (default 30)
- `-max-stdout` bytes per RG_OUT (default 20480)
- `-max-initial-input` bytes for standalone stdin (default 300000)
- `-max-transcript` is a deprecated compatibility alias for `-max-initial-input`
- `-log-json` or `-v` for logging
- `-no-json`: switch the assistant instructions to the bracket-based tool-call syntax (JSON envelopes remain accepted in this mode)
- `-api-key`, `--openai-api-key` (overrides `OPENAI_API_KEY`; accept `provider:key` overrides)
 - `-base-url`, `--openai-base-url` (overrides `OPENAI_BASE_URL`)
 - `-model`, `--openai-model` (overrides `OPENAI_MODEL`)
- `-session-id`, `-s` (optional): if provided, the first 5 characters tag the BEGIN/END markers for correlation across concurrent runs
- Dry-run (no network):
  - `-dry-run-rg`: enumerate native workspace files and print an `RG_OUT` block to stderr; the flag name is retained for compatibility
- `-pattern <regex>`: Go RE2 expression used to filter workspace-relative paths

Trajectory recording:
- `-trajectory <path>`: write a JSONL trajectory to the given path.
- `-no-trajectory`: disable trajectory recording entirely.
- Env: `FILE_DISCOVERY_TRAJECTORY` is used if `-trajectory` is not set.

When embedded in `mct-agent`, the initial byte setting is not the context
contract. `mct-agent` derives a token budget from the resolved discovery model
and any session `--context-length` override, fits every request (including
accumulated tool history and forced finalization), and uses a secondary ceiling
of 16 bytes per allowed token with a 1 MiB floor and 64 MiB maximum. Older
completed exchanges compact into bounded path/pattern/command state; structured
context overflow can reduce the active cap up to four times.

Example dry-run:
```bash
./file-discovery -dry-run-rg -pattern 'go|README' 2>&1
```

Text vs JSON logging examples (stderr):
- Text (default):
  ```bash
  cat issue.txt | ./file-discovery 2>stderr.txt
  ```
- JSON logs:
  ```bash
  cat issue.txt | ./file-discovery -log-json 2>stderr.jsonl
  ```
  Each log line is a standalone JSON object.

Version:
```bash
./file-discovery -version
```

## Trajectory Logging
- Default on: each run writes `trajectory-YYYYMMDD_HHMMSS-<pid>.jsonl` in the cwd
  unless `-no-trajectory` is set.
- Format: JSON Lines; one compact JSON object per line.
- Contents: full `llm_request` messages, raw `llm_response` content, parsed commands,
  rg execution and RG_OUT events, appended replies, final block validation, and
  run start/end metadata. Timestamps are RFC3339Nano; includes `event_id`, `round`, and `pid`.
- Redaction: API keys and secrets are never persisted; config snapshots redact `apiKey`.
- Robustness: any recorder error only disables recording and logs one warning to stderr.

## Protocol (strict)
Default runs require JSON function-call envelopes such as:

```json
{"tool":"file_search","args":{"kind":"files_pattern","pattern":"<regex>"}}
```

Pass `-no-json` to switch the system prompt to the bracket micro syntax:

```
[file-search]
kind: files_pattern
pattern: "<regex>"
[file-search]
```

Both modes expose the same three logical tools:
- `file_search`: enumerates deterministic workspace-relative source paths and filters them with a Go RE2 pattern (`kind` must be `files_pattern`).
- `read_file`: reads either an inclusive range such as `1,120p` or lines matching a Go RE2 form such as `/pattern/p`, up to 200 results. The path must already have appeared in an `RG_OUT`.
- `list_dir`: returns sorted, tab-delimited native metadata for a validated relative path (no prior `RG_OUT` requirement).

Paths must be relative (no leading `/`), cannot contain `..` or trailing `/`, and may not include backslashes. For `read_file`, the path must have been listed in a previous `RG_OUT` block during the same run.

Agent execution semantics:
- Opens the selected workspace root once and keeps all resolution and reads rooted there without changing the process working directory.
- Loads rooted `.gitignore`, `.ignore`, and `.rgignore` files. Later layers take precedence in that order, matching ripgrep-style precedence without consulting user-global ignore configuration.
- Post-filters common junk directories and binary/asset extensions (case-insensitive):
  - Dirs: `.git`, `node_modules`, `dist`, `build`, `vendor`, `.venv`, `__pycache__`, `.next`, `target`
  - Exts: `.png`, `.jpg`, `.jpeg`, `.gif`, `.bmp`, `.ico`, `.pdf`, `.zip`, `.jar`, `.exe`, `.dll`, `.so`, `.dylib`, `.bin`, `.wasm`, `.ttf`, `.otf`, `.woff`, `.woff2`, `.mp4`, `.mov`, `.mp3`, `.wav`, `.gz`, `.tar`, `.tgz`, `.7z`
- When `file_search` includes a pattern, applies it locally to the filtered path list.
- Returns a single `RG_OUT` block (truncated at 20 KB with a clear footer).
- For `read_file`, reads through the rooted handle and returns one `SED_OUT[PATH]:` compatibility block.
- For `list_dir`, emits stable `kind`, mode, size, UTC timestamp, quoted name, and optional symlink-target fields in one `LS_OUT[PATH]:` compatibility block.

Example `RG_OUT` (to the assistant, via user message):
```
RG_OUT:
README.md
main.go
[TRUNCATED: 20 KB limit]
END_RG_OUT
```

Example `SED_OUT`:
```
SED_OUT[path/to/file.go]:
package main
// ... up to 200 lines max ...
END_SED_OUT
```

Example `LS_OUT`:
```
LS_OUT[path/to/dir]:
file\t-rw-r--r--\t123\t2026-07-16T12:00:00Z\t".hidden"
file\t-rw-r--r--\t456\t2026-07-16T12:00:00Z\t"file.txt"
END_LS_OUT
```

## Final Output Contract
On success, prints exactly one block to stdout and exits 0.

Default markers:
```
BEGIN_RELEVANT_FILES[file-discovery]
path/one.ext
path/two.go
END_RELEVANT_FILES[file-discovery]
```

With `-session-id <id>` provided, the first 5 characters of `<id>` label the markers:
```
BEGIN_RELEVANT_FILES[1a2b3]
path/one.ext
path/two.go
END_RELEVANT_FILES[1a2b3]
```
Validation rules for paths:
- Relative only (no leading `/`), no `..`, no trailing `/`, no backslashes
- One path per line; de-duplicates while preserving order

All other logs (rounds, RG execution, warnings, errors) go to stderr.

## Limits
- Max rounds: 20
- Per-command timeout: 30s
- Per-operation `RG_OUT`: 20 KB (truncates with a footer)
- Global transcript limit: 300 KB (sum of message contents)
- SED per-command output: 200 lines (truncates with a footer)
- LS per-command output: 200 lines (truncates with a footer)

If the transcript limit is hit, the agent asks the model to produce the final block. If the model still doesn’t provide a valid block, the agent exits non-zero.

When the round cap is reached without a valid final block, the agent performs one extra, finalization-only turn (round `max_rounds + 1`) by appending a strict user instruction: no more tool calls are allowed and the assistant must emit exactly one `BEGIN_RELEVANT_FILES[file-discovery]` … `END_RELEVANT_FILES[file-discovery]` block. If a valid block is returned, it is normalized (applying the session marker label when provided) and printed; otherwise the agent exits non-zero.

## Logging
- Startup: base URL, model, limits, cwd
- Per round: bytes sent/received (approx), token usage if provided
- Native enumeration: duration, path count, skip counts, diagnostics count, bytes, and truncation
- Native read: duration, lines emitted, and truncation
- Native listing: duration, entries emitted, and truncation
- Final block detection/validation messages

## Exit Codes
- 0: printed the final relevant files block successfully
- 1: error/timeouts/invalid protocol/no final block by limit

## Common Errors (stderr)
- `API key not provided (use -api-key or OPENAI_API_KEY)`
- `Command timed out after 30s` (configurable via `-cmd-timeout`)
- `read_file path not allowed: <path> not found in any prior RG_OUT`
- `Transcript limit reached without final block`
- `No relevant file block produced`

## Security & Scope
- Performs no process execution in file-discovery production code
- Uses `os.Root` plus component-wise symlink reconciliation; absolute and relative symlink targets must resolve inside the canonical workspace
- Uses read-only filesystem calls and never mutates repository files
- Applies strict protocol-path validation and requires `read_file` paths to have been listed in a prior `RG_OUT`
- Hard caps outputs: 20 KB for `RG_OUT`; 200 lines for read/list output; 1 MiB per scanned line

## Development Notes
- Project layout:
  - `cmd/file-discovery`: CLI entrypoint and flag parsing
  - `internal/discovery`: core loop, compatibility protocol, and API calls
  - `internal/fileops`: rooted enumeration, reads, listings, ignores, and symlink policy
  - `internal/config`: config, logging, trajectory recorder, helpers
- HTTP client calls OpenAI-compatible `/chat/completions` with `temperature=1` and `max_tokens≈2048`
- Compatibility marker/event names (`RG_*`, `SED_*`, and `LS_*`) are intentionally stable for existing trajectories and prompts.

## Scope

This native backend is used by file-discovery during `machtiani sync`.
Standalone shell-agent execution is separate, and snippet-discovery and
deprecated mct-code behavior are unchanged.
