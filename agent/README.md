# mct-agent — Agent Orchestrator

Agent “composer” that iteratively asks focused questions via the `mct` CLI (the worker) and decides when to stop to produce the final answer itself. It shells out to `mct prompt`, parses the saved chat (including the “Retrieved File Paths”), maintains a running summary, and uses its own LLM for planning and final composition.

## Requirements
- Go 1.22+
- `mct` CLI available in PATH (run `./scripts/install-all.sh` from repo root to build/install `mct`, `file-discovery`, `patcher`, and `mct-agent` together; a transparent manual command block lives in the workspace `README.md` under Quick Install)
- OpenAI‑compatible model configuration (no implicit defaults):
  - `OPENAI_API_KEY` (required)
  - `OPENAI_BASE_URL` (required)
  - `OPENAI_MODEL` (required)

## Install

From the repo root, run the unified installer to build **mct**, **file-discovery**, **patcher**, and **mct-agent** together:

```
./scripts/install-all.sh
```

The script writes binaries to `~/.local/bin` by default. Override the destination with `PREFIX` if you prefer a different path:

```
PREFIX="$PWD/.mct-bin" ./scripts/install-all.sh
export PATH="$PWD/.mct-bin/bin:$PATH"
```

Prefer to inspect every step? Copy the manual snippet from the repository `README.md` (Quick Install section); it builds the same binaries without any extra flags.

After installation, confirm the tools resolve via PATH:

```
mct --help | head -n 1
file-discovery -version
patcher --version
mct-agent --version
```

## Quick Start
Set model configuration (used by both agent and `mct`):
```
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1   # or your provider
export OPENAI_MODEL=gpt-4o-mini
```
Run the agent:
```
mct-agent run "Explain X and identify root cause" --verbose
```

## Usage
```
mct-agent run "<issue or question>" [flags]
```
Flags:
- `--max-steps int`: maximum turns before finalizing (default: 4)
- `--openai-api-key string`: API key for OpenAI-compatible endpoint
- `--openai-base-url string`: Base URL for OpenAI-compatible endpoint
- `--openai-model string`: Model name for planner and `mct` calls (alias: `--model`)
- `--timeout-per-turn int`: per-turn timeout in seconds (default: 120; set 0 for unlimited)
- `--version`: print build metadata for the agent and exit
- `--dry-run`: print intended `mct` calls; no subprocess or LLM
- `--verbose`: verbose agent logging (prints the exact `mct` command)
- `--final-file string`: path to write final answer-only artifact
- `--transcript-file string`: path to write transcript (default: `.machtiani/chat/agent-<timestamp>.md`)
- `--file-discovery-trajectory string`: absolute/relative file path for the file-discovery trajectory JSONL
- `--file-discovery-output-dir string`: directory to place file-discovery artifacts (default: `.machtiani/chat`)

## How It Works
- The agent controls the loop: it plans either `Decision: ask` with one next question or `Decision: finalize`.
- On `ask`, it invokes `mct prompt --mode=default` with that question. It then reads `.machtiani/chat/machtiani-response.md` and extracts:
  - “Retrieved File Paths” section
  - A short answer excerpt for the running summary
- It maintains a concise evolving summary/evidence log across turns.
- On finalize (or at `--max-steps`), the agent composes the final answer via its own LLM and prints it.
- A transcript is saved to `.machtiani/chat/agent-<timestamp>.md` with per-turn entries and the final conclusion.

## Environment Details
- `OPENAI_*` resolution precedence in agent:
  - Flags `--openai-*` override
  - Then `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`
  - Legacy envs `AGENT_MODEL_*` / `MCT_MODEL_*` accepted as fallback with a deprecation warning
- Pass-through: agent injects the effective `OPENAI_*` into the `mct` subprocess environment.
- `MACHTIANI_SESSION_ID` is generated per run and passed to the `mct` subprocess via `--session` for correlation.
- `--timeout-per-turn` applies to both the `mct` subprocess calls and the planner/finalizer LLM calls. Set to `0` to disable the deadline for all per-turn operations.

## Troubleshooting
- “mct not found”
  - Run `./scripts/install-all.sh` (or use the manual snippet in the repo `README.md`) and ensure the chosen prefix is on PATH.
- “Missing model configuration”
  - Provide a valid `.machtiani/config.toml` (or set `MACHTIANI_CONFIG`) containing the model alias, or export `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` so the agent can generate one.
- “Saved chat missing/unreadable”
  - The agent reads `.machtiani/chat/machtiani-response.md`. Ensure `mct prompt` ran successfully and wrote the file.
- “mct prompt error: signal: killed” or "timed out after N seconds"
  - The `mct` subprocess likely exceeded the per-turn timeout and was terminated. Increase `--timeout-per-turn` (e.g., `--timeout-per-turn=600`) or set `--timeout-per-turn=0` to disable the deadline.

## Notes
- The agent shells out to `mct` and does not import `mct/internal/*`.
- It avoids parsing streamed stdout; always reads the saved chat file as the source of truth.
- `--dry-run` simulates planning and prints the intended commands without executing `mct` or calling any LLM.

## Testing

### Unit Tests
See `TESTING.md` for detailed commands. Quick reference:
```
cd agent
GOCACHE=$(pwd)/.gocache go test ./...
```

### Integration Tests (Live or Dry-Run)
`agent/tests/run-live.sh` exercises the PATH-installed binaries end-to-end (see `TESTING.md` for full details).

Prerequisites:
1. Run `./scripts/install-all.sh` (or copy the manual command block from the repo `README.md`) so that `mct`, `file-discovery`, `patcher`, and `mct-agent` are on PATH.
2. Optional for live mode: export `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL`. When these variables are absent the script forces deterministic dry-run mode.

What the script does:
- Performs a preflight that resolves each binary on PATH, prints `--version`/`go version -m` metadata, and fails if the commit/time does not match the current sources.
- Generates a temporary `.machtiani/config.toml` under `agent/tests/tmp/` and exports `MACHTIANI_CONFIG` for the duration of the run. The file uses your `OPENAI_*` values in live mode and stub credentials in dry-run.
- Runs Issue A/B/C happy-path scenarios (1-turn and 3-turn variants) plus deterministic error cases (empty input, missing config when in live mode). Artifacts land under `test-out-*` directories in the repo root.

Run from the repo root:

```
./scripts/install-all.sh && bash agent/tests/run-live.sh
```
If you prefer not to invoke the script, run the manual block from the root `README.md` first, then execute `bash agent/tests/run-live.sh`.

The script no longer mutates PATH or accepts binary override flags; everything must resolve via PATH.
- When `OPENAI_*` are not provided, the generated config points at stub credentials and the script forces `--dry-run`, so no network or `mct` subprocess calls occur; transcripts remain available for assertions while the final artifact is intentionally skipped.
- Edge cases: Empty inputs, missing config/deps, timeouts (flaky; manual check advised).
- Custom: Run in a test repo branch for git/file interactions.

For CI: Include explicit `go build` and script invocation; provide OpenAI secrets.
Note: Tests may vary by LLM (non-deterministic multi-turn); refine prompts if needed.
