#!/usr/bin/env bash
set -euo pipefail

# nix develop supplies a private TMPDIR that daemon build users cannot
# traverse. Nested update builds must use the container-wide /tmp instead.
unset TMPDIR TEMPDIR TMP TEMP

echo "==> Verifying managed source auto-update..."
root=$(mktemp -d)
seed="$root/seed"
remote="$root/remote.git"
update_home="$HOME"
prefix="$HOME/.local"
source_dir="$update_home/.machtiani/installations/mct-agent/source"
user_clone="$update_home/src/mct-install"
probe="$root/probe"
legacy_artifact="$update_home/.machtiani/existing-project/artifacts/preserve-me.txt"

cleanup_update_smoke() {
  chmod -R u+w "$root" 2>/dev/null || true
  rm -rf "$root"
}
trap cleanup_update_smoke EXIT

mkdir -p "$seed" "$probe" "$(dirname "$user_clone")" \
  "$(dirname "$legacy_artifact")"
printf 'legacy-project-sentinel\n' >"$legacy_artifact"
cp -a /fixtures/mct-source/. "$seed/"
rm -rf "$seed/.git" "$seed/.gocache" "$seed/.machtiani"

git -C "$seed" init --quiet --initial-branch=rolling
git -C "$seed" config user.email smoke-update@example.invalid
git -C "$seed" config user.name "mct update smoke"
git -C "$seed" add -A
git -C "$seed" commit --quiet -m initial
commit_a=$(git -C "$seed" rev-parse HEAD)

git init --quiet --bare "$remote"
git -C "$seed" remote add origin "$remote"
git -C "$seed" push --quiet -u origin rolling
git -C "$remote" symbolic-ref HEAD refs/heads/rolling

git clone --quiet --depth 1 --single-branch "file://$remote" "$user_clone"
(cd "$user_clone" && HOME="$update_home" nix run .#install -- --prefix "$prefix")
grep -Fxq 'legacy-project-sentinel' "$legacy_artifact"
test "$(HOME="$update_home" "$prefix/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_a"
test -d "$source_dir/.git"
test "$(git -C "$source_dir" remote get-url origin)" = "file://$remote"
receipt="$update_home/.machtiani/installations/mct-agent/receipt.json"
grep -Fq '"schema_version": 2' "$receipt"
grep -Fq '"source_dir": "'"$source_dir"'"' "$receipt"
grep -Fq '"profile": "'"$update_home/.machtiani/installations/mct-agent/profile"'"' "$receipt"

rm -rf "$user_clone"

git -C "$seed" commit --quiet --allow-empty -m explicit-update
commit_b=$(git -C "$seed" rev-parse HEAD)
git -C "$seed" push --quiet origin rolling
HOME="$update_home" "$prefix/bin/mct-agent" update --check --json |
  grep -q '"status":"available"'
HOME="$update_home" "$prefix/bin/mct-agent" update --yes --no-interactive --json |
  grep -q '"status":"updated"'
test "$(HOME="$update_home" "$prefix/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_b"

cat >"$update_home/.machtiani/installations/mct-agent/update.toml" <<'EOF'
policy = "auto"
cooldown_hours = 0
failure_retry_hours = 0
EOF

git -C "$seed" commit --quiet --allow-empty -m automatic-update
commit_c=$(git -C "$seed" rev-parse HEAD)
git -C "$seed" push --quiet origin rolling
(
  cd "$probe"
  expect <<EOF
set timeout 300
spawn env HOME=$update_home PATH=$prefix/bin:\$env(PATH) mct-agent project show
expect "Status:"
expect eof
catch wait result
exit [lindex \$result 3]
EOF
)
test "$(HOME="$update_home" "$prefix/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_c"

# The default branch is authoritative even after a forced history rewrite.
git -C "$seed" reset --quiet --hard "$commit_a"
git -C "$seed" commit --quiet --allow-empty -m rewritten-parent
commit_d_parent=$(git -C "$seed" rev-parse HEAD)
git -C "$seed" commit --quiet --allow-empty -m rewritten-default
commit_d=$(git -C "$seed" rev-parse HEAD)
git -C "$seed" push --quiet --force origin rolling
if git -C "$source_dir" cat-file -e "$commit_d_parent^{commit}" 2>/dev/null; then
  echo "rewritten parent unexpectedly exists before update" >&2
  exit 1
fi
(
  cd "$probe"
  expect <<EOF
set timeout 300
spawn env HOME=$update_home PATH=$prefix/bin:\$env(PATH) mct-agent project show
expect "Status:"
expect eof
catch wait result
exit [lindex \$result 3]
EOF
)
test "$(HOME="$update_home" "$prefix/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_d"
test "$(git -C "$source_dir" rev-parse HEAD)" = "$commit_d"

# A bad flake candidate never replaces the last working profile or receipt.
sed -i '1i this is not valid Nix' "$seed/flake.nix"
git -C "$seed" add flake.nix
git -C "$seed" commit --quiet -m broken-flake
git -C "$seed" push --quiet origin rolling
receipt_before=$(sha256sum "$receipt" | cut -d' ' -f1)
profile_before=$(readlink "$update_home/.machtiani/installations/mct-agent/profile")
if HOME="$update_home" "$prefix/bin/mct-agent" update --yes --no-interactive >/dev/null 2>&1; then
  echo "broken update candidate unexpectedly installed" >&2
  exit 1
fi
test "$(HOME="$update_home" "$prefix/bin/mct-agent" --version | sed -n 's/^commit: //p')" = "$commit_d"
test "$(sha256sum "$receipt" | cut -d' ' -f1)" = "$receipt_before"
test "$(readlink "$update_home/.machtiani/installations/mct-agent/profile")" = "$profile_before"
test "$(find "$update_home/.machtiani/installations/mct-agent" -maxdepth 1 -name 'profile-*-link' | wc -l)" -le 2

echo "UPDATE SMOKE PASSED"
