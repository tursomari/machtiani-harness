#!/usr/bin/env bash
set -euo pipefail

run_sync_under_pty() {
  local output_file="$1"
  local sync_args="${2:-}"
  local status
  set +e
  expect <<EOF >/dev/null
set timeout -1
log_user 1
log_file -noappend "$output_file"
spawn sh -c {stty cols 240; exec ${MCT_SMOKE_AGENT:-machtiani} sync $sync_args}
expect eof
set result [wait]
exit [lindex \$result 3]
EOF
  status=$?
  set -e
  if (( status != 0 )); then
    echo "PTY sync failed; captured output follows:" >&2
    cat "$output_file" >&2
  fi
  return "$status"
}

strip_ansi() {
  sed -E $'s/\033\\[[0-9;?]*[[:alpha:]]//g' "$1" | tr -d '\r'
}

export HOME=/tmp/mct-smoke-home
export PATH="$HOME/.local/bin:$PATH"
mkdir -p "$HOME"
bash /fixtures/mct-source/tests/smoke/update-container.sh

# Step 1: Initialize a clean Git project
echo "==> Initializing Git project..."
git init --initial-branch=main
git config user.email "smoke-test@example.invalid"
git config user.name "machtiani smoke test"
git commit --allow-empty -m "Initial commit"

# Step 2: Check binary presence
echo "==> Checking binary version..."
machtiani --version

# Step 3: Exercise the complete script-safe configuration lifecycle. This
# leaves one clean DeepSeek-compatible provider/model configuration for the
# live sync and run below.
bash /fixtures/mct-source/tests/smoke/config-crud.sh

# Step 4: Prove automatic context correction against a deterministic local
# ChatCompletion endpoint. Discovery and answer use separate aliases. The first
# discovery request overflows, its reduced retry succeeds, and only the
# discovery alias learns the smaller context.
echo "==> Verifying automatic context overflow correction..."
overflow_config=/tmp/context-overflow-config.toml
machtiani config add --path "$overflow_config" \
  --provider overflow \
  --url http://context-overflow/v1 \
  --api-key smoke-key \
  --model discovery-model \
  --alias discovery-overflow \
  --context-length 128000 \
  --no-cache \
  --no-interactive
machtiani config model add --path "$overflow_config" answer-stable \
  --provider overflow \
  --model answer-model \
  --context-length 128000 \
  --no-interactive
machtiani config model show --path "$overflow_config" discovery-overflow > /tmp/context-overflow-discovery-before.stdout
machtiani config model show --path "$overflow_config" answer-stable > /tmp/context-overflow-answer-before.stdout
grep -Fq 'context_length: 128000 (model)' /tmp/context-overflow-discovery-before.stdout
grep -Fq 'context_length: 128000 (model)' /tmp/context-overflow-answer-before.stdout
(
  cd /fixtures/mct-source/agent
  MACHTIANI_CONFIG="$overflow_config" \
    go run ./tests/context-overflow-smoke
) \
  > /tmp/context-overflow.stdout \
  2> /tmp/context-overflow.stderr
grep -Fq 'CONTEXT OVERFLOW SMOKE PASSED: discovery_requests=2 answer_requests=2' /tmp/context-overflow.stdout
machtiani config check --path "$overflow_config"
machtiani config model show --path "$overflow_config" discovery-overflow > /tmp/context-overflow-discovery-after.stdout
machtiani config model show --path "$overflow_config" answer-stable > /tmp/context-overflow-answer-after.stdout
grep -Fq 'context_length: 63999 (model)' /tmp/context-overflow-discovery-after.stdout
grep -Fq 'context_length: 128000 (model)' /tmp/context-overflow-answer-after.stdout

# Step 5: Initialize the repository's internal README state and verify the
# interactive footer on both a model-backed sync and a subsequent no-op.
echo "==> Synchronizing repository state..."
sync_output=$(mktemp)
sync_clean=$(mktemp)
wrapper_agent=$(readlink -f "$(command -v machtiani)")
native_agent="$(dirname "$wrapper_agent")/.machtiani-wrapped"
test -x "$native_agent"

echo "==> Verifying command supervision with a planted PATH blocker..."
MACHTIANI_BIN="$native_agent" \
  MCT_SUPERVISOR_SMOKE_REPO="$PWD" \
  bash /fixtures/mct-source/agent/tests/command-supervisor-smoke.sh

shell_tool_trap=$(mktemp -d)
for tool in rg sed ls; do
  printf '#!/bin/sh\necho "unexpected sync file-tool invocation: %s" >&2\nexit 97\n' "$tool" > "$shell_tool_trap/$tool"
  chmod +x "$shell_tool_trap/$tool"
done
MCT_SMOKE_AGENT="$native_agent" PATH="$shell_tool_trap:$PATH" run_sync_under_pty "$sync_output" --verbose
strip_ansi "$sync_output" > "$sync_clean"
grep -q 'Readme synced for commit ' "$sync_clean"
grep -Eq 'session token input [0-9,]+[[:space:]]+\(cache [0-9]+%\)[[:space:]]+output [0-9,]+' "$sync_clean"
grep -Eq 'sync [0-9a-f]{12}' "$sync_clean"
grep -q 'discovery ' "$sync_clean"
grep -q 'answer ' "$sync_clean"
grep -Fq 'llm.context_budget.resolved context_length=128000 source=model_default' "$sync_clean"

config_checksum=$(sha256sum "$HOME/.machtiani/config.toml" | cut -d ' ' -f 1)
machtiani sync --verbose --context-length 64000 > /tmp/sync-context.stdout 2> /tmp/sync-context.stderr
grep -Fq 'llm.context_budget.resolved context_length=64000 source=session_flag' /tmp/sync-context.stderr
grep -Eq 'llm.context_budget.resolved context_length=64000 .*stage=file-discovery' /tmp/sync-context.stderr
grep -Eq 'llm.context_budget.resolved context_length=64000 .*stage=answer' /tmp/sync-context.stderr
test "$(sha256sum "$HOME/.machtiani/config.toml" | cut -d ' ' -f 1)" = "$config_checksum"

sync_noop_output=$(mktemp)
sync_noop_clean=$(mktemp)
run_sync_under_pty "$sync_noop_output"
strip_ansi "$sync_noop_output" > "$sync_noop_clean"
grep -Eq 'session token input 0[[:space:]]+\(cache 0%\)[[:space:]]+output 0' "$sync_noop_clean"
grep -Eq 'sync [0-9a-f]{12}' "$sync_noop_clean"
if grep -Eq '(discovery|answer)[[:space:]]' "$sync_noop_clean"; then
  echo "No-op sync footer unexpectedly claimed an LLM model." >&2
  exit 1
fi
rm -f "$sync_output" "$sync_clean" "$sync_noop_output" "$sync_noop_clean"
rm -rf "$shell_tool_trap"

# Step 6: Live execution
echo "==> Running live smoke test..."
project_store=$(machtiani project show --json | sed -n 's/^[[:space:]]*"store": "\([^"]*\)"[,]\{0,1\}$/\1/p')
test -n "$project_store"
default_run_output=$(mktemp)
machtiani run -p "List the last commit message, then finish." --max-turns 5 | tee "$default_run_output"
default_session_id=$(sed -n 's/.*--resume \(agent-[0-9TZ]\+-[0-9]\+\).*/\1/p' "$default_run_output" | tail -n 1)
test -n "$default_session_id"
default_session="$project_store/sessions/$default_session_id"
default_conversation="$default_session/artifacts/conversation.json"
test -s "$default_conversation"
default_final="$default_session/chat/agent-final-answer.md"
test -s "$default_final"
default_display="$default_final"
if [[ "$default_display" == "$HOME"/* ]]; then
  default_display="~${default_display#$HOME}"
fi
python3 - "$default_run_output" "$default_display" <<'PY'
import pathlib
import sys

output = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
path = sys.argv[2]
expected = (
    "  Answer saved to:\n"
    f"    {path}\n\n"
    "  Resume this session:\n"
)
if expected not in output:
    raise SystemExit(f"missing final-answer continuation block {expected!r} in {output!r}")
PY
test ! -e "$default_session/artifacts/llm"
grep -Fq '"kind":"llm.context_budget.resolved"' "$default_session/trajectory/agent.jsonl"
grep -Fq '"context_length":128000' "$default_session/trajectory/agent.jsonl"
grep -Fq '"source":"model_default"' "$default_session/trajectory/agent.jsonl"

echo "==> Verifying run session context override..."
machtiani run --context-length 64000 -p "List the last commit message, then finish." --max-turns 5
grep -R -Fq '"context_length":64000' "$project_store/sessions"/*/trajectory/agent.jsonl
grep -R -Fq '"source":"session_flag"' "$project_store/sessions"/*/trajectory/agent.jsonl
test "$(sha256sum "$HOME/.machtiani/config.toml" | cut -d ' ' -f 1)" = "$config_checksum"

echo "==> Verifying explicit LLM input logging..."
machtiani run --log-llm-inputs -p "List the last commit message, then finish." --max-turns 5
logged_input=$(find "$project_store/sessions" -path '*/artifacts/llm/inputs.jsonl' -type f -print -quit)
test -s "$logged_input"
logged_session=${logged_input%/artifacts/llm/inputs.jsonl}

# Step 7: Prove that a historical checkout injects the README mapped to that
# exact project commit, even while the shared compatibility artifact contains
# the README for a later commit.
echo "==> Verifying historical internal README injection..."
rollback_dir=$(mktemp -d)
readme_repo="$project_store/artifacts/readme"

printf 'package architecture\n\nconst Version = "early"\n' > architecture.go
git add architecture.go
git commit -m "smoke: early architecture"
early_project_commit=$(git rev-parse HEAD)
MCT_README_TEST_STUB=smoke-early machtiani sync
early_readme_commit=$(git -C "$readme_repo" rev-parse "oid-${early_project_commit}^{commit}")
git -C "$readme_repo" show "${early_readme_commit}:internal-readme.md" > "$rollback_dir/early.md"

printf 'package architecture\n\nconst Version = "late"\n' > architecture.go
git add architecture.go
git commit -m "smoke: late architecture"
late_project_commit=$(git rev-parse HEAD)
MCT_README_TEST_STUB=smoke-late machtiani sync
late_readme_commit=$(git -C "$readme_repo" rev-parse "oid-${late_project_commit}^{commit}")
git -C "$readme_repo" show "${late_readme_commit}:internal-readme.md" > "$rollback_dir/late.md"

cmp "$rollback_dir/late.md" "$readme_repo/internal-readme.md"
if cmp -s "$rollback_dir/early.md" "$readme_repo/internal-readme.md"; then
  echo "Historical and current README fixtures unexpectedly match." >&2
  exit 1
fi

git checkout --detach "$early_project_commit"
test "$(git rev-parse HEAD)" = "$early_project_commit"
rollback_session_id="smoke-readme-rollback"
MACHTIANI_SESSION_ID="$rollback_session_id" machtiani run --dry-run \
  --max-turns 1 \
  -p "Verify historical internal README injection."
rollback_conversation="$project_store/sessions/$rollback_session_id/artifacts/conversation.json"
test -s "$rollback_conversation"
jq -j 'first(.messages[] | select(.turn == 0 and .metadata.type == "work_result") | .content)' \
  "$rollback_conversation" > "$rollback_dir/injected.md"
cmp "$rollback_dir/early.md" "$rollback_dir/injected.md"
cmp "$rollback_dir/late.md" "$readme_repo/internal-readme.md"
git checkout main

# Step 8: Best-effort additional provider matrix discovered from the host's
# repository configuration. Each case is configured through the public CRUD
# surface and uses low reasoning. Credentials remain environment references.
IFS=',' read -r -a smoke_providers <<<"${SMOKE_MATRIX:-}"
for provider in "${smoke_providers[@]}"; do
  [[ -n "$provider" ]] || continue
  upper=$(printf '%s' "$provider" | tr '[:lower:]' '[:upper:]')
  key_var="SMOKE_${upper}_API_KEY"
  url_var="SMOKE_${upper}_BASE_URL"
  model_var="SMOKE_${upper}_MODEL"
  key=${!key_var:-}
  url=${!url_var:-}
  model=${!model_var:-}
  if [[ -z "$key" || -z "$url" || -z "$model" ]]; then
    echo "==> Skipping incomplete ${provider} smoke target."
    continue
  fi

  echo "==> Running ${provider} live configuration smoke..."
  machtiani config provider add "smoke-${provider}" \
    --url "$url" \
    --api-key-env "$key_var" \
    --no-interactive
  machtiani config model add "smoke-${provider}" \
    --provider "smoke-${provider}" \
    --model "$model" \
    --reasoning low \
    --no-interactive
  machtiani config cache disable --model "smoke-${provider}" --no-interactive
  machtiani config check
  machtiani run --model "smoke-${provider}" \
    -p "List the last commit message, then finish." \
    --max-turns 5
done

# Step 9: Verify session artifacts
echo "==> Verifying session artifacts..."
test -f .machtiani/project.uuid
test -d "$project_store/sessions"
ls "$project_store"/sessions/*/artifacts/conversation.json
find "$project_store"/sessions -path '*/trajectory/agent.jsonl' -type f -print -quit | grep -q .

echo "==> Verifying disposable session pruning..."
machtiani session prune --dry-run --json | grep -q '"removed_llm_input_files": 1'
machtiani session prune --no-interactive --yes
test ! -e "$logged_session/artifacts/llm"
find "$project_store"/sessions -path '*/trajectory/agent.jsonl' -type f -print -quit | grep -q .

# Step 10: Exercise legacy migration in a separate disposable project.
echo "==> Verifying legacy migration..."
workspace=$PWD
legacy_repo=$(mktemp -d)
legacy_home=$(mktemp -d)
git -C "$legacy_repo" init --quiet
mkdir -p "$legacy_repo/.machtiani/sessions/legacy-session"
printf 'legacy smoke artifact\n' >"$legacy_repo/.machtiani/sessions/legacy-session/result.txt"
mkdir -p "$legacy_repo/.machtiani/sessions/legacy-session/artifacts/llm"
printf 'full input\n' >"$legacy_repo/.machtiani/sessions/legacy-session/artifacts/llm/inputs.jsonl"
mkdir -p "$legacy_repo/.machtiani/sessions/legacy-session/shell-agent/1"
printf 'state\n' >"$legacy_repo/.machtiani/sessions/legacy-session/shell-agent/1/state.json"
printf 'trajectory\n' >"$legacy_repo/.machtiani/sessions/legacy-session/shell-agent/1/trajectory.json"
(
  cd "$legacy_repo"
  HOME="$legacy_home" machtiani migrate --dry-run --no-interactive
  test ! -f .machtiani/project.uuid
  HOME="$legacy_home" machtiani migrate --no-interactive --yes
  test -f .machtiani/project.uuid
  migrated_store=$(HOME="$legacy_home" machtiani project show --json | sed -n 's/^[[:space:]]*"store": "\([^"]*\)"[,]\{0,1\}$/\1/p')
  test -n "$migrated_store"
  test -f "$migrated_store/sessions/legacy-session/result.txt"
  test -f "$migrated_store/sessions/legacy-session/shell-agent/1/trajectory.json"
  test ! -e "$migrated_store/sessions/legacy-session/artifacts/llm"
  test ! -e "$migrated_store/sessions/legacy-session/shell-agent/1/state.json"
  ls -d .machtiani.legacy-*
)
cd "$workspace"

# Step 11: Cleanup
echo "==> Cleaning up..."
rm -rf .machtiani/ "$project_store" "$legacy_repo" "$legacy_home" "$rollback_dir"

# Step 12: Print success message
echo "SMOKE TEST PASSED: All checks completed successfully."
