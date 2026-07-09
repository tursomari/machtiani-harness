#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# ab-deep-swe.sh — A/B regression test of mct-agent against Deep-SWE benchmark
# ============================================================================

# ----------------------------------------------------------------------------
# Usage
# ----------------------------------------------------------------------------
usage() {
    cat <<'EOF'
Usage: ab-deep-swe.sh [OPTIONS]

Options:
  --control-commit <ref>      Git ref for control baseline (default: master)
  --treatment-commit <ref>    Git ref for treatment experiment (default: HEAD)
  --tasks, -p <path>          Path to Deep-SWE task directory
                              (default: $HOME/projects/deep-swe/tasks)
  --concurrent, -n <int>      Number of concurrent pier trials (default: 4)
  --agent-timeout-multiplier <float>  Timeout multiplier for pier agent (default: 1.0)
  --agent-import-path <path>  Pier agent import path
                              (default: mct_pier_adapter.mct_agent:MctAgent)
  --treatment-only           Skip control benchmark and comparison; only run
                              the treatment benchmark and persist results
  --agent-name <name>        Agent label for the results tree
                              (default: mct-orchestrator)
  --treatment-name <name>    Treatment label for the results tree
                              (default: with-peer-review)
  --help, -h                  Print this help message and exit
EOF
    exit 0
}

# ----------------------------------------------------------------------------
# Defaults and parse CLI arguments
# ----------------------------------------------------------------------------
CONTROL_COMMIT="master"
TREATMENT_COMMIT="HEAD"
TASKS="${HOME}/projects/deep-swe/tasks"
CONCURRENT="4"
TIMEOUT_MULTIPLIER="1.0"
AGENT_IMPORT_PATH="mct_pier_adapter.mct_agent:MctAgent"
AGENT_NAME="mct-orchestrator"
TREATMENT_NAME="with-peer-review"
TREATMENT_ONLY="false"
DEEP_SWE_REPO="${HOME}/projects/deep-swe"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --control-commit)     CONTROL_COMMIT="$2";     shift 2 ;;
        --treatment-commit)   TREATMENT_COMMIT="$2";   shift 2 ;;
        --tasks|-p)           TASKS="$2";              shift 2 ;;
        --concurrent|-n)      CONCURRENT="$2";         shift 2 ;;
        --agent-import-path)  AGENT_IMPORT_PATH="$2";  shift 2 ;;
        --agent-timeout-multiplier) TIMEOUT_MULTIPLIER="$2"; shift 2 ;;
        --treatment-only)     TREATMENT_ONLY="true";  shift ;;
        --agent-name)         AGENT_NAME="$2";        shift 2 ;;
        --treatment-name)     TREATMENT_NAME="$2";    shift 2 ;;
        --help|-h)            usage ;;
        *) echo "Error: unknown option: $1" >&2; usage ;;
    esac
done

# ----------------------------------------------------------------------------
# Validate environment variables
# ----------------------------------------------------------------------------
for var in TEST_API_KEY TEST_BASE_URL TEST_MODEL; do
    if [[ -z "${!var:-}" ]]; then
        echo "Error: ${var} must be set and non-empty." >&2
        exit 1
    fi
done

# ----------------------------------------------------------------------------
# Resolve paths and setup output directories
# ----------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
OUTPUT_BASE="/tmp/mct-ab-deepswe"
CONTROL_OUT="${OUTPUT_BASE}/control"
TREATMENT_OUT="${OUTPUT_BASE}/treatment"

# Per-task persistence tree under the repo for treatment results.
BENCH_DIR="${REPO_ROOT}/.bench"
BENCH_TIMESTAMP_FS="$(date -u +%Y-%m-%dT%H-%M-%SZ)"
BENCH_TIMESTAMP_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
FULL_BENCH_DIR="${BENCH_DIR}/deep-swe/${AGENT_NAME}/${TREATMENT_NAME}/${BENCH_TIMESTAMP_FS}"
mkdir -p "${BENCH_DIR}" "${FULL_BENCH_DIR}"

echo "[setup] Preparing ${OUTPUT_BASE}"
rm -rf "${OUTPUT_BASE}"
mkdir -p "${CONTROL_OUT}" "${TREATMENT_OUT}"

declare -a WORKTREES=()
trap 'for w in "${WORKTREES[@]}"; do git -C "${REPO_ROOT}" worktree remove --force "$w" 2>/dev/null || true; done' EXIT

CONTROL_BIN="${CONTROL_OUT}/mct-agent"
TREATMENT_BIN="${TREATMENT_OUT}/mct-agent"
CONTROL_META_BIN="${CONTROL_OUT}/meta-orchestrator"
TREATMENT_META_BIN="${TREATMENT_OUT}/meta-orchestrator"
CONTROL_FORGE_BIN="${CONTROL_OUT}/forge"
TREATMENT_FORGE_BIN="${TREATMENT_OUT}/forge"

# ----------------------------------------------------------------------------
# Helper: build mct-agent from a given commit into a given output path
# ----------------------------------------------------------------------------
build_agent() {
    local commit="$1" output_path="$2" label="$3" meta_output_path="$4"

    echo "[build:${label}] Building mct-agent at ${commit} -> ${output_path}"

    if ! git -C "${REPO_ROOT}" rev-parse --verify "${commit}" >/dev/null 2>&1; then
        echo "Error: commit ${commit} does not exist in the repository." >&2
        exit 1
    fi

    local worktree_dir
    worktree_dir="$(mktemp -d /tmp/mct-ab-worktree-XXXXXXXX)"
    WORKTREES+=("${worktree_dir}")

    git -C "${REPO_ROOT}" worktree add --detach "${worktree_dir}" "${commit}"

    # Remove submodule placeholder directories from worktree
    rm -rf "${worktree_dir}/agent/internal/shell-agent"
    rm -rf "${worktree_dir}/agent/internal/file-discovery/tests/undici"
    # Copy submodule contents from main worktree
    cp -a "${REPO_ROOT}/agent/internal/shell-agent" "${worktree_dir}/agent/internal/shell-agent"
    cp -a "${REPO_ROOT}/agent/internal/file-discovery/tests/undici" "${worktree_dir}/agent/internal/file-discovery/tests/undici"
    # Copy mct-forge wrapper from repo root into the temporary worktree
    mkdir -p $(dirname "${worktree_dir}/peripherals/mct-forge")
    cp -a "${REPO_ROOT}/peripherals/mct-forge" "${worktree_dir}/peripherals/mct-forge"
    # Remove any .git metadata to prevent Go module confusion
    find "${worktree_dir}/agent/internal/shell-agent" -name ".git" -type f -delete 2>/dev/null || true
    find "${worktree_dir}/agent/internal/file-discovery/tests/undici" -name ".git" -type f -delete 2>/dev/null || true

    if [[ ! -d "${worktree_dir}/agent/cmd/mct-agent" ]]; then
        echo "Error: agent/cmd/mct-agent not found at commit ${commit}." >&2
        exit 1
    fi

    ( cd "${worktree_dir}/agent" && go build -o "${output_path}" ./cmd/mct-agent )

    if [[ -d "${worktree_dir}/agent/cmd/meta-orchestrator" ]]; then
        ( cd "${worktree_dir}/agent" && go build -o "${meta_output_path}" ./cmd/meta-orchestrator )
    else
        echo "[build:${label}] Warning: meta-orchestrator not available at this commit; skipping"
    fi

    git -C "${REPO_ROOT}" worktree remove --force "${worktree_dir}" 2>/dev/null || true

    if [[ ! -x "${output_path}" ]]; then
        echo "Error: build did not produce an executable at ${output_path}" >&2
        exit 1
    fi
    echo "[build:${label}] Done."
}

# ----------------------------------------------------------------------------
# Helper: persist trial results as each task completes.
#
# Arguments:
#   $1 JOBS_DIR    Pier jobs directory (e.g. ${TREATMENT_OUT}/jobs)
#   $2 BENCH_DIR    Destination bench directory (e.g. ${FULL_BENCH_DIR})
#   $3 TASKS_DIR    Deep-SWE tasks directory (for instruction.md)
#   $4 JOB_NAME     Pier job name (e.g. mct-ab-treatment)
# ----------------------------------------------------------------------------
persist_results() {
    local JOBS_DIR="$1" BENCH_DIR="$2" TASKS_DIR="$3" JOB_NAME="$4"
    declare -A PERSISTED

    # Inner helper: sweep all trial directories once and persist any new ones.
    _persist_sweep() {
        local trial_dir task_name
        for trial_dir in "${JOBS_DIR}/${JOB_NAME}/"*__*; do
            [[ -d "${trial_dir}" ]] || continue
            [[ -f "${trial_dir}/verifier/reward.json" ]] || continue
            task_name="$(basename "${trial_dir}" | sed 's/__.*$//')"
            if [[ -z "${PERSISTED[${task_name}]:-}" ]]; then
                mkdir -p "${BENCH_DIR}/${task_name}"
                cp "${trial_dir}/verifier/reward.json" \
                    "${BENCH_DIR}/${task_name}/reward.json" 2>/dev/null || true
                if [[ -f "${TASKS_DIR}/${task_name}/instruction.md" ]]; then
                    cp "${TASKS_DIR}/${task_name}/instruction.md" \
                        "${BENCH_DIR}/${task_name}/instruction.md" 2>/dev/null || true
                fi
                if [[ -d "${trial_dir}/agent" ]]; then
                    rsync -a "${trial_dir}/agent/" \
                        "${BENCH_DIR}/${task_name}/agent/" 2>/dev/null || true
                fi
                echo "[persist] captured results for ${task_name}"
                PERSISTED["${task_name}"]=1
            fi
        done
    }

    while true; do
        _persist_sweep

        # If pier run is no longer running, do one final sweep and exit.
        if ! pgrep -f "pier run" >/dev/null 2>&1; then
            _persist_sweep
            break
        fi

        sleep 5
    done
}

# ----------------------------------------------------------------------------
# Build both binaries
# ----------------------------------------------------------------------------
# Treatment binary is always built, even in --treatment-only mode.
if [[ "${TREATMENT_ONLY}" != "true" ]]; then
    build_agent "${CONTROL_COMMIT}" "${CONTROL_BIN}" "control" "${CONTROL_META_BIN}"
    "${REPO_ROOT}/scripts/download-forge-musl.sh" "${CONTROL_FORGE_BIN}"
fi
build_agent "${TREATMENT_COMMIT}" "${TREATMENT_BIN}" "treatment" "${TREATMENT_META_BIN}"
"${REPO_ROOT}/scripts/download-forge-musl.sh" "${TREATMENT_FORGE_BIN}"

# ----------------------------------------------------------------------------
# Run control benchmark
# ----------------------------------------------------------------------------
if [[ "${TREATMENT_ONLY}" != "true" ]]; then
    echo ""
    echo "==== Step 1 — Run control benchmark ===="

    export MCT_META_ORCHESTRATOR_BINARY=${CONTROL_META_BIN}
    export MCT_AGENT_BINARY=${CONTROL_BIN}
    export MCT_FORGE_BINARY=${CONTROL_FORGE_BIN}
    pier run \
        --ae "MCT_AGENT_BINARY=${CONTROL_BIN}" \
        --ae "MCT_META_ORCHESTRATOR_BINARY=${CONTROL_META_BIN}" \
        --ae "MCT_FORGE_BINARY=${CONTROL_FORGE_BIN}" \
        --ae "TEST_API_KEY=${TEST_API_KEY}" \
        --ae "TEST_BASE_URL=${TEST_BASE_URL}" \
        --ae "TEST_MODEL=${TEST_MODEL}" \
        --agent-import-path "${AGENT_IMPORT_PATH}" \
        --job-name "mct-ab-control" \
        --jobs-dir "${CONTROL_OUT}/jobs" \
        --n-concurrent "${CONCURRENT}" \
        --agent-timeout-multiplier "${TIMEOUT_MULTIPLIER}" \
        -p "${TASKS}"

    echo "[run:control] Complete."
else
    echo ""
    echo "==== Step 1 — Skipped (--treatment-only) ===="
fi

# ----------------------------------------------------------------------------
# Run treatment benchmark
# ----------------------------------------------------------------------------
echo ""
echo "==== Step 2 — Run treatment benchmark ===="

export MCT_META_ORCHESTRATOR_BINARY=${TREATMENT_META_BIN}
export MCT_AGENT_BINARY=${TREATMENT_BIN}
export MCT_FORGE_BINARY=${TREATMENT_FORGE_BIN}
pier run \
    --ae "MCT_AGENT_BINARY=${TREATMENT_BIN}" \
    --ae "MCT_META_ORCHESTRATOR_BINARY=${TREATMENT_META_BIN}" \
    --ae "MCT_FORGE_BINARY=${TREATMENT_FORGE_BIN}" \
    --ae "TEST_API_KEY=${TEST_API_KEY}" \
    --ae "TEST_BASE_URL=${TEST_BASE_URL}" \
    --ae "TEST_MODEL=${TEST_MODEL}" \
    --agent-import-path "${AGENT_IMPORT_PATH}" \
    --job-name "mct-ab-treatment" \
    --jobs-dir "${TREATMENT_OUT}/jobs" \
    --n-concurrent "${CONCURRENT}" \
    --agent-timeout-multiplier "${TIMEOUT_MULTIPLIER}" \
    -p "${TASKS}"

echo "[run:treatment] Complete."
# Run per-task persistence in the background and wait for it to finish.
( persist_results "${TREATMENT_OUT}/jobs" "${FULL_BENCH_DIR}" "${TASKS}" "mct-ab-treatment" ) &
PERSIST_PID=$!
wait "${PERSIST_PID}" 2>/dev/null || true

# ----------------------------------------------------------------------------
# Write run-metadata.json at FULL_BENCH_DIR
# ----------------------------------------------------------------------------
MCT_BENCH_HEAD="$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || echo unknown)"
DEEP_SWE_HEAD="$(git -C "${DEEP_SWE_REPO}" rev-parse HEAD 2>/dev/null || echo unknown)"
jq -n \
    --arg timestamp "${BENCH_TIMESTAMP_ISO}" \
    --arg agent "${AGENT_NAME}" \
    --arg treatment "${TREATMENT_NAME}" \
    --arg mct_bench_head "${MCT_BENCH_HEAD}" \
    --arg deep_swe_head "${DEEP_SWE_HEAD}" \
    --arg model "${TEST_MODEL}" \
    --arg deep_swe_repo "${DEEP_SWE_REPO}" \
    --argjson n_concurrent "${CONCURRENT}" \
    '{
        timestamp: $timestamp,
        agent: $agent,
        treatment: $treatment,
        mct_bench_head: $mct_bench_head,
        deep_swe_head: $deep_swe_head,
        model: $model,
        deep_swe_repo: $deep_swe_repo,
        n_concurrent: $n_concurrent
    }' > "${FULL_BENCH_DIR}/run-metadata.json"
echo "[persist] wrote run-metadata.json to ${FULL_BENCH_DIR}"

# ============================================================================
# Compare results via embedded Python
# ============================================================================
if [[ "${TREATMENT_ONLY}" != "true" ]]; then
    echo ""
    echo "==== Step 3 — Compare results ===="

    python3 << 'PYEOF'
import json, os

CJ = "/tmp/mct-ab-deepswe/control/jobs"
TJ = "/tmp/mct-ab-deepswe/treatment/jobs"

def find_rewards(jd):
    """Return dict of task_name -> reward.json path."""
    r = {}
    if not os.path.isdir(jd):
        return r
    for root, _, files in os.walk(jd):
        if "reward.json" in files:
            rel = os.path.relpath(root, jd).split(os.sep)
            if len(rel) >= 3:
                r[rel[-3]] = os.path.join(root, "reward.json")
    return r

def load(path):
    with open(path) as f:
        d = json.load(f).get("reward_stats", {})
    f2 = d.get("f2p", {})
    return {
        "fc": f2.get("correct", 0),
        "ft": f2.get("total", 0),
        "pc": d.get("p2p", {}).get("correct", 0),
        "pt": d.get("p2p", {}).get("total", 0),
        "pa": d.get("partial", 0),
    }

def passed(s):
    return s["fc"] > 0 and s["ft"] > 0 and s["fc"] == s["ft"]

cr = find_rewards(CJ)
tr = find_rewards(TJ)
tasks = sorted(set(cr) | set(tr))

if not tasks:
    print("No reward.json files found. Cannot compare.")
    raise SystemExit(0)

rows = []
for t in tasks:
    cs = load(cr[t]) if t in cr else None
    ts = load(tr[t]) if t in tr else None
    cp = passed(cs) if cs else False
    tp = passed(ts) if ts else False
    cc = cs["fc"] if cs else 0
    ct_ = cs["ft"] if cs else 0
    tc = ts["fc"] if ts else 0
    tt = ts["ft"] if ts else 0

    if tp and not cp:
        delta = "FIX"
    elif cp and not tp:
        delta = "REGRESSION"
    else:
        delta = "SAME"
    rows.append((t, cc, ct_, tc, tt, delta))

# Print table
print()
print(f"{'Task':<52} {'Control F2P':>14} {'Treatment F2P':>16} {'Delta':>12}")
print("-" * 96)
for t, cc, ct_, tc, tt, delta in rows:
    name = t[:50] if len(t) > 50 else t
    print(f"{name:<52} {cc}/{ct_:>13} {tc}/{tt:>15} {delta:>12}")

# Summary
total = len(rows)
fix   = sum(1 for r in rows if r[5] == "FIX")
reg   = sum(1 for r in rows if r[5] == "REGRESSION")
same  = sum(1 for r in rows if r[5] == "SAME")
csum  = sum(r[1] for r in rows)
tsum  = sum(r[3] for r in rows)

print()
print("-" * 96)
print("Summary")
print("-" * 96)
print(f"  Total tasks:       {total}")
print(f"  Improved (FIX):    {fix}")
print(f"  Regressed (REGR):  {reg}")
print(f"  Unchanged (SAME):  {same}")
print(f"  Control  F2P correct sum:  {csum}")
print(f"  Treatment F2P correct sum: {tsum}")
print()
PYEOF
else
    echo ""
    echo "==== Step 3 — Summary of treatment results ===="
    echo "  (skipped comparison: --treatment-only)"
    echo "  Results persisted to: ${FULL_BENCH_DIR}"
    echo "  Captured files:"
    ( cd "${FULL_BENCH_DIR}" && find . -type f | sort | sed 's#^\./#  #' )
fi

echo ""
echo "==== Done ===="
echo "  Control jobs:   ${CONTROL_OUT}/jobs"
echo "  Treatment jobs: ${TREATMENT_OUT}/jobs"
