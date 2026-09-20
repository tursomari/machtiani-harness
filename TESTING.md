# Testing Machtiani Harness

This is the canonical entrypoint for tests owned by the Machtiani Harness
repository. Test commands, prerequisites, environment variables, artifacts,
and debugging guidance belong here or in a specialized document linked from
here. Umbrella and sibling-submodule tests are intentionally outside this
document's ownership.

## Recommended starting checks

Use the narrowest Go package suite while iterating. For example:

```bash
cd agent
GOCACHE=$(pwd)/.gocache go test ./internal/session ./internal/config
```

Before landing a Harness branch, run at least the maintained package build:

```bash
nix build '.#machtiani'
```

Use `nix flake check` when the change affects multiple packages, Nix wiring,
packaging, or repository-wide contracts. Add the clean-container smoke, live
agent suite, TUI record/replay, or evaluation harness only when the changed
behavior reaches that boundary.

## Test Layout

- Go `*_test.go` files stay beside the packages they exercise.
- `agent/tests/` owns integration harnesses specific to the `machtiani` binary and their helpers.
- `tests/smoke/` owns the clean-container smoke Dockerfile, host runner, and container assertions.
- `tests/tui/` owns whole-terminal record/replay regression tooling.
- `tests/python/` owns Python adapter tests.
- Other root `tests/` subtrees hold cross-component integration and adapter harnesses.
- `scripts/` retains installation, benchmark, treatment, and evaluation runners; those stable paths are intentionally separate from test harnesses.

## Prerequisites
- Go 1.26.5, provided by `nix develop .#default`.
- `git` for cloning fixture repositories during integration tests.
- `rg`, `sed`, and `ls` are not prerequisites for sync file-discovery. Some
  separate shell-agent tests intentionally exercise the host command toolchain.
- Docker, for the clean-container smoke test.
- For live LLM runs: valid `TEST_API_KEY`, `TEST_BASE_URL`, and `TEST_MODEL` values, or the `OPENAI_*` fallbacks supported by the selected harness.

## Go Package Tests

Run from the repository root to cover all Go packages:

```bash
cd agent
GOCACHE=$(pwd)/.gocache go test ./...
cd ..
```

- No provider environment variables are required. This is a broad package
  sweep, not an instant unit-only loop: packages such as `internal/update`
  exercise Nix-backed update behavior and can take substantially longer than
  focused package tests.

### Targeted Component Suites

The root sweep covers the standard Go suites below. Use these commands when focusing on one component or enabling a non-default build tag.

#### File Discovery

```bash
cd agent/internal/file-discovery
go test ./...
go test ./e2e -v
go test ./internal/discovery -run TestToolCallExecution_NativeOperations -v
go test -tags e2e_slow_rg ./e2e -run CmdTimeout -v
```

The E2E suite clears runtime `PATH`, so it proves that file discovery does not
invoke external commands. To exercise the flag suite in its component test image:

```bash
cd agent/internal/file-discovery
docker build -f tests/Dockerfile -t file-discovery-tests .
docker run --rm -it --entrypoint /usr/local/bin/run-flags.sh file-discovery-tests
docker run --rm -it -e RUN_SLOW=1 --entrypoint /usr/local/bin/run-flags.sh file-discovery-tests
```

#### Internal README Manager

```bash
cd agent
GOCACHE=$(pwd)/.gocache go test ./...
```

#### Shell Agent

From `agent/internal/shell-agent`:

```bash
GOCACHE=$(pwd)/.gocache go test ./...
GOCACHE=$(pwd)/.gocache go test ./tests/integration/shell-integration
GOCACHE=$(pwd)/.gocache go test ./tests/e2e/run-live
GOCACHE=$(pwd)/.gocache go test ./tests/e2e/shell-subprocess
GOCACHE=$(pwd)/.gocache go test ./tests/e2e/run-live \
  -run TestRetryShellCallRecoversAfterTransientFailure
```

These deterministic suites exercise real local shell behavior but do not call model providers or require API keys.

#### Snippet Discovery

```bash
cd agent/internal/snippet-discovery
go test ./internal/...
go test ./e2e -tags e2e_stub_llm
```

The tagged E2E suite builds a temporary binary and supplies stub responses through `SNIPPET_DISCOVERY_E2E_RESPONSES`.

#### Python Pier Adapter

```bash
python3 tests/python/test_machtiani_pier_adapter.py
```

This suite tests protected-artifact staging and preservation commands and verifies that the existing benchmark treatment launchers do not expose verifier logs to agents.

## Clean-Container Smoke Test

The Docker smoke test builds `machtiani` in a clean image, exercises the full
script-safe configuration lifecycle, restores a clean provider configuration,
performs live agent runs, and verifies that session conversation artifacts were
created. Explicit `TEST_*` values select the primary target. When they are not
set, the host runner reads `.machtiani/config.toml` and uses DeepSeek (or the
first complete provider) as the primary target.

```bash
export TEST_API_KEY=sk_...
export TEST_BASE_URL=https://api.openai.com/v1
export TEST_MODEL=gpt-4o-mini
./tests/smoke/run.sh
```

The managed updater has a provider-independent container path that creates a
local bare Git remote, clones it into a disposable path under `$HOME/src`, and
bootstraps the canonical source under `$HOME/.machtiani`. It removes the
original clone before proving explicit update and automatic startup
update/re-execution. Before bootstrapping, it seeds legacy configuration and
project artifacts and verifies that the migration script preserves them:

```bash
./tests/smoke/run.sh --update-only
```

This mode requires Docker and Git but no model-provider credentials. The full
smoke run executes the same updater scenario before its configuration and live
provider checks. The local updater harness also verifies rejection of dirty
bootstrap and managed checkouts, origin mismatches, and embedded credentials;
the Go updater tests verify managed-source and rollback behavior.

To run only the deterministic command-supervisor container scenario, without
provider credentials, plant a blocking `go` executable at the front of `PATH`
and verify that its process group is stopped after an explicit supervisor
decision:

```bash
./tests/smoke/run.sh --command-supervisor-only
```

Prerequisites:

- Docker with a running daemon.
- Git.
- Either non-empty `TEST_API_KEY`, `TEST_BASE_URL`, and `TEST_MODEL` values, or
  a repository `.machtiani/config.toml` containing at least one complete
  provider/model. This harness has no stub or dry-run mode.

When the repository config contains complete entries for OpenAI, OpenRouter,
DeepInfra, or DeepSeek, the runner also builds a best-effort live matrix. It
uses OpenAI `gpt-5.6-luna`, OpenRouter `deepseek/deepseek-v4-flash`, DeepSeek
`deepseek-v4-flash`, and a configured DeepInfra model, all at low reasoning.
A matrix entry matching the primary `TEST_*` base URL and model is not run
twice. Secrets are passed as environment variables only; the config file is
not copied into the image and literal keys are not placed in CLI arguments.

The host runner creates a detached Git worktree at the current committed `HEAD`, builds `tests/smoke/Dockerfile`, and runs `tests/smoke/container.sh` inside the resulting image. Optional source-only submodules are not copied into smoke. Consequently, uncommitted changes are not included. Commit the changes you intend to test, or manually build the image from the current checkout when iterating on the smoke infrastructure itself.

The container test must complete all of these checks before printing its success message:

1. A clean Git repository with an initial commit is created in `/workspace`.
2. `machtiani --version` runs.
3. `config-crud.sh` inspects the built-in provider catalogue, then creates the
   primary provider/model with the DeepSeek preset and an API-key environment
   reference; the literal live key is never passed as a CLI argument or written
   to TOML.
4. `machtiani init --no-interactive` creates a UUID marker/home store, installs
   canonical modes, selects global config, and remains idempotent without
   replacing existing configuration or runtime sentinels.
5. A pseudo-terminal wizard run verifies that an existing OpenRouter provider
   retains `Search current model catalogue` when another model is added, without
   making a live OpenRouter request or changing the disposable configuration.
   The same configuration suite runs `tests/smoke/config-chatgpt-wizard.py`
   against the real CLI and a local model-host fixture. It covers browser and
   device-code login, current subscription model/effort discovery, unchanged
   display names, prefixed and custom aliases, collisions with API models,
   account reuse after provider rename, cancellation, and failed setup.
   These tests require no subscription credentials and make no provider calls.
6. Every provider, model, and cache subcommand is exercised, including
   reference-aware rename/removal, reasoning and parameter updates, cache
   inheritance, inspection, validation, and expected failure paths.
7. `--path`, `--global`, `MACHTIANI_CONFIG`, and explicit environment override
   behavior are verified with disposable configurations.
8. Rejected mutations are checksum-verified as non-writing, scratch resources
   are removed, and the final primary configuration is validated with caching
   disabled.
9. A deterministic local ChatCompletion server gives discovery and answer
   separate aliases, rejects discovery's first request with a structured
   context-overflow error, accepts its reduced retry, and verifies that only
   the unambiguous discovery alias learns the lower `context_length`.
10. `machtiani sync` initializes the repository's internal README state.
11. `machtiani run` completes successfully against the primary live provider.
12. Any additional discoverable provider targets are added with `config
    provider/model`, validated, and exercised with a low-reasoning live run.
13. Two deterministic internal READMEs are synced at successive project
    commits; after checking out the earlier commit while the mutable artifact
    still contains the later README, a fresh dry-run session injects the exact
    earlier tagged blob into `conversation.json`.
14. `<project-store>/sessions/*/artifacts/conversation.json` exists and the
    repository contains `.machtiani/project.uuid` rather than session data.
15. A planted `go test ./...` command blocks through a `PATH` shim, triggers a
    short command-supervisor review, and leaves no running blocker process.
16. A separate synthetic legacy repository passes migration dry-run and actual
    non-interactive migration, including verified home-store data and a legacy
    archive.

The scripts use `set -euo pipefail`; any failed command must produce a non-zero harness exit and must not print `SMOKE TEST PASSED`.

To iterate on only the ChatGPT wizard tests with an already-built candidate,
use `MACHTIANI_SMOKE_AGENT=/absolute/path/to/machtiani nix develop .#smoke -c
python3 tests/smoke/config-chatgpt-wizard.py`. The full container smoke invokes
the same test from `config-crud.sh`. This verifies the configuration and
model-host protocol boundary with a fixture; a real account sign-in remains
a separate live acceptance check.

The model selection CLI can also be tested independently with
`MACHTIANI_SMOKE_AGENT=/absolute/path/to/machtiani nix develop .#smoke -c
python3 tests/smoke/config-model-selection.py`. This suite uses disposable
configurations and real terminals to check shell-agent selection, explicit
scope targeting, preservation of other settings, invalid arguments, and
confirmation/cancellation. It runs from `config-crud.sh` in the container smoke
suite and makes no provider requests.

## Integration Tests
All integration harnesses default to deterministic stub or dry-run behavior. Export the listed environment variables to invoke live LLM calls.

### Live Agent Integration Tests (`agent/tests/run-live.sh`)
End-to-end regression suite for the installed `machtiani` binary.

```bash
export TEST_API_KEY=sk_...
export TEST_BASE_URL=https://api.openai.com/v1
export TEST_MODEL=gpt-4o-mini
rev="$(git rev-parse HEAD)"
short="$(git rev-parse --short=12 HEAD)"
built="$(git show -s --format=%ct HEAD)"
test_agent="$(mktemp -d)/mct-rg-ls-sed-test-agent"
nix develop .#default -c bash -c \
  "cd agent && CGO_ENABLED=0 go build -trimpath -ldflags '-X main.Version=dev-$short -X main.Commit=$rev -X main.BuiltAt=$built -X main.Dirty=clean' -o '$test_agent' ./cmd/machtiani"
MACHTIANI_BIN="$test_agent" \
MACHTIANI_REQUIRE_LIVE=true \
bash agent/tests/run-live.sh
```

- Tests committed `HEAD`; commit intended changes before building the exact revision.
- `MACHTIANI_BIN` must resolve to an absolute executable file. The outer harness validates and canonicalizes it before creating its detached worktree, and every setup/test call uses that path.
- `MACHTIANI_REQUIRE_LIVE=true` fails preflight unless complete `TEST_*` or supported fallback credentials are available. A stub or dry-run cannot satisfy this mode.
- `TEST_*` takes precedence over `OPENAI_*`. When neither complete set is available, the script generates stub credentials, writes a temporary `config.toml`, and forces `--dry-run`.
- Artifacts land in `test-out-*` directories at the repo root; each case includes stdout, stderr, transcripts, and (for live runs) generated assets.
- Optional overrides:
  - `TEST_ORCH_MODEL`, `TEST_FILE_DISCOVERY_MODEL` — preferred per-component remote-model overrides; the corresponding `OPENAI_*` values are fallbacks.
  - `OPENAI_ORCH_MODEL_ALIAS`, `OPENAI_FILE_DISCOVERY_MODEL_ALIAS` — supply config aliases when reusing a shared `config.toml`.
  - `OPENAI_API_KEY`/`BASE_URL`/`MODEL` remain authoritative even when aliases are set.

Scenarios include Issue A/B/C happy paths, one-turn and three-turn bounds, per-component model selection, the `--mode code` regression in stub and live paths, empty prompts, missing configuration, menu flow, shell-command trajectory events, and related error paths.

The deterministic planted-command case can be targeted independently. It uses
a local ChatCompletion stub even when live credentials are exported:

```bash
MACHTIANI_BIN="$test_agent" bash agent/tests/run-live.sh command-supervisor-smoke
```

#### Targeting Cases

Run every registered case with no arguments, or pass one or more case names:

```bash
bash agent/tests/run-live.sh
bash agent/tests/run-live.sh issue-a-1turn
bash agent/tests/run-live.sh issue-a-1turn empty-goal test_code_no_forge
```

Dedicated test functions use underscore names, while generic happy/error cases may use hyphenated IDs. Passing an unknown name prints the registered case list:

```bash
bash agent/tests/run-live.sh no-such-case 2>&1 | head -5
```

#### Repo Synchronization and Installed Binary

The harness does not install, profile-link, PATH-resolve, or invoke a host
command named `machtiani`. Build committed `HEAD` under a collision-free test
name, then pass that absolute path explicitly:

```bash
rev="$(git rev-parse HEAD)"
short="$(git rev-parse --short=12 HEAD)"
built="$(git show -s --format=%ct HEAD)"
test_agent="$(mktemp -d)/mct-rg-ls-sed-test-agent"
nix develop .#default -c bash -c \
  "cd agent && CGO_ENABLED=0 go build -trimpath -ldflags '-X main.Version=dev-$short -X main.Commit=$rev -X main.BuiltAt=$built -X main.Dirty=clean' -o '$test_agent' ./cmd/machtiani"
MACHTIANI_BIN="$test_agent" bash agent/tests/run-live.sh discovery-multiround-budget
```

This workflow leaves `~/.local/bin/machtiani` and
`~/.machtiani/installations/machtiani/profile` untouched. Installation and
update behavior belongs only in the disposable clean-container smoke test.

The timeout and context-policy cases include a delayed local endpoint, a
real-provider large committed README/diff, and a deterministic 21-request
discovery flow that grows tool history through forced finalization. The latter
asserts every request stays at or below its derived cap and observes compacted
state. Target them directly with:

```bash
MACHTIANI_BIN="$agent_store/bin/machtiani" bash agent/tests/run-live.sh \
  discovery-turn-timeout \
  discovery-multiround-budget \
  discovery-context-budget-live
```

The harness pre-assigns session IDs and reads turn counts and final-answer assertions directly from session artifacts rather than scraping stderr.

#### Artifacts and Live Debugging

Each case writes a `test-out-*` directory with stdout, stderr, transcripts, and final artifacts. The authoritative trajectory is under the UUID home store. Resolve it with `PROJECT_STORE="$HOME/.machtiani/$(cat .machtiani/project.uuid)"`.

```bash
ls -dt test-out-* | head
tail -f test-out-*/stderr-*.txt
tail -f test-out-*/stdout-*.txt
PROJECT_STORE="$HOME/.machtiani/$(cat .machtiani/project.uuid)"
jq -r '.kind' "$PROJECT_STORE/sessions/<session-id>/trajectory/agent.jsonl" | sort -u
jq 'select(.level != "info") | {ts, level, kind, err: (.err.message // null)}' \
  "$PROJECT_STORE/sessions/<session-id>/trajectory/agent.jsonl"
jq 'select(.kind == "agent.turn.end") | {step: .payload.step, decision: .payload.decision, status: .payload.status, finalized: (.payload.finalized // false), err: (.err.message // null)}' \
  "$PROJECT_STORE/sessions/<session-id>/trajectory/agent.jsonl"
```

Useful harness toggles:

- `TRACE_TEST_CONFIG=true` prints the generated test configuration; do not expose real credentials in shared logs.
- `KEEP_TEST_CONFIG=true` preserves the generated configuration under `agent/tests/tmp/`.
- `TEST_ORCH_MODEL` and `TEST_FILE_DISCOVERY_MODEL` override component models in live mode.

The harness stops at the first failing case. Immediate DNS or connection failures usually indicate unavailable network access. A later shell-agent timeout points to the ask execution path, not initial provider wiring.

#### Concurrent Startup Cleanup Reproduction

The targeted regression harness launches overlapping sessions to verify that a second startup does not delete the first session's live workspace:

```bash
EXPECT_REPRO=false bash agent/tests/repro-concurrent-run-cleanup.sh
```

Use `EXPECT_REPRO=true` only to confirm the historical buggy behavior. `KEEP_REPRO_ARTIFACTS=true` preserves logs, and `STUB_DELAY_SECONDS=...` adjusts the overlap window.

### Standalone and specialist agent scripts

The following executable files are intentionally outside the default Go and
`run-live.sh` ladders. Their status matters when choosing a test:

| Entrypoint | Status and use |
| --- | --- |
| `agent/tests/command-supervisor-smoke.sh` | Maintained deterministic helper used by `run-live.sh` and the clean-container smoke. Prefer those parent entrypoints unless diagnosing the supervisor fixture itself. |
| `agent/tests/print-mode-test.sh` | Maintained opt-in live smoke for `machtiani run --exec`; it skips unless `MACHTIANI_LIVE_PRINT_TEST=1` and requires a configured live model. |
| `agent/tests/ab-dev-host-isolation-test.sh` | Specialist Docker regression for the A/B runner's host-worktree isolation. Run when changing `scripts/ab-dev.sh` or its build context. |
| `agent/tests/enforce_early_commands_test.sh` | Specialist live A/B comparison. The maintained pass/fail regression is the `test_enforce_early_commands` case in `run-live.sh`; use this standalone script only for comparative trajectory evidence. |
| `agent/tests/replay_fewshot_test.sh` | Legacy exploratory A/B script that expects a repository-root binary and private configuration. It is not a current landing gate; use the session Go tests for few-shot placement and `run-live.sh` for maintained end-to-end behavior. |
| `agent/tests/ab-live.sh` | Output parser used by `scripts/ab-dev.sh --parse-results`, not an independent product test. It always exits successfully and reports parsed status in TSV. |

The A/B workflow itself remains a supported specialist tool and is documented
in `agent/tests/README.md` and `scripts/README.md`. It is not required for an
ordinary change unless the task calls for control-versus-treatment evidence.

## TUI Record and Replay

Use the record/replay harness when changing terminal output, footer rendering, startup headers, token accounting, final-answer presentation, or anything else where a normal stdout diff misses cursor movement. The harness records one live `machtiani run`, captures the LLM responses as fixtures, then replays the same prompt against a local replay server.

### Record a Fresh Session

Build the binaries you want to test under collision-free names and pass the
agent path explicitly:

```bash
cd agent
GOCACHE=$(pwd)/.gocache go build -o ../.data/bin/mct-tui-test-agent ./cmd/machtiani
GOCACHE=$(pwd)/.gocache go build -o ../.data/bin/replay-server ./cmd/replay-server
cd ..

MACHTIANI_BIN="$PWD/.data/bin/mct-tui-test-agent" \
  ./tests/tui/record-replay.sh \
  --name planner-shell-agent-cache \
  --prompt-file .data/tui-replay/planner-shell-agent-cache/prompt.txt
```

Use `--verbose` to capture the diagnostic control path. Use
`--theme machtiani-dark` (or `machtiani-light`, `terminal`, `none`) to pin the
visual profile in both live and replay phases.

The harness prints `run_dir=...`. A typical path is:

```text
.data/tui-replay/planner-shell-agent-cache/runs/20260710T010414Z
```

The live phase requires network access to the configured LLM provider. The replay phase does not; it uses the captured fixtures.

### Artifact Layout

Each run directory contains:

- `live.terminal.log` and `replay.terminal.log` — full PTY transcripts captured with `script(1)`. Use these for user-visible TUI regressions, because they include ANSI cursor movement.
- `live.tui.txt` and `replay.tui.txt` — formatter-level capture from `MACHTIANI_TUI_CAPTURE`. Use these for event-rendering checks, but not for exact terminal repaint behavior.
- `live.llm-fixtures.jsonl` — recorded provider responses used by replay.
- `replay.config.toml` — sanitized config whose provider URLs point to the local replay server and whose API keys are `dummy`.
- `manifest.txt` — run directory, binary paths, session ids, fixture count, line counts, trajectory action counts, and final-answer hashes.
- `machtiani.version.txt`, `binaries.txt`, `live.exit`, `replay.exit`, and `replay-server.log` — provenance and process diagnostics.

### What to Check

Start with `manifest.txt`. For a healthy deterministic replay, the live and replay final-answer hashes should match, and event/action counts should be equal:

```bash
RUN_DIR=.data/tui-replay/planner-shell-agent-cache/runs/<timestamp>
cat "$RUN_DIR/manifest.txt"
```

For TUI-specific checks, inspect targeted substrings instead of line-by-line plaintext diffs. ANSI scroll regions and footer repainting can make stripped logs look concatenated.

Useful checks:

```bash
rg -- 'Resume this session:|machtiani run|tokens|session agent-|turn [0-9]+' "$RUN_DIR/replay.terminal.log"
rg '^\[trajectory\] unified stream:|^Session: ' "$RUN_DIR/replay.terminal.log" || true
rg 'achtiani|\x1b\[6n|\x1b\]11;\?' "$RUN_DIR/replay.terminal.log" || true
```

Expected normal-output behavior:

- no startup `[trajectory] unified stream: ...`
- no startup `Session: <id>`
- normal completion output contains the saved final-answer path and concise `machtiani run -p "..." --resume <session-id>` command; the detailed completion summary is reserved for `--verbose`
- final footer includes elapsed time, token totals, turn, and `session <id>`
- fresh-session logo appears once when that feature is enabled
- no unexpected terminal cursor/background queries such as `ESC[6n` or `OSC 11`; theme selection never probes the background

### Development Feedback Loop

Use the full harness when you need new live fixtures, then iterate quickly with fixture replay:

1. Build the local binary under a collision-free name such as `.data/bin/mct-tui-test-agent`.
2. Run `tests/tui/record-replay.sh` once to create a fresh run directory.
3. Inspect `live.terminal.log`, `replay.terminal.log`, and `manifest.txt`.
4. Make a focused code change.
5. Rebuild `.data/bin/mct-tui-test-agent`.
6. Replay from the existing `live.llm-fixtures.jsonl` instead of spending another live LLM run.

Manual replay from an existing fixture:

```bash
SRC=.data/tui-replay/planner-shell-agent-cache/runs/<timestamp>
RUN_DIR=.data/tui-replay/planner-shell-agent-cache/runs/manual-replay
PORT=20115
mkdir -p "$RUN_DIR"
cp "$SRC/replay.config.toml" "$RUN_DIR/replay.config.toml"
perl -0pi -e "s#http://127\\.0\\.0\\.1:[0-9]+#http://127.0.0.1:$PORT#g" "$RUN_DIR/replay.config.toml"

.data/bin/replay-server \
  -port "$PORT" \
  -fixtures "$SRC/live.llm-fixtures.jsonl" \
  > "$RUN_DIR/replay-server.log" 2>&1 &
server_pid=$!

PROMPT="$(cat "$SRC/prompt.txt")"
MACHTIANI_CONFIG="$PWD/$RUN_DIR/replay.config.toml" \
  script -q -f -e -c "env MACHTIANI_CONFIG='$PWD/$RUN_DIR/replay.config.toml' MACHTIANI_TUI_CAPTURE='$PWD/$RUN_DIR/replay.tui.txt' '$PWD/.data/bin/mct-tui-test-agent' run --prompt '$PROMPT' --turn-timeout 0" \
  "$RUN_DIR/replay.terminal.log"

kill "$server_pid"
```

When running under a sandbox, the replay server needs permission to bind a local `127.0.0.1` port. If the replay appears stuck with zero token usage, check `replay-server.log` for a socket bind error.

### Capture and Replay Components

`MACHTIANI_TUI_CAPTURE=/path/to/file` tees formatter-rendered output to a file. It does not capture all direct process writes or terminal cursor behavior, so use the PTY-based harness for visual regressions.

`LLM_RECORD_FIXTURES=/path/to/fixtures.jsonl` records provider HTTP responses for deterministic replay. Build the standalone replay server with:

```bash
cd agent
go build -o ../.data/bin/replay-server ./cmd/replay-server
cd ..
```

Run it with `.data/bin/replay-server -fixtures <fixture.jsonl> -port <port>`. Responses are replayed sequentially; requests after fixture exhaustion receive HTTP 502. Prefer `tests/tui/record-replay.sh` over manually coordinating these components unless debugging the recorder or server itself.

## Evaluation

### HEAD-Based Evaluation (run_eval_head.sh)

A separate evaluation pipeline that tests the machtiani and Forge on arbitrary tasks on the current repository HEAD, without requiring pre-defined ground truth commits. It creates worktrees from HEAD, runs agents in plan and implement phases, and uses a judge to score their plans and implementations against the current repo state. Supports two modes:

- write (default): The judge also produces its own implementation as an unscored benchmark.
- read-only: The judge only evaluates and scores the agents.

#### Quick Start

```bash
# Read the API key for your LLM provider from the configured secrets file
API_KEY=$(cat <path-to-your-api-key-file>)

# Run the HEAD-based evaluation with default write mode
env -u MACHTIANI_SESSION_ID -u MACHTIANI_SESSION_TEMP_ROOT \
  MACHTIANI_SESSION_TEMP_ROOT=/tmp/isolated_machtiani_sessions \
  bash scripts/run_eval_head.sh \
    --repo "$(pwd)" \
    --prompt /path/to/task_prompt.md \
    --model glm-5-high-deepinfra \
    --api-key "<provider>:${API_KEY}" \
    --config .machtiani/config.toml \
    --output-dir /tmp/my_eval_output \
    --keep
```

Replace <path-to-your-api-key-file> with the path to your API key file. Replace <provider> with the provider matching the model in .machtiani/config.toml (e.g., deepinfra, openai, openrouter, anthropic)., or use the `TEST_API_KEY (or your own API key)` environment variable if set: `--api-key <provider>:${TEST_API_KEY}`.

#### Flags

| Flag | Required | Description |
| --repo | yes | Path to the target git repository |
| --prompt | yes | Path to a markdown file describing the task |
| --model | no | Model alias for machtiani and Forge (default: glm-5-high-deepinfra) |
| --judge-model | no | Separate model alias for the judge agent |
| --mode | no | write (default) or read-only |
| --api-key | no | API key in provider:key format (e.g., deepinfra:sk-... ; the provider must match the model's provider in .machtiani/config.toml) |
| --config | no | Path to Machtiani config file (default: .machtiani/config.toml) |
| --output-dir | no | Artifact output directory (default: /tmp/eval_timestamp) |
| --keep | flag | Preserve worktrees after run |
| --sequential | flag | Run agents sequentially instead of in parallel |

#### Output Artifacts

All files land under output-dir/:

- worktree_mct/, worktree_forge/, worktree_judge/ - agent worktrees checked out at HEAD
- mct_plan.md, forge_plan.md - agent plans
- mct_answer.md, forge_answer.md - agent implementation answers
- mct_changes.patch, forge_changes.patch - agent code diffs
- judgment.md - judge scoring output
- judge_impl_prompt.md, judge_answer.md, judge_changes.patch - judge implementation (write mode only)
- report.md - summary report with scores and winners

#### Session Lock Workaround

When running inside an active machtiani session (e.g., during development), unset the inherited session variables to avoid flock conflicts:

```bash
env -u MACHTIANI_SESSION_ID -u MACHTIANI_SESSION_TEMP_ROOT \
  MACHTIANI_SESSION_TEMP_ROOT=/tmp/isolated_machtiani_sessions \
  bash scripts/run_eval_head.sh ...
```

#### Validation Checklist

After a successful run, verify:

1. All three worktrees exist with HEAD checked out. Use git -C worktree rev-parse HEAD to check.
2. Plan answer files (mct_plan.md, forge_plan.md) contain non-empty content
3. Implementation patches (star_changes.patch) contain changes or are legitimately empty
4. judgment.md contains scores for both axes (Plan Quality: Accuracy, Completeness, Specificity; Implementation Quality: Correctness, Precision, Completeness)
5. In write mode, judge_changes.patch and judge_answer.md are present
6. report.md is generated with a summary table

### Ground-Truth Evaluation (run_eval.sh)

The original evaluation pipeline, documented in scripts/run_eval.sh itself. It compares agent outputs against a known ground truth commit and requires --eval-commit and --ground-truth flags. Refer to the script header and inline comments for usage details.

## Environment Variable Reference
- `TEST_API_KEY`, `TEST_BASE_URL`, `TEST_MODEL` — preferred live credentials for repository test harnesses and required by the Docker smoke test.
- `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL` — general runtime credentials and the fallback for live harnesses that prefer `TEST_*`; harnesses with stubs use stub/dry-run mode when neither set is available.
- `MACHTIANI_TUI_CAPTURE` — formatter-level TUI capture path. The record/replay harness sets this automatically for `live.tui.txt` and `replay.tui.txt`.
- `MACHTIANI_THEME` — overrides `[ui].theme` with `terminal`, `machtiani-dark`, `machtiani-light`, or `none`. The record/replay harness also accepts `--theme`.
- `MACHTIANI_GLYPHS` — overrides `[ui].glyphs` with `unicode` or `ascii`.
- `LLM_RECORD_FIXTURES` — JSONL fixture output path for recorded LLM responses. The record/replay harness sets this automatically for `live.llm-fixtures.jsonl`.

## Troubleshooting
- Unit tests should pass without extra setup; if they fail due to missing cache directories, ensure your shell honors the `GOCACHE` export above.
- Integration runs that report a missing binary require a valid absolute
  `MACHTIANI_BIN`; rebuild committed `HEAD` under a collision-free test name as
  shown above and pass that path.
- Stub mode is active when outputs mention `stub-echo`, `mock`, or `--dry-run`. Verify that the required `OPENAI_*`/`MACHTIANI_CONFIG` values are exported to switch to live mode.
- The Docker smoke runner tests committed `HEAD`, not working-tree changes. A surprising old result usually means the intended change has not been committed.
- A Docker smoke run must not be considered successful merely because configuration initialization passed; verify the live run and conversation artifact checks also completed.

## Related Documentation
- `README.md` — quick-start install and environment setup guidance.
- `docs/machtiani-runbook.md` — repo-local synchronization and operation guidance.

## Shared installation ownership

`agent/internal/update/coordinated_test.go` checks that a DearMachine-owned
installation blocks standalone profile mutation, including custom data roots
and incomplete ownership records. The command suite checks that standalone
install and update requests delegate to the coordinated launcher. Run
`go test ./internal/update ./cmd/machtiani` from `agent` with isolated Git
configuration so disposable fixture commits do not inherit operator signing.
The sibling Installer's managed Nix container suite covers installation order,
launcher takeover, preserved data and restoration after failed activation.

## Configuration and credential isolation

`agent/internal/configfiles`, `agent/internal/llm`, and the configuration command
Go tests cover private credential storage, selected-file resolution, migration,
copying, environment-only compatibility, and non-disclosure. Keep both HOME and
XDG_CONFIG_HOME isolated in fixtures.

After building an absolute test binary, run the real command regression:

```console
python3 tests/smoke/config-isolation.py /absolute/path/to/test-machtiani
```

It creates a disposable home and two repositories, configures separate personal
and DearMachine credentials, and executes real `init` and `sync` commands against
a loopback-only fake provider. It checks which key reaches HTTP and verifies that
a missing selected credential fails before HTTP. No actual keys, email, installed
services, or host configuration are used. This deterministic check supplements
the existing smoke and product evaluation gates.

## Native Windows development proof

Run the cross-compiled `internal/shell-agent/internal/environments` test binary
inside a disposable Windows guest with native Git Bash on PATH.
`TestWindowsVisibleFiles` checks a Windows path with spaces and Unicode, and
`TestWindowsCancellationStopsNativeDescendants` checks that cancelling a shell
also terminates its native Windows descendants. See the umbrella's
`tests/windows-native/README.md` for the installed runtime and live file task.
