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

# Step 3: Initialize config
echo "==> Initializing configuration..."
mct-agent init \
  --provider-url "$TEST_BASE_URL" \
  --api-key "$TEST_API_KEY" \
  --model "$TEST_MODEL"

# Step 4: Verify config file exists and contains the expected model
echo "==> Verifying config file..."
test -f .machtiani/config.toml
grep -q "$TEST_MODEL" .machtiani/config.toml

# Step 5: Initialize the repository's internal README state
echo "==> Synchronizing repository state..."
mct-agent sync

# Step 6: Live execution
echo "==> Running live smoke test..."
mct-agent run -t "Reply with the word OK and nothing else."

# Step 7: Verify session artifacts
echo "==> Verifying session artifacts..."
test -d .machtiani/sessions
ls .machtiani/sessions/*/artifacts/conversation.json

# Step 8: Cleanup
echo "==> Cleaning up..."
rm -rf .machtiani/

# Step 9: Print success message
echo "SMOKE TEST PASSED: All checks completed successfully."
