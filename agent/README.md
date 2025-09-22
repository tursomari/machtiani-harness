# mct-agent — Agent Orchestrator

Agent “composer” that iteratively asks focused questions via the `mct` CLI (the worker) and decides when to stop to produce the final answer itself. It shells out to `mct prompt`, parses the saved chat (including the “Retrieved File Paths”), maintains a running summary, and uses its own LLM for planning and final composition.

## Requirements
- Go 1.22+
- `mct` CLI available in PATH or provided via `--mct-bin` (see repo `mct/README.md` to build/install `mct`)
- OpenAI‑compatible model configuration (no implicit defaults):
  - `OPENAI_API_KEY` (required)
  - `OPENAI_BASE_URL` (required)
  - `OPENAI_MODEL` (required)

## Install
Build the binary from this module:

- Build in place
```
cd agent && go build -o mct-agent ./cmd/mct-agent
```

- Install to GOPATH/GOBIN
```
cd agent && go install ./cmd/mct-agent
# Ensure $(go env GOPATH)/bin or $GOBIN is on PATH
```

- Install to a custom path (recommended for local use)
```
mkdir -p ~/.local/bin
cd agent && go build -o ~/.local/bin/mct-agent ./cmd/mct-agent
# Ensure your shell PATH includes ~/.local/bin
export PATH="$HOME/.local/bin:$PATH"
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
- `--mct-bin string`: explicit path to the `mct` binary
- `--dry-run`: print intended `mct` calls; no subprocess or LLM
- `--verbose`: verbose agent logging (prints the exact `mct` command)
- `--final-file string`: path to write final answer-only artifact
- `--transcript-file string`: path to write transcript (default: `.machtiani/chat/agent-<timestamp>.md`)

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
- `MACHTIANI_SESSION_ID` is generated per run and exported to the `mct` subprocess for correlation.
- `--timeout-per-turn` applies to both the `mct` subprocess calls and the planner/finalizer LLM calls. Set to `0` to disable the deadline for all per-turn operations.

## Troubleshooting
- “mct not found”
  - Build and install `mct`, or set `--mct-bin /path/to/mct`, or export `MCT_BIN`.
- “Missing model configuration”
  - Ensure `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` are set (via flags or envs).
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
Run `go test ./...` from `agent/` for internal modules (e.g., planner, runner, parser, transcript handling).

### Integration Tests (Live or Dry-Run)
End-to-end tests against the built `mct-agent` binary, modeled after `file-discovery/run-live.sh`.

Prerequisites:
1. Build `mct-agent`: `cd agent && go build -o mct-agent ./cmd/mct-agent` (the script builds automatically).
2. Build/in PATH: `mct` (via `mct/README.md`) and `patcher` (optional; only used when planner decides to patch).
3. For live mode: `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL` (e.g., gpt-4o-mini for speed).

Running:
- From repo root: `bash agent/tests/run-live.sh`
  - Covers Issues A (init/single-turn), B (multi-turn/context), C (finalize/transcript/errors).
  - 1-turn and 3-turn max variants.
  - Artifacts: `test-out-*` dirs with stdout/stderr/final/transcript files.
  - Validates: Turn counts (<= max), keywords (relevance), artifacts (non-empty), error handling.
  - If `OPENAI_*` not fully set, the script runs in `--dry-run` mode: no LLM calls or `mct` subprocess side effects; transcripts/finals are still generated for assertions.
- Edge cases: Empty inputs, missing config/deps, timeouts (flaky; manual check advised).
- Custom: Run in a test repo branch for git/file interactions.

For CI: Include explicit `go build` and script invocation; provide OpenAI secrets.
Note: Tests may vary by LLM (non-deterministic multi-turn); refine prompts if needed.
