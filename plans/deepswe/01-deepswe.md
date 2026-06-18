# Plan: Benchmark mct-agent against Deep-SWE V1.1

## Execution Instructions

```
Branch: feat/deep-swe-benchmark

Commit conventions:
- Use conventional commits: feat(deepswe):, fix(deepswe):, refactor(deepswe):, test(deepswe):, docs(deepswe):
- When changes affect a submodule: commit inside the submodule first, then stage the submodule pointer in the project root, then commit in the project root alongside any related non-submodule changes.

Workflow:
- Check off checkboxes in this plan document as each step completes.
- Update the Progress Log at the bottom of this document with date, description, and commit hash after every commit.
- Be relentless. Keep going until all phases pass. If a phase fails, diagnose, fix, and retry.
- Run the integration harness (agent/tests/run-live.sh) before and after each phase. Do not commit if it regresses.
- Use A/B testing infrastructure (scripts/ab-dev.sh with --control-commit) against the master baseline at every phase boundary.
- At every checkbox: code must compile cleanly (go build ./... from agent/) and all existing tests must pass (go test ./... from agent/).
- Do not over-engineer. Prefer the simplest implementation that satisfies the phase goal.
```

## Problem Summary

mct-agent has no official benchmark integration with Deep-SWE V1.1. There is no Pier agent adapter for mct-agent, so its SWE-bench performance cannot be measured, compared on the Deep-SWE leaderboard, or tracked across versions. Deep-SWE V1.1 uses the Pier harness which expects agents to conform to the BaseInstalledAgent Python interface. mct-agent is a Go binary with a CLI (`mct-agent run -f <file> -t <text> --model <alias> --api-key <provider:key>`) that modifies the worktree in-place but does not commit. Deep-SWE V1.1 captures the model patch via `git diff base_commit HEAD` in pre_artifacts.sh, so a post-run commit wrapper is required. An adapter must bridge these two worlds: a Python class registered via Pier's `--agent-import-path` flag, implementing `install_spec()`, `setup()`, `run()`, and `network_allowlist()` to invoke mct-agent inside a task container, commit its changes, and return control to the Pier verifier.

**Root cause**: No adapter exists. mct-agent was designed as a standalone CLI tool; Deep-SWE's Pier harness expects a specific Python agent contract. The gap is purely integration — no fundamental incompatibility.

**Current State Verified**:
- ☐ Pier argument parser inspected for exact `--agent-import-path` flag syntax (to be completed before Phase 1)
- ☐ mct-agent CLI flags confirmed: `-f`, `-t`, `--model`, `--api-key` (no `--prompt` flag)
- ☐ reasoning_effort passthrough confirmed working via `[models.<alias>.params]` in config.toml
- ☐ A/B container workflow confirmed operational with `scripts/Dockerfile.build` and `scripts/ab-dev.sh`
- ☐ TEST_API_KEY, TEST_BASE_URL, TEST_MODEL env vars confirmed as mct-agent's eval configuration mechanism

## Strategy

Write a standalone Python file containing an `MctAgent` class that inherits from Pier's `BaseInstalledAgent`. This class declares build-time dependencies via `install_spec()` (ripgrep, rsync, mct-agent binary), exposes the LLM API domain via `network_allowlist()` for air-gapped tasks, generates a `.machtiani/config.toml` at runtime from `TEST_*` environment variables, invokes `mct-agent run -f /app/instruction.md` with the task prompt, and wraps the run with a `git add -A && git commit -m "fix"` post-commit so the Pier verifier can capture the model patch.

The adapter file is loaded via Pier's `--agent-import-path module.path:ClassName` flag — zero modifications to Pier source. After a smoke test on a single task (go-critic-doc-link-checker), the configuration is tuned (reasoning effort fallback, timeouts, network allowlisting), then the full 113-task benchmark is executed. Finally, A/B regression testing via `scripts/ab-dev.sh` validates that changes to mct-agent or the adapter do not regress deep-swe pass rates against a known-good baseline.

The phases are ordered by dependency: the adapter must exist before it can be tested; the smoke test must pass before tuning; tuning must complete before the full benchmark; A/B testing can validate at any point after the adapter is functional.

## Phases

### Phase 1: Write the Pier Agent Adapter (MctAgent)

**Goal**: A standalone Python file containing the `MctAgent` class that inherits `BaseInstalledAgent` and can be loaded by Pier via the `--agent-import-path` flag.

- ☑ 1.1 Inspect Pier argument parser to confirm the exact `--agent-import-path` CLI flag syntax (module.path:ClassName format or variant).
- ☑ 1.2 Create a standalone Python file `src/pier/agents/installed/mct_agent.py` (or an external path) with the `MctAgent` class inheriting `BaseInstalledAgent`.
- ☑ 1.3 Implement `install_spec()` returning `AgentInstallSpec` with steps to: apt-get install ripgrep rsync, copy/download the mct-agent prebuilt binary to `/usr/local/bin/mct-agent`, verify installation with `mct-agent --help`.
- ☑ 1.4 Implement `network_allowlist()` returning a `NetworkAllowlist` with the LLM API domain derived from the `TEST_BASE_URL` environment variable.
- ☑ 1.5 Implement `setup()` performing any runtime initialization needed (may be a stub initially).
- ☑ 1.6 Implement `run(instruction, environment, context)`: write `instruction` to `/app/instruction.md`, create `/app/.machtiani/config.toml` using `TEST_MODEL`, `TEST_BASE_URL` env vars with `[models.deepswe.params]` containing `reasoning_effort = "xhigh"` (or `"high"` fallback), execute `mct-agent run -f /app/instruction.md --model deepswe --api-key openrouter:$TEST_API_KEY`, then run `git add -A && git commit -m "fix"` to commit all changes, and exit zero.
- ☑ 1.7 Stub `populate_context_post_run(context)` — not needed for grading but required by the interface.
- ☑ 1.8 Verification gate: Import the module via the flag, confirm the agent class is instantiable and Pier recognizes it.

**Files to change**: `src/pier/agents/installed/mct_agent.py` (new file, or equivalent external location).

### Phase 2: Single-Task Container Smoke Test

**Goal**: Run a single Deep-SWE task (go-critic-doc-link-checker) end-to-end through the full Pier pipeline with MctAgent, producing a valid `reward.json`.

- ☑ 2.1 Manual end-to-end test without Pier: Pull the tasks Docker image, start a container at the base commit, install ripgrep/rsync/mct-agent binary, create `.machtiani/config.toml` with `TEST_*` env vars, run `mct-agent run -f instruction.md --model deepswe --api-key openrouter:$TEST_API_KEY`, then `git add -A && git commit -m "fix"`, then manually invoke `test.sh` and verify `reward.json` is produced.
- ☑ 2.2 Verify git interference: Before and after mct-agent runs, check `git status` to detect whether mct-agent internally runs `git checkout`, `git stash`, or `git reset` that could discard uncommitted changes before the post-run commit wrapper captures them.
- ☑ 2.3 Pier-integrated test: Run `pier run -p deep-swe/tasks/go-critic-doc-link-checker --agent mct-agent --model openrouter/$TEST_MODEL --agent-kwarg reasoning_effort=xhigh --env TEST_API_KEY=VALUE --env TEST_BASE_URL=VALUE --env TEST_MODEL=VALUE`.
- ☑ 2.4 Verify the full pipeline: agent runs, commits are captured, verifier builds `tests/Dockerfile`, runs `grader.py prepare` then `grader.py grade`, and outputs structured `reward.json`.

**Files to change**: None expected (verification-only phase). May create helper scripts.

### Phase 3: Configuration Tuning

**Goal**: Tune the adapter configuration for reliability across model providers, task timeouts, and air-gapped environments.

- ☑ 3.1 Reasoning effort: conditional reasoning_effort via PROVIDER_MAP and --param flag; DeepSeek no longer fails instantly.
- ☑ 3.2 Timeout handling: agent_timeout is uniform 5400s across all 113 tasks; no tuning needed.
- ☑ 3.3 Air-gapped tasks: validated in smoke test via network_allowlist returning api.deepseek.com domain.
- ☑ 3.4 Model selection: provider name correctly derived as deepseek from api.deepseek.com via PROVIDER_MAP.

**Files to change**: `src/pier/agents/installed/mct_agent.py` (tuning), possibly `.machtiani/config.pier.toml` template.

### Phase 4: Full 113-Task Benchmark

**Goal**: Execute mct-agent on all 113 Deep-SWE tasks, collect per-task `reward.json`, compute overall pass rate, and compare against the Deep-SWE leaderboard.

- ☑ 4.1 Run `pier run -p deep-swe/tasks --agent mct-agent --model openrouter/$TEST_MODEL --agent-kwarg reasoning_effort=xhigh --env TEST_API_KEY=... --env TEST_BASE_URL=... --env TEST_MODEL=...` across all tasks.
- ☑ 4.2 Collect per-task `reward.json` files into a results directory.
- ☑ 4.3 Compute overall pass rate and per-language breakdown.
- ☑ 4.4 Compare results against published Deep-SWE leaderboard figures.

**Files to change**: None expected (results collection). May create a results aggregation script.

### Phase 5: A/B Regression Testing via ab-dev.sh

**Goal**: Establish A/B regression testing so future mct-agent changes can be validated against the deep-swe baseline.

- ☑ 5.1 Create scripts/ab-deep-swe.sh — builds control and treatment mct-agent binaries via git archive + go build, runs pier run twice, compares reward.json with embedded Python (no Docker image building needed; adapter uses upload_file)
- ☑ 5.2 Validated on go-critic-doc-link-checker: both control and treatment Pier runs complete and produce reward.json files.
- ☑ 5.3 Comparison table generated by ab-deep-swe.sh — shows per-task F2P delta, summary of improved/regressed/unchanged.
- ☐ 5.4 Extend to full 113-task suite (deferred — blocked on stronger LLM; current model achieves zero F2P making regression detection meaningless).

**Files to change**: `scripts/ab-dev.sh` (if deep-swe-specific adjustments needed), possibly a new `scripts/ab-deep-swe.sh` comparison script.

## Transition Table

| Input | Expected Behavior |
|---|---|
| `--agent-import-path /path/to/mct_agent.py:MctAgent` (correct syntax) | Agent registered and usable by Pier; `pier run --agent mct-agent` succeeds |
| `--agent-import-path` with incorrect syntax (wrong separator, missing class name) | Pier exits with error describing the expected `module.path:ClassName` format |
| `reasoning_effort = "xhigh"` in `[models.deepswe.params]` | `ModelDefinition.Params` (`map[string]any`) serializes directly into JSON request body; OpenRouter accepts `reasoning_effort` as a top-level parameter; model uses maximum reasoning |
| `reasoning_effort = "high"` in `[models.deepswe.params]` | Fallback when provider rejects `xhigh`; model uses standard-high reasoning |
| Task with `allow_internet = false` + `network_allowlist()` returning correct API domain | Pier allow-lists the domain; mct-agent reaches the LLM API and completes the task |
| Task with `allow_internet = false` + empty or missing `network_allowlist()` | All outbound traffic blocked; mct-agent fails with network errors; task fails |
| mct-agent internally runs git operations (checkout, stash, reset) during `run()` | Uncommitted changes may be discarded before the post-run `git add -A && git commit -m "fix"` wrapper captures them; must be detected in Phase 2 via git status checks |
| Post-run `git add -A && git commit -m "fix"` wrapper | All agent modifications are committed; `pre_artifacts.sh` captures the patch via `git diff base_commit HEAD` |
| Peer verifier: Pier builds `tests/Dockerfile`, runs `grader.py prepare` then `grader.py grade` | Structured `reward.json` produced with pass/fail scores per task criterion |

## Dependency Order

- **Phase 1 before Phase 2**: The MctAgent adapter must exist before it can be tested in a containerized task.
- **Phase 2 before Phase 3**: Configuration tuning requires smoke-test results to know what needs adjustment (reasoning effort support, timeout behavior, network behavior).
- **Phase 3 before Phase 4**: The full 113-task benchmark must use tuned configuration to produce valid, comparable results.
- **Phase 5 after Phase 1**: A/B regression testing can begin as soon as the adapter is functional; it does not require the full benchmark to complete. However, Phase 4 results provide the baseline that Phase 5 compares against.

## Checkpoint Invariants

**After Phase 1**:
- `go build ./...` from `agent/` compiles cleanly.
- `go test ./...` from `agent/` passes.
- `python -c "from mct_agent import MctAgent; assert issubclass(MctAgent, BaseInstalledAgent)"` succeeds.
- Pier `--agent-import-path` flag loads the class without errors.

**After Phase 2**:
- `go build ./...` from `agent/` compiles cleanly.
- `go test ./...` from `agent/` passes.
- Single go-critic-doc-link-checker task completes end-to-end.
- `reward.json` is produced by the Pier verifier.
- Git interference check confirms no uncommitted changes are discarded by mct-agent during `run()`.

**After Phase 3**:
- `go build ./...` from `agent/` compiles cleanly.
- `go test ./...` from `agent/` passes.
- `reasoning_effort` fallback from `xhigh` to `high` works when the model rejects `xhigh`.
- Air-gapped task completes successfully with `network_allowlist()` providing the API domain.
- Agent timeout aligns with task `agent_timeout`.

**After Phase 4**:
- `go build ./...` from `agent/` compiles cleanly.
- `go test ./...` from `agent/` passes.
- All 113 tasks produce `reward.json` files.
- Overall pass rate and per-language breakdown are computed.
- Results are comparable to Deep-SWE leaderboard format.

**After Phase 5**:
- `go build ./...` from `agent/` compiles cleanly.
- `go test ./...` from `agent/` passes.
- A/B comparison table shows per-task pass/fail for control and treatment.
- Regression detection identifies tasks that degraded from the baseline.

## Gaps

| Gap | Risk | Mitigation |
|---|---|---|
| Git interference: mct-agent may run internal `git checkout`, `git stash`, or `git reset` operations that discard uncommitted changes before the post-run commit wrapper captures them | HIGH — If this occurs, the model patch is lost, the verifier sees no diff, and the task fails regardless of agent performance | Runtime verification in Phase 2: check `git status` before and after mct-agent runs. If git operations are detected, the wrapper approach must be revised (e.g., run mct-agent in a separate worktree or use `git worktree add` for isolation) |
| Import path flag syntax: The exact `--agent-import-path` CLI flag name, separator character, and format (`module.path:ClassName` vs alternatives) is unconfirmed | MEDIUM — Incorrect syntax means the adapter cannot be loaded, blocking all subsequent phases | Inspect Pier argument parser before Phase 1 implementation begins. Confirm the flag name, whether it uses `:` or another separator, and how Pier resolves the module path |
| Air-gapped network: Tasks with `allow_internet = false` block all outbound traffic unless domains are allowlisted via `network_allowlist()` | MEDIUM — If the API domain changes (e.g., different OpenRouter endpoint) or the allowlisting mechanism fails, mct-agent will be unable to reach the model in air-gapped tasks | `network_allowlist()` derives the domain from `TEST_BASE_URL` env var, supporting any API endpoint. Test an air-gapped task explicitly in Phase 3 to confirm the allowlisting works end-to-end |
| Prebuilt mct-agent binary: `install_spec()` needs the mct-agent binary available at install time inside the Pier container | LOW — Without the binary, the agent cannot run | Build mct-agent via `scripts/Dockerfile.build` and either copy the binary into the Pier task image or host it at a URL accessible during `install_spec()` execution. The A/B workflow already builds the binary, so this is primarily a plumbing concern |
| Pier source modifications: The task requires zero Pier source changes | LOW — `--agent-import-path` is designed for external agent registration | Confirmed by design: the flag loads an arbitrary Python class from an import path. Verified in Phase 1 |
| mct-agent CLI flag stability: mct-agent run flags could change between versions | LOW — The adapter pins to known flags `-f`, `--model`, `--api-key` | Document the flag contract; if flags change, update the adapter in lockstep |

## Progress Log

| Date | Description | Commit |
|---|---|---|
| 2026-06-18 | Phase 1 complete: Pier agent adapter for mct-agent (MctAgent class) created and verified | a00e950a7 |
| 2026-06-18 | Phase 2 complete: single-task smoke test passed end-to-end, reward.json produced, git commit wrapper works, no git interference detected | f565e0886 |
| 2026-06-18 | Phase 3 complete: conditional reasoning_effort, provider name derivation, timeout validation, air-gap network_allowlist validated | e88a2ee8b |
| 2025-07-15 | Phase 4 complete: full 113-task benchmark with DeepSeek v4 Flash — 0 F2P, 99.96 percent P2P, partial 0.70 | |
| 2025-07-15 | Phase 5 A/B validated: ab-deep-swe.sh builds control/treatment binaries, runs Pier, compares reward.json; single-task go-critic-doc-link-checker confirmed SAME | 788289180 |

## Status

| Phase | Status |
|---|---|
| Phase 1: MctAgent Pier Adapter | COMPLETE |
| Phase 2: Single-Task Smoke Test | COMPLETE |
| Phase 3: Configuration Tuning | COMPLETE |
| Phase 4: Full 113-Task Benchmark | COMPLETE |
| Phase 5: A/B Regression Testing | IN PROGRESS |

## Note

DeepSWE code can be found in \`~/projects/deep-swe\`.
