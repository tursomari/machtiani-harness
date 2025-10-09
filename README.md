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

Prefer to see the full sequence? The commands below inline everything the installer does, but without any build metadata flags:

```bash
PREFIX="${PREFIX:-$HOME/.local}"
BIN_DIR="$PREFIX/bin"
mkdir -p "$BIN_DIR" mct/bin
: "${GOCACHE:=$PWD/.gocache}"; export GOCACHE; mkdir -p "$GOCACHE"

( cd mct && go build -o bin/mct ./cmd/mct )
( cd mct/submodules/file-discovery && go build -o ../../bin/file-discovery ./cmd/file-discovery )
( cd patcher && go build -o "$BIN_DIR/patcher" ./cmd/patcher )
( cd agent && go build -o "$BIN_DIR/mct-agent" ./cmd/mct-agent )
install -m 0755 mct/bin/mct "$BIN_DIR/mct"
install -m 0755 mct/bin/file-discovery "$BIN_DIR/file-discovery"

hash -r 2>/dev/null || true
"$BIN_DIR/mct" --version >/dev/null 2>&1 || true
"$BIN_DIR/mct-agent" --version >/dev/null 2>&1 || true
"$BIN_DIR/patcher" --version >/dev/null 2>&1 || true
"$BIN_DIR/file-discovery" -version >/dev/null 2>&1 || true
```

The manual block skips the ldflags metadata that the installer uses, so those version commands will show `dev`/`unknown` fields—this is expected.

## Environment Setup
Both `mct` and `mct-agent` use OpenAI-compatible configuration via `OPENAI_*`. Provide all three values explicitly (no implicit defaults):

```
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1   # or your gateway
export OPENAI_MODEL=gpt-4o-mini
```

Notes:
- Agent flag precedence: `--openai-*` flags override env vars.
- Legacy envs `AGENT_MODEL_*` are still accepted as a fallback with a deprecation warning.

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
- `--max-input-tokens int`: cap the estimated prompt size that `mct` may build from discovery results; truncates file tails and inserts stamps when necessary.
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

# Limit prompt size for models with strict budgets
mct prompt "Summarize architecture" --max-input-tokens 6000

# Answer-only mode (no discovery/saving)
mct prompt --mode=answer-only -f prompt.md
```
## Troubleshooting
- Command not found
  - Re-run `./scripts/install-all.sh` (or the manual block above) and ensure the chosen prefix (default `~/.local/bin`) is on PATH. Rehash your shell if needed (`hash -r`).
- Missing model configuration / auth errors
  - Set `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` (or pass `--openai-*` flags to the agent).
- `file-discovery` not found
  - Re-run the install step, or set `FILE_DISCOVERY_BIN` to the correct path.
- `rg` missing
  - Install ripgrep (`rg`) and ensure it’s on PATH.
- Saved chat not found
  - `mct-agent` reads `.machtiani/chats/machtiani-response.md` produced by `mct prompt`. Make sure `mct` runs successfully first.

## Notes and Pointers
- Detailed `mct` docs: see `mct/README.md` for configuration, discovery rules, and troubleshooting.
- `file-discovery` internals and flags: see `mct/submodules/file-discovery/README.md`.
- Agent specifics (flags, behavior): see `agent/README.md`.
- `mct` falls back to a bundled `file-discovery` if it can’t find one on PATH and was built via `build.sh`.

## Integration Tests (mct-agent)
`agent/tests/run-live.sh` exercises the PATH-installed binaries end-to-end.

Prerequisites:
1. Run `./scripts/install-all.sh` (or copy/paste the manual block above) so `mct`, `file-discovery`, `patcher`, and `mct-agent` resolve on PATH.
2. Optional for live mode: export `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL`. Without these, the script forces deterministic dry-run mode.

What the script does:
- Performs a preflight that resolves each binary on PATH, prints `--version`/`go version -m` metadata, and fails if the commit/time does not match the current sources.
- Generates a temporary `.machtiani/config.toml` under `agent/tests/tmp/` and exports `MACHTIANI_CONFIG` for the duration of the run. Live mode reuses your `OPENAI_*` values; dry-run mode writes stub credentials and appends `--dry-run`.
- Runs Issue A/B/C happy-path scenarios (1-turn and 3-turn variants) plus deterministic error cases (empty input, missing config when in live mode). Artifacts land under `test-out-*` directories in the repo root.

Run from the repo root:
```
./scripts/install-all.sh
bash agent/tests/run-live.sh
```
If you built via the manual block, skip the first line and run `bash agent/tests/run-live.sh` once the binaries are in place.

The script does not mutate PATH; ensure the install location is already exported. When `OPENAI_*` are absent it forces dry-run, so no network calls occur but transcripts remain for assertions. For full context (including CI guidance) see `agent/TESTING.md`.

## Uninstall
Remove the installed binaries (adjust paths to your environment):
```
rm -f ~/.local/bin/mct ~/.local/bin/mct-agent ~/.local/bin/file-discovery
```
