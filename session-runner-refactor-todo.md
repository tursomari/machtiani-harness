# Session runner refactor todo

Target: `agent/internal/session/runner.go:506` (`Run()`)

## Intent

Keep end-to-end session behavior the same, but turn `Run()` into a thin orchestrator that delegates lifecycle/state, finalize/shutdown, and transcript/conversation mechanics to focused helpers.

## Phase 1 — establish state/lifecycle seams

- [x] Introduce a lifecycle/state owner for persisted session state and resume bookkeeping.
- [x] Centralize conversation/transcript recording behind a recorder abstraction.
- [x] Move lifecycle/state helpers into an adjacent concern-based file.

Relevant files:
- `agent/internal/session/runner_state.go`

## Phase 2 — extract finalize/shutdown concerns

- [x] Extract finalize/shutdown helpers.
- [x] Centralize final answer writing, resume hint output, and final state persistence.
- [x] Decide whether extracted helpers belong in adjacent files by concern.

Relevant files:
- `agent/internal/session/runner_finalize.go`
- `agent/internal/session/runner.go:2565`
- `agent/internal/session/runner.go:2591`

## Phase 3 — keep shrinking `Run()` into orchestration only

- [x] Extract resume/input/bootstrap setup from `agent/internal/session/runner.go:515`.
- [x] Extract ask-turn execution and post-turn sync from `agent/internal/session/runner.go:946`.
- [x] Extract patch apply/review decision flow from `agent/internal/session/runner.go:1177`.
- [x] Extract phase orchestration and top-level error handling from `agent/internal/session/runner.go:506`.

Checkpoint note:
- [x] Re-run live `agent/tests/run-live.sh` end-to-end before the remaining Phase 3 bootstrap extraction.
- Why: live validation already caught one real regression from the turn-helper extraction (stale `runState` after ask/review/patch helpers), so a green live baseline was the safest checkpoint before changing resume/input/bootstrap setup.
- Status: focused stabilization work is in place across `agent/internal/session/runner.go`, `agent/internal/mct/prompt/preflight.go`, `.machtiani/templates/shell-agent/system_template.tpl`, and submodule `agent/internal/shell-agent/internal/agents/default.go`.
- Status: package tests passed for `go test ./internal/mct/prompt ./internal/shell-agent/internal/agents ./internal/session`, and `bash agent/tests/run-live.sh` completed successfully end-to-end.
- Next: return to `Extract resume/input/bootstrap setup`; if a later live failure appears, first classify whether it is in session-runner orchestration or shell-agent routing/search behavior before changing `Run()` again.

Desired end state for `Run()`:

```text
load/resume session
initialize runtime state
loop over planner decisions
finalize
persist/cleanup
```

## Phase 4 — clean up transition debt

- [ ] Remove mirrored local state that still has to be copied into `runLifecycleState` around `agent/internal/session/runner.go:638`.
- [ ] Remove `syncRunState` / `syncFromRunState` bridging once `Run()` stops owning duplicate lifecycle fields at `agent/internal/session/runner.go:899`.
- [ ] Collapse any remaining duplicate locals that only exist to support partial extraction.

## Non-goals

- [ ] Do not change external session behavior, transcript format, resume semantics, or patch/review behavior as part of the refactor.
- [ ] Do not broaden the refactor outside `agent/internal/session/*` unless needed to preserve clean boundaries.
