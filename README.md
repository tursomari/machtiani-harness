# mct-agent Monorepo — Build, Install, and Use

This repository contains three pieces that work together:

1) mct — a local prompting CLI that discovers relevant files and queries an OpenAI‑compatible LLM.
2) file-discovery — a helper binary (submodule) that performs LLM-guided file discovery using a strict RG> protocol.
3) mct-agent — an "agent orchestrator" that drives the loop and now embeds the other components directly via their Go packages.

Most users only need the `mct-agent` binary. The install script builds `mct-agent` by default and exposes an opt-in flag when you want the standalone `mct`, `file-discovery`, and `patcher` binaries.

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

## Quick Install (mct-agent)
Run the installer from the repo root to build **mct-agent** into `~/.local/bin`:

```
./scripts/install.sh
```

Prefer a different prefix? Supply `PREFIX=...` and add the resulting `bin` directory to PATH.

Need the standalone CLIs? Append `--install-peripherals` to also build **mct**, **file-discovery**, and **patcher**:

```
./scripts/install.sh --install-peripherals
```

Prefer to see the full sequence? The commands below inline the default install (without metadata ldflags):

```bash
PREFIX="${PREFIX:-$HOME/.local}"
BIN_DIR="$PREFIX/bin"
mkdir -p "$BIN_DIR"
: "${GOCACHE:=$PWD/.gocache}"; export GOCACHE; mkdir -p "$GOCACHE"

( cd agent && go build -o "$BIN_DIR/mct-agent" ./cmd/mct-agent )

hash -r 2>/dev/null || true
"$BIN_DIR/mct-agent" --version >/dev/null 2>&1 || true
```

To manually build the additional CLIs, run the block above and then:

```bash
( cd mct && ./build.sh )
install -m 0755 mct/bin/mct "$BIN_DIR/mct"
install -m 0755 mct/bin/file-discovery "$BIN_DIR/file-discovery"
( cd patcher && go build -o "$BIN_DIR/patcher" ./cmd/patcher )
```

The manual snippets skip the ldflags metadata that the installer uses, so version commands will show `dev`/`unknown` fields—this is expected.

## Environment Setup
`mct-agent` resolves OpenAI-compatible configuration via `OPENAI_*`. Provide all three values explicitly (no implicit defaults). If you also installed the standalone `mct` CLI, it honors the same variables.

```
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1   # or your gateway
export OPENAI_MODEL=gpt-4o-mini
```

Notes:
- Agent flag precedence: `--openai-*` flags override env vars.
- Legacy envs `AGENT_MODEL_*` are still accepted as a fallback with a deprecation warning.

Optional knobs:
- If you installed the standalone `mct` CLI, `FILE_DISCOVERY_BIN` overrides the `file-discovery` binary it invokes (otherwise it resolves one on PATH or its own bundled copy).
- `MACHTIANI_SESSION_ID`: correlation tag propagated to sub-tools.

## Verify Installation
```
mct-agent --version
```

If you also installed the peripherals, confirm each binary resolves on PATH:

```
mct --help | head -n 1
file-discovery -version
patcher --version
```

## Usage
Basic `mct-agent` run (drives the embedded discovery/planning loop and finalizes):
```
mct-agent run "Explain the architecture and identify main components" --verbose
```

Useful flags (agent):
- `--max-steps int`: max `mct` Q&A turns before finalizing (default ~4).
- `--openai-api-key string`: API key for OpenAI‑compatible endpoint.
- `--openai-base-url string`: Base URL for OpenAI‑compatible endpoint.
- `--openai-model string`: Model name used by the planner and discovery pipeline (alias: `--model`).
- `--max-input-tokens int`: cap the estimated prompt size that discovery may build from results; truncates file tails and inserts stamps when necessary.
- `--timeout-per-turn int`: seconds per turn for the agent loop.
- `--version`: print build metadata for the agent and exit.
- `--dry-run`: print intended calls without executing.
- `--verbose`: verbose logging.

## Optional: Standalone CLIs
If you installed the peripherals (`./scripts/install.sh --install-peripherals`), you can continue using the individual tools. Example `mct` flows:

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

See `patcher/README.md` for patch workflows and `mct/submodules/file-discovery/README.md` for direct `file-discovery` usage.

## Troubleshooting
- Command not found
  - Re-run `./scripts/install.sh` (append `--install-peripherals` if you need the optional CLIs) and ensure the chosen prefix (default `~/.local/bin`) is on PATH. Rehash your shell if needed (`hash -r`).
- Missing model configuration / auth errors
  - Set `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` (or pass `--openai-*` flags to the agent).
- `file-discovery` not found
  - Only applies if you installed the optional CLIs. Re-run the install with `--install-peripherals`, or point `FILE_DISCOVERY_BIN` at the desired binary.
- `rg` missing
  - Install ripgrep (`rg`) and ensure it’s on PATH.
- Saved chat not found
  - If you are using the optional `mct` CLI, ensure it completed successfully and wrote `.machtiani/chats/machtiani-response.md`.

## Notes and Pointers
- Detailed `mct` docs: see `mct/README.md` for configuration, discovery rules, and troubleshooting.
- `file-discovery` internals and flags: see `mct/submodules/file-discovery/README.md`.
- Agent specifics (flags, behavior): see `agent/README.md`.
- If you installed the `mct` CLI, it falls back to a bundled `file-discovery` if it can’t find one on PATH and was built via `build.sh`.

## Integration Tests (mct-agent)
`agent/tests/run-live.sh` exercises the PATH-installed `mct-agent` binary end-to-end. The default install is sufficient; add the peripherals only if you plan to use the standalone CLIs during the session.

Prerequisites:
1. Run `./scripts/install.sh` (add `--install-peripherals` if you also want the standalone CLIs on PATH).
2. Optional for live mode: export `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL`. Without these, the script forces deterministic dry-run mode.

What the script does:
- Performs a preflight that resolves `mct-agent` on PATH, prints `--version`/`go version -m` metadata, and fails if the commit/time does not match the current sources.
- Generates a temporary `.machtiani/config.toml` under `agent/tests/tmp/` and exports `MACHTIANI_CONFIG` for the duration of the run. Live mode reuses your `OPENAI_*` values; dry-run mode writes stub credentials and appends `--dry-run`.
- Runs Issue A/B/C happy-path scenarios (1-turn and 3-turn variants) plus deterministic error cases (empty input, missing config when in live mode). Artifacts land under `test-out-*` directories in the repo root.

Run from the repo root:
```
./scripts/install.sh
bash agent/tests/run-live.sh
```
If you used the manual block instead of the installer, make sure `mct-agent` is on PATH before running `bash agent/tests/run-live.sh`.

The script does not mutate PATH; ensure the install location is already exported. When `OPENAI_*` are absent it forces dry-run, so no network calls occur but transcripts remain for assertions. For full context (including CI guidance) see `agent/TESTING.md`.

## Uninstall
Remove the installed binaries (adjust paths to your environment):
```
rm -f ~/.local/bin/mct-agent ~/.local/bin/mct ~/.local/bin/file-discovery ~/.local/bin/patcher
```
