#!/usr/bin/env bash
set -euo pipefail

ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
RUN_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/mct-update-local.XXXXXX")
SEED="$RUN_ROOT/seed"
REMOTE="$RUN_ROOT/remote.git"
OTHER_REMOTE="$RUN_ROOT/other-remote.git"
TEST_HOME="$RUN_ROOT/home"
PREFIX="$RUN_ROOT/prefix"
SOURCE="$TEST_HOME/.machtiani/installations/mct-agent/source"
USER_CLONE="$TEST_HOME/src/mct-install"
OTHER_CLONE="$TEST_HOME/src/other-mct-install"

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

mkdir -p "$(dirname "$USER_CLONE")"
git clone --quiet --depth 1 --single-branch "file://$REMOTE" "$USER_CLONE"

printf 'local bootstrap edit\n' >"$USER_CLONE/local-change.txt"
if HOME="$TEST_HOME" PREFIX="$PREFIX" bash "$USER_CLONE/scripts/install.sh" --managed >/dev/null 2>&1; then
  echo "dirty bootstrap checkout unexpectedly installed" >&2
  exit 1
fi
test ! -e "$SOURCE"
test ! -e "$PREFIX/bin/mct-agent"
rm "$USER_CLONE/local-change.txt"

git -C "$USER_CLONE" remote set-url origin "https://user:bootstrap-secret@example.invalid/repo.git"
set +e
credential_output=$(HOME="$TEST_HOME" PREFIX="$PREFIX" bash "$USER_CLONE/scripts/install.sh" --managed 2>&1)
credential_status=$?
set -e
if [[ $credential_status -eq 0 ]]; then
  echo "credentialed bootstrap origin unexpectedly installed" >&2
  exit 1
fi
if [[ "$credential_output" == *"bootstrap-secret"* ]]; then
  echo "credentialed bootstrap origin leaked into installer output" >&2
  exit 1
fi
test ! -e "$SOURCE"
test ! -e "$PREFIX/bin/mct-agent"
git -C "$USER_CLONE" remote set-url origin "file://$REMOTE"

HOME="$TEST_HOME" PREFIX="$PREFIX" bash "$USER_CLONE/scripts/install.sh" --managed

test "$(HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_a"
test -f "$TEST_HOME/.machtiani/installations/mct-agent/receipt.json"
test -d "$SOURCE/.git"
test "$(git -C "$SOURCE" remote get-url origin)" = "file://$REMOTE"
python3 - "$TEST_HOME/.machtiani/installations/mct-agent/receipt.json" "$SOURCE" <<'PY'
import json, sys
receipt = json.load(open(sys.argv[1]))
assert receipt["source_dir"] == sys.argv[2], receipt
PY

git clone --quiet --bare "$REMOTE" "$OTHER_REMOTE"
git -C "$OTHER_REMOTE" symbolic-ref HEAD refs/heads/rolling
git clone --quiet --depth 1 --single-branch "file://$OTHER_REMOTE" "$OTHER_CLONE"
if HOME="$TEST_HOME" PREFIX="$PREFIX" bash "$OTHER_CLONE/scripts/install.sh" --managed >/dev/null 2>&1; then
  echo "mismatched bootstrap origin unexpectedly replaced managed source" >&2
  exit 1
fi
test "$(git -C "$SOURCE" remote get-url origin)" = "file://$REMOTE"
test "$(HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_a"
rm -rf "$OTHER_CLONE"

git -C "$SEED" commit --quiet --allow-empty -m managed-reinstall
commit_b=$(git -C "$SEED" rev-parse HEAD)
git -C "$SEED" push --quiet origin rolling
HOME="$TEST_HOME" PREFIX="$PREFIX" bash "$USER_CLONE/scripts/install.sh" --managed
test "$(HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_b"
test "$(git -C "$SOURCE" rev-parse HEAD)" = "$commit_b"

rm -rf "$USER_CLONE"

git -C "$SEED" commit --quiet --allow-empty -m explicit-update
commit_c=$(git -C "$SEED" rev-parse HEAD)
git -C "$SEED" push --quiet origin rolling

check_json=$(HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" update --check --json)
python3 - "$commit_b" "$commit_c" "$check_json" <<'PY'
import json, sys
old, new, payload = sys.argv[1:]
data = json.loads(payload)
assert data["status"] == "available", data
assert data["current_commit"] == old, data
assert data["candidate_commit"] == new, data
PY

HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" update --yes --no-interactive --json >/dev/null
test "$(HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_c"
test "$(git -C "$SOURCE" rev-parse HEAD)" = "$commit_c"

printf 'do not discard me\n' >"$SOURCE/local-change.txt"
git -C "$SEED" commit --quiet --allow-empty -m third
commit_d=$(git -C "$SEED" rev-parse HEAD)
git -C "$SEED" push --quiet origin rolling
if HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" update --yes --no-interactive >/dev/null 2>&1; then
  echo "dirty managed source unexpectedly updated" >&2
  exit 1
fi
test "$(HOME="$TEST_HOME" "$PREFIX/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_c"
test "$commit_d" != "$commit_c"
test -f "$SOURCE/local-change.txt"

echo "LOCAL UPDATE TEST PASSED"
