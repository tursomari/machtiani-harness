# Project-Wide Testing Guide

This guide collects the commands and environment settings needed to run the unit and integration suites in the `mct-agent` monorepo. Use it as the single entry point for local development or CI pipelines.

## Prerequisites
- Go 1.23+ installed and on PATH.
- `git` for cloning fixture repositories during integration tests.
- `rg` (ripgrep) recommended on PATH; the integration harnesses expect it when exercising the toolchain.
- For live LLM runs: valid values for `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL`.

## Unit Tests
Run from the repository root to cover all Go packages without touching integration harnesses:

```bash
cd agent
GOCACHE=$(pwd)/.gocache go test ./...
cd ..
```

- No environment variables are required.

## Integration Tests
All integration harnesses default to deterministic stub or dry-run behavior. Export the listed environment variables to invoke live LLM calls.

### Live Agent Integration Tests (`agent/tests/run-live.sh`)
End-to-end regression suite for the installed `mct-agent` binary.

```bash
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1
export OPENAI_MODEL=gpt-4o-mini
./scripts/install.sh && bash agent/tests/run-live.sh
```

- Assumes `mct-agent` is on PATH; `./scripts/install.sh` handles this in CI or a clean checkout.
- When the `OPENAI_*` variables are missing the script generates stub credentials, writes a temporary `config.toml`, and forces `--dry-run`.
- Artifacts land in `test-out-*` directories at the repo root; each case includes stdout, stderr, transcripts, and (for live runs) generated assets.
- Optional overrides:
  - `OPENAI_ORCH_MODEL`, `OPENAI_FILE_DISCOVERY_MODEL` — pick specific remote models per component.
  - `OPENAI_ORCH_MODEL_ALIAS`, `OPENAI_FILE_DISCOVERY_MODEL_ALIAS` — supply config aliases when reusing a shared `config.toml`.
  - `OPENAI_API_KEY`/`BASE_URL`/`MODEL` remain authoritative even when aliases are set.

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

PATH="$PWD/.data/bin:$PATH" ./scripts/tui-record-replay.sh \
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
2. Run `scripts/tui-record-replay.sh` once to create a fresh run directory.
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
- `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL` — primary credentials for live runs; omitting them keeps all harnesses in stub/dry-run mode.
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
- Inspect `agent/TESTING.md` and `tests/TESTING.md` for deep-dive scenarios, debugging scripts, and additional `jq` helpers.

## Related Documentation
- `agent/TESTING.md` — detailed walkthrough of `agent/tests/run-live.sh` scenarios, including the meta-mode `--mode code` regression coverage and telemetry.
- `tests/TESTING.md` — advanced options for the undici harness.
- `README.md` — quick-start install and environment setup guidance.
