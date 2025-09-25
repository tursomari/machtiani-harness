# mct-agent Monorepo — Build, Install, and Use

This repository contains three pieces that work together:

1) mct — a local prompting CLI that discovers relevant files and queries an OpenAI‑compatible LLM.
2) file-discovery — a helper binary (submodule) that performs LLM-guided file discovery using a strict RG> protocol.
3) mct-agent — an "agent orchestrator" that iteratively asks focused `mct` questions and composes the final answer.

This README shows a single, end-to-end path to install all three and run the agent.

## Prerequisites
- Go: install Go 1.23+ (to satisfy all modules; `mct` builds with 1.22+, `file-discovery` with 1.23).
- ripgrep: `rg` must be on PATH (used by `file-discovery`).
- OpenAI‑compatible API access:
  - API key and base URL for models used by `mct` and/or the agent.
- A writable bin directory on PATH (e.g., `~/.local/bin`).

If you cloned without submodules, initialize them before building:
```
git submodule update --init --recursive
```

## Quick Install (recommended)
Run the unified installer from the repo root to build **mct**, **file-discovery**, **patcher**, and **mct-agent** into `~/.local/bin`:

```
./scripts/install-all.sh
```

Prefer a different prefix? Supply `PREFIX=...` and add the resulting `bin` directory to PATH.

## Environment Setup
Both `mct` and `mct-agent` use OpenAI‑compatible configuration via `OPENAI_*`. Provide all three values explicitly (no implicit defaults):

```
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1   # or your gateway
export OPENAI_MODEL=gpt-4o-mini
```

Notes:
- Agent flag precedence: `--openai-*` flags override env vars.
- Legacy envs `AGENT_MODEL_*` / `MCT_MODEL_*` are still accepted as a fallback with a deprecation warning.

Optional knobs:
- `FILE_DISCOVERY_BIN`: path to a specific `file-discovery` binary (mct otherwise resolves one on PATH or its own bundled copy).
- `MACHTIANI_SESSION_ID`: correlation tag propagated to sub-tools.

## Verify Installation
```
mct --help | head -n 1
file-discovery -version
patcher --version
mct-agent --version
```

Run a quick prompt with `mct` to confirm LLM access:
```
mct prompt "Say hello in one sentence."
```

## Usage
Basic `mct-agent` run (asks up to a few focused questions via `mct`, then finalizes):
```
mct-agent run "Explain the architecture and identify main components" --verbose
```

Useful flags (agent):
- `--max-steps int`: max `mct` Q&A turns before finalizing (default ~4).
- `--openai-api-key string`: API key for OpenAI‑compatible endpoint.
- `--openai-base-url string`: Base URL for OpenAI‑compatible endpoint.
- `--openai-model string`: Model name used by planner and `mct` (alias: `--model`).
- `--timeout-per-turn int`: seconds per turn for the agent loop.
- `--version`: print build metadata for the agent and exit.
- `--dry-run`: print intended calls without executing.
- `--verbose`: verbose logging.

You can still use `mct` directly:
```
# Inline question
mct prompt "Summarize the project's README files."

# From a prompt file
mct prompt --file prompt.md

# Answer-only mode (no discovery/saving)
mct prompt --mode=answer-only -f prompt.md
```

## Alternative: Manual Build Steps
If you prefer not to run `mct/build.sh`, you can build each piece manually.

```
# Build mct (CLI)
cd mct
go build -o mct ./cmd/mct

# Build file-discovery from the submodule
(cd submodules/file-discovery && \
  mkdir -p ../../bin && \
  GOCACHE=$(pwd)/.gocache go build -o ../../bin/file-discovery ./cmd/file-discovery)

# Install both
install -m 0755 ./mct ~/.local/bin/mct
install -m 0755 ./bin/file-discovery ~/.local/bin/file-discovery
cd -

# Build and install mct-agent
cd agent
go build -o ~/.local/bin/mct-agent ./cmd/mct-agent
cd -

# Build and install patcher
cd patcher
go build -o ~/.local/bin/patcher ./cmd/patcher
cd -
```

## Troubleshooting
- Command not found
  - Re-run `./scripts/install-all.sh` (or the manual steps) and ensure the chosen prefix (default `~/.local/bin`) is on PATH. Rehash your shell if needed (`hash -r`).
- Missing model configuration / auth errors
  - Set `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` (or pass `--openai-*` flags to the agent).
- `file-discovery` not found
  - Re-run the install step, or set `FILE_DISCOVERY_BIN` to the correct path.
- `rg` missing
  - Install ripgrep (`rg`) and ensure it’s on PATH.
- Saved chat not found
  - `mct-agent` reads `.machtiani/chat/machtiani-response.md` produced by `mct prompt`. Make sure `mct` runs successfully first.

## Notes and Pointers
- Detailed `mct` docs: see `mct/README.md` for configuration, discovery rules, and troubleshooting.
- `file-discovery` internals and flags: see `mct/submodules/file-discovery/README.md`.
- Agent specifics (flags, behavior): see `agent/README.md`.
- `mct` falls back to a bundled `file-discovery` if it can’t find one on PATH and was built via `build.sh`.

## Uninstall
Remove the installed binaries (adjust paths to your environment):
```
rm -f ~/.local/bin/mct ~/.local/bin/mct-agent ~/.local/bin/file-discovery
```
