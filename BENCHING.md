# Deep-SWE Benching

This is the operator entrypoint for running and monitoring Deep-SWE benches from this repo.

## Prerequisites

- `TEST_API_KEY` and `TEST_BASE_URL` exported in the host shell.
- A Deep-SWE checkout. Set `DEEP_SWE_TASKS=/path/to/deep-swe/tasks` or pass
  `--tasks-path`; the launchers derive repository metadata from that checkout.
- Nix 2.24 or newer with flakes enabled. The launchers enter the locked
  `bench` shell automatically; a separately installed Go toolchain is ignored.
- Docker running, `pier` installed, and `mct_pier_adapter` importable.
- Run commands from the repo root.

## Run the 12-Task Treatment

Use `scripts/run-batch-subset.sh` for the standard 12-task batch. It builds
standalone treatment binaries from the current `HEAD` through the pinned Nix
toolchain, downloads the musl Forge binary, launches Pier, and persists results.

Treatment-only run with six workers:

```bash
RUN_ID="$(date -u +%Y-%m-%dT%H-%M-%SZ)"
WORK_DIR=".data/treatment12-${RUN_ID}"

export TEST_API_KEY=sk-...
export TEST_BASE_URL=https://api.deepseek.com
export DEEP_SWE_TASKS=/path/to/deep-swe/tasks

./scripts/run-batch-subset.sh \
  --treatment-only \
  --concurrent 6 \
  --work-dir "${WORK_DIR}" \
  --model deepseek-v4-pro
```

Full A/B run with control and treatment:

```bash
./scripts/run-batch-subset.sh \
  --concurrent 6 \
  --work-dir ".data/ab12-$(date -u +%Y-%m-%dT%H-%M-%SZ)" \
  --model deepseek-v4-pro \
  --litellm-model deepseek/deepseek-v4-pro
```

The script prints the treatment job name, usually `treatment-batch-<pid>`, and the treatment Pier log path. Keep both values for monitoring.

## Run a Single Retest

Use `scripts/run-single-treatment.sh` for one-off treatment retests. It rebuilds
standalone `mct-agent` and `meta-orchestrator` binaries from `HEAD` through the
pinned Nix toolchain, uses a fixed `/tmp/mct-single-treatment` workspace, and
persists results under `.bench/deep-swe/`.

```bash
export TEST_API_KEY=sk-...
export TEST_BASE_URL=https://api.deepseek.com

./scripts/run-single-treatment.sh abs-module-cache-flags deepseek-v4-pro deepseek-v4-pro
```

Use `--agent-name` and `--treatment-name` to control the persisted result path:

```bash
./scripts/run-single-treatment.sh \
  --agent-name mct-orchestrator \
  --treatment-name recovery-rerun-abs \
  abs-module-cache-flags
```

The single-task runner:

- builds static, container-portable `mct-agent` and `meta-orchestrator` from `HEAD`
- downloads the musl Forge binary
- sets `MCT_AGENT_BINARY`, `MCT_META_ORCHESTRATOR_BINARY`, and `MCT_FORGE_BINARY`
- runs Pier with `--agent-import-path mct_pier_adapter.mct_agent:MctAgent`
- runs with `--n-concurrent 1`
- preserves live job data under `/tmp/treatment-preserved-<epoch>/`
- persists durable results under `.bench/deep-swe/<agent-label>/<treatment-label>/<timestamp>/<task>/`

If you invoke Pier manually for a single task, the adapter needs binary paths exported in the host shell and passed as `--ae` runtime environment values:

```bash
export MCT_AGENT_BINARY=/path/to/mct-agent
export MCT_META_ORCHESTRATOR_BINARY=/path/to/meta-orchestrator
export MCT_FORGE_BINARY=/path/to/forge
export TEST_API_KEY=sk-...
export TEST_BASE_URL=https://api.deepseek.com
export TEST_MODEL=deepseek-v4-pro

pier run \
  --agent-import-path mct_pier_adapter.mct_agent:MctAgent \
  --ae "MCT_AGENT_BINARY=${MCT_AGENT_BINARY}" \
  --ae "MCT_META_ORCHESTRATOR_BINARY=${MCT_META_ORCHESTRATOR_BINARY}" \
  --ae "MCT_FORGE_BINARY=${MCT_FORGE_BINARY}" \
  --ae "TEST_API_KEY=${TEST_API_KEY}" \
  --ae "TEST_BASE_URL=${TEST_BASE_URL}" \
  --ae "TEST_MODEL=${TEST_MODEL}" \
  --jobs-dir /tmp/mct-single-treatment/jobs \
  --job-name mct-single-abs-module-cache-flags \
  --include-task-name abs-module-cache-flags \
  --n-concurrent 1 \
  --agent-timeout-multiplier 3.0 \
  -p "$DEEP_SWE_TASKS"
```

If `MCT_META_ORCHESTRATOR_BINARY` is missing or invalid in the host shell, the adapter skips the upload and the run can fall back to direct `mct-agent` instead of the multi-phase meta-orchestrator loop.

## Monitor a Batch

Use `scripts/monitor-treatment.py` against the work directory, treatment jobs directory, and job name printed by `run-batch-subset.sh`:

```bash
WORK_DIR=".data/treatment12-2026-07-10T00-00-00Z"
JOBS_DIR="${WORK_DIR}/treatment/jobs"
JOB_NAME="treatment-batch-12345"

python scripts/monitor-treatment.py \
  --work-dir "${WORK_DIR}" \
  --jobs-dir "${JOBS_DIR}" \
  --job-name "${JOB_NAME}" \
  --once
```

Continuous polling:

```bash
python scripts/monitor-treatment.py \
  --work-dir "${WORK_DIR}" \
  --jobs-dir "${JOBS_DIR}" \
  --job-name "${JOB_NAME}" \
  --interval 60
```

Strict mode exits nonzero when the monitor detects runner, sync, invocation, or shell-agent errors:

```bash
python scripts/monitor-treatment.py \
  --work-dir "${WORK_DIR}" \
  --jobs-dir "${JOBS_DIR}" \
  --job-name "${JOB_NAME}" \
  --strict \
  --once
```

The monitor reports:

- reward files and `f2p` / `p2p` / `partial` scores
- meta-orchestrator trajectory phase markers
- `mct-agent sync` attempts, retries, failures, and recoveries
- runner errors, including GLIBC / Forge binary compatibility failures
- shell-agent trajectory activity and `mct-forge` command errors
- live Docker container status when containers are still running

If the monitor prints `pier_running=False` while containers are still up, treat the batch as orphaned until proven otherwise. Inspect the Pier log and direct container process tables; new rewards may not be collected if the Pier supervisor has exited.

## Data Locations

Batch working data:

```text
<work-dir>/
  bin/                         # built mct-agent, meta-orchestrator, forge
  control/pier.log             # control Pier stdout/stderr, when control is run
  control/jobs/<job-name>/      # control Pier job state
  treatment/pier.log           # treatment Pier stdout/stderr
  treatment/jobs/<job-name>/    # treatment Pier job state
```

Per-task treatment trial data:

```text
<work-dir>/treatment/jobs/<job-name>/<task>__<suffix>/
  agent/mct-run.log
  agent/mct-sync.log
  agent/meta-orchestrator/
  agent/sessions/
  verifier/reward.json
  verifier/ctrf.json
  verifier/run.log
  result.json
  trial.log
```

Persisted benchmark results:

```text
.bench/deep-swe/<agent-label>/<treatment-label>/<timestamp>/<task>/
  reward.json
  instruction.md
  agent/
  metadata.json
```

Single-task treatment runs also use:

```text
/tmp/mct-single-treatment/jobs/
/tmp/treatment-preserved-<epoch>/
```

Reward files use `reward`, `f2p_total`, `f2p_passed`, `p2p_total`, `p2p_passed`, `f2p`, `p2p`, and `partial`. Use `partial` for weighted comparisons and `f2p` for the hidden-test pass fraction.

## Live Container Checks

There are often multiple Docker containers running concurrently. Container names use the task name, a random suffix, and `-main-1`, for example:

```text
abs-module-cache-flags__bjd773o-main-1
```

List matching containers:

```bash
docker ps --format '{{.Names}}\t{{.Status}}' | rg 'abs-module-cache-flags|tomlkit|vitest'
```

Check whether the meta-orchestrator is active:

```bash
CONTAINER=abs-module-cache-flags__bjd773o-main-1
docker exec "$CONTAINER" sh -lc \
  'ps -eo pid,ppid,etime,stat,cmd | grep -E "meta-orchestrator|mct-agent run|mct-forge|forge$|pytest|go test|pnpm|npm|verifier|git clean" | grep -v grep'
```

Check the worktree and recent commits:

```bash
docker exec "$CONTAINER" sh -lc \
  'git -C /app status --short && git -C /app log --oneline -8'
```

Find the active session ID:

```bash
docker exec "$CONTAINER" sh -lc \
  'ps aux | grep "mct-agent run" | grep -o -- "--session-id [^ ]*" || true; ls -t /app/.machtiani/sessions 2>/dev/null | head'
```

The meta-orchestrator may have its own session and the child `mct-agent` may have another. Do not assume the newest session is the one doing useful work; confirm from the process command line when possible.

## Heartbeat Signals

Use three signals together:

| Check | What it tells you | Caveat |
|---|---|---|
| process table | process is alive | does not prove progress |
| `conversation.json` mtime | an agent turn completed recently | only updates after a full assistant turn |
| child shell-agent trajectory | shell commands are executing | best real-time heartbeat |

The parent `trajectory/agent.jsonl` often records events only when `mct-agent` produces a final answer for the meta-orchestrator. Silence there does not mean the agent is dead. Check the child shell-agent trajectory first.

Inside the container:

```bash
SESSION=agent-...
docker exec "$CONTAINER" sh -lc \
  "ls /app/.machtiani/sessions/${SESSION}/shell-agent"

docker exec "$CONTAINER" sh -lc \
  "tail -20 /app/.machtiani/sessions/${SESSION}/shell-agent/<child-id>/trajectory.json"

docker exec "$CONTAINER" sh -lc \
  "grep -c '\"type\":\"answer\"' /app/.machtiani/sessions/${SESSION}/shell-agent/<child-id>/trajectory.json"
```

Quick one-liner:

```bash
CONTAINER=my-container
SESSION="$(docker exec "$CONTAINER" sh -lc 'ls -t /app/.machtiani/sessions 2>/dev/null | head -1')"
docker exec "$CONTAINER" sh -lc "echo branch=\$(git -C /app branch --show-current); \
  echo child=\$(ls -t /app/.machtiani/sessions/${SESSION}/shell-agent 2>/dev/null | head -1); \
  stat -c 'conversation_mtime=%y' /app/.machtiani/sessions/${SESSION}/conversation.json 2>/dev/null || true; \
  ps -eo pid,etime,stat,cmd | grep -E 'meta-orchestrator|mct-agent run|mct-forge|forge$' | grep -v grep"
```

Phase model:

- Initial implementation: agent reads, plans, edits, tests, and commits.
- Test failure fix loop: agent runs tests, gets failures, and iterates.
- Waiting on meta-orchestrator: `mct-agent` produced a final answer and exited; child shell-agent trajectory goes quiet.
- Re-invoked on resume: meta-orchestrator currently launches `mct-agent run --session-id <session-id> -p <follow-up>`, often producing a new shell-agent child trajectory. This is deprecated internal compatibility plumbing; user-facing invocations should use `mct-agent run -p "<your follow-up prompt>" --resume <session-id>` (or `-r`).

Always read `/app/instruction.md` before judging whether a task is over-scoped. Some benchmark tasks require large features even when the symptom sounds small.

## Quick Score Summary

List the best partial score per task across redundant MCT reward files and compare against control:

```bash
python - <<'PY'
import json
from pathlib import Path

mct_paths = []
mct_paths += sorted(Path('.bench/deep-swe/mct-orchestrator').glob('**/reward.json'))
mct_paths += sorted(Path('.bench/deep-swe/treatment-mct-orchestrator').glob('**/reward.json'))
for base in sorted(Path('/tmp').glob('treatment-preserved-*')) + [Path('/tmp/mct-single-treatment')]:
    if base.exists():
        mct_paths += sorted(base.glob('**/verifier/reward.json'))

control_paths = sorted(Path('.bench/deep-swe/mini-swe-agent/default').glob('**/*/reward.json'))

def task_for(path):
    parts = path.parts
    if 'verifier' in parts:
        return parts[-3].split('__', 1)[0]
    return path.parent.name

def partial(path):
    return float(json.loads(path.read_text()).get('partial', 0))

best = {}
for path in mct_paths:
    task = task_for(path)
    value = partial(path)
    if task not in best or value > best[task]:
        best[task] = value

control = {}
for path in control_paths:
    task = path.parent.name
    value = partial(path)
    if task not in control or value > control[task]:
        control[task] = value

print('task,best_mct_partial,control_partial,delta')
for task in sorted(best):
    c = control.get(task)
    delta = '' if c is None else f'{best[task] - c:.6f}'
    print(f'{task},{best[task]:.6f},{"-" if c is None else f"{c:.6f}"},{delta}')
PY
```

## Common Failure Modes to Watch

- `pier_running=False` with live containers: Pier likely exited or was killed while task containers kept running. The batch may be orphaned and not collect rewards.
- GLIBC / Forge binary errors: look for `GLIBC_`, `forge: .*GLIBC`, or `forge backend unavailable` in monitor output.
- Sync failures: `monitor-treatment.py` reports sync attempts, retries, failed syncs, and recovery markers.
- Dirty runtime state: `.machtiani/`, `instruction.md`, and session artifacts are runtime state and must not be deleted by reset phases.
- Stale reward files: use distinct `--work-dir`, job names, and treatment names when comparing runs.
- Parent vs child trajectory confusion: parent `agent.jsonl` is not always a real-time log; child shell-agent trajectory is the better heartbeat.
- Meta-session vs agent session confusion: the meta-orchestrator session can be done while the child agent session is still active.
- Conversation timestamp lag: `conversation.json` updates only on assistant turn boundaries, so it can look stale during long commands.
