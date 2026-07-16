# mct-agent — Agent Orchestrator

Initialize the current project with `mct-agent init`. Where paths below use
`$PROJECT_STORE`, resolve it with
`PROJECT_STORE="$HOME/.machtiani/$(cat .machtiani/project.uuid)"`.

## Requirements
- Nix 2.24+ for installation and updates; Go is supplied by the development shell
- OpenAI‑compatible model configuration (no implicit defaults):
  - `OPENAI_API_KEY` (required)
  - `OPENAI_BASE_URL` (required)
  - `OPENAI_MODEL` (required)
- Optional standalone CLIs (`mct`, `file-discovery`, `shell-agent`) are developer peripherals and are not part of the default package.

## Install

From a clean repo root, install **mct-agent** into the dedicated managed profile:

```
nix run .#install
```

Override the stable binary prefix if you prefer a different path:

```
nix run .#install -- --prefix "$PWD/.mct-bin"
export PATH="$PWD/.mct-bin/bin:$PATH"
```

After installation, confirm `mct-agent` resolves via PATH:

```
mct-agent --version
```

If you built the peripherals, check them too:

```
mct --help | head -n 1
file-discovery -version
```

## Quick Start
Set model configuration (shared across the agent and any optional `mct` CLI):
```
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1   # or your provider
export OPENAI_MODEL=gpt-4o-mini
```

### Debugging: log full LLM inputs

Full LLM request logging is disabled by default. To append the full redacted
JSON request payload (including prompts, messages, and source context) for one
run to the canonical session log, pass:

```
mct-agent run --log-llm-inputs "Explain X and identify root cause"
```

WARNING: this may write sensitive prompts and source material to disk, and the
file can grow quickly. The log is diagnostic only and is not used for resume.

For automation, an explicit environment path also enables logging and takes
precedence over the flag's canonical destination:

```
export MCT_LLM_INPUT_LOG=.machtiani/llm-input.log
```

Optional: set `MCT_LLM_STAGE` to tag log entries when a stage isn't explicitly
set by the caller.

There is deliberately no persistent configuration switch that silently enables
full input logging for future runs.

## Usage
```
mct-agent run "<issue or question>" [flags]
```
Flags:
- `--max-steps int`: maximum turns before finalizing (default: 4)
- `--api-key provider:key`: provider-specific API key override for this invocation (repeatable; takes precedence over config/env)
- `--openai-api-key string`: API key for OpenAI-compatible endpoint
- `--openai-base-url string`: Base URL for OpenAI-compatible endpoint
- `--orch-model string`: Model alias for planner/finalizer turns (alias: `--model`)
- `--answer-model string`: Model alias for final answer generation (default: `--orch-model`)
- `--file-discovery-model string`: Model alias for file discovery runs (default: orchestration model)
- `--openai-model string`: Direct upstream model name for orchestrator (deprecated; prefer aliases)
- `--timeout-per-turn int`: per-turn timeout in seconds (default: 120; set 0 for unlimited)
- `--version`: print build metadata for the agent and exit
- `--dry-run`: print intended discovery calls; no remote LLM requests executed
- `--verbose`: verbose agent logging (includes discovery and planner context)
- `--shell-agent`: run the standalone `shell-agent` binary first, then send its transcript (prefixed context) to the LLM for the final answer (requires `shell-agent` on PATH)
- `--final-file string`: path to write final answer-only artifact
- `--transcript-file string`: path to write transcript (default: `$PROJECT_STORE/sessions/<session-id>/chat/agent-transcript.adoc`)
- `--file-discovery-trajectory string`: absolute/relative file path for the file-discovery trajectory JSONL
- `--file-discovery-output-dir string`: directory to place file-discovery artifacts (default: `$PROJECT_STORE/sessions/<session-id>/artifacts`)

Example: mix models from different providers by repeating `--api-key` for each provider referenced by your aliases:

```
mct-agent run "triage regression" \
  --orch-model gpt-5-nano \
  --file-discovery-model haiku \
  --api-key openai:sk-openai-xxx \
  --api-key openrouter:sk-openrouter-yyy
```

## How It Works
- The agent controls the loop: it plans either `Decision: ask` with one next question or `Decision: finalize`.
- On `ask`, it runs the `mct` prompt service via Go packages, retrieving the answer text and retrieved-path metadata without invoking external binaries. The service still writes `$PROJECT_STORE/sessions/<session-id>/chat/machtiani-response.md` for compatibility, and the agent records the paths plus answer payload directly from memory.
- When `--shell-agent` is enabled, the agent first invokes the external `shell-agent` binary, tags the combined prompt with the transcript (`Here is possibly relevant information from the shell agent.`), and then asks the configured LLM for the final response. The shell-agent trajectory JSON file is still saved for post-run inspection.
- It maintains a concise evolving summary/evidence log across turns.
- On finalize (or at `--max-steps`), the agent composes the final answer via its own LLM and prints it.
- A transcript is saved to `$PROJECT_STORE/sessions/<session-id>/chat/agent-transcript.adoc` with per-turn entries and the final conclusion.

## Environment Details
- Shell commands start in the directory where `mct-agent` is launched. The
  removed `environment.cwd` key is rejected with migration guidance.
- Component model selection precedence:
  - Flags `--orch-model`, `--answer-model`, `--file-discovery-model`
  - Environment variables `MCT_ORCH_MODEL`, `MCT_ANSWER_MODEL`, `MCT_FILE_DISCOVERY_MODEL` (planner also honors `MCT_MODEL` as a legacy alias)
  - Shared `.machtiani/config.toml` defaults or legacy `--agent-model`
- `OPENAI_*` resolution controls direct upstream credentials when skipping aliases:
  - Flags `--openai-*` override
  - Then `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`
  - Legacy envs `AGENT_MODEL_*` accepted as fallback with a deprecation warning
- The orchestrator and discovery paths resolve their effective model; the shell-agent model falls back to the orchestrator configuration when not specified.
- `MACHTIANI_SESSION_ID` is generated per run and passed into the discovery service for correlation across artifacts.
- `MACHTIANI_SHELL_AGENT_MARKER_MAX_AGE` overrides the startup cleanup threshold for shell-agent marker files (Go duration like `30m`, `2h`; default `1h`).
- `--timeout-per-turn` applies to both the discovery steps and the planner/finalizer LLM calls. Set to `0` to disable the deadline for all per-turn operations.

## Troubleshooting
- “mct-agent not found”
  - Re-run `nix run .#install` and ensure the chosen prefix is on PATH.
- “Missing model configuration”
  - Provide a valid `.machtiani/config.toml` (or set `MACHTIANI_CONFIG`) containing the model alias, or export `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` so the agent can generate one.
- “Saved chat missing/unreadable”
  - The embedded discovery service still writes `$PROJECT_STORE/sessions/<session-id>/chat/machtiani-response.md`. Ensure the workspace is writable and no other process removed the file mid-run.
- “Discovery timed out”
  - Increase `--timeout-per-turn` (e.g., `--timeout-per-turn=600`) or set `--timeout-per-turn=0` to disable the deadline for discovery and planner steps.

## Notes
- The agent links against the `mct` Go package directly; no external binaries are required for default operation.
- Discovery responses are consumed in-memory while the library still persists `$PROJECT_STORE/sessions/<session-id>/chat/machtiani-response.md` for compatibility.
- `--dry-run` simulates planning and discovery without making outbound LLM requests.
- Patch planning is opt-in. Pass `--patch` to enable planner patch requests; without it the agent skips patch instructions entirely.
- Use `--patch-no-apply` to capture patch diagnostics and transcript turns without touching the working tree.

- Shell-agent mode depends on a `shell-agent` binary on PATH (build the bundled version via `cd agent/internal/shell-agent && GOCACHE=$(pwd)/../../.gocache go build -o ~/.local/bin/shell-agent ./cmd/shell-agent`).

## Testing

### Unit Tests
See `TESTING.md` for detailed commands. Quick reference:
```
cd agent
GOCACHE=$(pwd)/.gocache go test ./...
```

### Integration Tests (Live or Dry-Run)
`agent/tests/run-live.sh` exercises the PATH-installed `mct-agent` binary end-to-end (see `TESTING.md` for full details). The default install is sufficient; optional CLIs are not required for this harness.

Prerequisites:
1. Run `nix build .#mct-agent` and put `result/bin` on PATH.
2. Optional for live mode: export `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL`. When these variables are absent the script forces deterministic dry-run mode.

What the script does:
- Performs a preflight that resolves `mct-agent` on PATH, prints `--version`/`go version -m` metadata, and fails if the commit/time does not match the current sources.
- Generates a temporary `.machtiani/config.toml` under `agent/tests/tmp/` and exports `MACHTIANI_CONFIG` for the duration of the run. The file uses your `OPENAI_*` values in live mode and stub credentials in dry-run.
- Runs Issue A/B/C happy-path scenarios (1-turn and 3-turn variants) plus deterministic error cases (empty input, missing config when in live mode). Artifacts land under `test-out-*` directories in the repo root.
- Seeds shell-agent marker files under a per-run temp root and validates that stale markers are removed at startup while recent markers remain; override the threshold via `MACHTIANI_SHELL_AGENT_MARKER_MAX_AGE` or disable checks with `CHECK_SHELL_AGENT_MARKERS=false`.
  - Cleanup is explicit (no `RETURN` trap). If you later parallelize cases, run each case in a subshell and add a per-subshell `trap cleanup EXIT` to keep marker cleanup isolated.

Run from the repo root:

```
nix build .#mct-agent
PATH="$PWD/result/bin:$PATH" bash agent/tests/run-live.sh
```
Ensure the flake-built `mct-agent` is on PATH before executing `bash agent/tests/run-live.sh`.

The script no longer mutates PATH or accepts binary override flags; everything must resolve via PATH.
- When `OPENAI_*` are not provided, the generated config points at stub credentials and the script forces `--dry-run`, so no network or `mct` subprocess calls occur; transcripts remain available for assertions while the final artifact is intentionally skipped.
- Edge cases: Empty inputs, missing config/deps, timeouts (flaky; manual check advised).
- Custom: Run in a test repo branch for git/file interactions.

For CI: Include explicit `go build` and script invocation; provide OpenAI secrets.
Note: Tests may vary by LLM (non-deterministic multi-turn); refine prompts if needed.
