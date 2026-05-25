#!/bin/sh
set -e

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
exit 0
