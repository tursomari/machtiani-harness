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
- Config: produces a temporary `.machtiani/config.toml` under `agent/tests/tmp/`; exports `MACHTIANI_CONFIG` for the run. Live mode reuses your `OPENAI_*` values, dry-run mode writes stub credentials and forces `--dry-run`.

Scenarios covered
- Issue A/B/C happy paths (1-turn and 3-turn max steps)
- Error: empty prompt
- Error: missing config (only when live env vars are present)

Modes
- Live: export `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` before running.
- Dry-run: omit the env vars; the script injects stub credentials and appends `--dry-run`.

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
