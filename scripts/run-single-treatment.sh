#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# run-single-treatment.sh — Build and run a single treatment benchmark task
# with the meta-orchestrator enabled, then persist results under
# .bench/deep-swe/<agent>/<treatment>/<timestamp>/.
# ============================================================================

usage() {
    cat <<'EOF'
Usage: run-single-treatment.sh [OPTIONS] TASK_NAME [MODEL] [SHELL_AGENT_MODEL]

  TASK_NAME          Task directory name under the tasks path (required)
                     Example: abs-module-cache-flags

  MODEL              Model for agent and meta-orchestrator classifier
                     Default: deepseek-v4-pro

  SHELL_AGENT_MODEL  Model for shell-agent subprocesses
                     Default: deepseek-v4-pro

Options:
  --tasks-path PATH      Path to Deep-SWE task directory
                         (required unless DEEP_SWE_TASKS is set)
  --agent-name NAME       Agent label for the results tree
                          Default: mct-orchestrator
  --treatment-name NAME   Treatment label for the results tree
                          Default: with-peer-review
  -h, --help              Show this help and exit

Prerequisites:
  - TEST_API_KEY, TEST_BASE_URL exported in the environment
  - Deep-SWE tasks supplied with --tasks-path or DEEP_SWE_TASKS
  - Go toolchain for building binaries
  - Docker running, pier Python package installed

Output:
  - Binaries under /tmp/mct-single-treatment/
  - Pier job output under /tmp/mct-single-treatment/jobs/
  - Reward.json at jobs/<job-name>/<trial-dir>/verifier/reward.json
  - Persisted results under <repo>/.bench/deep-swe/<agent>/<treatment>/<ts>/

Example:
  export TEST_API_KEY=sk-...
  export TEST_BASE_URL=https://api.deepseek.com
  export DEEP_SWE_TASKS=/path/to/deep-swe/tasks
  ./scripts/run-single-treatment.sh abs-module-cache-flags glm-5-high deepseek-v4-pro
  ./scripts/run-single-treatment.sh --agent-name mct-orchestrator \
      --treatment-name with-peer-review abs-module-cache-flags
EOF
    exit 0
}

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
AGENT_NAME="mct-orchestrator"
TREATMENT_NAME="with-peer-review"
TASKS="${DEEP_SWE_TASKS:-}"
TASK_NAME=""
MODEL="deepseek-v4-pro"
SHELL_AGENT_MODEL="deepseek-v4-pro"

POSITIONAL=()
while [[ $# -gt 0 ]]; do
    case "$1" in
        --tasks-path)
            TASKS="${2:?--tasks-path requires a value}"
            shift 2
            ;;
        --tasks-path=*)
            TASKS="${1#*=}"
            shift
            ;;
        --agent-name)
            AGENT_NAME="${2:?--agent-name requires a value}"
            shift 2
            ;;
        --agent-name=*)
            AGENT_NAME="${1#*=}"
            shift
            ;;
        --treatment-name)
            TREATMENT_NAME="${2:?--treatment-name requires a value}"
            shift 2
            ;;
        --treatment-name=*)
            TREATMENT_NAME="${1#*=}"
            shift
            ;;
        -h|--help)
            usage
            ;;
        --)
            shift
            while [[ $# -gt 0 ]]; do POSITIONAL+=("$1"); shift; done
            ;;
        *)
            POSITIONAL+=("$1")
            shift
            ;;
    esac
done

TASK_NAME="${POSITIONAL[0]:-}"
MODEL="${POSITIONAL[1]:-${MODEL}}"
SHELL_AGENT_MODEL="${POSITIONAL[2]:-${SHELL_AGENT_MODEL}}"

if [[ -z "${TASK_NAME}" ]]; then
    echo "Error: TASK_NAME is required." >&2
    usage
fi

if [[ -z "${TASKS}" ]]; then
    echo "Error: pass --tasks-path or set DEEP_SWE_TASKS." >&2
    exit 1
fi
if [[ ! -d "${TASKS}" ]]; then
    echo "Error: Deep-SWE task directory does not exist: ${TASKS}" >&2
    exit 1
fi
TASKS="$(cd "${TASKS}" && pwd)"
DEEP_SWE_REPO="$(git -C "${TASKS}" rev-parse --show-toplevel 2>/dev/null || :)"
if [[ -z "${DEEP_SWE_REPO}" ]]; then
    echo "Error: Deep-SWE task directory is not inside a Git checkout: ${TASKS}" >&2
    exit 1
fi

for var in TEST_API_KEY TEST_BASE_URL; do
    if [[ -z "${!var:-}" ]]; then
        echo "Error: ${var} must be set and non-empty." >&2
        exit 1
    fi
done

# ---------------------------------------------------------------------------
# Resolve paths
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
OUTPUT_BASE="/tmp/mct-single-treatment"
OUTPUT_BIN="${OUTPUT_BASE}/bin"
JOBS_DIR="${OUTPUT_BASE}/jobs"
SAFE_AGENT_MOUNTS_JSON='[{"type":"bind","source":"${HOST_AGENT_LOGS_PATH}","target":"${ENV_AGENT_LOGS_PATH}"},{"type":"bind","source":"${HOST_ARTIFACTS_PATH}","target":"${ENV_ARTIFACTS_PATH}"}]'

AGENT_BIN="${OUTPUT_BIN}/machtiani"
META_BIN="${OUTPUT_BIN}/meta-orchestrator"
FORGE_BIN="${OUTPUT_BIN}/forge"

echo "[setup] Preparing ${OUTPUT_BASE}"
rm -rf "${OUTPUT_BASE}"
mkdir -p "${OUTPUT_BIN}" "${JOBS_DIR}"

# ---------------------------------------------------------------------------
# Build both binaries fresh from HEAD
# ---------------------------------------------------------------------------
echo "[build] Building pinned benchmark binaries from HEAD"
"${REPO_ROOT}/scripts/build-bench-binaries.sh" "${REPO_ROOT}" "${OUTPUT_BIN}"
test -x "${AGENT_BIN}"
test -x "${META_BIN}"

echo "[build] Downloading Forge musl binary -> ${FORGE_BIN}"
"${REPO_ROOT}/scripts/download-forge-musl.sh" "${FORGE_BIN}"
echo "[build] forge done."


# ---------------------------------------------------------------------------
# Run the single treatment benchmark
# ---------------------------------------------------------------------------
echo ""
echo "==== Running single treatment for ${TASK_NAME} ===="
echo "  Agent:            ${AGENT_NAME}"
echo "  Treatment:        ${TREATMENT_NAME}"
echo "  Model:            ${MODEL}"
echo "  Shell-agent model: ${SHELL_AGENT_MODEL}"
echo "  Meta-orchestrator: ${META_BIN}"
echo "  Agent binary:      ${AGENT_BIN}"
echo ""

export MACHTIANI_BIN="${AGENT_BIN}"
export MACHTIANI_META_ORCHESTRATOR_BINARY="${META_BIN}"
export MACHTIANI_FORGE_BINARY="${FORGE_BIN}"
export MACHTIANI_MODEL="${MODEL}"
export MACHTIANI_SHELL_AGENT_MODEL="${SHELL_AGENT_MODEL}"

# Start background preservation loop to continuously save job output
mkdir -p "${JOBS_DIR}"
PRESERVE_BASE="/tmp/treatment-preserved-$(date +%s)"
mkdir -p "${PRESERVE_BASE}"
(while true; do rsync -a --ignore-existing "${JOBS_DIR}/" "${PRESERVE_BASE}/" 2>/dev/null; sleep 2; done) &
PRESERVE_PID=$!
echo "[preserve] Background preservation loop started (PID ${PRESERVE_PID}) -> ${PRESERVE_BASE}"

pier run \
    --ae "MACHTIANI_BIN=${AGENT_BIN}" \
    --ae "MACHTIANI_META_ORCHESTRATOR_BINARY=${META_BIN}" \
    --ae "MACHTIANI_FORGE_BINARY=${FORGE_BIN}" \
    --ae "MACHTIANI_MODEL=${MODEL}" \
    --ae "MACHTIANI_SHELL_AGENT_MODEL=${SHELL_AGENT_MODEL}" \
    --ae "TEST_API_KEY=${TEST_API_KEY}" \
    --ae "TEST_BASE_URL=${TEST_BASE_URL}" \
    --ae "TEST_MODEL=${MODEL}" \
    --jobs-dir "${JOBS_DIR}" \
    --agent-import-path "machtiani_pier_adapter.machtiani_agent:MachtianiAgent" \
    --job-name "mct-single-${TASK_NAME}" \
    --include-task-name "${TASK_NAME}" \
    --n-concurrent 1 \
    --agent-timeout-multiplier 3.0 \
    --mounts-json "${SAFE_AGENT_MOUNTS_JSON}" \
    -p "${TASKS}"

kill $PRESERVE_PID 2>/dev/null || true; wait $PRESERVE_PID 2>/dev/null || true

# ---------------------------------------------------------------------------
# Persist results under .bench/deep-swe/<agent>/<treatment>/<timestamp>/
# ---------------------------------------------------------------------------
BENCH_TIMESTAMP_FS="$(date -u +%Y-%m-%dT%H-%M-%SZ)"
BENCH_TIMESTAMP_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
BENCH_DIR="${REPO_ROOT}/.bench/deep-swe/${AGENT_NAME}/${TREATMENT_NAME}/${BENCH_TIMESTAMP_FS}"
mkdir -p "${REPO_ROOT}/.bench"
mkdir -p "${BENCH_DIR}/${TASK_NAME}"
echo "[persist] BENCH_DIR=${BENCH_DIR}"

# Locate the task trial directory from the preserved data (Pier has already
# cleaned up the live jobs dir and containers by this point).
TRIAL_DIR="$(ls -d "${PRESERVE_BASE}/mct-single-${TASK_NAME}/${TASK_NAME}__"* 2>/dev/null | head -1 || true)"
if [[ -z "${TRIAL_DIR}" ]]; then
    echo "[persist] WARNING: no trial directory found under ${PRESERVE_BASE}/mct-single-${TASK_NAME}" >&2
fi

# Copy reward.json and instruction.md into BENCH_DIR/TASK_NAME.
if [[ -n "${TRIAL_DIR}" ]]; then
    if [[ -f "${TRIAL_DIR}/verifier/reward.json" ]]; then
        cp "${TRIAL_DIR}/verifier/reward.json" "${BENCH_DIR}/${TASK_NAME}/reward.json" || true
        echo "[persist] copied reward.json"
    else
        echo "[persist] WARNING: reward.json not found at ${TRIAL_DIR}/verifier/reward.json" >&2
    fi

    INSTRUCTION_SRC=""
    for cand in \
        "${TRIAL_DIR}/agent/repo/instruction.md" \
        "${TASKS}/${TASK_NAME}/instruction.md"; do
        if [[ -f "${cand}" ]]; then
            INSTRUCTION_SRC="${cand}"
            break
        fi
    done
    if [[ -n "${INSTRUCTION_SRC}" ]]; then
        cp "${INSTRUCTION_SRC}" "${BENCH_DIR}/${TASK_NAME}/instruction.md" || true
        echo "[persist] copied instruction.md (${INSTRUCTION_SRC})"
    else
        echo "[persist] WARNING: instruction.md not found" >&2
    fi

    # rsync the agent directory if present.
    if [[ -d "${TRIAL_DIR}/agent" ]]; then
        rsync -a "${TRIAL_DIR}/agent/" "${BENCH_DIR}/${TASK_NAME}/agent/" \
            && echo "[persist] rsynced agent directory" \
            || true
    else
        echo "[persist] NOTE: no agent directory present at ${TRIAL_DIR}/agent"
    fi
fi

# Capture meta-orchestrator trajectory and conversation from inside the container.
# NOTE: by this point Pier has usually already torn down the container, so these
# docker exec calls are best-effort and must not abort the script under set -e.
CONTAINER_NAME=$(docker ps --format '{{.Names}}' | grep -i "${TASK_NAME}" | head -1 || true)
if [[ -n "${CONTAINER_NAME}" ]]; then
    echo "[preserve] Found container: ${CONTAINER_NAME}"
    docker exec "${CONTAINER_NAME}" sh -c 'cat /app/.machtiani/meta-orchestrator/sessions/*/trajectory.jsonl 2>/dev/null' \
        > "${PRESERVE_BASE}/container-trajectory.jsonl" 2>/dev/null || true
    docker exec "${CONTAINER_NAME}" sh -c 'cat /app/.machtiani/sessions/*/conversation.json 2>/dev/null' \
        > "${PRESERVE_BASE}/container-conversation.json" 2>/dev/null || true

    # Best-effort capture of git log, diff stat, and implementation plan.
    docker exec "${CONTAINER_NAME}" sh -c 'git -C /app log 2>/dev/null' \
        > "${BENCH_DIR}/${TASK_NAME}/git-log.txt" 2>/dev/null || true
    docker exec "${CONTAINER_NAME}" sh -c 'git -C /app diff --stat 2>/dev/null; git -C /app diff --cached --stat 2>/dev/null' \
        > "${BENCH_DIR}/${TASK_NAME}/git-diff-stat.txt" 2>/dev/null || true
    docker exec "${CONTAINER_NAME}" sh -c 'cat /app/implementation-plan.md 2>/dev/null' \
        > "${BENCH_DIR}/${TASK_NAME}/implementation-plan.md" 2>/dev/null || true
else
    echo "[preserve] Note: Container for ${TASK_NAME} was already cleaned up (not running)"
fi

# Write run-metadata.json at the BENCH_DIR level.
MACHTIANI_BENCH_HEAD="$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)"
DEEP_SWE_HEAD="$(git -C "${DEEP_SWE_REPO}" rev-parse HEAD 2>/dev/null || echo unknown)"
jq -n \
    --arg timestamp "${BENCH_TIMESTAMP_ISO}" \
    --arg agent "${AGENT_NAME}" \
    --arg treatment "${TREATMENT_NAME}" \
    --arg mct_bench_head "${MACHTIANI_BENCH_HEAD}" \
    --arg deep_swe_head "${DEEP_SWE_HEAD}" \
    --arg model "${MODEL}" \
    --arg shell_agent_model "${SHELL_AGENT_MODEL}" \
    --argjson n_concurrent 1 \
    --argjson agent_timeout_multiplier 3.0 \
    '{
        timestamp: $timestamp,
        agent: $agent,
        treatment: $treatment,
        mct_bench_head: $mct_bench_head,
        deep_swe_head: $deep_swe_head,
        model: $model,
        shell_agent_model: $shell_agent_model,
        n_concurrent: $n_concurrent,
        agent_timeout_multiplier: $agent_timeout_multiplier
    }' > "${BENCH_DIR}/run-metadata.json"
echo "[persist] wrote run-metadata.json"

rsync -a "${JOBS_DIR}/" "${PRESERVE_BASE}/" || true
echo "[preserve] Final preservation to ${PRESERVE_BASE}"

if ls "${PRESERVE_BASE}/mct-single-${TASK_NAME}/${TASK_NAME}_"*"/verifier/reward.json" >/dev/null 2>&1; then
    echo "PRESERVATION SUCCESS: reward.json captured"
else
    echo "PRESERVATION WARNING: reward.json not found at expected path"
fi

sleep 2
echo ""

echo "==== Run complete ===="
echo ""
echo "Results persisted to:"
echo "  ${BENCH_DIR}"
echo ""
echo "Captured files:"
( cd "${BENCH_DIR}" && find . -type f | sort | sed 's#^\./#  #')
echo ""
echo "Check reward:"
echo "  cat ${PRESERVE_BASE}/mct-single-${TASK_NAME}/${TASK_NAME}_*/verifier/reward.json"
echo ""
echo "Check meta-orchestrator trajectory:"
echo "  ls ${PRESERVE_BASE}/mct-single-${TASK_NAME}/${TASK_NAME}_*/agent/meta-orchestrator/"
