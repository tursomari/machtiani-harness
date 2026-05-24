#!/usr/bin/env bash
# run-live.sh — Live integration test for mct-code.
#
# Sources TEST_* env vars if set, copies a git repo to a temporary workspace,
# invokes mct-code with a multi-step editing handoff, and reports pass/fail
# based on file diffs.
#
# Environment variables (all optional):
#   TEST_WORKSPACE    temp directory to work in (default: mktemp)
#   TEST_REPO         path to a git repo to copy as the initial workspace
#   MCT_DEFAULT_MODEL model alias passed to the LLM client (default: default)
#   MCT_CODE_BIN      path to the mct-code binary (default: ../cmd/mct-code)

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../" && pwd)
UNDICI_REPO="$REPO_ROOT/tests/repositories/undici"

export DEEPSEEK_API_KEY="${TEST_API_KEY:-}"
export DEEPSEEK_BASE_URL="${TEST_BASE_URL:-}"
export MCT_DEFAULT_MODEL="deepseek-v4-pro"

# --- resolve paths ----------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MCT_CODE_BIN="${MCT_CODE_BIN:-$PROJECT_DIR/mct-code}"

# --- source optional test config --------------------------------------------
: "${MCT_DEFAULT_MODEL:=default}"
: "${TEST_REPO:=}"

# Point mct-code at the project config so it can find the qwen3-coder-plus model alias and provider credentials.
export MACHTIANI_CONFIG="$REPO_ROOT/.machtiani/config.toml"

# If no TEST_REPO given, try common submodule locations.
if [[ -z "$TEST_REPO" ]]; then
	for candidate in \
		"$UNDICI_REPO" \
		"$PROJECT_DIR/../../file-discovery/tests/undici"; do
		if [[ -d "$candidate" ]]; then
			TEST_REPO="$candidate"
			break
		fi
	done
fi

# --- create workspace -------------------------------------------------------
WORKSPACE="${TEST_WORKSPACE:-$(mktemp -d -p "${SCRIPT_DIR}/_workspace" --suffix=.ws)}"
echo "=== Workspace: $WORKSPACE"

if [[ -n "$TEST_REPO" && -d "$TEST_REPO" ]]; then
	echo "=== Seeding workspace from $TEST_REPO"
	# Use rsync or cp to copy contents (excluding .git)
	if command -v rsync &>/dev/null; then
		rsync -a --exclude=.git "$TEST_REPO"/ "$WORKSPACE"/
	else
		cp -r "$TEST_REPO"/* "$WORKSPACE"/ 2>/dev/null || true
		cp -r "$TEST_REPO"/.[!.]* "$WORKSPACE"/ 2>/dev/null || true
	fi
fi

cd "$WORKSPACE"

# --- multi-step editing handoff note ---------------------------------------
HANDOFF=$(cat <<'EOF'
Please perform the following steps in order:
1. Use FSRead to read the file README.md in the workspace.
2. Use FSPatch to replace the word "Example" with "Edited" in README.md.
3. Use FSRead again to confirm the change was applied.
4. Use FSWrite to append a new line at the end of README.md with the content "// modified by integration test".
5. Use FSRead to confirm the final state.
When done, summarize what you did.
EOF
)

echo "=== Running mct-code with handoff..."
echo "$HANDOFF"

# Build the binary first if it doesn't exist or if source is newer.
if [[ ! -x "$MCT_CODE_BIN" ]]; then
	echo "=== Building mct-code..."
	(cd "$PROJECT_DIR" && go build -o "$MCT_CODE_BIN" ./cmd/mct-code/)
fi

# Run mct-code — capture stdout and stderr separately.
OUTPUT=$("$MCT_CODE_BIN" "$HANDOFF" 2>&1)
EXIT_CODE=$?
echo "=== mct-code output:"
echo "$OUTPUT"
echo "=== Exit code: $EXIT_CODE"

if [[ $EXIT_CODE -ne 0 ]]; then
	echo "=== DEBUG: command that failed was:"
	echo "  $MCT_CODE_BIN <handoff>"
	echo "=== DEBUG: exit code was $EXIT_CODE"
fi

# --- validation -------------------------------------------------------------
PASS=0
FAILS=0

# Check that the agent actually ran (output is non-empty).
if [[ -z "$OUTPUT" ]]; then
	echo "FAIL: agent produced no output"
	FAILS=$((FAILS + 1))
else
	PASS=$((PASS + 1))
fi

# Check that README.md was modified (if it existed originally).
if [[ -f "$WORKSPACE/README.md" ]]; then
	if grep -q "Edited" "$WORKSPACE/README.md" 2>/dev/null; then
		echo "PASS: README.md contains 'Edited' (FSPatch worked)"
		PASS=$((PASS + 1))
	else
		echo "FAIL: README.md does not contain 'Edited'"
		FAILS=$((FAILS + 1))
	fi

	if grep -q "modified by integration test" "$WORKSPACE/README.md" 2>/dev/null; then
		echo "PASS: README.md contains appended line (FSWrite worked)"
		PASS=$((PASS + 1))
	else
		echo "FAIL: README.md does not have the appended line"
		FAILS=$((FAILS + 1))
	fi
else
	echo "SKIP: README.md does not exist in workspace; skipped file-specific checks"
fi

# --- results ----------------------------------------------------------------
echo "=== Results: $PASS passed, $FAILS failed"
if [[ $FAILS -gt 0 ]]; then
	echo "FAIL"
	exit 1
fi
echo "PASS"
