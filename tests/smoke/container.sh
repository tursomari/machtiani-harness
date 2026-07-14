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
mct-agent run -t "List the last commit message, then finish." --max-turns 5

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
test -d .machtiani/sessions
ls .machtiani/sessions/*/artifacts/conversation.json

# Step 8: Cleanup
echo "==> Cleaning up..."
rm -rf .machtiani/

# Step 9: Print success message
echo "SMOKE TEST PASSED: All checks completed successfully."
