Agent Integration Tests (mct-agent)

- Entry script: `agent/tests/run-live.sh`
- Purpose: Exercise `mct-agent` end-to-end against a locally built binary. Supports live LLM calls or dry-run (no network/subprocess side effects).
- Auto-handles dependencies: builds `mct-agent` and, if missing, builds `mct` + `file-discovery` into `agent/tests/bin` using the standard `mct/build.sh` flow.
 - Optional override: set `MCT_BIN=/path/to/mct` to use a specific binary (ignored when `FORCE_REBUILD=true`).

Scenarios
- Issue A/B/C run twice each:
  - 1-turn fast path: `--max-steps=1` (timeout per turn 30s)
  - 3-turn path: `--max-steps=3` (timeout per turn 60s), including a `patch` decision

Modes
- Live: requires `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`; uses real LLM and `mct`.
- Dry-run: default when `OPENAI_*` not set; no network calls, no `mct` execution; produces deterministic transcript/final outputs for assertions.

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
