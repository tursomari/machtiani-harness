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
#   ./scripts/run-smoke-test.sh
#
# This script builds a clean Docker image from a git worktree at HEAD so
# that only committed sources are tested.  No remote fetching occurs.
# ---------------------------------------------------------------------------

WORKTREE="/tmp/mct-agent-smoke-context"

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

# --- Build the Docker image from the worktree ------------------------------
echo "==> Building Docker image 'mct-agent-smoke'..."
docker build -f "$WORKTREE/Dockerfile.smoke" -t mct-agent-smoke "$WORKTREE"

# --- Run the smoke test ----------------------------------------------------
echo "==> Running smoke-test container..."
set +e
docker run --rm \
  -e TEST_API_KEY \
  -e TEST_BASE_URL \
  -e TEST_MODEL \
  mct-agent-smoke \
  bash /scripts/smoke-test.sh
exit_code=$?
set -e

# --- Clean up the worktree (always) ----------------------------------------
echo "==> Removing git worktree..."
git worktree remove --force "$WORKTREE"

# --- Summary ----------------------------------------------------------------
echo
if [[ "${exit_code}" -eq 0 ]]; then
  echo "==> SUCCESS: Smoke test passed."
else
  echo "==> FAILURE: Smoke test exited with code ${exit_code}." >&2
fi
exit "${exit_code}"
