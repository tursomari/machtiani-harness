# mct-agent Monorepo — Build, Install, and Use

This repository now houses the full Machtiani toolchain inside a single Go module:

1) `agent/internal/file-discovery` — the helper binary that performs LLM-guided file discovery using a strict RG> protocol.
2) `agent` — the orchestrator that drives the loop and links against the internal libraries directly.

Most users only need the `machtiani` binary. The default Nix package contains
only that executable; standalone internal tools remain development builds.
Project state is keyed by a UUID in `~/.machtiani/<uuid>/`; the repository
contains only the trackable `.machtiani/project.uuid` marker.

## Repo-local `machtiani` workflow

If you are using `machtiani` inside this repository, start with `docs/mct-agent-runbook.md`.

- That runbook is the durable repo-specific guide for how to operate `machtiani` in this repository.
- Keep repo-specific operational guidance there; keep this README as the top-level discovery hook.

## Mode System
The agent now ships with a mode system that supervises multi-step work. When you enable it, a top-level session applies the mode's PlannerOverlay and task guidance, runs the agent loop with the configured mode presets, and finally emits a summary artifact that records the result.

- **Enable it per run** with `machtiani run --mode <mode> -p "<your prompt>"`. Modes are loaded only from `~/.machtiani/modes/<mode>/`. `machtiani init` installs or refreshes the canonical modes shipped by this binary.
- **What happens during a run:** the terminal prints `[mode]` updates as the mode system works through the plan. For every task the session applies the task's PlannerOverlay and records progress under the UUID project store returned by `machtiani project show`.
- **Outputs:** the session transcript collects all turns, and every task contributes its own artifacts under the session directory. The final summary lists the tasks, their status, and where to find the detailed artifacts.
- **Resume support:** progress is stored in `<project-store>/sessions/<session-id>/mode-plan.json`, so resuming the session continues with the remaining tasks instead of replaying everything from scratch.
- **Customize instructions:** do not edit a canonical mode in place. Copy it to a new name, then edit the copy: `cp -r ~/.machtiani/modes/code ~/.machtiani/modes/my-code`. Canonical refreshes leave custom mode names untouched.
- **Optional defaults:** when fewer than two tasks are defined for a mode, the mode system falls back to mode-specific defaults. Set different task files or bullet points if you want a custom workflow.
- Available modes include code, code-forge, code-strong-forge, and code-forge-skyvern.

## Sandboxing and Reproducibility
Sandboxing and environment isolation belong in an external scaffold layer, not inside mct-agent business logic. The agent itself supports only local process execution. For reproducible sandboxed runs, a separate scaffold such as the NixOS QEMU VM defined in the nixlab project or a Docker Compose setup provides the isolation boundary. The skyvern-docker branch preserves a Docker-based Skyvern experiment with VNC streaming as an example of external scaffolding. See shell.nix for a Nix-based Skyvern runtime environment.

## Prerequisites
- Nix 2.24 or newer with flakes enabled for installation, updates, and the
  pinned development environments.
- Go 1.23+ only for direct Go development outside the Nix shells.
- The installed Nix package supplies pinned Git, ripgrep, Bash, coreutils, and
  GNU sed for agent-launched commands; they do not need separate host installs.
- OpenAI‑compatible API access:
  - API key and base URL for models used by `mct` and/or the agent.
- A writable bin directory on PATH (e.g., `~/.local/bin`).
- A global `~/.machtiani/config.toml`, a selected UUID-project config, or a `MACHTIANI_CONFIG` path describing your agent, model, and environment settings. See the *Configuration* section below.

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
rev="$(git rev-parse HEAD)"
flake="git+file://$PWD?rev=$rev"
agent_store="$(nix build --no-link --print-out-paths "$flake#machtiani")"
MACHTIANI_BIN="$agent_store/bin/machtiani" \
MCT_REQUIRE_LIVE=true \
bash agent/tests/run-live.sh
```

- Builds committed `HEAD` without a result link or profile mutation, then exercises Issue A/B/C scenarios, discovery timeout/context policies, a `--mode code` regression, and error paths.
- `MACHTIANI_BIN` must be an absolute executable path. The harness carries it into its detached worktree and never resolves or invokes a host command named `machtiani`.
- `MCT_REQUIRE_LIVE=true` rejects stub/dry-run preflight; omit it only when intentionally exercising the non-live fallback mode.
- Writes `test-out-*` directories containing logs, transcripts, and artifacts in the repo root.
- If the harness fails with `mct is not synced at current git state ... Run machtiani sync before proceeding.`, run the repo-local sync command from `docs/mct-agent-runbook.md` and rerun the harness.
- When `TEST_*` and `OPENAI_*` are both unset the script injects stub credentials and forces `--dry-run`.

See `TESTING.md` for the complete testing guide, including prerequisites, commands, environment variables, artifacts, and debugging workflows for every harness.

## Managed and development installations

Use two distinct command names when testing development branches:

- `machtiani` is the managed installation. Its built-in updater follows the
  remote default branch.
- `machtiani-dev` is a development build from a checkout you advance and
  rebuild manually. It does not replace or update `machtiani`.

Both commands use the same Machtiani configuration and project data under
`~/.machtiani`; only their executable installation and update paths differ.

### Managed installation: `machtiani`

Nix 2.24 or newer with flakes enabled is required on NixOS, macOS, and other
Linux distributions. Install from a clean clone whose `origin` identifies the
update stream. The checkout must be at the tip of the remote default branch:

```bash
git clone <repository-url> ~/src/mct-bootstrap
cd ~/src/mct-bootstrap
nix run '.#install'
hash -r
machtiani --version
machtiani update --check
```

In Zsh, `rehash` can be used instead of `hash -r`. Quoting `'.#install'`
prevents Zsh from treating the flake selector as a glob.

In a terminal, the installer offers the default `~/.local/bin/machtiani`, up
to two common existing binary locations, and a custom prefix. Press Enter to
accept the default. Passing `--prefix <dir>` skips the prompt and installs the
binary at `<dir>/bin/machtiani`; automation can pass `--no-interactive` to use
the default without prompting.

The installer builds the exact remote default-branch commit through the locked
flake, activates it in the dedicated profile at
`~/.machtiani/installations/machtiani/profile`, and exposes
`~/.local/bin/machtiani`. Use `--prefix` to choose another stable binary
prefix. Existing configuration, update policy, projects, sessions, and
artifacts are preserved by an explicit reinstall; installations from the
earlier receipt format must be reinstalled once.

When the remote default branch advances, check and install the exact new
commit with:

```bash
machtiani update --check
machtiani update
machtiani --version
```

Do not rebuild the managed profile manually. `machtiani update` fetches,
validates, activates, and records the new default-branch commit.

### Development installation: `machtiani-dev`

Clone the branch to test. This example uses the `install` branch, but the same
workflow works for any development branch:

```bash
git clone --branch install --single-branch <repository-url> ~/src/mct-bootstrap
cd ~/src/mct-bootstrap

dev_profile="$HOME/.machtiani/installations/machtiani/dev-profile"
nix build --profile "$dev_profile" '.#machtiani'

mkdir -p "$HOME/.local/bin"
cat >"$HOME/.local/bin/machtiani-dev" <<'EOF'
#!/usr/bin/env sh
export MCT_AGENT_UPDATE_REEXEC=1
exec "$HOME/.machtiani/installations/machtiani/dev-profile/bin/machtiani" "$@"
EOF
chmod +x "$HOME/.local/bin/machtiani-dev"

hash -r
machtiani-dev --version
```

The wrapper disables automatic managed-update checks for development
invocations. It does not change `machtiani` or its managed profile. Ensure
`~/.local/bin` is on `PATH`; Zsh users may run `rehash` after creating the
wrapper.

When the development branch advances, pull it and rebuild the same profile:

```bash
cd ~/src/mct-bootstrap
git pull --ff-only origin install

dev_profile="$HOME/.machtiani/installations/machtiani/dev-profile"
nix build --profile "$dev_profile" '.#machtiani'
machtiani-dev --version
```

Use the commands independently:

```bash
machtiani-dev run -p "Test the development build"
machtiani run -p "Use the managed build"
```

Do not run `machtiani-dev update`; rebuild it with `nix build --profile`.
Reserve `machtiani update` for the managed installation.

Expanded commands:

```bash
nix build '.#machtiani'
./result/bin/machtiani install --source "$PWD" --verbose
machtiani update --check
machtiani update --check --json
machtiani update --yes --no-interactive --verbose
```

Updates fetch, build, and validate an exact commit before switching the
dedicated profile. A failed build restores the prior profile and source. The
profile retains at most the current and previous generations; the updater never
runs global garbage collection. Configure automatic behavior in
`~/.machtiani/installations/machtiani/update.toml` with `policy = "prompt"`
(the default), `"auto"`, `"notify"`, or `"off"`. Update notices use stderr;
JSON, non-interactive, redirected, help, and version invocations never prompt.

The runtime closure is approximately 184 MiB on verified `x86_64-linux` and
contains pinned Git, ripgrep, Bash, coreutils, and GNU sed, but not Go or Nix.
Those GNU tools are also placed first on agent-launched command PATH on macOS.
The flake evaluates for `x86_64-linux`, `aarch64-linux`, `x86_64-darwin`, and
`aarch64-darwin`; only `x86_64-linux` has been natively built and smoke-tested
as of 2026-07-16.

Skyvern remains an optional source-checkout workflow. Use a separate clone,
initialize `third_party/skyvern`, prepare its Python/browser environment, and
run `scripts/skyvern-start.sh`. Do not use the updater-owned source clone as a
Skyvern workspace because its environment, database, and logs make it dirty.

## Quick Start

From the project you want to use, initialize its identity, home store, canonical modes, and configuration:

```
machtiani init
```

By default, init explains that global configuration shares providers and models
while sessions remain project-specific, then asks
`Use global config? [Y/n] (recommended)`. It selects the existing
`~/.machtiani/config.toml`. Choosing `n` creates a complete config under the
UUID project store. It writes `.machtiani/project.uuid`; sessions, artifacts,
README state, and scratch data remain outside the repository.

The fully non-interactive equivalent is:

```bash
machtiani init --no-interactive --config-scope global
```

Configuration flags such as `--preset`, `--provider`, `--url`, `--model`, and
`--api-key-env` can be supplied directly to `init` when the selected config does
not yet exist. Re-running init keeps the same UUID and never replaces an
existing config. Inspect the resolved paths at any time:

```bash
machtiani project show
machtiani project show --json
```

Before starting an agent session at a new project commit, run `machtiani sync`.
On an interactive terminal, sync keeps its existing success message and shows
the same two-line elapsed-time and token footer as `run`, with the actual
`discovery` and `answer` models used by the operation. Redirected output remains
script-safe and contains only the ordinary success or error message.

Use the configuration manager for follow-up changes:

```
machtiani config
```

The manager safely adds or edits providers and models, selects the default
model, configures caching, and validates the result without replacing unrelated
settings.

Reasoning defaults to the provider's own setting and accepts provider-specific
values such as `xhigh` and `max`. Prompt caching defaults to enabled globally
for configurations created by the wizard.

Flags prefill interactive answers. For scripts and CI, pass
`--no-interactive`; this guarantees that the command never reads stdin and
fails if required information is missing. A preset supplies the URL, endpoint,
credential environment variable, default model, alias, and cache compatibility:

```
export DEEPSEEK_API_KEY=...
machtiani config add --preset deepseek --no-interactive
```

Inspect the available values with `machtiani config catalog list` and
`machtiani config catalog show deepseek`. Raw flags remain available for custom
providers and as preset overrides:

OpenRouter's preset also offers **Search current model catalogue** in the
interactive model menu. The wizard retrieves current model IDs from OpenRouter
using the configured credential, shows up to 25 matches, and retains **Other
model** as a manual fallback. The search remains available when adding more
models to that configured provider, including within the same wizard session.
For searchable providers such as OpenRouter, catalogue search is the initially
selected model action; use Down to choose the stable preset instead. The alias
prompt displays its suggested short name and states that Enter accepts it.
Non-interactive setup remains deterministic and uses the preset's documented
default unless `--model` overrides it.

```
machtiani config add \
  --provider example \
  --url https://api.example.com/v1 \
  --api-key-env EXAMPLE_API_KEY \
  --model my-model \
  --alias default \
  --no-interactive
```

Use `--no-cache` while creating a configuration when the provider does not
support explicit cache markers. Omit `--reasoning` to use the provider default.
`--api-key-env NAME` stores `${NAME}` rather than copying the secret into TOML.

Manage resources with typed subcommands:

```
machtiani config provider list
machtiani config catalog list
machtiani config provider set example --url https://api.example.com/v1
machtiani config model add reviewer --provider example --model review-model
machtiani config model set reviewer --reasoning xhigh
machtiani config model default reviewer
machtiani config cache disable --model reviewer
machtiani config show
machtiani config check
```

Mutations are interactive by default, even when flags prefill their values. Add
`--no-interactive` for an immediate validated write. Use `--global` to target
`$HOME/.machtiani/config.toml`, `--project` for the UUID-project config, or
`--path <file>` for an exact path. Otherwise `MACHTIANI_CONFIG` wins when set,
followed by the initialized project's selected config scope.
Every command prints the selected absolute path. Explicit path flags override
`MACHTIANI_CONFIG` and report that override.

Configuration mutations preserve parsed keys and write atomically with mode
`0600`, but canonically re-encode TOML; comments and original ordering may be
lost.

See the [comprehensive configuration guide](docs/configuration.md) for every
provider, model, cache, path-selection, interactive, and scripting command.

The resource command groups are:

```text
machtiani config provider <list|show|add|set|rename|remove>
machtiani config model <list|show|add|set|rename|remove|default>
machtiani config cache <show|enable|disable|inherit|set>
machtiani config catalog <list|show>
```

Provider `set` supports URL, API-key, endpoint, header, query, and reasoning
wire-format changes.
Model `set` supports provider/model reassignment, reasoning, request parameters,
exact inline JSON via `--param-json`, and restoring provider-default reasoning
with `--clear-reasoning`. Renames
update references. Referenced providers cannot be removed; removing a selected
model requires `--replacement <alias>` in noninteractive mode. Run the relevant
command with `--help` for its complete flags.

## Configuration

For command-by-command management instructions and provider/model/cache
semantics, read the [comprehensive configuration guide](docs/configuration.md).

All binaries read a unified TOML configuration. Resolution order is:

1. `MACHTIANI_CONFIG`, when set.
2. `~/.machtiani/<uuid>/config.toml` when the initialized project selects `project` scope.
3. A legacy repo-local `.machtiani/config.toml` before migration.
4. `~/.machtiani/config.toml` for global scope and fallback.

Switch scopes explicitly and without menus:

```bash
machtiani config scope show
machtiani config scope use project --copy-global --no-interactive
machtiani config scope use global --no-interactive
```

Migrate a legacy repo-local state tree only after reviewing the plan:

```bash
machtiani migrate --dry-run
machtiani migrate --no-interactive --yes
```

Migration copies and checksum-verifies runtime state in the UUID home store
before writing the marker and archiving migrated source entries beside the
repository's `.machtiani/` directory. Disposable full LLM-input logs and old
per-turn shell-agent state files are reported but not copied; their source
copies remain available in the archive. Pass `--keep-legacy` to retain all
verified source entries in place.

Create one of these files before your first run. A minimal example that targets an OpenRouter alias and runs shell commands locally:

```toml
default_model = "foo"

[planner]
max_turns = 150
turn_timeout = 0
system_template = "You are the planning layer for the Machtiani shell agent."
instance_template = "Task: {{.Task}}"

[shell-agent]
max_steps = 110
finalize_remaining_steps = 10
command_supervisor_after = 900
command_supervisor_timeout = 600
command_supervisor_failure_limit = 4
command_supervisor_max_steps = 20
command_supervisor_deadline_buffer = 900
format_error_template = "Your response did not include a properly formatted bash command. Please respond with exactly one fenced bash command."

[model_defaults]
context_length = 128000

[environment]
type = "local"
command_timeout = 86400
max_command_output_bytes = 65536

[providers.openrouter]
base_url = "https://openrouter.ai/api/v1"
api_key = "${OPENROUTER_API_KEY}"

[models.foo]
provider = "openrouter"
model    = "openai/gpt-5-nano"
```

See [`docs/examples/config.minimal.toml`](docs/examples/config.minimal.toml) for
a minimal starting point, or
[`docs/examples/config.comprehensive.toml`](docs/examples/config.comprehensive.toml)
for a reference covering every section and field.

Keys inside `[planner]`, `[shell-agent]`, `[providers]`, `[models]`, and `[environment]` are shared across Machtiani binaries. Omit `providers.<name>.api_key` to use the provider-derived environment variable, or set it to an exact `${NAME}` placeholder. When you need an alternate model temporarily, pass `--shell-agent-model <alias>` to `machtiani run --shell-agent` or to the standalone `shell-agent` binary.

Shell commands start in the directory where `machtiani` is launched. The old
`environment.cwd` key has been removed; delete it from existing configuration
files before running this version.

Configuration is validated automatically before `machtiani run` and `machtiani sync`. Use `machtiani config check` for an explicit preflight or CI check; it audits every configured model and requires credentials for every referenced provider to resolve from the current environment or configuration file. Unknown keys, wrong types, invalid references, and unknown inline model request parameters are rejected. Put ordinary request parameters under `[models.<alias>.params]`; use the inline `params_json` string when an exact JSON shape or `null` is required. Compatible reasoning shapes are negotiated only after a reasoning-specific HTTP 400 and remembered for the current process without rewriting configuration. See the [reasoning compatibility details](docs/configuration.md#reasoning-request-compatibility).

### Prompt Caching

New configurations created by `machtiani init` enable prompt caching through
inheritable model defaults:

```toml
[model_defaults]
cache_enabled = true
cache_key_name = "cache_control"
cache_control = { type = "ephemeral" }
cache_trigger_threshold = 4096
cache_lookback_offset = 1
```

Every named model inherits these values. A model can disable caching or
override individual defaults:

```toml
[models.uncached]
provider = "openrouter"
model = "example/model"
cache_enabled = false

[models.large-context]
provider = "openrouter"
model = "example/large-context-model"
cache_trigger_threshold = 8192
```

Existing per-model caching configuration remains supported. Without
`[model_defaults]`, set a cache key name, cache control payload, token
threshold, and optionally a lookback offset directly on the model:

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
- `MACHTIANI_SESSION_ID`: pre-set session ID to use for the current run; overridden by the `--resume` flag. The legacy `--session-id` flag also overrides it during the deprecation window.
- `MCT_LLM_INPUT_LOG`: explicit path for the full redacted LLM request log; this enables logging and overrides `--log-llm-inputs`' canonical session path.
- `MACHTIANI_THEME`: override `[ui].theme` with `terminal`, `machtiani-dark`, `machtiani-light`, or `none`.
- `MACHTIANI_GLYPHS`: override `[ui].glyphs` with `unicode` or `ascii`.
- `FILE_DISCOVERY_BIN`: override the discovery binary that `mct` invokes.

### Shared discovery timeout and context policy

Embedded file discovery uses the same controls as the rest of `mct-agent`:

- `[planner].turn_timeout` and `--turn-timeout` apply independently to every discovery provider call, including forced finalization. A value of `0` preserves parent cancellation but adds no per-call deadline.
- A model's `context_length`, inherited `[model_defaults].context_length`, or the session `--context-length` override determines both discovery and answer input budgets. When discovery has fallbacks, requests fit the smallest configured resolved budget in the chain.
- Every discovery request is token-fitted before it is sent. Older completed exchanges compact into bounded state, retained tool-output tails are reduced before the initial cue, and the newest two assistant/tool exchanges plus protocol core remain available through finalization.
- A structured provider context-overflow response may reduce the active input cap four times. A successful reduction is persisted only when the successful discovery identity is unambiguous; fallback ambiguity emits telemetry without rewriting configuration.
- Bytes are not the embedded product budget. An emergency initial-input ceiling is derived from the token cap at 16 bytes per token, with a 1 MiB floor and 64 MiB maximum.

Shell command timeouts remain controlled separately by `[environment].command_timeout`; discovery's standalone compatibility flags do not create new `mct-agent` controls.

### Terminal Theme

Human-facing `machtiani run` output, interactive setup/configuration menus, and
Markdown rendered by the standalone `mct` command share a semantic theme.
Configure it globally:

```toml
[ui]
theme = "terminal"
glyphs = "unicode"
```

- `terminal` uses the terminal's standard cyan, green, magenta, yellow, and red foregrounds, so the terminal controls their light/dark appearance.
- `machtiani-dark` and `machtiani-light` use muted palettes designed for the corresponding background. Selection is explicit and reproducible; Machtiani does not query terminal background color or transparency.
- `none` emits no ANSI styling. `TERM=dumb` and non-terminal output also disable ANSI automatically.
- `NO_COLOR` removes color while retaining useful emphasis such as bold, italic, and underline on an interactive terminal.
- `glyphs = "unicode"` uses balanced box-drawing rules and semantic marks. Use `glyphs = "ascii"` for terminals or fonts that do not reliably display them. Glyph selection is independent from color and never rewrites user or model content.

In interactive menus, the current selection uses the Truth color and bold
emphasis. An unselected **Finish** action uses the Goodness color; when selected,
it uses the same selection styling as every other action.

The roles express the Machtiani aesthetic: Truth identifies structure and live
state, Goodness marks successful continuation, Beauty marks final responses and
links, provenance identifies models/sessions/tokens/code, and rupture marks
errors. Verbose diagnostic lines and machine-oriented subcommands remain plain.

## Verify Installation
```
machtiani --version
```

If you also installed the peripherals, confirm each binary resolves on PATH:

```
mct --help | head -n 1
file-discovery -version
```

## Usage
Basic `machtiani` run (drives the embedded discovery/planning loop and finalizes):
```
machtiani run -p "Explain the architecture and identify main components" --verbose
```

Useful flags (agent):
- `--max-turns int`: maximum turns before finalizing (default: 150).
- `-p`, `--prompt string`: inline prompt text, mutually exclusive with `--file`. Positional prompt arguments are not supported.
- `-f`, `--file path`: read the prompt from a file, mutually exclusive with `--prompt`.
- `-x`, `--exec`: one-shot mode; print only raw final-answer markdown to stdout, while warnings and errors remain on stderr. It does not change session persistence, transcript/final-answer writes, exit codes, or execution behavior.
- `--focused`: display only the banner, conclusion, warnings, and errors. This is a display tier, not one-shot mode.
- `--resume string`, `-r string`: Resume a previous session by ID. When specified, the agent loads the prior transcript and goal, then appends any new instruction to the goal. If omitted, a new session ID is auto-generated.
- `--session-id string`: Deprecated alias for `--resume`. It remains available during the deprecation window, but new user-facing commands should use `machtiani run -p "<your follow-up prompt>" --resume <session-id>` (or `-r`).
- `--api-key provider:key`: provider-specific API key override for this run (repeatable; beats config/env).
- `--openai-api-key string`: API key for OpenAI‑compatible endpoint.
- `--openai-base-url string`: Base URL for OpenAI‑compatible endpoint.
- `--openai-model string`: Model name used by the planner and discovery pipeline (alias: `--model`).
- `--shell-agent-model string`: Override the shell-agent model alias for the current run (default comes from `[model].model_name`).
- `--context-length int`: enforce a total input-plus-output token window for this session; model configuration is used when omitted.
- `--turn-timeout int`: seconds per LLM turn; `0` removes the per-call deadline while preserving parent cancellation.
- `--version`: print build metadata for the agent and exit.
- `--dry-run`: print intended calls without executing.
- `--verbose`: verbose logging.
- `--shell-agent`: gather terminal context by running the standalone `shell-agent` binary first, append its transcript to the prompt, and then ask the LLM for the final answer (requires `shell-agent` on PATH; the subprocess automatically receives the current `MACHTIANI_CONFIG`).

To mix providers in a single invocation, repeat `--api-key` once per provider referenced by your model aliases:

```
machtiani run -p "triage regression" \
  --orch-model gpt-5-nano \
  --file-discovery-model haiku \
  --api-key openai:sk-openai-xxx \
  --api-key openrouter:sk-openrouter-yyy
```

### Resuming Conversations

Sessions are resumable by session ID. If your process is interrupted (Ctrl+C) or you would like to append instructions to an ongoing task, use `machtiani run` with a session flag:

The examples below use the initialized project store:

```bash
PROJECT_STORE="$HOME/.machtiani/$(cat .machtiani/project.uuid)"
```

```bash
# Start a new session (auto-assigned ID)
machtiani run -p "Fix all lint issues" --verbose
# Output includes: Session ID: <session-id>

# Later, resume the same session, optionally with new instructions
machtiani run -p "<your follow-up prompt>" --resume <session-id>
```

`machtiani run --resume` always requires an explicit session ID. It never guesses or
auto-resumes the most recent session; use `machtiani session list` to find the
ID you want. For compatibility, `machtiani run --resume=<session-id>` and `-r <session-id>` are
equivalent. The older `--session-id` spelling is deprecated but not removed.

Normal completion output shows the saved final-answer path followed by the
resume command. Paths under the current home directory use `~/`. Add
`--verbose` when you also want the detailed session ID, turn count, and goal
summary; verbose output reports the saved path once in its diagnostic output.

When resuming, the agent:
- Loads the prior goal and transcript from disk
- Appends your new instruction to the goal (resulting in a combined objective)
- Resumes the conversation from where it left off
- Writes new turns to the same transcript and trajectory files

Session state is stored in `$PROJECT_STORE/sessions/<session-id>/session-state.json` and includes the goal, turn count, and paths to transcript artifacts. This allows you to pause, inspect results, and resume later without losing context.

### Session Management

List all sessions:
```bash
machtiani session list
```

Show details for a specific session:
```bash
machtiani session show <session-id>
```

Fork a session without duplicating disposable full-input logs or deprecated
shell-agent state files:

```bash
machtiani session fork <session-id>
```

Inspect or remove disposable data from every inactive session, or from one
specific session:

```bash
machtiani session prune --dry-run
machtiani session prune --no-interactive --yes
machtiani session prune <session-id> --dry-run
```

Pruning removes full LLM input logs, deprecated shell-agent `state.json`
files, and the empty directories they leave behind. It preserves conversations,
outputs, `trajectory/agent.jsonl`, and shell-agent `trajectory.json` checkpoints.

Both commands support `--json` for machine-readable output:
```bash
machtiani session list --json
machtiani session show <session-id> --json
```

### Graceful Interruption and Auto-Save Mechanism

When `machtiani` receives `SIGINT` (Ctrl+C) or `SIGTERM`, it:
- Gracefully terminates the current operation
- Saves session state to `session-state.json` immediately
- If interrupted shell-agent work has a resumable checkpoint, prints the current resume conclusion:
  ```
  SHELL-AGENT INTERRUPTED
  Shell-agent work is resumable.
  Resume the interrupted shell-agent work:
    $ machtiani run -p "<your follow-up prompt>" --resume <session-id>
  ```

The older `=== SESSION INTERRUPTED ===` banner is historical and is no longer
emitted by the current Go implementation.

No manual backup is needed. All context (transcript, goals, artifacts) is preserved and ready for resumption. This is especially useful when working on large codebases where discovery or planning may take time—you can interrupt safely and continue later without re-running earlier steps.

### Session Artifacts & Trajectory Logs

Every run stores artifacts under `$PROJECT_STORE/sessions/<session-id>/`, including:
- **`artifacts/conversation.json`** — canonical conversation and resumable session state
- **`chat/agent-transcript.adoc`** — per-turn planning decisions and evidence
- **`chat/agent-final-answer.md`** — final answer from the orchestrator
- **`trajectory/agent.jsonl`** — unified trajectory stream with structured telemetry (see below)
- **`shell-agent/<turn>/trajectory.json`** — shell-agent checkpoint state used to resume interrupted work
- **`artifacts/llm/inputs.jsonl`** — optional full redacted input log, created only by `--log-llm-inputs` or `MCT_LLM_INPUT_LOG`

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
  $PROJECT_STORE/sessions/<session-id>/trajectory/agent.jsonl

# Show planner responses with timing and trimmed content
jq 'select(.kind == "planner.response") | {step: .payload.step, duration_ms: .payload.duration_ms, response: .payload.response_excerpt_first}' \
  $PROJECT_STORE/sessions/<session-id>/trajectory/agent.jsonl

# Quickly list error events emitted during the run
jq 'select(.level == "error") | {kind, message: .err.message, span: .span_id}' \
  $PROJECT_STORE/sessions/<session-id>/trajectory/agent.jsonl
```

These events complement the transcript and final artifact, providing structured telemetry that is easy to diff or feed into downstream tooling.

### Session Temporary Directories and Locks

Each run uses up to two per-session locations: the scratch root plus one durable session record.

- **Session scratch root:** `<scratch-root>/<session-id>/`
  - In an initialized repo, `<scratch-root>` is `$PROJECT_STORE/tmp/`. Outside a project it is usually `$HOME/.machtiani/tmp/`.
  - This is the host-local runtime directory that contains `session.lock`, shell-agent marker files, and other ephemeral session artifacts.
  - `session.lock` is created when the run starts, held with an exclusive flock, refreshed every second, and removed when the run exits normally unless `--persist-tmp-data` is set.
- **Persistent session record:** `$PROJECT_STORE/sessions/<session-id>/`
  - This durable root holds `session-state.json`, transcripts, trajectories, and other resumable artifacts.

The environment variable for the session scratch root:

- `MACHTIANI_SESSION_TEMP_ROOT` points at the host-local session scratch root that owns `session.lock`.
- `MACHTIANI_TMP_ROOT` normally acts as a scratch-root override.

Startup cleanup also treats these paths differently:

- Session scratch directories are pruned when their `session.lock` is missing or older than three seconds.
- Other stale temp directories under the scratch roots, including old `workspace-<session-id>` directories, are pruned after 24 hours.
- The persistent session record under `$PROJECT_STORE/sessions/<session-id>/` is not affected by temporary-directory pruning, so interrupted sessions remain resumable even after scratch cleanup.

When investigating failures, treat `$PROJECT_STORE/tmp/<session-id>/session.lock` and nearby runtime artifacts as protected evidence. Do not delete or mutate them unless you are intentionally testing cleanup or recovery behavior.

## Internal Tools (Development/Debugging Only)

These tools are used internally by `mct-agent` and are exposed for development or debugging purposes. Most users should use `machtiani` directly.

Developer peripherals are not part of the default Nix installation. If you build `mct` directly from the source tree, example flows are:

```
# Inline question
mct prompt "Summarize the project's README files."

# From a prompt file
mct prompt --file prompt.md

# Limit prompt size for models with strict budgets
mct prompt "Summarize architecture" --context-length 64000

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

The standalone `shell-agent` is a developer peripheral and is not installed by the default package; build it directly from the source tree when needed.

See `agent/internal/file-discovery/README.md` for direct `file-discovery` usage.

## mct-code

`mct-code` is a native Go code-editing agent that replaces the external `forgecode`/`mct-forge` dependency. It provides the file-operation primitives `FSRead`, `FSWrite`, `FSPatch`, `FSMultiPatch`, `FSRemove`, and `FSUndo`. It has been live-tested with DeepSeek and OpenRouter models, supports multi-file handoffs, exposes a `--verbose` flag, and is integrated via `--mode mct-code`. The aim is to fully replace mct-forge; note that mct-code is not yet fully vetted and may need additional work. Next steps include more complex workflow tests, error-handling hardening, optional structured logging, and integration into evaluation pipelines.

## Troubleshooting
- Command not found
  - Re-run `nix run '.#install'`, ensure the chosen prefix (default `~/.local/bin`) is on PATH, and rehash your shell (`hash -r`).
- `managed installation requires a clean source checkout`
  - Managed mode rejects tracked changes and untracked files so local work is not silently excluded from the remote-backed build. Check with `git status --short`, or bootstrap from a fresh clone or temporary clean worktree. The bootstrap checkout can be deleted after installation.
- Missing model configuration / auth errors
  - Set `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` (or pass `--openai-*` flags to the agent).
- `file-discovery` not found
  - Only applies to manually built developer peripherals; build `file-discovery` from the source tree or point `FILE_DISCOVERY_BIN` at a compatible binary.
- `rg` missing
  - Sync file-discovery does not require `rg`. This error can only come from a
    separate shell-agent workflow; install ripgrep if that workflow needs it.
- Saved chat not found
  - If you are using the optional `mct` CLI, ensure it completed successfully and wrote `$PROJECT_STORE/sessions/<session-id>/chat/machtiani-response.md`.

## Notes and Pointers
- Detailed `mct` docs: see `agent/internal/mct/README.md` for configuration, discovery rules, and troubleshooting.
- `file-discovery` internals and flags: see `agent/internal/file-discovery/README.md`.
- Agent specifics (flags, behavior): see `agent/README.md`.
- If you installed the `mct` CLI, it falls back to a bundled `file-discovery` if it can’t find one on PATH and was built via `build.sh`.
- **Ask categorization** — The planner's `no-shell`/`shell` categories shape the question, not the execution path. [`docs/adr/0001`](docs/adr/0001-ask-categorization-as-cognitive-scaffold.md)

## Integration Tests (mct-agent)
See the [Testing](#testing) section above or `TESTING.md` for up-to-date commands, environment requirements, and artifact locations for `agent/tests/run-live.sh` and the smoke harness.

## HEAD-Based Evaluation

There is a second evaluation script at scripts/run_eval_head.sh for tasks without a pre-existing ground truth commit. It creates all agent worktrees from HEAD instead of specified commits. Use --mode write (default) to have the judge produce an unscored implementation benchmark, or --mode read-only for evaluation only. Use --judge-model to specify a separate model for the judge. See docs/eval_head.md for full usage and output artifact descriptions.

## Development

For the Docker-based development workflow (incremental builds, A/B comparison of changes, and containerized testing), see [docs/development-workflow.md](docs/development-workflow.md).

For Deep-SWE bench execution, monitoring commands, score summaries, reward locations, and common failure modes, see [BENCHING.md](BENCHING.md).

## Uninstall
Remove the installed binaries (adjust paths to your environment):
```
rm -f ~/.local/bin/machtiani ~/.local/bin/mct ~/.local/bin/file-discovery ~/.local/bin/shell-agent
```

Remove `~/.machtiani/installations/machtiani/` as well to discard the managed
source clone and updater state. Project UUID stores and model configuration are
independent and are not removed by this step.
