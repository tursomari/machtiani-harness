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
