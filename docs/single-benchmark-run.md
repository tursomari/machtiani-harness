# Running a Single Deep-SWE Benchmark Task with Meta-Orchestrator

This guide covers running a single treatment benchmark task with the meta-orchestrator enabled. Use this for rapid iteration when testing changes to the meta-orchestrator classifier, the mct-agent binary, or the pier adapter.

## Prerequisites

- Both binaries must be pre-built: the treatment mct-agent and the meta-orchestrator. The A/B script at scripts/ab-deep-swe.sh builds these from HEAD but you can point to any pre-built binaries.
- An API key with access to the target model provider (DeepSeek by default).
- The Deep-SWE tasks dataset checked out at a known path (typically /home/david/projects/deep-swe/tasks).
- The pier Python package installed and the mct_pier_adapter on the Python path.
- Docker running.

## Key Environment Variables for the Host Shell

These must be exported in the host shell before invoking pier run. The setup method of the adapter reads them via os.environ.get to determine which binaries to upload into the container.

export MCT_AGENT_BINARY=/path/to/treatment/mct-agent
export MCT_META_ORCHESTRATOR_BINARY=/path/to/treatment/meta-orchestrator
export TEST_MODEL=deepseek-v4-pro
export TEST_API_KEY=sk-xxxxxxxx
export TEST_BASE_URL=https://api.deepseek.com

If MCT_META_ORCHESTRATOR_BINARY is not set or the binary path is invalid, the adapter will skip the upload and print a warning: "Warning: meta-orchestrator binary not found at ...; skipping upload." The run method then checks inside the container whether /usr/local/bin/meta-orchestrator exists; if it was not uploaded, it falls back to direct mct-agent run without the multi-stage validation loop.

## Invoking pier run for a Single Task

All environment variables for the adapter and agent must be passed both as host-shell exports (for the setup phase) and as --ae flags (for the container runtime).

cd /path/to/mct-bench
source .env  # or export TEST_API_KEY directly

export MCT_AGENT_BINARY=/tmp/mct-ab-deepswe/treatment/mct-agent
export MCT_META_ORCHESTRATOR_BINARY=/tmp/mct-ab-deepswe/treatment/meta-orchestrator
export TEST_MODEL=deepseek-v4-pro

pier run \
    --ae "MCT_AGENT_BINARY=/tmp/mct-ab-deepswe/treatment/mct-agent" \
    --ae "MCT_META_ORCHESTRATOR_BINARY=/tmp/mct-ab-deepswe/treatment/meta-orchestrator" \
    --ae "TEST_API_KEY=${TEST_API_KEY}" \
    --ae "TEST_BASE_URL=https://api.deepseek.com" \
    --ae "TEST_MODEL=deepseek-v4-pro" \
    --agent-import-path "mct_pier_adapter.mct_agent:MctAgent" \
    --job-name "mct-ab-treatment-single" \
    --include-task-name abs-module-cache-flags \
    --n-concurrent 1 \
    -p /home/david/projects/deep-swe/tasks

Key flags:
- --agent-import-path: Required. Tells pier where to find the MctAgent adapter class. Must be "mct_pier_adapter.mct_agent:MctAgent".
- --include-task-name: Filters to a single task by its directory name under the tasks path.
- --n-concurrent 1: Single-task run, no parallelism.
- --job-name: Distinct name to avoid clobbering other runs.
- --ae: Passes environment variables through to the container and to the agent runtime. Must mirror the host exports for MCT_AGENT_BINARY and MCT_META_ORCHESTRATOR_BINARY.

## Verifying the Meta-Orchestrator is Running

After pier starts, wait for the container to come up and the mct-agent sync phase to finish (typically 1 to 3 minutes). Then check the process list inside the container:

CT=$(docker ps -q --filter "name=abs-module-cache-flags.*main")
docker exec "$CT" ps aux | grep meta-orchestrator

If the meta-orchestrator is active, you will see a process line containing meta-orchestrator --mode code-forge. If you only see mct-agent run, the fallback path was used.

You can also verify the binary was uploaded:

docker exec "$CT" ls -la /usr/local/bin/meta-orchestrator

## Checking Results

After the run completes, the reward file is at:

jobs/<job-name>/<trial-dir>/verifier/reward.json

Example:

cat jobs/mct-ab-treatment-single/abs-module-cache-flags__*/verifier/reward.json

Fields: reward (0 or 1 for binary pass/fail), f2p_total, f2p_passed, p2p_total, p2p_passed, f2p (0.0 to 1.0), p2p (0.0 to 1.0), partial (weighted score).

The meta-orchestrator trajectory is copied to the agent output directory:

jobs/<job-name>/<trial-dir>/agent/meta-orchestrator/

It contains a JSONL file with invocation records, system prompts, and LLM messages showing the multi-stage validation phases.

## Common Issues

1. Meta-orchestrator not invoked despite env var being set: Ensure MCT_META_ORCHESTRATOR_BINARY is exported in the host shell before running pier run. The setup method reads os.environ directly; --ae flags alone are not enough.

2. TASK_API_KEY not found: Source the .env file or export TEST_API_KEY before the pier run command.

3. CancelledError / timeout: Single tasks with the meta-orchestrator take longer than direct mct-agent runs because of the multi-stage validation phases. The default pier timeout is usually sufficient but long runs may need --agent-timeout-multiplier.

4. Stale job directories: Delete jobs/<job-name> before re-running with the same job name to avoid stale result files confusing the output.

## Using the Convenience Script

The quickest way to run a single treatment benchmark is via the `run-single-treatment.sh` script. It handles building both binaries fresh from HEAD, setting all required environment variables, and invoking pier with the correct flags.

```bash
# Source API credentials
source .env  # or: export TEST_API_KEY=sk-... TEST_BASE_URL=https://api.deepseek.com

# Run with defaults (deepseek-v4-pro for both agent and classifier)
./scripts/run-single-treatment.sh abs-module-cache-flags

# Run with specific models (e.g., glm-5-high for agent/orchestrator, deepseek-v4-pro for shell-agent)
./scripts/run-single-treatment.sh abs-module-cache-flags glm-5-high deepseek-v4-pro
```

The script:
- Builds `mct-agent` and `meta-orchestrator` **fresh from HEAD** on every invocation.
- Cleans up old output under `/tmp/mct-single-treatment/` before each run.
- Passes `--model` to both `mct-agent` and `meta-orchestrator` (the classifier LLM).
- Passes `--shell-agent-model` to `mct-agent` for its subprocess model.
- Runs with `--n-concurrent 1` and `--include-task-name` for single-task isolation.
- Sets `MCT_AGENT_BINARY` and `MCT_META_ORCHESTRATOR_BINARY` both as host exports and `--ae` flags.

## Model Configuration Flow

| Component | Controlled by | Default |
|---|---|---|
| mct-agent planner LLM | `--model` flag | `deepseek-v4-pro` |
| meta-orchestrator classifier LLM | `--model` flag (passed through) | `deepseek-v4-pro` |
| shell-agent tool-calling LLM | `--shell-agent-model` flag | `deepseek-v4-pro` |
| mct-agent sync LLM | `MCT_SYNC_MODEL` env var | `deepseek-v4-pro` |

All model flags are propagated through the **meta-orchestrator → mct-agent** chain: the meta-orchestrator receives `--model` and passes it through to every `mct-agent run` invocation (both the initial one and CONTINUE re-invocations with `--session-id`).
