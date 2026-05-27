#!/usr/bin/env bash
# reliability-loop.sh — Reliability loop for mct-code integration tests.
#
# Runs mct-code against a repository in a loop (with parallel workers),
# validates that LICENSE files are updated to 2025, and reports results.
#
# Environment variables (all optional):
#   RELIABILITY_ROUNDS      Number of test rounds per worker (default: 10)
#   RELIABILITY_PARALLEL    Number of parallel worker jobs (default: 3)
#   RELIABILITY_REPO        Path to the repository to test against (default: tests/repositories/undici)
#   RELIABILITY_ARTIFACT_ROOT  Directory for artifacts (default: $SCRIPT_DIR/reliability-artifacts)
#   MCT_CODE_BIN            Path to mct-code binary (empty = build it)
#   MCT_DEFAULT_MODEL       Model alias passed to the LLM client (default: deepseek-v4-pro)
#   RELIABILITY_KEEP_TEMP   If "true", keep temp workspaces after run (default: false)
#   RELIABILITY_PROMPT_TYPE Prompt variant to use (default: license-update)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../../" && pwd)"
AGENT_SRC_DIR="$REPO_ROOT/agent"
# If running inside a Docker container, use /build/agent instead.
if [[ -d "/build/agent" ]]; then
    AGENT_SRC_DIR="/build/agent"
fi

# --- environment variables with defaults ------------------------------------
: "${RELIABILITY_ROUNDS:=10}"
: "${RELIABILITY_PARALLEL:=3}"
: "${RELIABILITY_REPO:=$REPO_ROOT/tests/repositories/undici}"
# If the default does not exist, try the standard container repo location.
if [[ ! -d "$RELIABILITY_REPO" ]] && [[ -d "/workspace" ]]; then
    RELIABILITY_REPO="/workspace"
fi
: "${RELIABILITY_ARTIFACT_ROOT:=$SCRIPT_DIR/reliability-artifacts}"
: "${MCT_CODE_BIN:=}"
: "${MCT_DEFAULT_MODEL:=deepseek-v4-pro}"
: "${RELIABILITY_KEEP_TEMP:=false}"
: "${RELIABILITY_PROMPT_TYPE:=license-update}"
: "${RELIABILITY_PROMPT_TEXT:=}"
: "${RELIABILITY_PROMPT_FILE:=}"
: "${RELIABILITY_VERBOSE:=}"

export DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-}"
export DEEPSEEK_BASE_URL="${DEEPSEEK_BASE_URL:-}"
export MCT_DEFAULT_MODEL
# Preserve MACHTIANI_CONFIG from environment if already set.
# Otherwise try the computed repo-root path, then the standard container path.
if [[ -z "${MACHTIANI_CONFIG:-}" ]]; then
    if [[ -f "$REPO_ROOT/.machtiani/config.toml" ]]; then
        export MACHTIANI_CONFIG="$REPO_ROOT/.machtiani/config.toml"
    elif [[ -f "/workspace/.machtiani/config.toml" ]]; then
        export MACHTIANI_CONFIG="/workspace/.machtiani/config.toml"
    fi
fi

# --- temp directory cleanup -------------------------------------------------
# Clean up /tmp/license-* and /tmp/reliability-* unless RELIABILITY_KEEP_TEMP
# is set to "true" (consistent with the rest of the script).
if [[ "$RELIABILITY_KEEP_TEMP" != "true" ]]; then
    echo "Cleaning up /tmp/license-* and /tmp/reliability-* directories..."
    # Safety check: only run rm -rf if at least one path matches the glob.
    # This prevents surprises if the glob expands unexpectedly.
    if ls /tmp/license-* /tmp/reliability-* &>/dev/null 2>&1; then
        rm -rf /tmp/license-* /tmp/reliability-*
    fi
fi

# --- helpers ----------------------------------------------------------------

# Return the handoff prompt text based on RELIABILITY_PROMPT_TYPE.
prompt_text() {
    if [[ -n "${RELIABILITY_PROMPT_FILE:-}" && -f "${RELIABILITY_PROMPT_FILE}" ]]; then
        cat "${RELIABILITY_PROMPT_FILE}"
        return 0
    fi
    if [[ -n "${RELIABILITY_PROMPT_TEXT:-}" ]]; then
        echo "${RELIABILITY_PROMPT_TEXT}"
        return 0
    fi

    case "$RELIABILITY_PROMPT_TYPE" in
        license-update)
            cat <<'EOF'
Update every LICENSE file copyright year to 2025. Read each LICENSE file first, then use FSPatch to update the year. After editing, read the file again to confirm. Do not modify any other files. When done, summarize every file you changed and what year was replaced.
EOF
            ;;
        rename-variable)
            cat <<'EOF'
Rename all instances of the variable request to req in all .js and .ts files, but skip test files (files in test directories or named *.test.js/*.test.ts) and node_modules. Use FSSearch or Shell to find the files first, then use FSRead to read each file, FSPatch to make the change, and FSRead again to verify. When done, summarize every file changed.
EOF
            ;;
        *)
            echo "Error: unknown RELIABILITY_PROMPT_TYPE: $RELIABILITY_PROMPT_TYPE" >&2
            return 1
            ;;
    esac
}

refined_rename_prompt() {
    cat <<'EOF'
Rename the JavaScript/TypeScript variable `request` to `req` in all `.js` and `.ts` files, but skip test files (files in `test` directories or named `*.test.js`/`*.test.ts`) and `node_modules`. You MUST use FSSearch or Shell ONLY to locate the files. After finding the files, you MUST use FSRead to read each file, then FSPatch to make the change per file, then FSRead again to verify the change. Do NOT use Shell to edit any file. Do NOT use bulk shell commands for replacement. Edit each file individually with FSPatch. When done, summarize every file you changed.
EOF
}
# Copy a source dir into a destination dir, excluding .git.
_copy_dir() {
    local src="$1" dest="$2"
    mkdir -p "$dest"
    if command -v rsync &>/dev/null; then
        rsync -a --exclude=.git "$src"/ "$dest"/
    else
        cp -r "$src"/* "$dest"/ 2>/dev/null || true
        cp -r "$src"/.[!.]* "$dest"/ 2>/dev/null || true
    fi
}

# Copy RELIABILITY_REPO into the given workspace directory.
setup_workspace() {
    local workspace="$1"
    _copy_dir "$RELIABILITY_REPO" "$workspace"
}

# Check that every LICENSE file under workspace contains "2025".
# Excludes node_modules, .git, generated, and artifact directories.
# Returns 0 if all LICENSE files pass, 1 if any fails.
validate_license_update() {
    local workspace="$1"
    local failed=0
    while IFS= read -r -d '' f; do
        # Only check files that contain a four-digit year (19xx or 20xx).
        # Files without any year are skipped.
        if grep -qE "19[0-9]{2}|20[0-9]{2}" "$f"; then
            if ! grep -q "2025" "$f"; then
                echo "FAIL: $f contains a year but not 2025"
                failed=1
            fi
        fi
    done < <(find "$workspace" \
        -name LICENSE \
        -not -path "*/node_modules/*" \
        -not -path "*/.git/*" \
        -not -path "*/generated/*" \
        -not -path "*/artifact*" \
        -print0)
    return $failed
}

# Check that the agent output contains "Changes made:" and does not contain "error:".
# Returns 0 if passes, 1 if fails.
validate_output() {
    local stdout_file="$1"
    local stderr_file="$2"

    if ! grep -q "Changes made:" "$stdout_file" "$stderr_file" 2>/dev/null; then
        echo "FAIL: output does not contain 'Changes made:'"
        return 1
    fi
    if grep -q "error:" "$stdout_file" "$stderr_file" 2>/dev/null; then
        echo "FAIL: output contains 'error:'"
        return 1
    fi
    return 0
}

# Check that no .js or .ts file (excluding test files and node_modules) contains
# the word "request" at a word boundary.
# Returns 0 if passes, 1 if fails.
validate_rename_variable() {
    local workspace="$1"

    if grep -rw "request" \
        --include="*.js" \
        --include="*.ts" \
        --exclude="*.test.js" \
        --exclude="*.test.ts" \
        --exclude-dir=node_modules \
        --exclude-dir=test \
        "$workspace" 2>/dev/null; then
        echo "FAIL: Found remaining 'request' in .js/.ts files"
        return 1
    fi
    return 0
}

# Run a single mct-code test in the given workspace.
# Arguments: workspace_path run_id artifact_dir_path
run_single_test() {
    local workspace="$1"
    local run_id="$2"
    local artifact_dir="$3"

    # If a prebuilt binary exists at the standard container location, use it.
    if [[ -x "/usr/local/bin/mct-code" ]]; then
        MCT_CODE_BIN="/usr/local/bin/mct-code"
        echo "Using prebuilt mct-code binary at $MCT_CODE_BIN"
    else
        # Build the binary if MCT_CODE_BIN is not set or not executable.
        if [[ ! -x "$MCT_CODE_BIN" ]]; then
            MCT_CODE_BIN="${MCT_CODE_BIN:-$AGENT_SRC_DIR/internal/mct-code/mct-code}"
            echo "Building mct-code to $MCT_CODE_BIN..."
            (cd "$AGENT_SRC_DIR" && GOCACHE=.cache go build -o "$MCT_CODE_BIN" ./internal/mct-code/cmd/mct-code/)
        fi
    fi

    # If MCT_CODE_BIN is a relative path, convert to absolute before changing
    # directories later (the script runs mct-code from within the workspace).
    if [[ -n "$MCT_CODE_BIN" ]] && [[ "$MCT_CODE_BIN" != /* ]]; then
        if ! MCT_CODE_BIN="$(readlink -f "$MCT_CODE_BIN")"; then
            echo "Error: could not resolve MCT_CODE_BIN to an absolute path: $MCT_CODE_BIN" >&2
            exit 1
        fi
    fi

    local prompt
    prompt=$(prompt_text)

    local timeout_sec
    timeout_sec=$((RELIABILITY_ROUNDS * 120))
    if [[ $timeout_sec -lt 300 ]]; then
        timeout_sec=1200
    fi

    local flags=()
    if [[ -n "$RELIABILITY_VERBOSE" && "$RELIABILITY_VERBOSE" != "0" ]]; then
        flags+=("--verbose")
    fi

    echo "Running mct-code (timeout: ${timeout_sec}s)..."
    mct_code_pid=$$
    (
        cd "$workspace"
        timeout "$timeout_sec" "$MCT_CODE_BIN" run --text "$prompt" "${flags[@]}"
    ) > "$artifact_dir/stdout.log" 2> "$artifact_dir/stderr.log"
    local exit_code=$?
    echo "Exit code: $exit_code"
    return $exit_code
}

# Run the full reliability loop for one worker.
# Arguments: workspace_root run_prefix pristine_workspace_path
run_reliability_loop() {
    local workspace_root="$1"
    local run_prefix="$2"
    local pristine_workspace="$3"

    local passes=0
    local failures=0

    for ((round=1; round<=RELIABILITY_ROUNDS; round++)); do
        local round_start
        round_start="$(date +%s)"
        local round_date
        round_date="$(date '+%Y-%m-%d %H:%M:%S')"

        local round_artifact_dir="$RELIABILITY_ARTIFACT_ROOT/$run_prefix/round_$round"
        mkdir -p "$round_artifact_dir"

        local round_workspace="${workspace_root}/${run_prefix}/round_${round}"
        _copy_dir "$pristine_workspace" "$round_workspace"

        echo "[$round_date] [$run_prefix] Round $round/$RELIABILITY_ROUNDS starting..."

        set +e
        run_single_test "$round_workspace" "${run_prefix}_round_${round}" "$round_artifact_dir"
        set -e

        local failed=0

        if ! validate_license_update "$round_workspace"; then
            failed=1
        fi

        if ! validate_output "$round_artifact_dir/stdout.log" "$round_artifact_dir/stderr.log"; then
            failed=1
        fi

        if [[ "$RELIABILITY_PROMPT_TYPE" == "rename-variable" ]]; then
            if ! validate_rename_variable "$round_workspace"; then
                failed=1
            fi
        fi

        local round_end
        round_end="$(date +%s)"
        local elapsed=$((round_end - round_start))

        if [[ $failed -eq 0 ]]; then
            echo "PASS: [$run_prefix] Round $round (${elapsed}s)"
            passes=$((passes + 1))
        else
            echo "FAIL: [$run_prefix] Round $round (${elapsed}s)"
            failures=$((failures + 1))
            if [[ "$RELIABILITY_KEEP_TEMP" != "true" ]]; then
                rm -rf "$round_workspace"
            fi
            break
        fi

        if [[ "$RELIABILITY_KEEP_TEMP" != "true" ]]; then
            rm -rf "$round_workspace"
        fi
    done

    echo "$passes" > "$RELIABILITY_ARTIFACT_ROOT/$run_prefix/PASSES"
    echo "$failures" > "$RELIABILITY_ARTIFACT_ROOT/$run_prefix/FAILURES"
    echo "$passes $failures"
}

# --- main -------------------------------------------------------------------

main() {
    # Validate that the source repo exists
    if [[ ! -d "$RELIABILITY_REPO" ]]; then
        echo "Error: RELIABILITY_REPO directory does not exist: $RELIABILITY_REPO" >&2
        exit 1
    fi

    mkdir -p "$RELIABILITY_ARTIFACT_ROOT"

    # Create pristine workspace from the source repo
    local pristine_workspace
    pristine_workspace="$(mktemp -d -p "$RELIABILITY_ARTIFACT_ROOT" --suffix=.pristine)"
    echo "Creating pristine workspace at $pristine_workspace"
    setup_workspace "$pristine_workspace"

    # Launch parallel worker jobs
    local worker_pids=()
    local workspace_root="$RELIABILITY_ARTIFACT_ROOT/workspaces"
    mkdir -p "$workspace_root"

    for ((i=1; i<=RELIABILITY_PARALLEL; i++)); do
        run_reliability_loop "$workspace_root" "worker_$i" "$pristine_workspace" &
        worker_pids+=($!)
    done

    # Wait for all workers to finish
    for pid in "${worker_pids[@]}"; do
        wait "$pid"
    done

    # Print summary
    echo ""
    echo "=== RELIABILITY TEST SUMMARY ==="
    local total_passes=0
    local total_failures=0
    for ((i=1; i<=RELIABILITY_PARALLEL; i++)); do
        local p=0 f=0
        if [[ -f "$RELIABILITY_ARTIFACT_ROOT/worker_$i/PASSES" ]]; then
            p=$(cat "$RELIABILITY_ARTIFACT_ROOT/worker_$i/PASSES")
        fi
        if [[ -f "$RELIABILITY_ARTIFACT_ROOT/worker_$i/FAILURES" ]]; then
            f=$(cat "$RELIABILITY_ARTIFACT_ROOT/worker_$i/FAILURES")
        fi
        echo "  worker_$i: ${p} passes, ${f} failures"
        total_passes=$((total_passes + p))
        total_failures=$((total_failures + f))
    done
    echo "  Total: ${total_passes} passes, ${total_failures} failures"

    # Clean up pristine and workspace dirs unless KEEP_TEMP is set
    if [[ "$RELIABILITY_KEEP_TEMP" != "true" ]]; then
        rm -rf "$pristine_workspace"
        rm -rf "$workspace_root"
    fi
}

main

# --- post-run session artifact preservation ---
# After mct-code finishes, find the session directory and copy inputs.jsonl / conversation.json
_preserve_session_artifacts() {
    local artifact_dir="$1"
    local mct_code_pid="$2"
    # Wait for any delayed writes.
    sleep 2
    # Look for the most recently modified inputs.jsonl in the whole filesystem.
    local session_dir
    session_dir=$(find /tmp -name 'inputs.jsonl' -printf '%T@ %p\n' 2>/dev/null | sort -n | tail -1 | awk '{print $2}' | xargs dirname 2>/dev/null)
    if [[ -z "$session_dir" || ! -d "$session_dir" ]]; then
        # Fallback: search under user home and /workspace.
        session_dir=$(find /home /workspace -name 'inputs.jsonl' -printf '%T@ %p\n' 2>/dev/null | sort -n | tail -1 | awk '{print $2}' | xargs dirname 2>/dev/null)
    fi
    if [[ -n "$session_dir" && -d "$session_dir" ]]; then
        mkdir -p "$artifact_dir/session"
        cp "$session_dir/inputs.jsonl" "$artifact_dir/session/" 2>/dev/null || true
        cp "$session_dir/conversation.json" "$artifact_dir/session/" 2>/dev/null || true
        echo "Session artifacts copied from $session_dir to $artifact_dir/session"
    else
        echo "Warning: could not locate session directory for artifacts" >&2
    fi
}
