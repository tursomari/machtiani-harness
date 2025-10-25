# mct-agent Monorepo — Build, Install, and Use

This repository now houses the full Machtiani toolchain inside a single Go module:

1) `agent/internal/mct` — the local prompting CLI that discovers relevant files and queries an OpenAI‑compatible LLM.
2) `agent/internal/file-discovery` — the helper binary that performs LLM-guided file discovery using a strict RG> protocol.
3) `agent` — the orchestrator that drives the loop and links against the internal libraries directly.

Most users only need the `mct-agent` binary. The install script builds `mct-agent` by default and exposes an opt-in flag when you want the standalone `mct`, `file-discovery`, and `patcher` binaries.

## Prerequisites
- Go: install Go 1.23+ (to satisfy all internal packages; `mct` builds with 1.22+, `file-discovery` with 1.23).
- ripgrep: `rg` must be on PATH (used by `file-discovery`).
- OpenAI‑compatible API access:
  - API key and base URL for models used by `mct` and/or the agent.
- A writable bin directory on PATH (e.g., `~/.local/bin`).

## Testing

### Unit Tests (Core Components)
Run from the repo root to exercise all Go packages without the integration harnesses:

```bash
cd agent
GOCACHE=$(pwd)/.gocache go test ./...
cd ..
```

These tests complete quickly and require no environment variables.

### Integration Tests
The integration suites fall back to deterministic stub or dry-run behavior when the required environment variables are missing. Export the variables below to enable live LLM calls.

**Live Agent Integration Tests** (`agent/tests/run-live.sh`):

```bash
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1
export OPENAI_MODEL=gpt-4o-mini
./scripts/install.sh && bash agent/tests/run-live.sh
```

- Installs `mct-agent` on PATH and exercises Issue A/B/C scenarios plus error paths.
- Writes `test-out-*` directories containing logs, transcripts, and artifacts in the repo root.
- When `OPENAI_*` variables are unset the script injects stub credentials and forces `--dry-run`.

**Undici Harness** (`tests/run-agent-undici.sh`):

```bash
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1
export OPENAI_MODEL=gpt-4o-mini
MACHTIANI_CONFIG=$HOME/.machtiani/config.toml \
MODEL_ALIAS=qwen3-coder-plus \
./tests/run-agent-undici.sh
```

- Builds the toolchain into an isolated temp PATH and clones the undici fixture repository.
- Defaults to offline stubs unless `DISABLE_MCT_STUBS=true` is exported; live runs require the `OPENAI_*` variables above.
- Leaves artifacts under `tests/artifacts/agent-undici/` and preserves scratch work with `KEEP_AGENT_TMP=true`.

**Internal Undici Regression Harness** (`agent/internal/mct/tests/run-undici-readme-integration.sh`):

```bash
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1
export OPENAI_MODEL=gpt-4o-mini
bash agent/internal/mct/tests/run-undici-readme-integration.sh
```

- Uses stub providers by default (`MCT_LLM_TEST_STUB`, `MCT_README_TEST_STUB`); set the `OPENAI_*` variables for live validation.
- Accepts `KEEP_README_TEST_TMP=true` to retain the temporary workspace.
- Produces artifacts under `agent/internal/mct/tests/artifacts/readme/`.

See `TESTING.md` for a full matrix of options and links to component-specific guides (`agent/TESTING.md`, `tests/TESTING.md`).

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
( cd agent/internal/mct && ./build.sh )
install -m 0755 agent/internal/mct/bin/mct "$BIN_DIR/mct"
install -m 0755 agent/internal/mct/bin/file-discovery "$BIN_DIR/file-discovery"
( cd agent/internal/patcher && go build -o "$BIN_DIR/patcher" ./cmd/patcher )
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
- `--shell-agent`: gather terminal context by running the standalone `shell-agent` binary first, append its transcript to the prompt, and then ask the LLM for the final answer (requires `shell-agent` on PATH).

### Session Artifacts & Trajectory Logs

Every run stores artifacts under `.machtiani/sessions/<session-id>/`, including the transcript (`chat/agent-transcript.md`), final answer (`chat/agent-final-answer.md`), and a unified trajectory JSONL stream at `trajectory/agent.jsonl`. The trajectory is enabled by default and can be controlled with the following flags (or their matching `MACHTIANI_TRAJECTORY_*` env vars):

- `--trajectory-file` — override the output path.
- `--no-trajectory` — disable emission entirely.
- `--trajectory-excerpt` — tune how many characters are captured for prompt/response excerpts (default 512).
- `--trajectory-verbose-llm` — include expanded LLM diagnostics.
- `--trajectory-stream-tokens` — log token streaming progress events.
- `--trajectory-omit-repo-root` — omit the detected repository root from payloads.

Inspect the JSONL stream with `jq` or similar tools. Examples:

```bash
# Summarise each turn outcome
jq 'select(.kind == "agent.turn.end") | {turn: .payload.step, decision: .payload.decision, status: .payload.status}' \
  .machtiani/sessions/<session-id>/trajectory/agent.jsonl

# Show planner responses with timing and trimmed content
jq 'select(.kind == "planner.response") | {step: .payload.step, duration_ms: .payload.duration_ms, response: .payload.response_excerpt_first}' \
  .machtiani/sessions/<session-id>/trajectory/agent.jsonl

# Quickly list error events emitted during the run
jq 'select(.level == "error") | {kind, message: .err.message, span: .span_id}' \
  .machtiani/sessions/<session-id>/trajectory/agent.jsonl
```

These events complement the transcript and final artifact, providing structured telemetry that is easy to diff or feed into downstream tooling.

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

# Shell-agent context first, then LLM response
mct prompt --shell-agent "Upgrade dependencies and report any issues"
```

The `--shell-agent` flag expects a `shell-agent` binary on PATH. Build the included implementation with:

```
cd agent/internal/shell-agent
GOCACHE=$(pwd)/../../.gocache go build -o ~/.local/bin/shell-agent ./cmd/shell-agent
```

See `agent/internal/patcher/README.md` for patch workflows and `agent/internal/file-discovery/README.md` for direct `file-discovery` usage.

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
  - If you are using the optional `mct` CLI, ensure it completed successfully and wrote `.machtiani/sessions/<session-id>/chat/machtiani-response.md`.

## Notes and Pointers
- Detailed `mct` docs: see `agent/internal/mct/README.md` for configuration, discovery rules, and troubleshooting.
- `file-discovery` internals and flags: see `agent/internal/file-discovery/README.md`.
- Agent specifics (flags, behavior): see `agent/README.md`.
- If you installed the `mct` CLI, it falls back to a bundled `file-discovery` if it can’t find one on PATH and was built via `build.sh`.

## Integration Tests (mct-agent)
See the [Testing](#testing) section above or `TESTING.md` for up-to-date commands, environment requirements, and artifact locations for `agent/tests/run-live.sh` and the undici regression harnesses.

## Uninstall
Remove the installed binaries (adjust paths to your environment):
```
rm -f ~/.local/bin/mct-agent ~/.local/bin/mct ~/.local/bin/file-discovery ~/.local/bin/patcher
```
