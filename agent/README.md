# mct-agent — Agent Orchestrator

Agent “composer” that iteratively asks focused questions using the embedded `mct` discovery pipeline and decides when to stop to produce the final answer. It now calls the `mct` packages directly (no external `mct` binary required), parses the retrieved paths and answers in-memory, maintains a running summary, and uses its own LLM for planning and final composition.

## Requirements
- Go 1.22+ (when building from source)
- OpenAI‑compatible model configuration (no implicit defaults):
  - `OPENAI_API_KEY` (required)
  - `OPENAI_BASE_URL` (required)
  - `OPENAI_MODEL` (required)
- Optional standalone CLIs (`mct`, `file-discovery`, `patcher`) are only needed for direct use; install them with `./scripts/install.sh --install-peripherals`.

## Install

From the repo root, run the installer to build **mct-agent** into `~/.local/bin`:

```
./scripts/install.sh
```

Need the standalone CLIs too? Append `--install-peripherals` to build **mct**, **file-discovery**, and **patcher** alongside `mct-agent`:

```
./scripts/install.sh --install-peripherals
```

Override the destination with `PREFIX` if you prefer a different path:

```
PREFIX="$PWD/.mct-bin" ./scripts/install.sh
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
patcher --version
```

## Quick Start
Set model configuration (shared across the agent and any optional `mct` CLI):
```
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1   # or your provider
export OPENAI_MODEL=gpt-4o-mini
```
Run the agent:
```
mct-agent run "Explain X and identify root cause" --verbose
```

## Usage
```
mct-agent run "<issue or question>" [flags]
```
Flags:
- `--max-steps int`: maximum turns before finalizing (default: 4)
- `--openai-api-key string`: API key for OpenAI-compatible endpoint
- `--openai-base-url string`: Base URL for OpenAI-compatible endpoint
- `--orch-model string`: Model alias for planner/finalizer turns (alias: `--model`)
- `--answer-model string`: Model alias for final answer generation (default: `--orch-model`)
- `--patcher-model string`: Reserved placeholder; patch instructions are generated via the orchestrator model
- `--file-discovery-model string`: Model alias for file discovery runs (default: orchestration model)
- `--openai-model string`: Direct upstream model name for orchestrator (deprecated; prefer aliases)
- `--timeout-per-turn int`: per-turn timeout in seconds (default: 120; set 0 for unlimited)
- `--version`: print build metadata for the agent and exit
- `--dry-run`: print intended discovery calls; no remote LLM requests executed
- `--verbose`: verbose agent logging (includes discovery and planner context)
- `--shell-agent`: run the standalone `shell-agent` binary first, then send its transcript (prefixed context) to the LLM for the final answer (requires `shell-agent` on PATH)
- `--final-file string`: path to write final answer-only artifact
- `--transcript-file string`: path to write transcript (default: `.machtiani/sessions/<session-id>/chat/agent-transcript.md`)
- `--file-discovery-trajectory string`: absolute/relative file path for the file-discovery trajectory JSONL
- `--file-discovery-output-dir string`: directory to place file-discovery artifacts (default: `.machtiani/sessions/<session-id>/artifacts`)

## How It Works
- The agent controls the loop: it plans either `Decision: ask` with one next question or `Decision: finalize`.
- On `ask`, it runs the `mct` prompt service via Go packages, retrieving the answer text and retrieved-path metadata without invoking external binaries. The service still writes `.machtiani/sessions/<session-id>/chat/machtiani-response.md` for compatibility, and the agent records the paths plus answer payload directly from memory.
- When `--shell-agent` is enabled, the agent first invokes the external `shell-agent` binary, tags the combined prompt with the transcript (`Here is possibly relevant information from the shell agent.`), and then asks the configured LLM for the final response. The trajectory path emitted by `shell-agent` is surfaced in the agent telemetry for post-run inspection.
- It maintains a concise evolving summary/evidence log across turns.
- On finalize (or at `--max-steps`), the agent composes the final answer via its own LLM and prints it.
- A transcript is saved to `.machtiani/sessions/<session-id>/chat/agent-transcript.md` with per-turn entries and the final conclusion.

## Environment Details
- Component model selection precedence:
  - Flags `--orch-model`, `--answer-model`, `--patcher-model`, `--file-discovery-model`
  - Environment variables `MCT_ORCH_MODEL`, `MCT_ANSWER_MODEL`, `MCT_PATCHER_MODEL`, `MCT_FILE_DISCOVERY_MODEL` (planner also honors `MCT_MODEL` as a legacy alias)
  - Shared `.machtiani/config.toml` defaults or legacy `--agent-model`
- `OPENAI_*` resolution controls direct upstream credentials when skipping aliases:
  - Flags `--openai-*` override
  - Then `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`
  - Legacy envs `AGENT_MODEL_*` accepted as fallback with a deprecation warning
- The orchestrator and discovery paths resolve their effective model; the patcher flag is a placeholder today and falls back to the orchestrator configuration.
- `MACHTIANI_SESSION_ID` is generated per run and passed into the discovery service for correlation across artifacts.
- `--timeout-per-turn` applies to both the discovery steps and the planner/finalizer LLM calls. Set to `0` to disable the deadline for all per-turn operations.

## Troubleshooting
- “mct-agent not found”
  - Re-run `./scripts/install.sh` (append `--install-peripherals` if you also need the standalone CLIs) and ensure the chosen prefix is on PATH.
- “Missing model configuration”
  - Provide a valid `.machtiani/config.toml` (or set `MACHTIANI_CONFIG`) containing the model alias, or export `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` so the agent can generate one.
- “Saved chat missing/unreadable”
  - The embedded discovery service still writes `.machtiani/sessions/<session-id>/chat/machtiani-response.md`. Ensure the workspace is writable and no other process removed the file mid-run.
- “Discovery timed out”
  - Increase `--timeout-per-turn` (e.g., `--timeout-per-turn=600`) or set `--timeout-per-turn=0` to disable the deadline for discovery and planner steps.

## Notes
- The agent links against the `mct` and `patcher` Go packages directly; no external binaries are required for default operation.
- Discovery responses are consumed in-memory while the library still persists `.machtiani/sessions/<session-id>/chat/machtiani-response.md` for compatibility.
- `--dry-run` simulates planning and discovery without making outbound LLM requests.
- Patch planning is opt-in. Pass `--patch` to enable planner patch requests; without it the agent skips patch instructions entirely.
- Use `--patch-no-apply` to capture patch diagnostics and transcript turns without touching the working tree.
- `--patcher-model` is currently informational only; the planner (orchestrator model) generates patch instructions and the runner ignores this alias.
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
1. Run `./scripts/install.sh` (add `--install-peripherals` if you also want the standalone CLIs on PATH).
2. Optional for live mode: export `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL`. When these variables are absent the script forces deterministic dry-run mode.

What the script does:
- Performs a preflight that resolves `mct-agent` on PATH, prints `--version`/`go version -m` metadata, and fails if the commit/time does not match the current sources.
- Generates a temporary `.machtiani/config.toml` under `agent/tests/tmp/` and exports `MACHTIANI_CONFIG` for the duration of the run. The file uses your `OPENAI_*` values in live mode and stub credentials in dry-run.
- Runs Issue A/B/C happy-path scenarios (1-turn and 3-turn variants) plus deterministic error cases (empty input, missing config when in live mode). Artifacts land under `test-out-*` directories in the repo root.

Run from the repo root:

```
./scripts/install.sh && bash agent/tests/run-live.sh
```
If you prefer not to invoke the installer, run the manual block from the root `README.md` and ensure `mct-agent` is on PATH before executing `bash agent/tests/run-live.sh`.

The script no longer mutates PATH or accepts binary override flags; everything must resolve via PATH.
- When `OPENAI_*` are not provided, the generated config points at stub credentials and the script forces `--dry-run`, so no network or `mct` subprocess calls occur; transcripts remain available for assertions while the final artifact is intentionally skipped.
- Edge cases: Empty inputs, missing config/deps, timeouts (flaky; manual check advised).
- Custom: Run in a test repo branch for git/file interactions.

For CI: Include explicit `go build` and script invocation; provide OpenAI secrets.
Note: Tests may vary by LLM (non-deterministic multi-turn); refine prompts if needed.
