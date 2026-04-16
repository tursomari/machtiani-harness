Unit Tests

- Run from the repo root:
  ```
  cd agent
  GOCACHE=$(pwd)/.gocache go test ./...
  ```
- The explicit `GOCACHE` keeps build artifacts inside the workspace when sandboxed or running in CI environments that restrict `$HOME`.

Agent Integration Tests (mct-agent)

- Entry script: `agent/tests/run-live.sh`
- Purpose: Exercise `mct-agent` end-to-end using the binaries already on PATH. Supports live LLM calls or deterministic dry-run.
- Preflight: validates the `mct-agent` binary on PATH, prints `--version`/`go version -m` metadata, and fails if the commit/time is out of sync with the current repo. Optional CLIs are not required for this harness.
- Config: produces a temporary `.machtiani/config.toml` under `agent/tests/tmp/`; exports `MACHTIANI_CONFIG` for the run. Live mode uses `TEST_API_KEY` / `TEST_BASE_URL` / `TEST_MODEL` when set, otherwise falls back to `OPENAI_*`; dry-run mode writes stub credentials and forces `--dry-run`.

Targeted repros
- Concurrent startup cleanup regression: `agent/tests/repro-concurrent-run-cleanup.sh`
- Purpose: launches overlapping `mct-agent run` sessions to check that a second startup does not delete the first session's live `workspace-*` directory.
- Modes:
  - `EXPECT_REPRO=true bash agent/tests/repro-concurrent-run-cleanup.sh` confirms the old buggy behavior.
  - `EXPECT_REPRO=false bash agent/tests/repro-concurrent-run-cleanup.sh` confirms the fix.
- Helpful toggles:
  - `KEEP_REPRO_ARTIFACTS=true` preserves logs and generated config under `agent/tests/tmp/`.
  - `STUB_DELAY_SECONDS=...` widens or narrows the overlap window between the two sessions.
- Like `agent/tests/run-live.sh`, this harness uses the `mct-agent` binary already on `PATH`.

Scenarios covered
- Issue A/B/C happy paths (1-turn and 3-turn max steps)
- Meta-orchestrator `--mode code` regression coverage in both stub-backed and live-provider paths
- Error: empty prompt
- Error: missing config (only when live test/provider env vars are present)

Modes
- Live: export `TEST_API_KEY`, `TEST_BASE_URL`, and `TEST_MODEL` before running, or omit `TEST_*` and let the harness fall back to `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL`.
- Optional live overrides: `TEST_ORCH_MODEL`, `TEST_PATCHER_MODEL`, and `TEST_FILE_DISCOVERY_MODEL` take precedence over the corresponding `OPENAI_*` component model vars.
- Dry-run: omit the env vars; the script injects stub credentials and appends `--dry-run`.

Running from Codex or other agents
- Preferred live setup when the agent itself also uses `OPENAI_*` for its own auth/config:
  ```bash
  export TEST_MODEL=google/gemini-3-flash-preview
  export TEST_BASE_URL="https://openrouter.ai/api/v1"
  export TEST_API_KEY="..."
  bash agent/tests/run-live.sh
  ```
- `TEST_*` takes precedence over `OPENAI_*` inside `agent/tests/run-live.sh`, so the harness can target a live provider without changing the agent's own environment.
- Run from the repo root with `bash agent/tests/run-live.sh`. The path `agent/tests/run-live.sh` also works directly when the current working directory is already the repo root.
- In sandboxed agent environments, live mode may need one-time network approval before the harness can reach the configured provider. A DNS/network failure before the first case completes is usually an environment restriction, not a bad `TEST_*` value.
- Avoid printing secrets while debugging. It is fine to verify that `TEST_API_KEY` is set, but do not echo the full value into logs or transcripts.
- Helpful debug toggles:
  - `TRACE_TEST_CONFIG=true` prints the generated test config.
  - `KEEP_TEST_CONFIG=true` preserves the generated `.machtiani` config tree under `agent/tests/tmp/`.

Artifacts and validations
- Each case writes `test-out-*` directories containing stdout/stderr, transcripts, and final artifacts when applicable.
- Transcript turn counts checked against the `--max-steps` bounds; keywords asserted in stdout (and final artifacts in live mode).
- Error cases assert canonical error messages in stderr/stdout.
- The unified trajectory stream lives under `.machtiani/sessions/<session-id>/trajectory/agent.jsonl`. Handy `jq` probes when debugging:
  - `jq -r '.kind' trajectory/agent.jsonl | sort -u` — inventory of emitted event kinds for the session.
  - `jq 'select(.kind == "agent.session.start") | {session: .session_id, goal_sha: .payload.goal_sha256, max_steps: .payload.config.max_steps}' trajectory/agent.jsonl`
  - `jq 'select(.kind == "agent.turn.end") | {step: .payload.step, decision: .payload.decision, status: .payload.status, finalized: (.payload.finalized // false), err: (.err.message // null)}' trajectory/agent.jsonl`
  - `jq 'select(.kind == "planner.request") | {step: (.payload.step // null), alias: .payload.model_alias, prompt_excerpt: .payload.prompt_excerpt_first}' trajectory/agent.jsonl`
  - `jq 'select(.kind == "planner.response") | {level: .level, step: (.payload.step // null), parse_ok: .payload.parse_ok, decision: (.payload.parse.decision? // null), err: (.err.message // null)}' trajectory/agent.jsonl`
  - `jq 'select(.kind == "mct.prompt.result") | {mode: .payload.mode, retrieved: .payload.retrieved_count, saved_chat: .payload.saved_chat_path, dry_run: (.payload.dry_run // false)}' trajectory/agent.jsonl`
  - `jq 'select(.kind | startswith("llm.")) | {kind, level, payload, err: (.err.message // null)}' trajectory/agent.jsonl`
- `jq 'select(.level != "info") | {ts, level, kind, err: (.err.message // null)}' trajectory/agent.jsonl` — quick sweep for LLM issues or other failures.
- `jq 'select(.kind == "transcript.write") | {op: .payload.op, bytes: .payload.written_bytes, decision: (.payload.decision // null), path: .payload.path}' trajectory/agent.jsonl`

Monitoring a live run
- Watch the harness stderr in real time first; each case prints `Running ...`, `Passed: ...`, or `Failed (rc=...)`.
- When a case starts, inspect the newest `test-out-*` directory:
  ```bash
  ls -dt test-out-* | head
  ```
- Tail the case logs while the harness is still running:
  ```bash
  tail -f test-out-*/stderr-*.txt
  tail -f test-out-*/stdout-*.txt
  ```
- Parse the active session ID from stderr (`Session: <id>`), then inspect `.machtiani/sessions/<id>/trajectory/agent.jsonl` for ground truth.
- To confirm that `TEST_*` wiring is actually in effect, look for all of these in stdout or trajectory:
  - `provider":"run-live-provider"`
  - `base_url":"https://openrouter.ai/api/v1"`
  - `alias":"google/gemini-3-flash-preview"` or your configured `TEST_MODEL`
- A quick provider-wiring check:
  ```bash
  rg 'run-live-provider|https://openrouter.ai/api/v1|google/gemini-3-flash-preview' \
    .machtiani/sessions/<id>/trajectory/agent.jsonl
  ```

Interpreting failures
- Immediate `dial tcp`, DNS, or `Temporary failure in name resolution` errors usually mean the agent sandbox still blocks outbound network access.
- `llm http error 502` or similar upstream failures can be transient provider errors; the harness may retry and later succeed.
- If live mode activates and the trajectory shows the expected provider/model values, the `TEST_*` env wiring is working even if a later case fails.
- A later `shell-agent exited` or `timed out after 300s` failure points to the ask execution path or timeout budget, not to the initial provider config.
- The harness stops at the first failing case, so the most recent `test-out-*` directory and matching session trajectory are the first places to inspect.

Real-time tailing
- The `internal/trajectory/listener` package can follow the JSONL file while a run is in-flight.
- Quick scratch program for local debugging:
  ```go
  package main

  import (
    "context"
    "errors"
    "flag"
    "fmt"
    "log"
    "time"

    "github.com/tursomari/machtiani/agent/internal/trajectory/listener"
  )

  func main() {
    path := flag.String("path", "", "trajectory JSONL to follow")
    followLatest := flag.Bool("latest", false, "start at end of file")
    flag.Parse()
    if *path == "" {
      log.Fatal("missing --path")
    }
    sub, err := listener.New(*path)
    if err != nil {
      log.Fatal(err)
    }
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    err = sub.Subscribe(ctx, listener.SubscribeOptions{FollowFromLatest: *followLatest}, func(ctx context.Context, evt listener.Event) error {
      fmt.Printf("%s %-24s %s\n", evt.Timestamp.Format(time.RFC3339), evt.Component, evt.Kind)
      return nil
    })
    if err != nil && !errors.Is(err, context.Canceled) {
      log.Fatal(err)
    }
  }
  ```
- Save the snippet as `tail.go` and run `GOCACHE=$(pwd)/.gocache go run tail.go --path ~/.machtiani/sessions/<id>/trajectory/agent.jsonl`.
- Filter by kind/component with `SubscribeOptions.Kinds` or `SubscribeOptions.Components` once you know which events matter for your debugging session.

Run locally
```
./scripts/install.sh
bash agent/tests/run-live.sh
```
Prefer to avoid the installer? Use the manual command block in the root `README.md` first, then run `bash agent/tests/run-live.sh`.

CI guidance
```
PREFIX="$HOME/.local" ./scripts/install.sh
export PATH="$HOME/.local/bin:$PATH"
bash agent/tests/run-live.sh
```
When using an alternate install location, replicate the manual build block from the root `README.md` and adjust `PATH` accordingly before running the tests.

Notes
- The script never mutates PATH or accepts binary override flags; ensure the install location is already on PATH before invoking it.
- Timeout simulations that previously used stubs are skipped; rely on the live preflight + PATH binaries instead.
