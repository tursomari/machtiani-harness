#!/usr/bin/env bash
#
# Regression test: ab-dev.sh with --control-commit must not mutate the host
# git worktree's HEAD. The script is expected to build the control image from
# a copy of the repo checked out in a temporary directory; this test asserts
# that the host repo's current HEAD is unchanged after the invocation
# completes.
#
# See commit 20379361e ("fix(ab-dev): isolate control-commit build context
# from host worktree") for the original fix this test guards against
# regressing.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

ORIG_HEAD="$(git -C "$REPO_ROOT" rev-parse HEAD)"
echo "ORIG_HEAD=$ORIG_HEAD"

# The `|| ab_rc=$?` pattern keeps `set -e` from tripping on a non-zero
# exit from ab-dev.sh, so we can surface the actual code to the caller
# rather than letting `set -e` mask it.
ab_rc=0
(
  cd "$REPO_ROOT"
  scripts/ab-dev.sh HEAD~1 --control-commit HEAD~1 --no-run
) || ab_rc=$?

if [[ "$ab_rc" -ne 0 ]]; then
  echo "FAIL: ab-dev.sh exited with code $ab_rc"
  exit "$ab_rc"
fi

NEW_HEAD="$(git -C "$REPO_ROOT" rev-parse HEAD)"
echo "NEW_HEAD=$NEW_HEAD"

if [[ "$ORIG_HEAD" == "$NEW_HEAD" ]]; then
  echo "PASS: host worktree HEAD unchanged after ab-dev.sh --control-commit"
  exit 0
else
  echo "FAIL: host worktree HEAD changed from $ORIG_HEAD to $NEW_HEAD"
  exit 1
fi
