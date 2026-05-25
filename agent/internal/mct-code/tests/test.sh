#!/bin/sh
set -e

# Save pristine copy of the workspace before any test modifications.
# Used by tests that need a fresh, unmodified copy of the undici repo.
PRISTINE_WS=$(mktemp -d)
cp -a /workspace/. "$PRISTINE_WS"

echo "=== Test 1: FSSearch discovery ==="
OUTPUT=$(/usr/local/bin/mct-code "1. Use FSSearch to find which file defines the function request as module.exports.request with content output and show_line_numbers true. 2. Read that file and find the exact line. 3. Summarize what you found. Do not modify any files." 2>&1)
if [ -z "$OUTPUT" ]; then
  echo "FAIL: Test 1 - no output"
  exit 1
fi
echo "PASS: Test 1"

echo "=== Test 2: Update LICENSE copyright year ==="
/usr/local/bin/mct-code "Update every LICENSE file copyright year to 2027. Do not modify any other files." 2>&1

FAILED=0
for f in $(find . -name LICENSE -not -path "*/node_modules/*" -not -path "*/.git/*" -not -path "*/generated/*" -not -path "*/artifact*"); do
  if grep -qE "20[0-9]{2}" "$f"; then
    if ! grep -q 2027 "$f"; then
      echo "FAIL: $f has a year but is not 2027"
      FAILED=1
    else
      echo "OK: $f (year updated to 2027)"
    fi
  else
    echo "OK: $f (no year present, correctly left untouched)"
  fi
done
if [ "$FAILED" -eq 1 ]; then
  echo "FAIL: some LICENSE files with years were not updated"
  exit 1
fi
echo "PASS: Test 2"

echo "=== Test 3: Rename isBlobLike to isBlobLikeType ==="

# (1) Create a fresh copy of the undici workspace from the pristine backup
TEST3_WS=$(mktemp -d)
cp -a "$PRISTINE_WS"/. "$TEST3_WS"
cd "$TEST3_WS"

# (2) Run mct-code with the rename handoff, overriding the model
HANDOFF="Rename the function isBlobLike to isBlobLikeType everywhere it appears in the codebase, and update all callers. After making the changes, summarize what you renamed."
OUTPUT=$(MCT_DEFAULT_MODEL=deepseek-v4-pro /usr/local/bin/mct-code "$HANDOFF" 2>&1)
EXIT_CODE=$?

# (3) Assert exit code is 0 and output is non-empty
FAILED=0
if [ "$EXIT_CODE" -ne 0 ]; then
  echo "FAIL: Test3 - mct-code exited with code $EXIT_CODE"
  FAILED=1
fi

if [ -z "$OUTPUT" ]; then
  echo "FAIL: Test3 - no output"
  FAILED=1
fi

# (4) Assert the output contains diff lines (starting with "-" or "+")
if ! echo "$OUTPUT" | grep -qE '^[+-]'; then
  echo "FAIL: Test3 - output does not contain diff lines (no lines starting with - or +)"
  FAILED=1
fi

# (5) Assert isBlobLike no longer appears in non-test, non-generated source files
#     but isBlobLikeType does appear in the renamed locations. Use git grep.
# Pathspecs are individually quoted to prevent unwanted shell glob expansion.
if git -C "$TEST3_WS" grep -q "isBlobLike" -- . ':!test/' ':!node_modules/' ':!generated/' ':!artifact*'; then
  echo "FAIL: Test3 - isBlobLike still appears in non-test, non-generated source files"
  FAILED=1
fi

if ! git -C "$TEST3_WS" grep -q "isBlobLikeType" -- . ':!test/' ':!node_modules/' ':!generated/' ':!artifact*'; then
  echo "FAIL: Test3 - isBlobLikeType does not appear in any source files (rename may not have occurred)"
  FAILED=1
fi

if [ "$FAILED" -eq 1 ]; then
  echo "FAIL: Test3 - one or more assertions failed"
  exit 1
fi

echo "PASS: Test 3"
exit 0
