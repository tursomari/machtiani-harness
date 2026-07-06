#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# run-single-treatment.sh — Build and run a single treatment benchmark task
# with the meta-orchestrator enabled.
# ============================================================================

usage() {
    cat <<'EOF'
Usage: run-single-treatment.sh TASK_NAME [MODEL] [SHELL_AGENT_MODEL]

  TASK_NAME          Task directory name under the tasks path (required)
                     Example: abs-module-cache-flags

  MODEL              Model for agent and meta-orchestrator classifier
                     Default: deepseek-v4-pro

  SHELL_AGENT_MODEL  Model for shell-agent subprocesses
                     Default: deepseek-v4-pro

Prerequisites:
  - TEST_API_KEY, TEST_BASE_URL exported in the environment
  - Deep-SWE tasks at ~/projects/deep-swe/tasks
  - Go toolchain for building binaries
  - Docker running, pier Python package installed

Output:
  - Binaries under /tmp/mct-single-treatment/
  - Pier job output under /tmp/mct-single-treatment/jobs/
  - Reward.json at jobs/<job-name>/<trial-dir>/verifier/reward.json

Example:
  export TEST_API_KEY=sk-...
  export TEST_BASE_URL=https://api.deepseek.com
  ./scripts/run-single-treatment.sh abs-module-cache-flags glm-5-high deepseek-v4-pro
EOF
    exit 0
}

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
TASK_NAME="${1:-}"
MODEL="${2:-deepseek-v4-pro}"
SHELL_AGENT_MODEL="${3:-deepseek-v4-pro}"

if [[ -z "${TASK_NAME}" ]]; then
    echo "Error: TASK_NAME is required." >&2
    usage
fi

if [[ "$1" == "--help" || "$1" == "-h" ]]; then
    usage
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
TASKS="${HOME}/projects/deep-swe/tasks"

AGENT_BIN="${OUTPUT_BIN}/mct-agent"
META_BIN="${OUTPUT_BIN}/meta-orchestrator"

echo "[setup] Preparing ${OUTPUT_BASE}"
rm -rf "${OUTPUT_BASE}"
mkdir -p "${OUTPUT_BIN}" "${JOBS_DIR}"

# ---------------------------------------------------------------------------
# Build both binaries fresh from HEAD
# ---------------------------------------------------------------------------
echo "[build] Building mct-agent from HEAD -> ${AGENT_BIN}"
( cd "${REPO_ROOT}/agent" && go build -o "${AGENT_BIN}" ./cmd/mct-agent )

if [[ ! -x "${AGENT_BIN}" ]]; then
    echo "Error: mct-agent build did not produce an executable." >&2
    exit 1
fi
echo "[build] mct-agent done."

echo "[build] Building meta-orchestrator from HEAD -> ${META_BIN}"
( cd "${REPO_ROOT}/agent" && go build -o "${META_BIN}" ./cmd/meta-orchestrator )

if [[ ! -x "${META_BIN}" ]]; then
    echo "Error: meta-orchestrator build did not produce an executable." >&2
    exit 1
fi
echo "[build] meta-orchestrator done."

# ---------------------------------------------------------------------------
# Run the single treatment benchmark
# ---------------------------------------------------------------------------
echo ""
echo "==== Running single treatment for ${TASK_NAME} ===="
echo "  Model:            ${MODEL}"
echo "  Shell-agent model: ${SHELL_AGENT_MODEL}"
echo "  Meta-orchestrator: ${META_BIN}"
echo "  Agent binary:      ${AGENT_BIN}"
echo ""

export MCT_AGENT_BINARY="${AGENT_BIN}"
export MCT_META_ORCHESTRATOR_BINARY="${META_BIN}"
export MCT_MODEL="${MODEL}"
export MCT_SHELL_AGENT_MODEL="${SHELL_AGENT_MODEL}"

# Start background preservation loop to continuously save job output
mkdir -p "${JOBS_DIR}"
PRESERVE_DIR="/tmp/treatment-preserved-$(date +%s)"
mkdir -p "${PRESERVE_DIR}"
(while true; do rsync -a --ignore-existing "${JOBS_DIR}/" "${PRESERVE_DIR}/" 2>/dev/null; sleep 2; done) &
PRESERVE_PID=$!
echo "[preserve] Background preservation loop started (PID ${PRESERVE_PID}) -> ${PRESERVE_DIR}"

pier run \
    --ae "MCT_AGENT_BINARY=${AGENT_BIN}" \
    --ae "MCT_META_ORCHESTRATOR_BINARY=${META_BIN}" \
    --ae "MCT_MODEL=${MODEL}" \
    --ae "MCT_SHELL_AGENT_MODEL=${SHELL_AGENT_MODEL}" \
    --ae "TEST_API_KEY=${TEST_API_KEY}" \
    --ae "TEST_BASE_URL=${TEST_BASE_URL}" \
    --ae "TEST_MODEL=${MODEL}" \
    --jobs-dir "${JOBS_DIR}" \
    --agent-import-path "mct_pier_adapter.mct_agent:MctAgent" \
    --job-name "mct-single-${TASK_NAME}" \
    --include-task-name "${TASK_NAME}" \
    --n-concurrent 1 \
    --agent-timeout-multiplier 3.0 \
    -p "${TASKS}"

kill $PRESERVE_PID 2>/dev/null; wait $PRESERVE_PID 2>/dev/null

# Capture meta-orchestrator trajectory and conversation from inside the container
CONTAINER_NAME=$(docker ps --format '{{.Names}}' | grep -i "${TASK_NAME}" | head -1)
if [[ -n "${CONTAINER_NAME}" ]]; then
    echo "[preserve] Found container: ${CONTAINER_NAME}"
    docker exec "${CONTAINER_NAME}" sh -c 'cat /app/.machtiani/meta-orchestrator/sessions/*/trajectory.jsonl 2>/dev/null' > "${PRESERVE_DIR}/container-trajectory.jsonl" 2>/dev/null || \
        echo "[preserve] Could not read trajectory.jsonl from container"
    docker exec "${CONTAINER_NAME}" sh -c 'cat /app/.machtiani/sessions/*/conversation.json 2>/dev/null' > "${PRESERVE_DIR}/container-conversation.json" 2>/dev/null || \
        echo "[preserve] Could not read conversation.json from container"
else
    echo "[preserve] Note: Container for ${TASK_NAME} was already cleaned up (not running)"
fi

rsync -a "${JOBS_DIR}/" "${PRESERVE_DIR}/" || true
echo "[preserve] Final preservation to ${PRESERVE_DIR}"

if ls "${PRESERVE_DIR}/mct-single-${TASK_NAME}/${TASK_NAME}_"*"/verifier/reward.json" >/dev/null 2>&1; then
    echo "PRESERVATION SUCCESS: reward.json captured"
else
    echo "PRESERVATION WARNING: reward.json not found at expected path"
fi

sleep 2
echo ""

echo "==== Run complete ===="
echo ""
echo "Check reward:"
echo "  cat ${JOBS_DIR}/mct-single-${TASK_NAME}/${TASK_NAME}_*/verifier/reward.json"
echo ""
echo "Check meta-orchestrator trajectory:"
echo "  ls ${JOBS_DIR}/mct-single-${TASK_NAME}/${TASK_NAME}_*/agent/meta-orchestrator/"
