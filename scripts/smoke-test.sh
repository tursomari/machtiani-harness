#!/usr/bin/env bash
set -euo pipefail

# Step 1: Check binary presence
echo "==> Checking binary version..."
mct-agent --version

# Step 2: Initialize config
echo "==> Initializing configuration..."
mct-agent init \
  --provider-url "$TEST_BASE_URL" \
  --api-key "$TEST_API_KEY" \
  --model "$TEST_MODEL"

# Step 3: Verify config file exists and contains the expected model
echo "==> Verifying config file..."
test -f .machtiani/config.toml
grep -q "$TEST_MODEL" .machtiani/config.toml

# Step 4: Live execution
echo "==> Running live smoke test..."
mct-agent run -t "Reply with the word OK and nothing else."

# Step 5: Verify session artifacts
echo "==> Verifying session artifacts..."
test -d .machtiani/sessions
ls .machtiani/sessions/*/artifacts/conversation.json

# Step 6: Cleanup
echo "==> Cleaning up..."
rm -rf .machtiani/

# Step 7: Print success message
echo "SMOKE TEST PASSED: All checks completed successfully."
