# mct-agent Monorepo — Build, Install, and Use

This repository now houses the full Machtiani toolchain inside a single Go module:

1) `agent/internal/file-discovery` — the helper binary that performs LLM-guided file discovery using a strict RG> protocol.
2) `agent` — the orchestrator that drives the loop and links against the internal libraries directly.

Most users only need the `mct-agent` binary. The install script builds `mct-agent` by default and exposes an opt-in flag when you want the standalone `mct`, `file-discovery`, `shell-agent`, and `patcher` binaries. All of these tools now share one configuration source (`.machtiani/config.toml` or `MACHTIANI_CONFIG`).

## Repo-local `mct-agent` workflow

If you are using `mct-agent` inside this repository, start with `docs/mct-agent-runbook.md`.

- That runbook is the durable repo-specific guide for how to operate `mct-agent` in this repository.
- Keep repo-specific operational guidance there; keep this README as the top-level discovery hook.

## Meta-Orchestrator
The agent now ships with a meta-orchestrator that supervises multi-step work. When you enable it, a top-level session decomposes the goal into a series of child sessions, runs each one with the appropriate mode presets, and finally emits a summary artifact that stitches the results together.

- **Enable it per run** with `mct-agent run --mode <mode> "<goal>"`. For repo-local usage in this repository, follow `docs/mct-agent-runbook.md`. Each configured mode resolves its instructions from `.machtiani/meta-orchestrator/custom-instructions/<mode>/` (or configured overrides), and each non-empty bullet / line or declared task becomes a meta task.
- **What happens during a run:** the terminal prints `[meta]` updates as the orchestrator works through the plan. For every task it spawns a child session (e.g., "Review existing context", "Implement the Issue"), captures the transcript/final answers, and records progress to `.machtiani/sessions/<parent-session-id>/meta-plan.json`.
- **Outputs:** the parent transcript gains a “Meta-Orchestrator Summary” turn, and every child session keeps its own transcript/final-answer under `.machtiani/sessions/<child-session-id>/`. The summary lists the tasks, their status, and where to find the detailed artifacts.
- **Resume support:** progress is stored in `.machtiani/sessions/<parent-session-id>/meta-plan.json`, so resuming the parent session continues with the remaining tasks instead of replaying everything from scratch.
- **Customize instructions** by editing the shipped mode files or pointing elsewhere with `--meta-instruction-dir <dir>`. Use `instruction` for task-local objectives, `description` for metadata/display text, and `system_prompt` for repo/mode planner guidance. You can also configure search paths in `[meta-orchestrator]` within `.machtiani/config.toml` (set `instruction_dir` or per-mode `instruction_file`). The agent looks in the override directory first, then the config entries, and finally falls back to repo-local custom instructions relative to the repo/config.
- **Optional defaults:** when fewer than two tasks are defined for a mode, the orchestrator falls back to mode-specific defaults. Set different task files or bullet points if you want a custom workflow.

## Prerequisites
- Go: install Go 1.23+ (to satisfy all internal packages; `mct` builds with 1.22+, `file-discovery` with 1.23).
- ripgrep: `rg` must be on PATH (used by `file-discovery`).
- OpenAI‑compatible API access:
  - API key and base URL for models used by `mct` and/or the agent.
- A writable bin directory on PATH (e.g., `~/.local/bin`).
- A `.machtiani/config.toml` (or `MACHTIANI_CONFIG` path) describing your agent, model, and environment settings. See the *Global Configuration* section below.

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
The integration suites fall back to deterministic stub or dry-run behavior when the required environment variables are missing. For this repo's testing flows, prefer `TEST_API_KEY`, `TEST_BASE_URL`, and `TEST_MODEL`; `agent/tests/run-live.sh` uses `TEST_*` first and only falls back to `OPENAI_*` when the test-specific variables are unset.

**Live Agent Integration Tests** (`agent/tests/run-live.sh`):

```bash
export TEST_API_KEY=sk_...
export TEST_BASE_URL=https://api.openai.com/v1
export TEST_MODEL=gpt-4o-mini
./scripts/install.sh && bash agent/tests/run-live.sh
```

- Installs `mct-agent` on PATH and exercises Issue A/B/C scenarios, a meta-mode `--mode code` regression, and error paths.
- Writes `test-out-*` directories containing logs, transcripts, and artifacts in the repo root.
- If the harness fails with `mct is not synced at current git state ... Run mct-agent sync before proceeding.`, run the repo-local sync command from `docs/mct-agent-runbook.md` and rerun the harness.
- When `TEST_*` and `OPENAI_*` are both unset the script injects stub credentials and forces `--dry-run`.

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

Need the standalone CLIs? Append `--install-peripherals` to also build **mct**, **file-discovery**, **shell-agent**, and **patcher**:

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
( cd agent/internal/shell-agent && go build -o "$BIN_DIR/shell-agent" ./cmd/shell-agent )
( cd agent/internal/patcher && go build -o "$BIN_DIR/patcher" ./cmd/patcher )
```

The manual snippets skip the ldflags metadata that the installer uses, so version commands will show `dev`/`unknown` fields—this is expected.

## Global Configuration (.machtiani/config.toml)
All binaries now read a unified TOML configuration. By default `mct-agent`, `mct`, and `shell-agent` look for:

1. The path set in `MACHTIANI_CONFIG` (recommended for scripts/CI), or
2. `.machtiani/config.toml` at the repo root, or
3. `$HOME/.machtiani/config.toml`.

Create one of these files before your first run. A minimal example that targets an OpenRouter alias and runs shell commands locally:

```toml
listen = "127.0.0.1:8042"
default_model = "foo"

[planner]
step_limit = 6
system_template = "You are the planning layer for the Machtiani shell agent."
instance_template = "Task: {{.Task}}"

[shell-agent]
format_error_template = "Please respond with a natural-language description of the next action."
lightweight_max_attempts = 3

[model]
model_name = "foo"        # alias defined under [models]
api_key = ""              # omit to fall back to OPENROUTER_API_KEY / OPENAI_API_KEY

[environment]
type = "local"
timeout = 30
cwd = "."
internet_access = true # capability hint for ask classification; not an escalation trigger

[providers.openrouter]
base_url = "https://openrouter.ai/api/v1"
api_key = "${OPENROUTER_API_KEY}"

[models.foo]
provider = "openrouter"
model    = "openai/gpt-5-nano"
```

Keys inside `[planner]`, `[shell-agent]`, `[model]`, and `[environment]` are shared across Machtiani binaries; omit `model.api_key` to keep credentials out of the file. `[environment].internet_access` is only a capability hint for planner guardrails so the agent can distinguish information gaps from user-authority gaps; it does not itself force escalation to the user. When you need an alternate model temporarily, pass `--shell-agent-model <alias>` to `mct-agent run --shell-agent` or to the standalone `shell-agent` binary.

### Prompt Caching (per model)
Prompt caching is configured per entry under `[models.<alias>]`. To enable it, set a cache key name, cache control payload, a token threshold, and (optionally) a lookback offset for the initial anchor placement:

```toml
[models.haiku]
provider = "openrouter"
model = "anthropic/claude-haiku-4.5"
cache_key_name = "cache_control"
cache_control = { type = "ephemeral" }
cache_trigger_threshold = 4096
cache_lookback_offset = 1
```

To rotate anchors as the prompt grows, set one or both re-anchor thresholds. Rotation is disabled when these are unset or `0`:

```toml
cache_reanchor_tokens = 4096
cache_reanchor_messages = 20
cache_reanchor_min_cached_tokens = 2048
```

- `cache_reanchor_tokens`: rotate when tokens since the active anchor exceed this.
- `cache_reanchor_messages`: rotate when messages since the active anchor exceed this.
- `cache_reanchor_min_cached_tokens`: require at least this many cached tokens before rotating.
- `cache_lookback_offset`: controls the initial anchor placement (how far from the end).

### Environment Variables and Flags
`OPENAI_*` (or the legacy `AGENT_MODEL_*`) environment variables still work; they override missing parts of `[model]` and remain useful for secrets. Command-line flags such as `--openai-api-key` continue to take highest precedence.

Other helpful overrides:
- `MACHTIANI_CONFIG`: explicit path to the config file.
- `MACHTIANI_SESSION_ID`: pre-set session ID to use for the current run; overridden by `--session-id` flag.
- `FILE_DISCOVERY_BIN`: override the discovery binary that `mct` invokes.

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
mct-agent run --t "Explain the architecture and identify main components" --verbose
```

Useful flags (agent):
- `--max-steps int`: max `mct` Q&A turns before finalizing (default ~4).
- `--t string`: Required flag to specify the prompt/question. Positional arguments for prompts are no longer supported.
- `--session-id string`: Continue or resume a previous session by ID. When specified, the agent loads prior transcript and goal, then appends your new instruction to the goal. If omitted, a new session ID is auto-generated.
- `--api-key provider:key`: provider-specific API key override for this run (repeatable; beats config/env).
- `--openai-api-key string`: API key for OpenAI‑compatible endpoint.
- `--openai-base-url string`: Base URL for OpenAI‑compatible endpoint.
- `--openai-model string`: Model name used by the planner and discovery pipeline (alias: `--model`).
- `--shell-agent-model string`: Override the shell-agent model alias for the current run (default comes from `[model].model_name`).
- `--max-input-tokens int`: cap the estimated prompt size that discovery may build from results; truncates file tails and inserts stamps when necessary.
- `--timeout-per-turn int`: seconds per turn for the agent loop.
- `--version`: print build metadata for the agent and exit.
- `--dry-run`: print intended calls without executing.
- `--verbose`: verbose logging.
- `--shell-agent`: gather terminal context by running the standalone `shell-agent` binary first, append its transcript to the prompt, and then ask the LLM for the final answer (requires `shell-agent` on PATH; the subprocess automatically receives the current `MACHTIANI_CONFIG`).

To mix providers in a single invocation, repeat `--api-key` once per provider referenced by your model aliases:

```
mct-agent run --t "triage regression" \
  --orch-model gpt-5-nano \
  --file-discovery-model haiku \
  --api-key openai:sk-openai-xxx \
  --api-key openrouter:sk-openrouter-yyy
```

### Continuing Conversations With Session Resumption

Sessions are resumable by session ID. If your process is interrupted (Ctrl+C) or you'd like to append additional instructions to an ongoing task, specify the `--session-id` flag:

```bash
# Start a new session (auto-assigned ID)
mct-agent run --t "Fix all lint issues" --verbose
# Output includes: Session ID: <session-id>

# Later, continue the same session with new instructions
mct-agent run --t "Also ensure comments are updated" --session-id <session-id>
```

When resuming, the agent:
- Loads the prior goal and transcript from disk
- Appends your new instruction to the goal (resulting in a combined objective)
- Continues the conversation from where it left off
- Writes new turns to the same transcript and trajectory files

Session state is stored in `.machtiani/sessions/<session-id>/session-state.json` and includes the goal, turn count, and paths to transcript artifacts. This allows you to pause, inspect results, and resume later without losing context.

### Graceful Interruption and Auto-Save Mechanism

When `mct-agent` receives `SIGINT` (Ctrl+C) or `SIGTERM`, it:
- Gracefully terminates the current operation
- Saves session state to `session-state.json` immediately
- Prints an interruption summary including session ID and turns completed:
  ```
  === SESSION INTERRUPTED ===
  Session ID: <session-id>
  Turns completed: 2
  To resume: mct-agent run "<continue question>" --session-id <session-id>
  ```

No manual backup is needed. All context (transcript, goals, artifacts) is preserved and ready for resumption. This is especially useful when working on large codebases where discovery or planning may take time—you can interrupt safely and continue later without re-running earlier steps.

### Session Artifacts & Trajectory Logs

Every run stores artifacts under `.machtiani/sessions/<session-id>/`, including:
- **`session-state.json`** — persisted session metadata (goal, turn count, transcript path) used for resuming sessions
- **`chat/agent-transcript.adoc`** — per-turn planning decisions and evidence
- **`chat/agent-final-answer.md`** — final answer from the orchestrator
- **`trajectory/agent.jsonl`** — unified trajectory stream with structured telemetry (see below)

The trajectory is enabled by default and can be controlled with the following flags (or their matching `MACHTIANI_TRAJECTORY_*` env vars):

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

### Session Temporary Directories, Locks, and Snapshot Workspaces

Each run uses up to three per-session locations: two scratch-root siblings plus one durable session record.

- **Session scratch root:** `<scratch-root>/<session-id>/`
  - In a repo, `<scratch-root>` is usually `.machtiani/tmp/`. Outside a repo it is usually `$HOME/.machtiani/tmp/`.
  - This is the host-local runtime directory that contains `session.lock`, shell-agent marker files, and other ephemeral session artifacts.
  - `session.lock` is created when the run starts, held with an exclusive flock, refreshed every second, and removed when the run exits normally unless `--persist-tmp-data` is set.
- **Snapshot workspace root:** `<workspace-base>/workspace-<session-id>/`
  - This exists only for non-`local` environments such as `docker`.
  - By default `<workspace-base>` is `.machtiani/tmp/`, but `[environment].tmp_root` can move it elsewhere.
  - The mounted repository is a subdirectory at `<workspace-base>/workspace-<session-id>/repo/`; workspace-level metadata such as manifests can live alongside `repo/`.
- **Persistent session record:** `.machtiani/sessions/<session-id>/`
  - This durable root holds `session-state.json`, transcripts, trajectories, and other resumable artifacts.

For debugging, the important distinction is that `docker` keeps the live lock in the host-local session scratch root while container work happens in the separate snapshot workspace. Normal edits inside the mounted snapshot repo do not, by themselves, delete `.machtiani/tmp/<session-id>/session.lock`.

The environment variables also diverge during non-`local` runs:

- `MACHTIANI_SESSION_TEMP_ROOT` points at the host-local session scratch root that owns `session.lock`.
- `MACHTIANI_TMP_ROOT` normally acts as a scratch-root override, but during non-`local` runs it is intentionally repurposed to point at the snapshot workspace root so shell-agent and workspace helpers operate inside the snapshot instead of the lock directory.
- In `local` mode there is no snapshot workspace, and the runtime uses `MACHTIANI_SESSION_TEMP_ROOT` / normal scratch-root resolution instead.

Startup cleanup also treats these paths differently:

- Session scratch directories are pruned when their `session.lock` is missing or older than three seconds.
- Other stale temp directories under the scratch roots, including old `workspace-<session-id>` directories, are pruned after 24 hours.
- The persistent session record under `.machtiani/sessions/<session-id>/` is not affected by temporary-directory pruning, so interrupted sessions remain resumable even after scratch cleanup.

When investigating failures, treat `.machtiani/tmp/<session-id>/session.lock` and nearby runtime artifacts as protected evidence. Do not delete or mutate them unless you are intentionally testing cleanup or recovery behavior.

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

If you installed with `--install-peripherals`, the installer already places `shell-agent` alongside the other binaries so you can invoke it directly (`shell-agent --help`).

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
