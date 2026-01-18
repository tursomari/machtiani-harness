# snippet-discovery — LLM-driven snippet discovery

`snippet-discovery` is a Go CLI that asks an LLM to identify relevant line ranges inside a set of allow-listed files. It exposes a minimal tool protocol: a single `<show>` tool to display numbered file contents and a JSON-only final output.

## Requirements
- Go 1.23 (Linux)
- OpenAI-compatible Chat Completions endpoint
  - `OPENAI_API_KEY` (required)
  - `OPENAI_BASE_URL` (required)
  - `OPENAI_MODEL` (required)

## Build
From this directory:
```bash
mkdir -p .gocache
GOCACHE=$(pwd)/.gocache go build -o snippet-discovery ./cmd/snippet-discovery
```

## Usage
```bash
cat issue.txt | ./snippet-discovery -r "Find auth-related snippets" -f internal/auth.go -f cmd/server.go
```

The tool prints a JSON mapping of file paths to line ranges:
```json
{
  "internal/auth.go": [
    { "start": 12, "end": 42 }
  ],
  "cmd/server.go": [
    { "start": 88, "end": 104 }
  ]
}
```

### Key flags
- `-r`, `-reason`: required reason/query.
- `-f`: allow-listed file path (repeatable or comma-separated).
- `-max-rounds` (default 10)
- `-timeout` seconds (default 60)
- `-max-lines` lines per file for `<show>` output (default 500)
- `-max-transcript` bytes cap for stdin transcript (default 300000)
- `-trajectory` / `-no-trajectory`

Trajectory logging:
- Default on: writes `trajectory-YYYYMMDD_HHMMSS-<pid>.jsonl` in the working directory unless `-no-trajectory` is set.
- Env override: `SNIPPET_DISCOVERY_TRAJECTORY` if `-trajectory` is not set.

## Protocol
Tool call (exact syntax, no extra text):
```
<show>
path/to/file
another/file
</show>
```

Final output: JSON only (no surrounding text).

## Workspace snapshots
If `MACHTIANI_WORKSPACE_ROOT` is set, the CLI will operate in that directory. Otherwise, if `MACHTIANI_TMP_ROOT` is set, it will look for a snapshot at `$MACHTIANI_TMP_ROOT/repo` and fall back to the current working directory if missing.
