Flag-Focused Tests

Overview
- End-to-end tests under `./e2e` spawn the compiled `file-discovery` binary and validate CLI flag behavior in dry-run mode. They avoid live network calls and only require `rg` (ripgrep) in PATH.
- Unit tests under `./internal/...` cover helpers (`applyExcludes`, `applyPattern`, `formatRGOut`, `validateAndNormalizeFinalBlock`) and `config.TrajectoryRecorder`.
- Tool-call parsing/execution tests (`internal/discovery/toolcall_test.go`) iterate a shared scenario matrix and execute real `rg`, `sed`, and `ls` binaries against synthetic fixtures. Make sure those tools are on PATH.

Local Run
- Prerequisites: Go 1.23+, ripgrep installed (`rg` in PATH).
- Run all tests: `go test ./...`
- Run only E2E: `go test ./e2e -v`
- Run tool-call execution suite: `go test ./internal/discovery -run TestToolCallExecution_RealCommands -v`

Timeout Scenario
- The E2E timeout test uses a special build tag that overrides the RG runner to simulate a slow command.
- Not run by default in `run-flags`. Run explicitly:
  - Local: `go test -tags e2e_slow_rg ./e2e -run CmdTimeout -v`
  - Docker: `docker run --rm -it -e RUN_SLOW=1 --entrypoint /usr/local/bin/run-flags.sh file-discovery-tests`

Docker
- Build the test image: `docker build -f tests/Dockerfile -t file-discovery-tests .`
- Run the flag tests (override entrypoint):
  `docker run --rm -it --entrypoint /usr/local/bin/run-flags.sh file-discovery-tests`

Notes
- E2E tests skip automatically if `rg` is missing.
- Tests isolate environment by clearing `OPENAI_*` vars and manage `FILE_DISCOVERY_TRAJECTORY` explicitly.
- The RG output truncation footer is asserted by presence only (string starts with `[TRUNCATED:`), not by exact size value.
- The flag suite asserts that `-no-json` toggles the bracket tool-call mode via trajectory metadata.

Live Integration Tests (run-live)

Overview
- Exercise `file-discovery` against a real repository (Undici submodule) using Docker. Requires a valid OpenAI-compatible API key for live calls. The runner performs loose, structure-focused assertions on the final block and trajectory. The container’s entrypoint defaults to `run-live.sh`.

Run
- Local quick run (no Docker):
  ```bash
  ./scripts/install.sh --install-peripherals
  cd agent/internal/file-discovery/tests/undici
  export OPENAI_API_KEY=...
  export OPENAI_BASE_URL=...
  export OPENAI_MODEL=...
  bash ../run-live.sh
  ```
  - The install script drops freshly built binaries (including `file-discovery`) into `~/.local/bin`; ensure that directory is on `PATH` before invoking the runner.
  - Adjust the exported `OPENAI_BASE_URL`/`OPENAI_MODEL` values as needed for your endpoint; unset them only if you want the binary to fall back to its defaults.
- Provide your API key (and optionally base URL/model) and mount the host-built Linux/amd64 binary:
  `docker run --rm \
    -e OPENAI_API_KEY -e OPENAI_BASE_URL -e OPENAI_MODEL \
    -v "$PWD/file-discovery:/usr/local/bin/file-discovery:ro" \
    file-discovery-tests`
- Exit codes:
  - `0`: all scenarios passed
  - `2`: `OPENAI_API_KEY` missing
  - non-zero: scenario failure; check stderr artifacts

Artifacts
- For each scenario, the runner writes `stdout-issue-*.txt`, `stderr-issue-*.txt`, and `trajectory-issue-*.jsonl` in the working directory inside the container (`/workspace/undici`).
- To persist artifacts on the host, mount a directory and set `ARTIFACT_DIR`:
  `docker run --rm \
    -e OPENAI_API_KEY -e OPENAI_BASE_URL -e OPENAI_MODEL \
    -e ARTIFACT_DIR=artifacts \
    -v "$(pwd)/artifacts:/workspace/undici/artifacts" \
    -v "$PWD/file-discovery:/usr/local/bin/file-discovery:ro" \
    file-discovery-tests`
- Do not mount over `/workspace/undici` root, as it hides the vendored git repo.

Assertions (high level)
- Stdout contains a valid single final block with relative, deduplicated paths.
- At least one selected path exists in `rg --files --hidden` (allows a small number of misses).
- Trajectory JSONL includes `rg_exec`, `RG_OUT`, `sed_exec`, `sed_out_emitted`, `ls_exec`, `ls_out_emitted`, and `run_end` events.
- Each scenario also checks a few loose, issue-specific invariants.

Notes
- The `tests/run-live.sh` script runs three pinned Undici scenarios and is the image entrypoint.
- You can still use the same image for flag-focused tests by overriding the entrypoint to `run-flags.sh` (see above).
- If you see exit code 127, the mounted binary is likely built for the wrong OS/arch. Rebuild with `GOOS=linux GOARCH=amd64`.
