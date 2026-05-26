#!/bin/sh
set -e
git config --global --add safe.directory "*"

# Save pristine copy of the workspace before any test modifications.
# Used by tests that need a fresh, unmodified copy of the undici repo.
PRISTINE_WS=$(mktemp -d)
cp -a /workspace/. "$PRISTINE_WS"

validate_dispatcher_rename() {
    local workspace="$1"
    local fail=0

    # No remaining DispatcherFoundation in lib/dispatcher
    local remaining
    remaining=$(grep -rw "DispatcherFoundation" "$workspace/lib/dispatcher" --include="*.js" 2>/dev/null)
    if [ -n "$remaining" ]; then
        echo "  FAIL: DispatcherFoundation still present in lib/dispatcher:"
        echo "$remaining"
        fail=1
    else
        echo "  OK: No remaining DispatcherFoundation in lib/dispatcher"
    fi

    # DispatcherFoundation appears in exactly the expected 7 files in lib/dispatcher
    local found_files
    found_files=$(grep -rlw "DispatcherFoundation" "$workspace/lib/dispatcher" --include="*.js" 2>/dev/null | sed "s|$workspace/||" | sort)
    local expected_files
    expected_files=$(printf "%s\n" \
        "lib/dispatcher/agent.js" \
        "lib/dispatcher/client.js" \
        "lib/dispatcher/dispatcher-base.js" \
        "lib/dispatcher/env-http-proxy-agent.js" \
        "lib/dispatcher/pool-base.js" \
        "lib/dispatcher/proxy-agent.js" \
        "lib/dispatcher/socks5-proxy-agent.js" | sort)
    if [ "$found_files" != "$expected_files" ]; then
        echo "  FAIL: DispatcherFoundation not in the expected 7 files."
        echo "  Expected: $expected_files"
        echo "  Found:    $found_files"
        fail=1
    else
        echo "  OK: DispatcherFoundation present in all 7 expected files"
    fi

    # Ensure test/issue-4780.js was also updated (it references DispatcherFoundation outside lib/dispatcher)
    if ! grep -q "DispatcherFoundation" "$workspace/test/issue-4780.js" 2>/dev/null; then
        echo "  FAIL: test/issue-4780.js does not contain DispatcherFoundation (it references DispatcherFoundation)"
        fail=1
    else
        echo "  OK: test/issue-4780.js updated"
    fi

    # Only the expected files should be changed (lib/dispatcher/ + test/issue-4780.js)
    local changed
    changed=$(cd "$workspace" && git diff --name-only 2>/dev/null | grep -ve "^lib/dispatcher/" -e "^test/issue-4780.js$")
    if [ -n "$changed" ]; then
        echo "  FAIL: Unexpected files were modified outside the expected set:"
        echo "$changed"
        fail=1
    else
        echo "  OK: Only expected files were changed (lib/dispatcher/*.js + test/issue-4780.js)"
    fi

    return $fail
}

test1() {
  echo "=== Test 1: FSSearch discovery ==="
  FAILED=0
  OUTPUT=$(/usr/local/bin/mct-code "1. Use FSSearch to find which file defines the function request as module.exports.request with content output and show_line_numbers true. 2. Read that file and find the exact line. 3. Summarize what you found. Do not modify any files." 2>&1)
  if [ -z "$OUTPUT" ]; then
    echo "FAIL: Test 1 - no output"
    FAILED=1
  fi
  if [ "$FAILED" -eq 1 ]; then
    return 1
  fi
  echo "PASS: Test 1"
}

test2() {
  echo "=== Test 2: Update LICENSE copyright year ==="
  FAILED=0
  /usr/local/bin/mct-code "Update every LICENSE file copyright year to 2027. Do not modify any other files." 2>&1
  for f in $(find . -name LICENSE -not -path "*/node_modules/*" -not -path "*/.git/*" -not -path "*/generated/*" -not -path "*/artifact*"); do
    ORIG_YEAR=$(grep -oE '20[0-9]{2}' "$f" | head -1)
    if [ -n "$ORIG_YEAR" ]; then
      if ! grep -q 2027 "$f"; then
        echo "FAIL: $f had year $ORIG_YEAR but does not contain 2027"
        FAILED=1
      else
        echo "OK: $f (year updated from $ORIG_YEAR to 2027)"
      fi
    else
      echo "OK: $f (no year present, correctly left untouched)"
    fi
  done
  if [ "$FAILED" -eq 1 ]; then
    return 1
  fi
  echo "PASS: Test 2"
}

test3() {
  echo "=== Test 3: Rename getGlobalOrigin to getGlobalOriginRenamed ==="

  # (1) Create a fresh copy of the undici workspace from the pristine backup
  TEST3_WS=$(mktemp -d)
  cp -a "$PRISTINE_WS"/. "$TEST3_WS"
  cd "$TEST3_WS"

  # (2) Run mct-code with the rename handoff, overriding the model
  HANDOFF="Use FSSearch to find all occurrences of the function getGlobalOrigin in the codebase, excluding test files and generated directories. Then, for each file found, use FSPatch to replace getGlobalOrigin with getGlobalOriginRenamed. Do not summarize until all files have been edited. Once done, summarize what you renamed."
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

  # (5) Assert the output contains the "Changes made:" header (diffs printed to stdout)
  if ! echo "$OUTPUT" | grep -q "Changes made:"; then
    echo "FAIL: Test3 - output does not contain 'Changes made:' header (no diffs printed)"
    FAILED=1
  fi

  # (6) Assert isBlobLike no longer appears in non-test, non-generated source files
  #     but isBlobLikeType does appear in the renamed locations. Use git grep.
  # Pathspecs are individually quoted to prevent unwanted shell glob expansion.
  if git -C "$TEST3_WS" grep -qw "getGlobalOrigin" -- . ':!test/' ':!node_modules/' ':!generated/' ':!artifact*'; then
    echo "FAIL: Test3 - getGlobalOrigin still appears in non-test, non-generated source files"
    FAILED=1
  fi

  if ! git -C "$TEST3_WS" grep -qw "getGlobalOriginRenamed" -- . ':!test/' ':!node_modules/' ':!generated/' ':!artifact*'; then
    echo "FAIL: Test3 - getGlobalOriginRenamed does not appear in any source files (rename may not have occurred)"
    FAILED=1
  fi

  if [ "$FAILED" -eq 1 ]; then
    return 1
  fi

  echo "PASS: Test 3"
}

# ---------------------------------------------------------------------------
# Test 4: FSRead, FSPatch, FSUndo workflow
# ---------------------------------------------------------------------------
test4() {
  echo "=== Test 4: FSRead, FSPatch, FSUndo ==="

  TEST4_WS=$(mktemp -d)
  cd "$TEST4_WS"

  # Create the seed file
  echo "hello world" > test4-seed.txt

  # Handoff note: exactly as specified in the task
  handoff="1. Use FSRead to read test4-seed.txt. 2. Use FSPatch to replace world with there in test4-seed.txt. 3. Use FSUndo to undo the last edit. 4. Use FSRead to confirm the file is back to original."

  # Run mct-code and capture stdout
  OUTPUT=$(MCT_DEFAULT_MODEL=deepseek-v4-pro /usr/local/bin/mct-code "$handoff" 2>&1)

  FAILED=0

  # Assert stdout contains "Changes made:" header
  if ! echo "$OUTPUT" | grep -q "Changes made:"; then
    echo "FAIL: Test 4 - output does not contain 'Changes made:'"
    FAILED=1
  fi

  # Assert stdout contains a diff line with minus world (patch removed "world")
  if ! echo "$OUTPUT" | grep -qE '^-.*world'; then
    echo "FAIL: Test 4 - output does not contain minus world"
    FAILED=1
  fi

  # Assert stdout contains a diff line with plus there (patch added "there")
  if ! echo "$OUTPUT" | grep -qE '^\+.*there'; then
    echo "FAIL: Test 4 - output does not contain plus there"
    FAILED=1
  fi

  # Assert stdout contains a diff line with minus there (undo removed "there")
  if ! echo "$OUTPUT" | grep -qE '^-.*there'; then
    echo "FAIL: Test 4 - output does not contain minus there"
    FAILED=1
  fi

  # Assert stdout contains a diff line with plus world (undo added back "world")
  if ! echo "$OUTPUT" | grep -qE '^\+.*world'; then
    echo "FAIL: Test 4 - output does not contain plus world"
    FAILED=1
  fi

  # Assert the file content is back to the original "hello world"
  CONTENT=$(cat test4-seed.txt)
  if [ "$CONTENT" != "hello world" ]; then
    echo "FAIL: Test 4 - file content is '$CONTENT', expected 'hello world'"
    FAILED=1
  fi

  if [ "$FAILED" -eq 1 ]; then
    echo "FAIL: Test 4"
    return 1
  fi

  echo "PASS: Test 4"
}

test5() {
    echo "=== Test 5: Multi-file rename (DispatcherFoundation to DispatcherFoundation) ==="

    TEST5_WS=$(mktemp -d)
    cp -a "$PRISTINE_WS"/. "$TEST5_WS"
    cd "$TEST5_WS"

    HANDOFF="Rename the class DispatcherFoundation to DispatcherFoundation across the entire codebase. Find every file that mentions DispatcherFoundation, then update each file one at a time until every occurrence has been renamed. Do not summarize until all files have been edited. Once done, confirm that no references to the old name remain anywhere."
    OUTPUT=$(MCT_DEFAULT_MODEL=deepseek-v4-pro /usr/local/bin/mct-code "$HANDOFF" 2>&1)
    EXIT_CODE=$?

    FAILED=0
    if [ "$EXIT_CODE" -ne 0 ]; then
        echo "FAIL: Test 5 - mct-code exited with code $EXIT_CODE"
        FAILED=1
    fi

    if [ -z "$OUTPUT" ]; then
        echo "FAIL: Test 5 - no output"
        FAILED=1
    fi

    if ! echo "$OUTPUT" | grep -q "Changes made:"; then
        echo "FAIL: Test 5 - output does not contain Changes made: header"
        FAILED=1
    fi

    if ! echo "$OUTPUT" | grep -qE "^[+-]"; then
        echo "FAIL: Test 5 - output does not contain diff lines"
        FAILED=1
    fi

    if ! validate_dispatcher_rename "$TEST5_WS"; then
        FAILED=1
    fi

    if [ "$FAILED" -eq 1 ]; then
        return 1
    fi

    echo "PASS: Test 5"
}

test1 || echo "Test 1 FAILED"
test2 || echo "Test 2 FAILED"
test3 || echo "Test 3 FAILED"
test4 || echo "Test 4 FAILED"
test5 || echo "Test 5 FAILED"
echo "All tests completed"
exit 0
