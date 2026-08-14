# Plan: Production Deep-SWE Benchmark Run with mct-agent

## Execution Instructions

```
Branch: feat/deep-swe-benchmark (continue on existing branch)

Commit conventions:
- Use conventional commits: feat(deepswe):, fix(deepswe):, refactor(deepswe):, test(deepswe):, docs(deepswe):
- When changes affect a submodule: commit inside the submodule first, then stage the submodule pointer in the project root, then commit in the project root alongside any related non-submodule changes.

Workflow:
- Check off checkboxes in this plan document as each step completes.
- Update the Progress Log at the bottom of this document with date, description, and commit hash after every commit.
- Be relentless. Keep going until all phases pass. If a phase fails, diagnose, fix, and retry.
- Run the integration harness (agent/tests/run-live.sh) before and after each phase. Do not commit if it regresses.
- At every checkbox: code must compile cleanly (go build ./... from agent/) and all existing tests must pass (go test ./... from agent/).
- Do not over-engineer. Prefer the simplest implementation that satisfies the phase goal.
```

## Problem Summary

The initial Deep-SWE benchmark (Phase 1–5 of plan 01-deepswe) used a minimal mct-agent configuration: 4 steps, no mode, DeepSeek v4 Flash, generated config.toml from env vars, no `mct-agent sync`. This produced **0 F2P** (fail-to-pass) out of 52 test cases across 113 tasks, with 99.96% P2P (pass-to-pass) and 0.70 partial score. The adapter infrastructure works (zero adapter errors across all 113 tasks), but the agent configuration was far too weak.

The root cause is threefold: (1) no project context (`mct-agent sync` was never run, so the agent used a generic fallback prompt), (2) no mode overlay (bare run with no `code-forge` system prompt), and (3) insufficient step budget and model capability (4 steps with DeepSeek v4 Flash). This plan reconfigures the adapter for a production-grade run.

## Strategy

Replace the generated config approach with uploading the host `.machtiani/config.toml` and `.machtiani/modes/code-forge/` into the container, add a `mct-agent sync` step (with 10 retries on failure), and invoke `mct-agent run` with production flags (`--mode code-forge`, `--max-turns 1000`, `--turn-timeout 0`, `--model deepseek-v4-pro`, `--shell-agent-model deepseek-v4-pro`, `--max-input-tokens 800000`). Network allowlisting reads all provider domains from the uploaded config.toml. All CLI flags are configurable via environment variables with sensible defaults.

## mct-agent Invocation Sequence (inside container)

```
# 1. Upload host files into /app/.machtiani/
#    - config.toml → /app/.machtiani/config.toml (auto-discovered by walk-up-from-CWD, no MACHTIANI_CONFIG env var needed)
#    - modes/code-forge/ → /app/.machtiani/modes/code-forge/
#      (code.txt, shell-agent-system-prompt.txt, tasks.toml)

# 2. Sync project README (retry up to 10 times, fail task if all fail)
mct-agent sync --model deepseek-v4-pro --max-input-tokens 800000

# 3. Run agent
mct-agent run \
  --mode code-forge \
  --max-turns 1000 \
  --turn-timeout 0 \
  --model deepseek-v4-pro \
  --shell-agent-model deepseek-v4-pro \
  --max-input-tokens 800000 \
  -f /app/instruction.md

# 4. Commit changes for Pier verifier
git add -A && git commit -m "fix" --allow-empty || true
```

No `--api-key` flag — keys come from the uploaded config.toml automatically via mct-agent resolution order (CLI flag > config file `api_key` field > env var).

Set `DEEP_SWE_TASKS` to the `tasks` directory in the Deep-SWE checkout before
running the benchmark commands below.

## Key Design Decisions

| Decision | Rationale |
|---|---|
| Upload host config.toml instead of generating from env vars | Host config already has provider keys, model definitions, and reasoning effort params. No need to reconstruct this from `TEST_*` env vars. |
| Upload modes/code-forge/ only, not entire .machtiani/ | The .machtiani/ directory contains sessions/ and artifacts/ from the host repo that are irrelevant to the task container. Only config.toml and the specific mode directory are needed. |
| mct-agent sync before run | Without sync, the agent falls back to a generic prompt with no project context. Sync generates a project-specific README giving the agent repository understanding. |
| 10 sync retries, then fail task | Sync involves an LLM call that can fail due to network errors or API errors. 10 retries with backoff handles transient failures. If all 10 fail, the task is marked as failed rather than running without context. |
| All CLI flags configurable via env vars | Allows tuning per-run without editing the adapter file. Defaults match the production configuration above. |
| Network allowlist from config.toml providers | Air-gapped tasks (`allow_internet = false`) need all provider domains allowlisted. Parsing config.toml is more reliable than a single `TEST_BASE_URL` env var. |
| Same model for sync and run (deepseek-v4-pro) | Consistency. The sync step generates a README, which does not require a powerful model, but using the same model avoids maintaining two model configurations. |

## Phases

### Phase A: Update the Pier Adapter

**Goal**: Rewrite `mct_pier_adapter/mct_agent.py` to upload the host config, run sync with retries, invoke mct-agent with production flags, and parse provider domains from config.toml.

- ☑ A.1 Replace `_generate_config()` with config upload: in `run()`, use `environment.upload_file()` to copy the host `.machtiani/config.toml` to `/app/.machtiani/config.toml`. Source path: `./.machtiani/config.toml` relative to the repo root (the directory where `pier run` is executed).
- ☑ A.2 Upload `.machtiani/modes/code-forge/` directory: use `environment.upload_dir()` to copy the host `.machtiani/modes/code-forge/` to `/app/.machtiani/modes/code-forge/`. Verify the directory contains `code.txt`, `shell-agent-system-prompt.txt`, and `tasks.toml`.
- ☑ A.3 Add `mct-agent sync` step: after uploading config and modes, run `mct-agent sync --model deepseek-v4-pro --max-input-tokens 800000` via `self.exec_as_agent()`. Check exit code: 0 = success, non-zero = retry. Retry up to 10 times with exponential backoff (1s, 2s, 4s, 8s, 16s, 32s, 64s, 128s, 256s, 512s). If all 10 attempts fail, raise an exception to fail the task (do not proceed to `mct-agent run`).
- ☑ A.4 Update `mct-agent run` invocation: construct the command from env vars with defaults:
  - `MCT_MODE` (default: `code-forge`)
  - `MCT_MAX_STEPS` (default: `1000`)
  - `MCT_TIMEOUT_PER_TURN` (default: `0`)
  - `MCT_MODEL` (default: `deepseek-v4-pro`)
  - `MCT_SHELL_AGENT_MODEL` (default: `deepseek-v4-pro`)
  - `MCT_MAX_INPUT_TOKENS` (default: `800000`)
  - `MCT_SYNC_MODEL` (default: `deepseek-v4-pro`)
  - Remove `--api-key` from the command entirely.
  - Remove all `TEST_MODEL`/`TEST_BASE_URL`/`TEST_API_KEY` generation logic.
- ☑ A.5 Update `network_allowlist()`: parse the uploaded config.toml for all `[providers.<name>]` sections with `base_url` fields, extract hostnames, and return them in the allowlist. Fallback: `MCT_NETWORK_DOMAINS` env var (comma-separated) for explicit override. Last resort fallback: `["api.deepseek.com", "api.deepinfra.com", "openrouter.ai"]`.
- ☑ A.6 Update `install_spec()`: keep the existing apt-get step for ripgrep/rsync. The binary upload via `environment.upload_file()` in `setup()` remains unchanged.
- ☑ A.7 Remove all `TEST_MODEL`/`TEST_BASE_URL`/`TEST_API_KEY` references from the adapter. These env vars are no longer used for config generation. The only env vars the adapter reads are `MCT_*` flags and `MCT_AGENT_BINARY`.
- ☑ A.8 Verification gate: `cd agent && go build ./... && go test ./...` passes. Python syntax check on the adapter. Import check (ast.parse).

**Files to change**: `mct_pier_adapter/mct_agent.py`

### Phase A2: Forge Integration

**Goal**: Integrate the `forge` binary and `mct-forge` wrapper into the Pier container so that `--mode code-forge` can execute forge commands successfully. The Phase B smoke test revealed that forge was not available in the container, causing mct-forge commands to fail silently.

- ☑ A2.1 In `setup()`, upload the forge binary from `MCT_FORGE_BINARY` env var (defaulting to `~/.local/bin/forge`) to `/usr/local/bin/forge` and `chmod +x`.
- ☑ A2.2 Upload the `mct-forge` wrapper from `MCT_FORGE_WRAPPER` env var (defaulting to `peripherals/mct-forge`) to `/usr/local/bin/mct-forge` and `chmod +x`.
- ☑ A2.3 Upload the `~/.forge` directory from `MCT_FORGE_HOME` env var (defaulting to `~/.forge`) to `/root/.forge`.
- ☑ A2.4 Ensure `/root/.forge` exists before upload by running `mkdir -p /root/.forge`.
- ☑ A2.5 Update `install_spec()` if forge needs any system dependencies.
- ☑ A2.6 Verification gate: `go build ./...` and `go test ./...` pass and Python syntax check on the adapter passes.

> **Note**: Session data extraction was added in Step 8 of run(), copying /app/.machtiani/sessions/ to /logs/agent/sessions/ on the host-visible mount. Also added mkdir -p for /root/.forge and /app/.machtiani/modes/code-forge before uploads.

**Files to change**: `mct_pier_adapter/mct_agent.py`

### Phase B: Single-Task Smoke Test

**Goal**: Re-run the go-critic-doc-link-checker task with forge properly integrated. The original Phase B smoke test completed with F2P 0.667 but revealed forge was not available in the container, causing mct-forge commands to fail silently.

> **Note**: The original Phase B smoke test completed with F2P 0.667 but revealed forge was not available in the container, causing mct-forge commands to fail silently. The phase goal is now to re-run the go-critic-doc-link-checker task with forge properly integrated.

- ☑ B.1 Build the mct-agent binary: `cd agent && go build -o ../agent/bin/mct-agent ./cmd/mct-agent`
- ☑ B.2 Verify the host config has the required model definitions: confirm `[models.deepseek-v4-pro]` and `[providers.deepseek]` with `api_key` exist in `.machtiani/config.toml`.
- ☑ B.3 Run Pier on a single task: `pier run -p "$DEEP_SWE_TASKS/go-critic-doc-link-checker" --agent-import-path mct_pier_adapter.mct_agent:MctAgent --ae MCT_AGENT_BINARY=$(pwd)/agent/bin/mct-agent --n-concurrent 1`
- ☐ B.4 After the task run, extract `.machtiani/sessions/` from the container to the host job directory, for example by adding a post-run step in `populate_context_post_run` or using a Pier hook.
- ☐ B.5 Verify that session transcripts show mct-forge commands executed successfully with no `command not found` errors.
- ☑ B.6 Verify: sync completes (check trial log for "Readme synced for commit"), agent runs with `--mode code-forge` (check command in trial log), git commit captured, `reward.json` produced.
- ☑ B.7 If sync fails on all 10 retries, diagnose: check network allowlisting, config.toml upload, and model resolution inside the container.

**Files to change**: None expected (verification-only phase).

### Phase B2: 3-Task Validation

**Goal**: Validate forge integration across 3 different Deep-SWE tasks before scaling up.

- ☐ B2.1 Run 3 different Deep-SWE tasks with forge enabled.
- ☐ B2.2 Extract session data for all tasks.
- ☐ B2.3 Verify forge commands succeeded in all session logs.
- ☐ B2.4 Only proceed if all 3 tasks show forge working.

### Phase B3: 10-Task Validation

**Goal**: Validate forge integration at scale across 10 tasks before the full benchmark.

- ☐ B3.1 Run 10 tasks with forge enabled.
- ☐ B3.2 Extract session data for all tasks.
- ☐ B3.3 Verify forge commands succeeded across all session logs.
- ☐ B3.4 Only proceed if all 10 tasks show forge working.

### Phase C: Full 113-Task Benchmark

**Goal**: Execute mct-agent on all 113 Deep-SWE tasks with the production configuration, collect per-task `reward.json`, compute overall pass rate, and compare against the initial 0-F2P baseline.

- ☐ C.1 Run `pier run -p "$DEEP_SWE_TASKS" --agent-import-path mct_pier_adapter.mct_agent:MctAgent --ae MCT_AGENT_BINARY=$(pwd)/agent/bin/mct-agent --job-name mct-agent-deepswe-prod-v1 --n-concurrent 4`
- ☐ C.2 Collect per-task `reward.json` files from `jobs/mct-agent-deepswe-prod-v1/`.
- ☐ C.3 Compute overall F2P, P2P, partial scores and per-language breakdown.
- ☐ C.4 Compare against the initial baseline (0 F2P, 99.96% P2P, 0.70 partial with DeepSeek v4 Flash).

**Files to change**: None expected (results collection). May create a results aggregation script.

### Phase D: A/B Regression Testing

**Goal**: Validate that the reconfigured adapter produces consistent results and that future mct-agent changes can be regression-tested against this baseline.

- ☐ D.1 Update `scripts/ab-deep-swe.sh` to pass `MCT_AGENT_BINARY` env var and use the new adapter flags (the script already builds control/treatment binaries from two commits).
- ☐ D.2 Run A/B comparison on a 5-task subset: `bash scripts/ab-deep-swe.sh --control-commit <baseline-commit> --treatment-commit HEAD --tasks <subset-path> --concurrent 1`
- ☐ D.3 Verify both control and treatment produce `reward.json` files and the comparison table shows expected results (SAME for identical binaries, potential differences for code changes).
- ☐ D.4 Document the A/B workflow in the plan for future regression testing.

**Files to change**: `scripts/ab-deep-swe.sh` (minor updates for new env vars).

## Transition Table

| Input | Expected Behavior |
|---|---|
| Host `.machtiani/config.toml` with `deepseek-v4-pro` model definition + `api_key` | mct-agent resolves the model and API key from config file; no `--api-key` CLI flag needed |
| Host `.machtiani/modes/code-forge/` uploaded to `/app/.machtiani/modes/code-forge/` | `--mode code-forge` loads the planner overlay and shell-agent system prompt |
| `mct-agent sync` succeeds (exit 0) | Project README generated at `.machtiani/artifacts/readme/`; agent has repository context |
| `mct-agent sync` fails 10 times | Task fails with exception; Pier records the failure; no `mct-agent run` attempted |
| `mct-agent run` with `--mode code-forge --max-turns 1000` | Agent runs with forge system prompt, up to 1000 steps, no per-turn timeout |
| Air-gapped task with `network_allowlist()` returning all provider domains | mct-agent reaches all LLM APIs; sync and run both succeed |
| Air-gapped task with missing provider domain in allowlist | mct-agent fails with network errors; task fails |
| `git add -A && git commit -m "fix" --allow-empty` after agent run | All modifications committed; Pier captures diff via `git diff base_commit HEAD` |

## Testing Discipline

- Never run more than 10 tasks until 3-task validation (Phase B2) passes.
- Never run the full 113-task benchmark until 10-task validation (Phase B3) passes.
- Always run Pier in background and poll the logs every 60 seconds.
- Always extract `.machtiani/sessions/` from the container to the host before container cleanup.

## Session Data Extraction

The adapter should copy `/app/.machtiani/sessions/` to the host job directory, possibly using `populate_context_post_run` or a cleanup hook, so that session transcripts and trajectories are preserved for analysis after the container exits.

## Checkpoint Invariants

**After Phase A**:
- `go build ./...` from `agent/` compiles cleanly.
- `go test ./...` from `agent/` passes.
- Python syntax check passes on `mct_pier_adapter/mct_agent.py`.
- Adapter no longer references `TEST_MODEL`, `TEST_BASE_URL`, or `TEST_API_KEY`.
- Adapter reads `MCT_*` env vars for all CLI flags with correct defaults.
- `network_allowlist()` parses config.toml provider domains.

**After Phase B**:
- `go build ./...` from `agent/` compiles cleanly.
- `go test ./...` from `agent/` passes.
- Single go-critic-doc-link-checker task completes end-to-end with sync + run.
- Trial log shows `mct-agent sync` completed successfully.
- Trial log shows `mct-agent run` invoked with `--mode code-forge --max-turns 1000`.
- `reward.json` produced by Pier verifier.

**After Phase C**:
- All 113 tasks produce `reward.json` files.
- F2P score is measurably higher than 0 (the baseline).
- Per-language breakdown computed.

**After Phase D**:
- A/B comparison table shows per-task results for control and treatment.
- `scripts/ab-deep-swe.sh` updated with new env var support.

## Gaps

| Gap | Risk | Mitigation |
|---|---|---|
| Sync adds ~30-60s per task | MEDIUM — 113 tasks × 30-60s = 1-2 hours of sync overhead. Incremental sync on shared commits helps (14 project+commit pairs share commits). | Sync is cheap for repeated commits (tag reuse, no LLM call). First sync per project is the expensive one. |
| config.toml contains API keys for multiple providers | LOW — Keys are uploaded into the task container, which is ephemeral and air-gapped. No risk of key leakage beyond the container. | Container is destroyed after the task. Network allowlisting restricts outbound traffic to LLM API domains only. |
| `upload_file` / `upload_dir` for modes directory | LOW — Pier `upload_dir` copies a host directory into the container. If the modes directory has subdirectories or symlinks, it may not copy correctly. | Verify in Phase B that `code-forge/` uploads correctly and `--mode code-forge` resolves. |
| `mct-agent sync` needs git history | LOW — The task container starts at a specific base commit with full git history. Sync should work. | Verify in Phase B that sync can resolve HEAD in the container. |
| `--turn-timeout 0` means no timeout | MEDIUM — A hung LLM call could block the agent indefinitely. The Pier task timeout (5400s) provides an outer bound. | Acceptable tradeoff: the task timeout kills the container after 90 minutes. |

## Progress Log

| Date | Description | Commit |
|---|---|---|
| 2026-06-27 | Phase A2: Forge integration — upload forge binary, mct-forge wrapper, and ~/.forge directory to container; added session data extraction step to copy sessions/ to host logs mount | 9c1829084 |
| 2026-06-27 | Phase B smoke test completed with F2P 0.667 but revealed forge binary not available in container; mode code-forge loaded overlay but mct-forge commands failed silently; Phase A2 needed for forge integration | — |
| 2026-06-27 | Phase B: Single-task smoke test passed — F2P 0.667 on go-critic-doc-link-checker, sync succeeded, agent ran with --mode code-forge, reward.json produced | 8f5d44a5f |
| 2026-06-27 | Phase A: Rewrote Pier adapter — upload host config/modes, sync with retries, production CLI flags, config.toml-based network allowlist | 2a5ce8f4f |

## Status

| Phase | Status |
|---|---|
| Phase A: Update Pier Adapter | COMPLETE |
| Phase A2: Forge Integration | COMPLETE |
| Phase B: Single-Task Smoke Test | PENDING |
| Phase C: Full 113-Task Benchmark | PENDING — only proceed after Phase B3 passes |
| Phase D: A/B Regression Testing | PENDING — only proceed after Phase B3 passes |

## Baseline Comparison

| Metric | Initial Run (DeepSeek v4 Flash, 4 steps, no sync, no mode) | Production Run Target |
|---|---|---|
| F2P | 0 / 52 | > 0 |
| P2P | 99.96% | ≥ 99.96% |
| Partial | 0.70 | > 0.70 |
| Adapter errors | 0 / 113 | 0 |
