# Project-Wide Testing Guide

This is the canonical, self-contained guide for every test harness in the `mct-agent` monorepo. Test commands, prerequisites, environment variables, artifacts, and debugging guidance belong here rather than in component-local testing documents.

## Test Layout

- Go `*_test.go` files stay beside the packages they exercise.
- `agent/tests/` owns integration harnesses specific to the `mct-agent` binary and their helpers.
- `tests/smoke/` owns the clean-container smoke Dockerfile, host runner, and container assertions.
- `tests/tui/` owns whole-terminal record/replay regression tooling.
- `tests/python/` owns Python adapter tests.
- Other root `tests/` subtrees hold cross-component fixtures and integration harnesses such as Undici.
- `scripts/` retains installation, benchmark, treatment, and evaluation runners; those stable paths are intentionally separate from test harnesses.

## Prerequisites
- Go 1.23+ installed and on PATH.
- `git` for cloning fixture repositories during integration tests.
- `rg` (ripgrep) recommended on PATH; the integration harnesses expect it when exercising the toolchain.
- Docker, for the clean-container smoke test.
- For live LLM runs: valid `TEST_API_KEY`, `TEST_BASE_URL`, and `TEST_MODEL` values, or the `OPENAI_*` fallbacks supported by the selected harness.

## Unit Tests
Run from the repository root to cover all Go packages without touching integration harnesses:

```bash
cd agent
GOCACHE=$(pwd)/.gocache go test ./...
cd ..
```

- No environment variables are required.

### Targeted Component Suites

The root sweep covers the standard Go suites below. Use these commands when focusing on one component or enabling a non-default build tag.

#### File Discovery

```bash
cd agent/internal/file-discovery
go test ./...
go test ./e2e -v
go test ./internal/discovery -run TestToolCallExecution_RealCommands -v
go test -tags e2e_slow_rg ./e2e -run CmdTimeout -v
```

The E2E and real-command suites require `rg`; tool-call execution also uses `sed` and `ls`. To exercise the flag suite in its component test image:

```bash
cd agent/internal/file-discovery
docker build -f tests/Dockerfile -t file-discovery-tests .
docker run --rm -it --entrypoint /usr/local/bin/run-flags.sh file-discovery-tests
docker run --rm -it -e RUN_SLOW=1 --entrypoint /usr/local/bin/run-flags.sh file-discovery-tests
```

For the live Undici scenarios, first install the peripheral binary, then run the component harness with live credentials:

```bash
./scripts/install.sh --install-peripherals
cd agent/internal/file-discovery/tests/undici
export OPENAI_API_KEY=...
export OPENAI_BASE_URL=...
export OPENAI_MODEL=...
bash ../run-live.sh
```

The live harness writes per-scenario stdout, stderr, and trajectory JSONL artifacts. Exit code `2` means the API key is missing; exit code `127` usually means a mounted binary has the wrong OS or architecture.

#### Internal README Manager

```bash
cd agent/internal/mct
GOCACHE=$(pwd)/.gocache go test ./...
```

Its five-scenario offline Undici synchronization harness is documented under “Internal Undici Regression Harness” below.

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
python3 tests/python/test_mct_pier_adapter.py
```

This suite tests protected-artifact staging and preservation commands and verifies that the existing benchmark treatment launchers do not expose verifier logs to agents.

## Clean-Container Smoke Test

The Docker smoke test builds `mct-agent` in a clean image, exercises the full
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
the Go updater tests verify initialization of the recorded build submodule.

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

The host runner creates a detached Git worktree at the current committed `HEAD`, copies populated submodules from the host checkout, builds `tests/smoke/Dockerfile`, and runs `tests/smoke/container.sh` inside the resulting image. Consequently, uncommitted changes are not included. Commit the changes you intend to test, or manually build the image from the current checkout when iterating on the smoke infrastructure itself.

The container test must complete all of these checks before printing its success message:

1. A clean Git repository with an initial commit is created in `/workspace`.
2. `mct-agent --version` runs.
3. `config-crud.sh` inspects the built-in provider catalogue, then creates the
   primary provider/model with the DeepSeek preset and an API-key environment
   reference; the literal live key is never passed as a CLI argument or written
   to TOML.
4. `mct-agent init --no-interactive` creates a UUID marker/home store, installs
   canonical modes, selects global config, and remains idempotent without
   replacing existing configuration or runtime sentinels.
5. A pseudo-terminal wizard run verifies that an existing OpenRouter provider
   retains `Search current model catalogue` when another model is added, without
   making a live OpenRouter request or changing the disposable configuration.
6. Every provider, model, and cache subcommand is exercised, including
   reference-aware rename/removal, reasoning and parameter updates, cache
   inheritance, inspection, validation, and expected failure paths.
7. `--path`, `--global`, `MACHTIANI_CONFIG`, and explicit environment override
   behavior are verified with disposable configurations.
8. Rejected mutations are checksum-verified as non-writing, scratch resources
   are removed, and the final primary configuration is validated with caching
   disabled.
9. A deterministic local ChatCompletion server rejects the first request with
   a structured context-overflow error, accepts the reduced retry, and verifies
   the warning plus automatic per-model `context_length` persistence.
10. `mct-agent sync` initializes the repository's internal README state.
11. `mct-agent run` completes successfully against the primary live provider.
12. Any additional discoverable provider targets are added with `config
    provider/model`, validated, and exercised with a low-reasoning live run.
13. Two deterministic internal READMEs are synced at successive project
    commits; after checking out the earlier commit while the mutable artifact
    still contains the later README, a fresh dry-run session injects the exact
    earlier tagged blob into `conversation.json`.
14. `<project-store>/sessions/*/artifacts/conversation.json` exists and the
    repository contains `.machtiani/project.uuid` rather than session data.
15. A separate synthetic legacy repository passes migration dry-run and actual
    non-interactive migration, including verified home-store data and a legacy
    archive.

The scripts use `set -euo pipefail`; any failed command must produce a non-zero harness exit and must not print `SMOKE TEST PASSED`.

## Integration Tests
All integration harnesses default to deterministic stub or dry-run behavior. Export the listed environment variables to invoke live LLM calls.

### Live Agent Integration Tests (`agent/tests/run-live.sh`)
End-to-end regression suite for the installed `mct-agent` binary.

```bash
export TEST_API_KEY=sk_...
export TEST_BASE_URL=https://api.openai.com/v1
export TEST_MODEL=gpt-4o-mini
./scripts/install.sh && bash agent/tests/run-live.sh
```

- Assumes `mct-agent` is on PATH; `./scripts/install.sh` handles this in CI or a clean checkout.
- `TEST_*` takes precedence over `OPENAI_*`. When neither complete set is available, the script generates stub credentials, writes a temporary `config.toml`, and forces `--dry-run`.
- Artifacts land in `test-out-*` directories at the repo root; each case includes stdout, stderr, transcripts, and (for live runs) generated assets.
- The provider-independent `managed_update_local_remote` case exercises the
  origin-derived bootstrap, managed-source reuse and safety checks, and the
  explicit updater against an isolated local bare Git remote. It is included
  in the default suite and can be targeted directly.
- Optional overrides:
  - `TEST_ORCH_MODEL`, `TEST_FILE_DISCOVERY_MODEL` — preferred per-component remote-model overrides; the corresponding `OPENAI_*` values are fallbacks.
  - `OPENAI_ORCH_MODEL_ALIAS`, `OPENAI_FILE_DISCOVERY_MODEL_ALIAS` — supply config aliases when reusing a shared `config.toml`.
  - `OPENAI_API_KEY`/`BASE_URL`/`MODEL` remain authoritative even when aliases are set.

Scenarios include Issue A/B/C happy paths, one-turn and three-turn bounds, per-component model selection, the `--mode code` regression in stub and live paths, empty prompts, missing configuration, menu flow, shell-command trajectory events, and related error paths.

#### Targeting Cases

Run every registered case with no arguments, or pass one or more case names:

```bash
bash agent/tests/run-live.sh
bash agent/tests/run-live.sh issue-a-1turn
bash agent/tests/run-live.sh managed_update_local_remote
bash agent/tests/run-live.sh issue-a-1turn empty-goal test_code_no_forge
```

Dedicated test functions use underscore names, while generic happy/error cases may use hyphenated IDs. Passing an unknown name prints the registered case list:

```bash
bash agent/tests/run-live.sh no-such-case 2>&1 | head -5
```

#### Repo Synchronization and Installed Binary

The harness uses `mct-agent` from `PATH`; it does not build the binary or mutate `PATH`. Install it first:

```bash
./scripts/install.sh
```

If a run reports that `mct` is not synced at the current Git state, synchronize it and rerun the harness:

```bash
MACHTIANI_CONFIG=.machtiani/config.toml \
mct-agent sync \
  --api-key "openrouter:$TEST_API_KEY" \
  --model glm-5-high \
  --context-length 128000
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

### Undici Harness (`tests/run-agent-undici.sh`)
Validates README regeneration against the undici fixture repository.

```bash
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1
export OPENAI_MODEL=gpt-4o-mini
MACHTIANI_CONFIG=$HOME/.machtiani/config.toml \
MODEL_ALIAS=qwen3-coder-plus \
./tests/run-agent-undici.sh
```

- Builds `mct`, `mct-agent`, and `file-discovery` into an isolated temp PATH; no prior install step required.
- Defaults to offline stubs unless `DISABLE_MCT_STUBS=true` is exported. Live runs need the `OPENAI_*` variables above plus a valid `MACHTIANI_CONFIG` and `MODEL_ALIAS` that maps to credentials in that config file.
- Emits artifacts under `tests/artifacts/agent-undici/<case>/`. Preserve the temp workspace by setting `KEEP_AGENT_TMP=true`.
- Additional knobs mirror the script defaults: `MAX_STEPS`, `TIMEOUT_PER_TURN`, `MCT_LLM_TEST_STUB`, `MCT_README_TEST_STUB`.

The five scenarios cover initial generation, a significant change, a repeated run, a docs-only change, and the latest state. The harness asserts README tags and commit behavior and exits non-zero on the first failed assertion.

Set `KEEP_AGENT_TMP=true` to retain the scratch repository and its `.machtiani` state. Useful diagnostics include:

```bash
find tests/tmp -name agent.jsonl
jq -r '.kind' <trajectory>/agent.jsonl | sort -u
jq 'select(.level != "info") | {ts, level, kind, err: (.err.message // null)}' \
  <trajectory>/agent.jsonl
jq -r '.type' tests/artifacts/agent-undici/<case>/file-discovery/file-discovery.jsonl | sort -u
```

### Internal Undici Regression Harness (`agent/internal/mct/tests/run-undici-readme-integration.sh`)
Maintains backward-compatibility checks for the internal README manager using the same undici fixture.

```bash
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1
export OPENAI_MODEL=gpt-4o-mini
bash agent/internal/mct/tests/run-undici-readme-integration.sh
```

- Uses stub providers by default (`MCT_LLM_TEST_STUB=stub-echo`, `MCT_README_TEST_STUB=mock`).
- Supply the `OPENAI_*` variables to exercise live mode; the script falls back to stub credentials otherwise.
- Produces artifacts in `agent/internal/mct/tests/artifacts/readme/` and cleans its temp workspace unless `KEEP_README_TEST_TMP=true` is set.

## TUI Record and Replay

Use the record/replay harness when changing terminal output, footer rendering, startup headers, token accounting, final-answer presentation, or anything else where a normal stdout diff misses cursor movement. The harness records one live `mct-agent run`, captures the LLM responses as fixtures, then replays the same prompt against a local replay server.

### Record a Fresh Session

Build the binaries you want to test, then make sure the repo-built `mct-agent` is first on `PATH`:

```bash
cd agent
GOCACHE=$(pwd)/.gocache go build -o ../.data/bin/mct-agent ./cmd/mct-agent
GOCACHE=$(pwd)/.gocache go build -o ../.data/bin/replay-server ./cmd/replay-server
cd ..

PATH="$PWD/.data/bin:$PATH" ./tests/tui/record-replay.sh \
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
- `mct-agent.version.txt`, `binaries.txt`, `live.exit`, `replay.exit`, and `replay-server.log` — provenance and process diagnostics.

### What to Check

Start with `manifest.txt`. For a healthy deterministic replay, the live and replay final-answer hashes should match, and event/action counts should be equal:

```bash
RUN_DIR=.data/tui-replay/planner-shell-agent-cache/runs/<timestamp>
cat "$RUN_DIR/manifest.txt"
```

For TUI-specific checks, inspect targeted substrings instead of line-by-line plaintext diffs. ANSI scroll regions and footer repainting can make stripped logs look concatenated.

Useful checks:

```bash
rg -- 'Continue with your next instruction:|--session-id|tokens|session agent-|turn [0-9]+' "$RUN_DIR/replay.terminal.log"
rg '^\[trajectory\] unified stream:|^Session: ' "$RUN_DIR/replay.terminal.log" || true
rg 'achtiani|\x1b\[6n|\x1b\]11;\?' "$RUN_DIR/replay.terminal.log" || true
```

Expected normal-output behavior:

- no startup `[trajectory] unified stream: ...`
- no startup `Session: <id>`
- normal completion output contains only the concise `--session-id <id>` continuation command; the detailed completion summary is reserved for `--verbose`
- final footer includes elapsed time, token totals, turn, and `session <id>`
- fresh-session logo appears once when that feature is enabled
- no unexpected terminal cursor/background queries such as `ESC[6n` or `OSC 11`; theme selection never probes the background

### Development Feedback Loop

Use the full harness when you need new live fixtures, then iterate quickly with fixture replay:

1. Build the local binary into `.data/bin/mct-agent`.
2. Run `tests/tui/record-replay.sh` once to create a fresh run directory.
3. Inspect `live.terminal.log`, `replay.terminal.log`, and `manifest.txt`.
4. Make a focused code change.
5. Rebuild `.data/bin/mct-agent`.
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
  script -q -f -e -c "env MACHTIANI_CONFIG='$PWD/$RUN_DIR/replay.config.toml' MACHTIANI_TUI_CAPTURE='$PWD/$RUN_DIR/replay.tui.txt' '$PWD/.data/bin/mct-agent' run --text '$PROMPT' --turn-timeout 0" \
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

A separate evaluation pipeline that tests the mct-agent and Forge on arbitrary tasks on the current repository HEAD, without requiring pre-defined ground truth commits. It creates worktrees from HEAD, runs agents in plan and implement phases, and uses a judge to score their plans and implementations against the current repo state. Supports two modes:

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
| --model | no | Model alias for mct-agent and Forge (default: glm-5-high-deepinfra) |
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

When running inside an active mct-agent session (e.g., during development), unset the inherited session variables to avoid flock conflicts:

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
- `MACHTIANI_CONFIG` — optional path to a pre-existing Machtiani config. Required when `tests/run-agent-undici.sh` runs live because it overrides `HOME`.
- `MODEL_ALIAS` — maps to a section inside `MACHTIANI_CONFIG` for undici live runs.
- `DISABLE_MCT_STUBS` — set to `true` to disable stubbed LLM providers in the undici harness and force real calls.
- `KEEP_AGENT_TMP`, `KEEP_README_TEST_TMP` — keep the temporary workspaces for post-run inspection.
- `MAX_STEPS`, `TIMEOUT_PER_TURN` — tuning knobs for the undici harness plan/execution loop.
- `MACHTIANI_TUI_CAPTURE` — formatter-level TUI capture path. The record/replay harness sets this automatically for `live.tui.txt` and `replay.tui.txt`.
- `MACHTIANI_THEME` — overrides `[ui].theme` with `terminal`, `machtiani-dark`, `machtiani-light`, or `none`. The record/replay harness also accepts `--theme`.
- `LLM_RECORD_FIXTURES` — JSONL fixture output path for recorded LLM responses. The record/replay harness sets this automatically for `live.llm-fixtures.jsonl`.

## Troubleshooting
- Unit tests should pass without extra setup; if they fail due to missing cache directories, ensure your shell honors the `GOCACHE` export above.
- Integration runs that report missing binaries typically mean the PATH does not include the install prefix. Re-run `./scripts/install.sh` or inspect the temp PATH emitted by the harness.
- Stub mode is active when outputs mention `stub-echo`, `mock`, or `--dry-run`. Verify that the required `OPENAI_*`/`MACHTIANI_CONFIG` values are exported to switch to live mode.
- The Docker smoke runner tests committed `HEAD`, not working-tree changes. A surprising old result usually means the intended change has not been committed.
- A Docker smoke run must not be considered successful merely because configuration initialization passed; verify the live run and conversation artifact checks also completed.

## Related Documentation
- `README.md` — quick-start install and environment setup guidance.
- `docs/mct-agent-runbook.md` — repo-local synchronization and operation guidance.
