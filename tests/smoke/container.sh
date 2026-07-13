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
mct-agent run -t "Reply with the word OK and nothing else."

# Step 6: Verify session artifacts
echo "==> Verifying session artifacts..."
test -d .machtiani/sessions
ls .machtiani/sessions/*/artifacts/conversation.json

# Step 7: Cleanup
echo "==> Cleaning up..."
rm -rf .machtiani/

# Step 8: Print success message
echo "SMOKE TEST PASSED: All checks completed successfully."
