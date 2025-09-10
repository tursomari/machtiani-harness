# mct-agent — Agent Orchestrator

Agent “composer” that iteratively asks focused questions via the `mct` CLI (the worker) and decides when to stop to produce the final answer itself. It shells out to `mct prompt`, parses the saved chat (including the “Retrieved File Paths”), maintains a running summary, and uses its own LLM for planning and final composition.

## Requirements
- Go 1.22+
- `mct` CLI available in PATH or provided via `--mct-bin` (see repo `mct/README.md` to build/install `mct`)
- Model credentials for an OpenAI‑compatible API:
  - For `mct`: `MCT_MODEL_API_KEY` (required), `MCT_MODEL_BASE_URL` (optional; defaults to `https://api.openai.com/v1`)
  - For the agent’s planner/finalizer (optional; falls back to the `mct` env):
    - `AGENT_MODEL_API_KEY`, `AGENT_MODEL_BASE_URL`

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
Set model credentials for `mct` (and optionally for the agent):
```
export MCT_MODEL_API_KEY=sk_...
export MCT_MODEL_BASE_URL=https://api.openai.com/v1   # or your provider
# Optional (else falls back to MCT_*):
export AGENT_MODEL_API_KEY=$MCT_MODEL_API_KEY
export AGENT_MODEL_BASE_URL=$MCT_MODEL_BASE_URL
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
- `--model string`: model for `mct prompt` calls (optional; mirrors mct default when omitted)
- `--agent-model string`: model for the agent’s planner/finalizer (optional)
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
- `mct` environment:
  - `MCT_MODEL_API_KEY` (required)
  - `MCT_MODEL_BASE_URL` (optional; default provided by mct)
  - `MACHTIANI_SESSION_ID` is generated per run and exported to the `mct` subprocess for correlation
- Agent environment (overrides; optional):
  - `AGENT_MODEL_API_KEY`, `AGENT_MODEL_BASE_URL`
  - If unset, the agent falls back to `MCT_MODEL_API_KEY` / `MCT_MODEL_BASE_URL`

## Troubleshooting
- “mct not found”
  - Build and install `mct`, or set `--mct-bin /path/to/mct`, or export `MCT_BIN`.
- “Missing model configuration”
  - Ensure `MCT_MODEL_API_KEY` is set; agent will also need credentials (falls back to MCT env).
- “Saved chat missing/unreadable”
  - The agent reads `.machtiani/chat/machtiani-response.md`. Ensure `mct prompt` ran successfully and wrote the file.

## Notes
- The agent shells out to `mct` and does not import `mct/internal/*`.
- It avoids parsing streamed stdout; always reads the saved chat file as the source of truth.
- `--dry-run` simulates planning and prints the intended commands without executing `mct` or calling any LLM.

