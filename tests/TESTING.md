# Agent + README Manager Integration Harness

This directory hosts the `run-agent-undici.sh` script, which exercises `mct-agent` against the undici fixture repo to validate the internal README manager in a near-real workflow. It reproduces the state assertions from `mct/tests/run-readme-integration.sh` but swaps stubbed CLI calls for end-to-end agent runs.

## What the Script Does
- Builds local `mct`, `mct-agent`, `patcher`, and `file-discovery` binaries into an isolated temp bin directory.
- Clones `tests/repositories/undici` into a scratch workspace and runs five scenarios (initial generation, significant change, repeat, docs-only, latest).
- Captures stdout, stderr, transcripts, final answers, and file-discovery outputs under `tests/artifacts/agent-undici/<case>/` for post-run triage.
- Defaults to offline stubs (`MCT_LLM_TEST_STUB=stub-echo`, `MCT_README_TEST_STUB=mock`) so it runs without network access; only the first agent turn executes (`MAX_STEPS=1`) to focus on README regeneration.

## Prerequisites
- Go toolchain installed (the script invokes `go build`).
- The undici fixture repo present at `tests/repositories/undici` (cloned automatically when you run the script).
- No prior temp runs left behind (the harness manages its own temp directories, but you can remove `tests/tmp/` if needed).

## Running the Harness
```bash
# From repo root
MACHTIANI_CONFIG=$HOME/.machtiani/config.toml \
OPENAI_API_KEY=... \
DISABLE_MCT_STUBS=true \
MODEL_ALIAS=qwen3-coder-plus \
KEEP_AGENT_TMP=true \
./tests/run-agent-undici.sh
```

The script emits high-level progress logs and exits non-zero if any scenario fails an assertion (e.g., missing tag, unexpected README commit count).

## Useful Environment Overrides
- `MAX_STEPS` — increase beyond `1` to allow planner-driven follow-up turns.
- `TIMEOUT_PER_TURN` — adjust per-turn timeout (seconds) if your machine is slow.
- `DISABLE_MCT_STUBS=true` — force real LLM usage (requires `OPENAI_*` config to resolve).
- `MODEL_ALIAS` — pass a `.machtiani/config.toml` model alias when running without stubs.

Artifacts are left in `tests/artifacts/agent-undici/` for inspection. Set `KEEP_AGENT_TMP=true` to keep the full temp workspace (including the transient `.machtiani` state) after the script finishes.
