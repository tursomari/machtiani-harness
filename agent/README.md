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
- `--timeout-per-turn int`: per-turn timeout in seconds (default: 120)
- `--mct-bin string`: explicit path to the `mct` binary
- `--dry-run`: print intended `mct` calls; no subprocess or LLM
- `--verbose`: verbose agent logging (prints the exact `mct` command)

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

## Troubleshooting
- “mct not found”
  - Build and install `mct`, or set `--mct-bin /path/to/mct`, or export `MCT_BIN`.
- “Missing model configuration”
  - Ensure `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` are set (via flags or envs).
- “Saved chat missing/unreadable”
  - The agent reads `.machtiani/chat/machtiani-response.md`. Ensure `mct prompt` ran successfully and wrote the file.

## Notes
- The agent shells out to `mct` and does not import `mct/internal/*`.
- It avoids parsing streamed stdout; always reads the saved chat file as the source of truth.
- `--dry-run` simulates planning and prints the intended commands without executing `mct` or calling any LLM.
