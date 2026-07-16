#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# run-batch-subset.sh — 12-task A/B batch comparing a control agent
# (mini-swe-agent) against a treatment agent (mct-orchestrator) on the
# Deep-SWE benchmark with deepseek-v4-pro and max reasoning, then persists
# all 24 trial results under the .bench/deep-swe/ tree.
# ============================================================================

# ----------------------------------------------------------------------------
# Usage
# ----------------------------------------------------------------------------
usage() {
    cat <<'EOF'
Usage: run-batch-subset.sh [OPTIONS]

Runs a 12-task A/B batch on the Deep-SWE benchmark:
  - Control side:   mini-swe-agent (built-in)
  - Treatment side: mct-orchestrator (built from HEAD)

Options:
  --tasks-path PATH         Path to Deep-SWE task directory
                            (default: $HOME/projects/deep-swe/tasks)
  -n, --concurrent N        Number of concurrent pier trials per side
                            (default: 2; control and treatment run in parallel)
  --timeout-mult F          Agent timeout multiplier (default: 3.0)
  --model NAME              mct-orchestrator model alias
                            (default: deepseek-v4-pro)
  --litellm-model NAME      mini-swe-agent litellm model
                            (default: deepseek/deepseek-v4-pro)
  --control-label L         Agent dir label for control side
                            (default: mini-swe-agent)
  --treatment-label L       Agent dir label for treatment side
                            (default: mct-orchestrator)
  --control-treatment L     Treatment subdir for control
                            (default: default)
  --treatment-treatment L   Treatment subdir for treatment
                            (default: with-peer-review)
  --treatment-only          Skip control and run only the 12-task treatment
  --work-dir PATH           Working directory for binaries and jobs
                            (default: REPO/.data/mct-batch-subset)
  -h, --help                Print this help message and exit

Environment:
  TEST_API_KEY              Required; API key passed to both sides.
  TEST_BASE_URL             Required; base URL passed to the treatment side.
  TEST_MODEL is NOT required; this script overrides it.

Both --flag value and --flag=value forms are accepted.
EOF
    exit 0
}

# ----------------------------------------------------------------------------
# Defaults and parse CLI arguments
# ----------------------------------------------------------------------------
TASKS_PATH="${HOME}/projects/deep-swe/tasks"
CONCURRENT="2"
TIMEOUT_MULTIPLIER="3.0"
MODEL="deepseek-v4-pro"
LITELLM_MODEL="deepseek/deepseek-v4-pro"
CONTROL_AGENT_LABEL="mini-swe-agent"
TREATMENT_AGENT_LABEL="mct-orchestrator"
CONTROL_TREATMENT_LABEL="default"
TREATMENT_TREATMENT_LABEL="with-peer-review"
TREATMENT_ONLY="false"
WORK_DIR="__REPO_DATA_MCT_BATCH_SUBSET__"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --tasks-path)
            TASKS_PATH="${2:?--tasks-path requires a value}"
            shift 2
            ;;
        --tasks-path=*)
            TASKS_PATH="${1#*=}"
            shift
            ;;
        -n|--concurrent)
            CONCURRENT="${2:?--concurrent requires a value}"
            shift 2
            ;;
        -n=*)
            CONCURRENT="${1#*=}"
            shift
            ;;
        --concurrent)
            CONCURRENT="${2:?--concurrent requires a value}"
            shift 2
            ;;
        --concurrent=*)
            CONCURRENT="${1#*=}"
            shift
            ;;
        --timeout-mult)
            TIMEOUT_MULTIPLIER="${2:?--timeout-mult requires a value}"
            shift 2
            ;;
        --timeout-mult=*)
            TIMEOUT_MULTIPLIER="${1#*=}"
            shift
            ;;
        --model)
            MODEL="${2:?--model requires a value}"
            shift 2
            ;;
        --model=*)
            MODEL="${1#*=}"
            shift
            ;;
        --litellm-model)
            LITELLM_MODEL="${2:?--litellm-model requires a value}"
            shift 2
            ;;
        --litellm-model=*)
            LITELLM_MODEL="${1#*=}"
            shift
            ;;
        --control-label)
            CONTROL_AGENT_LABEL="${2:?--control-label requires a value}"
            shift 2
            ;;
        --control-label=*)
            CONTROL_AGENT_LABEL="${1#*=}"
            shift
            ;;
        --treatment-label)
            TREATMENT_AGENT_LABEL="${2:?--treatment-label requires a value}"
            shift 2
            ;;
        --treatment-label=*)
            TREATMENT_AGENT_LABEL="${1#*=}"
            shift
            ;;
        --control-treatment)
            CONTROL_TREATMENT_LABEL="${2:?--control-treatment requires a value}"
            shift 2
            ;;
        --control-treatment=*)
            CONTROL_TREATMENT_LABEL="${1#*=}"
            shift
            ;;
        --treatment-treatment)
            TREATMENT_TREATMENT_LABEL="${2:?--treatment-treatment requires a value}"
            shift 2
            ;;
        --treatment-treatment=*)
            TREATMENT_TREATMENT_LABEL="${1#*=}"
            shift
            ;;
        --treatment-only)
            TREATMENT_ONLY="true"
            shift
            ;;
        --work-dir)
            WORK_DIR="${2:?--work-dir requires a value}"
            shift 2
            ;;
        --work-dir=*)
            WORK_DIR="${1#*=}"
            shift
            ;;
        -h|--help)
            usage
            ;;
        --help=*)
            usage
            ;;
        *)
            echo "Error: unknown option: $1" >&2
            usage
            ;;
    esac
done

# ----------------------------------------------------------------------------
# Validate environment variables
# NOTE: TEST_MODEL is intentionally NOT required; the script overrides it.
# ----------------------------------------------------------------------------
for var in TEST_API_KEY TEST_BASE_URL; do
    if [[ -z "${!var:-}" ]]; then
        echo "Error: ${var} must be set and non-empty." >&2
        exit 1
    fi
done

# ----------------------------------------------------------------------------
# Resolve paths
# ----------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

if [[ "$WORK_DIR" == "__REPO_DATA_MCT_BATCH_SUBSET__" ]]; then
    WORK_DIR="${REPO_ROOT}/.data/mct-batch-subset"
elif [[ "${WORK_DIR}" != /* ]]; then
    WORK_DIR="${REPO_ROOT}/${WORK_DIR}"
fi

# ----------------------------------------------------------------------------
# The 12 Deep-SWE tasks (exact order)
# ----------------------------------------------------------------------------
TASKS=(
    "ofetch-per-origin-circuit-breaker"
    "httpx-deterministic-cookie-store"
    "query-persist-restored-query-state"
    "vulture-persistent-analysis-cache"
    "arcane-drift-detection-baselines"
    "koota-entity-snapshot-rollback"
    "aiomonitor-task-snapshots-diff"
    "ts-pattern-match-each"
    "tomlkit-toml-table-converters"
    "vitest-duration-sharding"
    "task-task-graph-export"
    "abs-module-cache-flags"
)

# ----------------------------------------------------------------------------
# Work dir setup
# ----------------------------------------------------------------------------
CONTROL_DIR="${WORK_DIR}/control"
TREATMENT_DIR="${WORK_DIR}/treatment"
CONTROL_JOBS="${CONTROL_DIR}/jobs"
TREATMENT_JOBS="${TREATMENT_DIR}/jobs"
BIN_DIR="${WORK_DIR}/bin"
SAFE_AGENT_MOUNTS_JSON='[{"type":"bind","source":"${HOST_AGENT_LOGS_PATH}","target":"${ENV_AGENT_LOGS_PATH}"},{"type":"bind","source":"${HOST_ARTIFACTS_PATH}","target":"${ENV_ARTIFACTS_PATH}"}]'

TMP_AVAIL_KB="$(df -Pk /tmp | awk 'NR==2 {print $4}')"
MIN_TMP_AVAIL_KB=$((1024 * 1024))
if (( TMP_AVAIL_KB < MIN_TMP_AVAIL_KB )); then
    echo "Error: /tmp has less than 1 GiB free (${TMP_AVAIL_KB} KiB available)." >&2
    echo "Docker health checks and build steps use /tmp; free space before running the batch." >&2
    exit 1
fi

echo "[setup] Preparing ${WORK_DIR}"
rm -rf "${WORK_DIR}"
mkdir -p "${CONTROL_JOBS}" "${TREATMENT_JOBS}" "${BIN_DIR}"

# ----------------------------------------------------------------------------
# Build step: binaries feed the treatment side only; the control side uses
# the built-in mini-swe-agent.
# ----------------------------------------------------------------------------
AGENT_BIN="${BIN_DIR}/mct-agent"
META_BIN="${BIN_DIR}/meta-orchestrator"
FORGE_BIN="${BIN_DIR}/forge"

echo "[build] Building pinned benchmark binaries from HEAD"
"${REPO_ROOT}/scripts/build-bench-binaries.sh" "${REPO_ROOT}" "${BIN_DIR}"
test -x "${AGENT_BIN}"
test -x "${META_BIN}"

echo "[build] Downloading Forge musl binary -> ${FORGE_BIN}"
"${REPO_ROOT}/scripts/download-forge-musl.sh" "${FORGE_BIN}"
echo "[build] forge done."

# ----------------------------------------------------------------------------
# Build INCLUDE_FLAGS array (-i <task> for each task)
# ----------------------------------------------------------------------------
INCLUDE_FLAGS=()
for task in "${TASKS[@]}"; do
    INCLUDE_FLAGS+=("-i" "${task}")
done

# ----------------------------------------------------------------------------
# Snapshot git heads
# ----------------------------------------------------------------------------
MCT_BENCH_HEAD="$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)"
DEEP_SWE_HEAD="$(git -C "${HOME}/projects/deep-swe" rev-parse HEAD 2>/dev/null || echo unknown)"

# ----------------------------------------------------------------------------
# Compute batch timestamps BEFORE launching the runs so both sides share the
# same timestamp. FS form uses no colons; ISO form uses colons.
# ----------------------------------------------------------------------------
BATCH_TIMESTAMP_FS="$(date -u +%Y-%m-%dT%H-%M-%SZ)"
BATCH_TIMESTAMP_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Job names use the script PID ($$) so both sides are distinguishable.
CONTROL_JOB_NAME="control-batch-$$"
TREATMENT_JOB_NAME="treatment-batch-$$"

echo "[setup] batch timestamp: ${BATCH_TIMESTAMP_ISO}"
echo "[setup] control job:     ${CONTROL_JOB_NAME}"
echo "[setup] treatment job:   ${TREATMENT_JOB_NAME}"
echo "[setup] mct-bench head:  ${MCT_BENCH_HEAD}"
echo "[setup] deep-swe head:   ${DEEP_SWE_HEAD}"

# ----------------------------------------------------------------------------
# Launch control pier run in background
# ----------------------------------------------------------------------------
CONTROL_PID=""
if [[ "${TREATMENT_ONLY}" != "true" ]]; then
    echo ""
    echo "==== Launching control (mini-swe-agent) ===="
    pier run \
        --agent mini-swe-agent \
        --model "${LITELLM_MODEL}" \
        --ak reasoning_effort=max \
        --ae "DEEPSEEK_API_KEY=${TEST_API_KEY}" \
        --ae "MSWEA_API_KEY=${TEST_API_KEY}" \
        --jobs-dir "${CONTROL_JOBS}" \
        --job-name "${CONTROL_JOB_NAME}" \
        --n-concurrent "${CONCURRENT}" \
        --agent-timeout-multiplier "${TIMEOUT_MULTIPLIER}" \
        --mounts-json "${SAFE_AGENT_MOUNTS_JSON}" \
        -p "${TASKS_PATH}" \
        "${INCLUDE_FLAGS[@]}" \
        > "${CONTROL_DIR}/pier.log" 2>&1 &
    CONTROL_PID=$!
    echo "[run:control] Launched pier (PID ${CONTROL_PID}) -> ${CONTROL_DIR}/pier.log"
else
    echo ""
    echo "==== Skipping control (--treatment-only) ===="
fi

# ----------------------------------------------------------------------------
# Launch treatment pier run in background
# ----------------------------------------------------------------------------
echo ""
echo "==== Launching treatment (mct-orchestrator) ===="
env \
    MCT_AGENT_BINARY="${AGENT_BIN}" \
    MCT_META_ORCHESTRATOR_BINARY="${META_BIN}" \
    MCT_FORGE_BINARY="${FORGE_BIN}" \
    pier run \
    --agent-import-path mct_pier_adapter.mct_agent:MctAgent \
    --ae "MCT_AGENT_BINARY=${AGENT_BIN}" \
    --ae "MCT_META_ORCHESTRATOR_BINARY=${META_BIN}" \
    --ae "MCT_FORGE_BINARY=${FORGE_BIN}" \
    --ae "MCT_MODEL=${MODEL}" \
    --ae "MCT_SHELL_AGENT_MODEL=${MODEL}" \
    --ae "TEST_API_KEY=${TEST_API_KEY}" \
    --ae "TEST_BASE_URL=${TEST_BASE_URL}" \
    --ae "TEST_MODEL=${MODEL}" \
    --jobs-dir "${TREATMENT_JOBS}" \
    --job-name "${TREATMENT_JOB_NAME}" \
    --n-concurrent "${CONCURRENT}" \
    --agent-timeout-multiplier "${TIMEOUT_MULTIPLIER}" \
    --mounts-json "${SAFE_AGENT_MOUNTS_JSON}" \
    -p "${TASKS_PATH}" \
    "${INCLUDE_FLAGS[@]}" \
    > "${TREATMENT_DIR}/pier.log" 2>&1 &
TREATMENT_PID=$!
echo "[run:treatment] Launched pier (PID ${TREATMENT_PID}) -> ${TREATMENT_DIR}/pier.log"

# ----------------------------------------------------------------------------
# Wait for both runs to finish, capturing nonzero exit codes
# ----------------------------------------------------------------------------
echo ""
echo "==== Waiting for both runs to complete ===="
CONTROL_EXIT=0
TREATMENT_EXIT=0
if [[ -n "${CONTROL_PID}" ]]; then
    wait "${CONTROL_PID}" || CONTROL_EXIT=$?
fi
wait "${TREATMENT_PID}" || TREATMENT_EXIT=$?
echo "[wait] control exit:   ${CONTROL_EXIT}"
echo "[wait] treatment exit: ${TREATMENT_EXIT}"

# ----------------------------------------------------------------------------
# persist_side: copy trial artifacts under .bench/deep-swe/<agent>/<treatment>/<ts>/
# ----------------------------------------------------------------------------
persist_side() {
    local side_label="$1"
    local agent_label="$2"
    local treatment_label="$3"
    local jobs_dir="$4"
    local job_name="$5"
    local model_for_meta="$6"
    local shell_agent_model="$7"

    local bench_dir="${REPO_ROOT}/.bench/deep-swe/${agent_label}/${treatment_label}/${BATCH_TIMESTAMP_FS}"
    mkdir -p "${bench_dir}"
    echo "[persist:${side_label}] bench_dir=${bench_dir}"

    find_trial_dir() {
        local jobs_dir="$1"
        local job_name="$2"
        local task="$3"
        local trial_dir=""

        trial_dir="$(ls -d "${jobs_dir}/${job_name}/${task}__"* 2>/dev/null | head -1 || true)"
        if [[ -n "${trial_dir}" ]]; then
            printf '%s\n' "${trial_dir}"
            return
        fi

        # Pier may truncate long task names when deriving trial/container names.
        # Match by the longest available prefix so those trials still persist.
        local prefix_len=${#task}
        local prefix
        while (( prefix_len >= 20 )); do
            prefix="${task:0:prefix_len}"
            trial_dir="$(ls -d "${jobs_dir}/${job_name}/${prefix}"__* 2>/dev/null | head -1 || true)"
            if [[ -n "${trial_dir}" ]]; then
                printf '%s\n' "${trial_dir}"
                return
            fi
            prefix_len=$((prefix_len - 1))
        done
    }

    local n_reward=0
    local n_missing=0
    local task trial_dir task_dir container_name instruction_src

    for task in "${TASKS[@]}"; do
        task_dir="${bench_dir}/${task}"
        mkdir -p "${task_dir}"

        # Locate the trial directory for this task.
        trial_dir="$(find_trial_dir "${jobs_dir}" "${job_name}" "${task}")"
        if [[ -z "${trial_dir}" ]]; then
            echo "[persist:${side_label}] WARNING: no trial directory found for task '${task}' under ${jobs_dir}/${job_name}" >&2
            n_missing=$((n_missing + 1))
            continue
        fi

        # reward.json
        if [[ -f "${trial_dir}/verifier/reward.json" ]]; then
            cp "${trial_dir}/verifier/reward.json" "${task_dir}/reward.json" || true
            n_reward=$((n_reward + 1))
        else
            echo "[persist:${side_label}] WARNING: reward.json not found at ${trial_dir}/verifier/reward.json" >&2
        fi

        # instruction.md: prefer trial copy, fall back to tasks path.
        instruction_src=""
        for cand in \
            "${trial_dir}/agent/repo/instruction.md" \
            "${TASKS_PATH}/${task}/instruction.md"; do
            if [[ -f "${cand}" ]]; then
                instruction_src="${cand}"
                break
            fi
        done
        if [[ -n "${instruction_src}" ]]; then
            cp "${instruction_src}" "${task_dir}/instruction.md" || true
        fi

        # agent directory
        if [[ -d "${trial_dir}/agent" ]]; then
            rsync -a "${trial_dir}/agent/" "${task_dir}/agent/" || true
        fi

        # Git artifacts: prefer a live container, fall back to the repo on disk.
        container_name="$(docker ps --format '{{.Names}}' | grep -i "${task}" | head -1 || true)"
        if [[ -n "${container_name}" ]]; then
            echo "[persist:${side_label}] task '${task}': using container ${container_name}"
            docker exec "${container_name}" sh -c 'git -C /app log 2>/dev/null' \
                > "${task_dir}/git-log.txt" 2>/dev/null || true
            docker exec "${container_name}" sh -c 'git -C /app diff --stat 2>/dev/null; git -C /app diff --cached --stat 2>/dev/null' \
                > "${task_dir}/git-diff-stat.txt" 2>/dev/null || true
            docker exec "${container_name}" sh -c 'cat /app/implementation-plan.md 2>/dev/null' \
                > "${task_dir}/implementation-plan.md" 2>/dev/null || true
        else
            # Fall back to the repo checkout inside the trial directory.
            if [[ -d "${trial_dir}/agent/repo" ]]; then
                git -C "${trial_dir}/agent/repo" log \
                    > "${task_dir}/git-log.txt" 2>/dev/null || true
                { git -C "${trial_dir}/agent/repo" diff --stat 2>/dev/null
                  git -C "${trial_dir}/agent/repo" diff --cached --stat 2>/dev/null; } \
                    > "${task_dir}/git-diff-stat.txt" 2>/dev/null || true
                if [[ -f "${trial_dir}/agent/repo/implementation-plan.md" ]]; then
                    cp "${trial_dir}/agent/repo/implementation-plan.md" "${task_dir}/implementation-plan.md" || true
                fi
            fi
        fi
    done

    # Write run-metadata.json at the bench_dir level.
    jq -n \
        --arg timestamp "${BATCH_TIMESTAMP_ISO}" \
        --arg agent "${agent_label}" \
        --arg treatment "${treatment_label}" \
        --arg mct_bench_head "${MCT_BENCH_HEAD}" \
        --arg deep_swe_head "${DEEP_SWE_HEAD}" \
        --arg model "${model_for_meta}" \
        --arg shell_agent_model "${shell_agent_model}" \
        --argjson n_concurrent "${CONCURRENT}" \
        --argjson agent_timeout_multiplier "${TIMEOUT_MULTIPLIER}" \
        --argjson n_tasks "${#TASKS[@]}" \
        --argjson n_reward "${n_reward}" \
        --argjson n_missing "${n_missing}" \
        '{
            timestamp: $timestamp,
            agent: $agent,
            treatment: $treatment,
            mct_bench_head: $mct_bench_head,
            deep_swe_head: $deep_swe_head,
            model: $model,
            shell_agent_model: $shell_agent_model,
            n_concurrent: $n_concurrent,
            agent_timeout_multiplier: $agent_timeout_multiplier,
            n_tasks: $n_tasks,
            n_reward: $n_reward,
            n_missing: $n_missing
        }' > "${bench_dir}/run-metadata.json"

    echo "[persist:${side_label}] summary: n_reward=${n_reward} n_missing=${n_missing} (of ${#TASKS[@]} tasks)"
}

# ----------------------------------------------------------------------------
# Persist both sides
# ----------------------------------------------------------------------------
echo ""
if [[ "${TREATMENT_ONLY}" != "true" ]]; then
    echo "==== Persisting control results ===="
    persist_side \
        "control" \
        "${CONTROL_AGENT_LABEL}" \
        "${CONTROL_TREATMENT_LABEL}" \
        "${CONTROL_JOBS}" \
        "${CONTROL_JOB_NAME}" \
        "${LITELLM_MODEL}" \
        "n/a"
else
    echo "==== Skipping control persistence (--treatment-only) ===="
fi

echo ""
echo "==== Persisting treatment results ===="
persist_side \
    "treatment" \
    "${TREATMENT_AGENT_LABEL}" \
    "${TREATMENT_TREATMENT_LABEL}" \
    "${TREATMENT_JOBS}" \
    "${TREATMENT_JOB_NAME}" \
    "${MODEL}" \
    "${MODEL}"

# The treatment is only valid if the Pier adapter uploaded and invoked the
# meta-orchestrator. The adapter copies that trajectory to agent/meta-orchestrator.
TREATMENT_BENCH_DIR="${REPO_ROOT}/.bench/deep-swe/${TREATMENT_AGENT_LABEL}/${TREATMENT_TREATMENT_LABEL}/${BATCH_TIMESTAMP_FS}"
missing_meta=0
for task in "${TASKS[@]}"; do
    if [[ ! -d "${TREATMENT_BENCH_DIR}/${task}/agent/meta-orchestrator" ]]; then
        echo "[verify:treatment] ERROR: missing meta-orchestrator artifact for ${task}" >&2
        missing_meta=$((missing_meta + 1))
    fi
done
if (( missing_meta > 0 )); then
    echo "[verify:treatment] ERROR: ${missing_meta} treatment task(s) missing meta-orchestrator artifacts" >&2
    exit 1
fi
echo "[verify:treatment] all treatment tasks include meta-orchestrator artifacts"

# ----------------------------------------------------------------------------
# Batch complete banner
# ----------------------------------------------------------------------------
CONTROL_BENCH_DIR="${REPO_ROOT}/.bench/deep-swe/${CONTROL_AGENT_LABEL}/${CONTROL_TREATMENT_LABEL}/${BATCH_TIMESTAMP_FS}"
TREATMENT_BENCH_DIR="${REPO_ROOT}/.bench/deep-swe/${TREATMENT_AGENT_LABEL}/${TREATMENT_TREATMENT_LABEL}/${BATCH_TIMESTAMP_FS}"

echo ""
echo "========================================"
echo " Batch complete"
echo "========================================"
echo "  Control exit:    ${CONTROL_EXIT}"
echo "  Treatment exit:  ${TREATMENT_EXIT}"
echo ""
if [[ "${TREATMENT_ONLY}" != "true" ]]; then
    echo "  Control bench:   ${CONTROL_BENCH_DIR}"
else
    echo "  Control bench:   skipped"
fi
echo "  Treatment bench: ${TREATMENT_BENCH_DIR}"
echo ""
if [[ "${TREATMENT_ONLY}" != "true" ]]; then
    echo "  Control log:     ${CONTROL_DIR}/pier.log"
else
    echo "  Control log:     skipped"
fi
echo "  Treatment log:   ${TREATMENT_DIR}/pier.log"
echo "========================================"
