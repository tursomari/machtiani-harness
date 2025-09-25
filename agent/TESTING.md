Agent Integration Tests (mct-agent)

- Entry script: `agent/tests/run-live.sh`
- Purpose: Exercise `mct-agent` end-to-end against a locally built binary. Supports live LLM calls or dry-run (no network/subprocess side effects).
- Auto-handles dependencies: builds `mct-agent` and, if missing, builds `mct` + `file-discovery` into `agent/tests/bin` using the standard `mct/build.sh` flow.
 - Optional override: set `MCT_BIN=/path/to/mct` to use a specific binary (ignored when `FORCE_REBUILD=true`).
- Config management: writes a temporary `.machtiani/config.toml` under `agent/tests/bin`, exports `MACHTIANI_CONFIG`, and invokes the agent with `--model <alias>` that matches the generated config (your `OPENAI_MODEL` in live mode, a stub alias in dry-run). Live mode mirrors your `OPENAI_*` values into that file; dry-run mode uses stub credentials.

Scenarios
- Issue A/B/C run twice each:
  - 1-turn fast path: `--max-steps=1` (timeout per turn 300s)
  - 3-turn path: `--max-steps=3` (timeout per turn 300s), often surfacing planner `ask`/`patch` decisions

Modes
- Live: set `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL` so the generated config points at a real provider and the script runs without `--dry-run`.
- Dry-run: default when `OPENAI_*` not set; the generated config uses stub credentials, `--dry-run` is added, and the run avoids network and `mct` subprocess calls while still producing deterministic transcripts (final artifacts are skipped by the agent in this mode).

Validations performed
- `Conclusion:` block present in stdout
- Transcript file exists and contains `## Conclusion` and expected turn headings
- For 3-turn: planner decisions logged (may vary by model)
- Final answer artifact exists and is non-empty

Run locally
```
# From repo root; the script builds required binaries automatically
bash agent/tests/run-live.sh
```
Artifacts directory: created as `test-out-*` under the current working directory.

CI guidance
```
# Optional explicit build; otherwise the script will build locally
FORCE_REBUILD=true bash agent/tests/run-live.sh
```
