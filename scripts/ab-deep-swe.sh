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
  --agent-import-path <path>  Pier agent import path
                              (default: mct_pier_adapter.mct_agent:MctAgent)
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
AGENT_IMPORT_PATH="mct_pier_adapter.mct_agent:MctAgent"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --control-commit)     CONTROL_COMMIT="$2";     shift 2 ;;
        --treatment-commit)   TREATMENT_COMMIT="$2";   shift 2 ;;
        --tasks|-p)           TASKS="$2";              shift 2 ;;
        --concurrent|-n)      CONCURRENT="$2";         shift 2 ;;
        --agent-import-path)  AGENT_IMPORT_PATH="$2";  shift 2 ;;
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

echo "[setup] Preparing ${OUTPUT_BASE}"
rm -rf "${OUTPUT_BASE}"
mkdir -p "${CONTROL_OUT}" "${TREATMENT_OUT}"

declare -a WORKTREES=()
trap 'for w in "${WORKTREES[@]}"; do git -C "${REPO_ROOT}" worktree remove --force "$w" 2>/dev/null || true; done' EXIT

CONTROL_BIN="${CONTROL_OUT}/mct-agent"
TREATMENT_BIN="${TREATMENT_OUT}/mct-agent"
CONTROL_META_BIN="${CONTROL_OUT}/meta-orchestrator"
TREATMENT_META_BIN="${TREATMENT_OUT}/meta-orchestrator"

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
# Build both binaries
# ----------------------------------------------------------------------------
build_agent "${CONTROL_COMMIT}" "${CONTROL_BIN}" "control" "${CONTROL_META_BIN}"
build_agent "${TREATMENT_COMMIT}" "${TREATMENT_BIN}" "treatment" "${TREATMENT_META_BIN}"

# ----------------------------------------------------------------------------
# Run control benchmark
# ----------------------------------------------------------------------------
echo ""
echo "==== Step 1 — Run control benchmark ===="

export MCT_META_ORCHESTRATOR_BINARY=${CONTROL_META_BIN}
export MCT_AGENT_BINARY=${CONTROL_BIN}
pier run \
    --ae "MCT_AGENT_BINARY=${CONTROL_BIN}" \
    --ae "MCT_META_ORCHESTRATOR_BINARY=${CONTROL_META_BIN}" \
    --ae "TEST_API_KEY=${TEST_API_KEY}" \
    --ae "TEST_BASE_URL=${TEST_BASE_URL}" \
    --ae "TEST_MODEL=${TEST_MODEL}" \
    --agent-import-path "${AGENT_IMPORT_PATH}" \
    --job-name "mct-ab-control" \
    --jobs-dir "${CONTROL_OUT}/jobs" \
    --n-concurrent "${CONCURRENT}" \
    -p "${TASKS}"

echo "[run:control] Complete."

# ----------------------------------------------------------------------------
# Run treatment benchmark
# ----------------------------------------------------------------------------
echo ""
echo "==== Step 2 — Run treatment benchmark ===="

export MCT_META_ORCHESTRATOR_BINARY=${TREATMENT_META_BIN}
export MCT_AGENT_BINARY=${TREATMENT_BIN}
pier run \
    --ae "MCT_AGENT_BINARY=${TREATMENT_BIN}" \
    --ae "MCT_META_ORCHESTRATOR_BINARY=${TREATMENT_META_BIN}" \
    --ae "TEST_API_KEY=${TEST_API_KEY}" \
    --ae "TEST_BASE_URL=${TEST_BASE_URL}" \
    --ae "TEST_MODEL=${TEST_MODEL}" \
    --agent-import-path "${AGENT_IMPORT_PATH}" \
    --job-name "mct-ab-treatment" \
    --jobs-dir "${TREATMENT_OUT}/jobs" \
    --n-concurrent "${CONCURRENT}" \
    -p "${TASKS}"

echo "[run:treatment] Complete."

# ============================================================================
# Compare results via embedded Python
# ============================================================================
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

echo ""
echo "==== Done ===="
echo "  Control jobs:   ${CONTROL_OUT}/jobs"
echo "  Treatment jobs: ${TREATMENT_OUT}/jobs"
