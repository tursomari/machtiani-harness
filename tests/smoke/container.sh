#!/usr/bin/env bash
set -euo pipefail

# Step 1: Initialize a clean Git project
echo "==> Initializing Git project..."
git init --initial-branch=main
git config user.email "smoke-test@example.invalid"
git config user.name "mct-agent smoke test"
git commit --allow-empty -m "Initial commit"

# Step 2: Check binary presence
echo "==> Checking binary version..."
mct-agent --version

# Step 3: Exercise the complete script-safe configuration lifecycle. This
# leaves one clean DeepSeek-compatible provider/model configuration for the
# live sync and run below.
bash /tests/smoke/config-crud.sh

# Step 4: Initialize the repository's internal README state
echo "==> Synchronizing repository state..."
mct-agent sync

# Step 5: Live execution
echo "==> Running live smoke test..."
project_store=$(mct-agent project show --json | sed -n 's/^[[:space:]]*"store": "\([^"]*\)"[,]\{0,1\}$/\1/p')
test -n "$project_store"
mct-agent run -t "List the last commit message, then finish." --max-turns 5
default_session=$(find "$project_store/sessions" -path '*/artifacts/conversation.json' -printf '%T@ %h\n' | sort -n | tail -1 | xargs dirname)
test -n "$default_session"
test ! -e "$default_session/artifacts/llm"

echo "==> Verifying explicit LLM input logging..."
mct-agent run --log-llm-inputs -t "List the last commit message, then finish." --max-turns 5
logged_session=$(find "$project_store/sessions" -path '*/artifacts/conversation.json' -printf '%T@ %h\n' | sort -n | tail -1 | xargs dirname)
test -s "$logged_session/artifacts/llm/inputs.jsonl"

# Step 6: Best-effort additional provider matrix discovered from the host's
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
  mct-agent config provider add "smoke-${provider}" \
    --url "$url" \
    --api-key-env "$key_var" \
    --no-interactive
  mct-agent config model add "smoke-${provider}" \
    --provider "smoke-${provider}" \
    --model "$model" \
    --reasoning low \
    --no-interactive
  mct-agent config cache disable --model "smoke-${provider}" --no-interactive
  mct-agent config check
  mct-agent run --model "smoke-${provider}" \
    -t "List the last commit message, then finish." \
    --max-turns 5
done

# Step 7: Verify session artifacts
echo "==> Verifying session artifacts..."
test -f .machtiani/project.uuid
test -d "$project_store/sessions"
ls "$project_store"/sessions/*/artifacts/conversation.json
find "$project_store"/sessions -path '*/trajectory/agent.jsonl' -type f -print -quit | grep -q .

echo "==> Verifying disposable session pruning..."
mct-agent session prune --dry-run --json | grep -q '"removed_llm_input_files": 1'
mct-agent session prune --no-interactive --yes
test ! -e "$logged_session/artifacts/llm"
find "$project_store"/sessions -path '*/trajectory/agent.jsonl' -type f -print -quit | grep -q .

# Step 8: Exercise legacy migration in a separate disposable project.
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
  HOME="$legacy_home" mct-agent migrate --dry-run --no-interactive
  test ! -f .machtiani/project.uuid
  HOME="$legacy_home" mct-agent migrate --no-interactive --yes
  test -f .machtiani/project.uuid
  migrated_store=$(HOME="$legacy_home" mct-agent project show --json | sed -n 's/^[[:space:]]*"store": "\([^"]*\)"[,]\{0,1\}$/\1/p')
  test -n "$migrated_store"
  test -f "$migrated_store/sessions/legacy-session/result.txt"
  test -f "$migrated_store/sessions/legacy-session/shell-agent/1/trajectory.json"
  test ! -e "$migrated_store/sessions/legacy-session/artifacts/llm"
  test ! -e "$migrated_store/sessions/legacy-session/shell-agent/1/state.json"
  ls -d .machtiani.legacy-*
)
cd "$workspace"

# Step 9: Cleanup
echo "==> Cleaning up..."
rm -rf .machtiani/ "$project_store" "$legacy_repo" "$legacy_home"

# Step 10: Print success message
echo "SMOKE TEST PASSED: All checks completed successfully."
