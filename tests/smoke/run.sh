#!/usr/bin/env bash
set -euo pipefail

# ---------------------------------------------------------------------------
# Host-side runner for the mct-agent smoke test.
#
# Prerequisites
#   - Docker
#   - Git
#   - Environment variables:
#       TEST_API_KEY
#       TEST_BASE_URL
#       TEST_MODEL
#
# Usage:
#   ./tests/smoke/run.sh
#
# This script builds a clean Docker image from a git worktree at HEAD so
# that only committed sources are tested.  Submodules are populated from
# the host's shared .git/modules/ directory without any network access.
# ---------------------------------------------------------------------------

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKTREE="/tmp/mct-agent-smoke-context"

cd "$ROOT"

# --- Ensure the worktree is always cleaned up on exit ----------------------
cleanup() {
  git worktree remove --force "$WORKTREE" 2>/dev/null || true
}
trap cleanup EXIT

# --- Validate required environment variables -------------------------------
missing=false

for var in TEST_API_KEY TEST_BASE_URL TEST_MODEL; do
  if [[ -z "${!var:-}" ]]; then
    echo "ERROR: Required environment variable '${var}' is not set or empty." >&2
    missing=true
  fi
done

if [[ "${missing}" == "true" ]]; then
  echo >&2
  echo "Please set the missing variable(s) and try again. Example:" >&2
  echo "  export TEST_API_KEY=sk-..." >&2
  echo "  export TEST_BASE_URL=https://api.openai.com/v1" >&2
  echo "  export TEST_MODEL=gpt-4" >&2
  exit 1
fi

# --- Clean up any stale worktree from a previous run -----------------------
echo "==> Cleaning up stale worktree (if any)..."
git worktree remove --force "$WORKTREE" 2>/dev/null || true

# --- Create a fresh worktree from HEAD -------------------------------------
echo "==> Creating git worktree from HEAD..."
git worktree add --detach "$WORKTREE" HEAD

# --- Copy submodules from host working directory (no network) --------------
echo "==> Copying submodules from host working directory..."
HOST_REPO_ROOT=$(git rev-parse --show-toplevel)
if [[ -z "${HOST_REPO_ROOT:-}" ]]; then
  echo "ERROR: HOST_REPO_ROOT is not set or empty." >&2
  exit 1
fi
grep -E '^\s*path\s*=' "${WORKTREE}/.gitmodules" 2>/dev/null | while IFS= read -r line; do
  submodule_path=$(echo "$line" | sed 's/.*path\s*=\s*//' | xargs)
  if [[ -z "${submodule_path:-}" ]]; then
    continue
  fi
  host_dir="${HOST_REPO_ROOT}/${submodule_path}"
  worktree_dir="${WORKTREE}/${submodule_path}"
  if [[ -d "${host_dir}" ]] && [[ -n "$(ls -A "${host_dir}" 2>/dev/null)" ]]; then
    rm -rf "${worktree_dir}"
    mkdir -p "$(dirname "${worktree_dir}")"
    cp -a "${host_dir}" "${worktree_dir}"
    rm -rf "${worktree_dir}/.git"
  fi
done

# --- Build the Docker image from the worktree ------------------------------
echo "==> Building Docker image 'mct-agent-smoke'..."
docker build -f "$WORKTREE/tests/smoke/Dockerfile" -t mct-agent-smoke "$WORKTREE"

# --- Run the smoke test ----------------------------------------------------
echo "==> Running smoke-test container..."
set +e
docker run --rm \
  -e TEST_API_KEY \
  -e TEST_BASE_URL \
  -e TEST_MODEL \
  mct-agent-smoke \
  bash /tests/smoke/container.sh
exit_code=$?
set -e

# --- Summary ----------------------------------------------------------------
echo
if [[ "${exit_code}" -eq 0 ]]; then
  echo "==> SUCCESS: Smoke test passed."
else
  echo "==> FAILURE: Smoke test exited with code ${exit_code}." >&2
fi
exit "${exit_code}"
