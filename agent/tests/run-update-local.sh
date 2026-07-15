#!/usr/bin/env bash
set -euo pipefail

ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
RUN_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/mct-update-local.XXXXXX")
SEED="$RUN_ROOT/seed"
REMOTE="$RUN_ROOT/remote.git"
TEST_HOME="$RUN_ROOT/home"
PREFIX="$RUN_ROOT/prefix"
SOURCE="$TEST_HOME/.machtiani/installations/mct-agent/source"

cleanup() {
  chmod -R u+w "$RUN_ROOT" 2>/dev/null || true
  rm -rf "$RUN_ROOT"
}
trap cleanup EXIT

mkdir -p "$SEED" "$TEST_HOME"
(
  cd "$ROOT"
  git archive HEAD
) | tar -C "$SEED" -xf -
if ! git -C "$ROOT" diff --quiet -- . ':!third_party' ':!tests/repositories'; then
  git -C "$ROOT" diff --binary -- . ':!third_party' ':!tests/repositories' | git -C "$SEED" apply
fi
(
  cd "$ROOT"
  git ls-files --others --exclude-standard -z -- agent scripts tests docs README.md TESTING.md |
    tar --null -T - -cf -
) | tar -C "$SEED" -xf -
rm -rf "$SEED/agent/internal/shell-agent"
cp -a "$ROOT/agent/internal/shell-agent" "$SEED/agent/internal/shell-agent"
rm -rf "$SEED/agent/internal/shell-agent/.git"

git -C "$SEED" init --quiet --initial-branch=rolling
git -C "$SEED" config user.email update-test@example.invalid
git -C "$SEED" config user.name "mct update test"
git -C "$SEED" add -A
git -C "$SEED" commit --quiet -m initial
commit_a=$(git -C "$SEED" rev-parse HEAD)

git init --quiet --bare "$REMOTE"
git -C "$SEED" remote add origin "$REMOTE"
git -C "$SEED" push --quiet -u origin rolling
git -C "$REMOTE" symbolic-ref HEAD refs/heads/rolling

mkdir -p "$(dirname "$SOURCE")"
git clone --quiet --depth 1 --single-branch "file://$REMOTE" "$SOURCE"
HOME="$TEST_HOME" PREFIX="$PREFIX" bash "$SOURCE/scripts/install.sh" --managed

test "$(HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_a"
test -f "$TEST_HOME/.machtiani/installations/mct-agent/receipt.json"

git -C "$SEED" commit --quiet --allow-empty -m next
commit_b=$(git -C "$SEED" rev-parse HEAD)
git -C "$SEED" push --quiet origin rolling

check_json=$(HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" update --check --json)
python3 - "$commit_a" "$commit_b" "$check_json" <<'PY'
import json, sys
old, new, payload = sys.argv[1:]
data = json.loads(payload)
assert data["status"] == "available", data
assert data["current_commit"] == old, data
assert data["candidate_commit"] == new, data
PY

HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" update --yes --no-interactive --json >/dev/null
test "$(HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_b"
test "$(git -C "$SOURCE" rev-parse HEAD)" = "$commit_b"

printf 'do not discard me\n' >"$SOURCE/local-change.txt"
git -C "$SEED" commit --quiet --allow-empty -m third
commit_c=$(git -C "$SEED" rev-parse HEAD)
git -C "$SEED" push --quiet origin rolling
if HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" update --yes --no-interactive >/dev/null 2>&1; then
  echo "dirty managed source unexpectedly updated" >&2
  exit 1
fi
test "$(HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_b"
test "$commit_c" != "$commit_b"
test -f "$SOURCE/local-change.txt"

echo "LOCAL UPDATE TEST PASSED"
