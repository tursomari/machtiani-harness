#!/usr/bin/env bash
set -euo pipefail

# Cleanup worktrees on script exit
cleanup() {
    if [ "${KEEP:-false}" = true ]; then
        return
    fi
    if [ -n "${MCT_WORKTREE:-}" ]; then
        rm -rf "$MCT_WORKTREE" 2>/dev/null
    fi
    if [ -n "${FORGE_WORKTREE:-}" ]; then
        rm -rf "$FORGE_WORKTREE" 2>/dev/null
    fi
    if [ -n "${JUDGE_WORKTREE:-}" ]; then
        rm -rf "$JUDGE_WORKTREE" 2>/dev/null
    fi
    if [ -n "${REPO:-}" ]; then
        git -C "$REPO" worktree prune 2>/dev/null || true
    fi
}
trap cleanup EXIT

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ============================================================================
# run_eval.sh — Two-phase evaluation pipeline comparing mct-agent vs Forge
# ============================================================================

usage() {
    cat <<'EOF'
Usage: run_eval.sh --repo <path> --prompt <path> --eval-commit <oid> --ground-truth <oid>
                   [--model <alias>]
                   [--api-key-file <provider:path>]
                   [--api-key <provider:key>]
                   [--config <path>]
                   [--output-dir <path>]
                   [--keep]

Automated evaluation pipeline that compares mct-agent (code mode) against
Forge (muse agent) on a resolved issue in a git repository.

Required:
  --repo <path>            Path to the git repository
  --prompt <path>          Path to the task prompt file
  --eval-commit <oid>      Pre-fix commit OID to roll back to
  --ground-truth <oid>     Fix commit OID (the ground truth)

Optional:
  --model <alias>          mct-agent model alias (default: glm-5-high-deepinfra)
  --sync-model <alias>     model alias for the sync step (default: glm-5-high-deepinfra)
  --api-key-file <provider:path>
                           API key file in provider:path format
  --api-key <provider:key>
                           API key in provider:key format
  --sync-api-key <provider:key>
                           API key for the sync step (defaults to --api-key if not set)
  --config <path>          Path to machtiani config.toml (auto-discovered if not set)
  --output-dir <path>      Directory for all artifacts (default: /tmp/eval_<timestamp>)
  --keep                   Keep worktree and artifacts after completion (for debugging)
  --sequential             Run agents sequentially instead of in parallel (default: parallel)
  --help                   Print this message and exit
EOF
    exit 1
}

# ----------------------------------------------------------------------------
# Defaults
# ----------------------------------------------------------------------------
REPO=""
PROMPT=""
EVAL_COMMIT=""
GROUND_TRUTH=""
SYNC_MODEL="glm-5-high-deepinfra"
MODEL="glm-5-high-deepinfra"
API_KEY_FILE=""
API_KEY=""
SYNC_API_KEY=""
CONFIG=""
OUTPUT_DIR=""
KEEP=false
SEQUENTIAL=0

# ----------------------------------------------------------------------------
# Parse arguments
# ----------------------------------------------------------------------------
while [ $# -gt 0 ]; do
    case "$1" in
        --repo)
            REPO="$2"
            shift 2
            ;;
        --prompt)
            PROMPT="$2"
            shift 2
            ;;
        --eval-commit)
            EVAL_COMMIT="$2"
            shift 2
            ;;
        --ground-truth)
            GROUND_TRUTH="$2"
            shift 2
            ;;
        --model)
            MODEL="$2"
            shift 2
            ;;
        --sync-model)
            SYNC_MODEL="$2"
            shift 2
            ;;
        --api-key-file)
            API_KEY_FILE="$2"
            shift 2
            ;;
        --api-key)
            API_KEY="$2"
            shift 2
            ;;
        --sync-api-key)
            SYNC_API_KEY="$2"
            shift 2
            ;;
        --config)
            CONFIG="$2"
            shift 2
            ;;
        --output-dir)
            OUTPUT_DIR="$2"
            shift 2
            ;;

        --keep)
            KEEP=true
            shift
            ;;
        --sequential)
            SEQUENTIAL=1
            shift
            ;;
        --help)
            usage
            ;;
        *)
            echo "Unknown option: $1" >&2
            usage
            ;;
    esac
done

# ----------------------------------------------------------------------------
# Helper: derive model provider from config.toml
# ----------------------------------------------------------------------------
derive_model_provider() {
    local model="$1"
    local config="$2"

    if [ -z "$config" ] || [ ! -f "$config" ]; then
        echo ""
        return
    fi

    awk -v target="$model" '
        /^\[models\./ {
            in_section = 0
            section_name = $0
            gsub(/^\[models\./, "", section_name)
            gsub(/\]$/, "", section_name)
            if (section_name == target) {
                in_section = 1
            }
            next
        }
        in_section && /^provider/ {
            val = $0
            sub(/^provider[[:space:]]*=[[:space:]]*/, "", val)
            gsub(/"/, "", val)
            gsub(/^[[:space:]]+|[[:space:]]+$/, "", val)
            print val
            exit
        }
    ' "$config"
}

# ----------------------------------------------------------------------------
# Validate required arguments
# ----------------------------------------------------------------------------
if [ -z "$REPO" ]; then
    echo "Error: --repo is required" >&2
    usage
fi
if [ -z "$PROMPT" ]; then
    echo "Error: --prompt is required" >&2
    usage
fi
if [ -z "$EVAL_COMMIT" ]; then
    echo "Error: --eval-commit is required" >&2
    usage
fi
if [ -z "$GROUND_TRUTH" ]; then
    echo "Error: --ground-truth is required" >&2
    usage
fi

if [ ! -d "$REPO" ]; then
    echo "Error: --repo is not a directory: $REPO" >&2
    exit 1
fi
if [ ! -f "$PROMPT" ]; then
    echo "Error: --prompt file not found: $PROMPT" >&2
    exit 1
fi

# ----------------------------------------------------------------------------
# Resolve paths
# ----------------------------------------------------------------------------
REPO="$(realpath "$REPO")"
PROMPT="$(realpath "$PROMPT")"

# ----------------------------------------------------------------------------
# Parallel execution helper
# ----------------------------------------------------------------------------
# run_parallel - Run two commands concurrently with 5-second stagger
# Usage: run_parallel "cmd1" "logfile1" "cmd2" "logfile2"
# Returns the higher exit code of the two commands
run_parallel() {
    local cmd1="$1"
    local log1="$2"
    local cmd2="$3"
    local log2="$4"
    local pid1="" pid2=""
    local rc1=0 rc2=0

    set +e

    echo "[parallel] Starting command1 (log: $log1)"
    eval "$cmd1" > "$log1" 2>&1 &
    pid1=$!
    echo "[parallel] Command1 PID: $pid1"

    echo "[parallel] Sleeping 5 seconds before starting command2..."
    sleep 5

    echo "[parallel] Starting command2 (log: $log2)"
    eval "$cmd2" > "$log2" 2>&1 &
    pid2=$!
    echo "[parallel] Command2 PID: $pid2"

    echo "[parallel] Waiting for command1 (PID: $pid1)..."
    wait $pid1
    rc1=$?
    echo "[parallel] Command1 finished with exit code: $rc1"

    echo "[parallel] Waiting for command2 (PID: $pid2)..."
    wait $pid2
    rc2=$?
    echo "[parallel] Command2 finished with exit code: $rc2"

    set -e

    if [ "$rc1" -ge "$rc2" ]; then
        return $rc1
    else
        return $rc2
    fi
}

# ============================================================================
# Step 0 - Setup
# ============================================================================
echo ""
echo "============================================================"
echo "  Step 0 - Setup"
echo "============================================================"

# Resolve short OIDs to full SHAs
echo "[setup] Resolving commit OIDs..."
EVAL_COMMIT_FULL="$(git -C "$REPO" rev-parse "$EVAL_COMMIT")"
GROUND_TRUTH_FULL="$(git -C "$REPO" rev-parse "$GROUND_TRUTH")"
echo "[setup] eval-commit:   $EVAL_COMMIT -> $EVAL_COMMIT_FULL"
echo "[setup] ground-truth:  $GROUND_TRUTH -> $GROUND_TRUTH_FULL"

# Create output directory
if [ -z "$OUTPUT_DIR" ]; then
    TIMESTAMP="$(date +%s)"
    OUTPUT_DIR="/tmp/eval_${TIMESTAMP}"
fi
mkdir -p "$OUTPUT_DIR"
echo "[setup] Output directory: $OUTPUT_DIR"

# Create three separate git worktrees
MCT_WORKTREE="$OUTPUT_DIR/worktree_mct"
FORGE_WORKTREE="$OUTPUT_DIR/worktree_forge"
JUDGE_WORKTREE="$OUTPUT_DIR/worktree_judge"

echo "[setup] Creating worktree for mct-agent at $MCT_WORKTREE (commit $EVAL_COMMIT_FULL)..."
rm -rf "$MCT_WORKTREE"
git -C "$REPO" worktree add "$MCT_WORKTREE" "$EVAL_COMMIT_FULL"

echo "[setup] Creating worktree for forge at $FORGE_WORKTREE (commit $EVAL_COMMIT_FULL)..."
rm -rf "$FORGE_WORKTREE"
git -C "$REPO" worktree add "$FORGE_WORKTREE" "$EVAL_COMMIT_FULL"

echo "[setup] Creating worktree for judge at $JUDGE_WORKTREE (commit $GROUND_TRUTH_FULL)..."
rm -rf "$JUDGE_WORKTREE"
git -C "$REPO" worktree add "$JUDGE_WORKTREE" "$GROUND_TRUTH_FULL"

echo "[setup] All three worktrees created successfully."

# Derive names
PROJECT_NAME="$(basename "$REPO")"
ARTIFACT_SUFFIX="$(git -C "$REPO" rev-parse --short "$EVAL_COMMIT")"
echo "[setup] Project:        $PROJECT_NAME"
echo "[setup] Artifact suffix: $ARTIFACT_SUFFIX"

# Auto-discover config
if [ -z "$CONFIG" ]; then
    SEARCH_DIR="$REPO"
    while [ "$SEARCH_DIR" != "/" ] && [ "$SEARCH_DIR" != "." ]; do
        if [ -f "$SEARCH_DIR/.machtiani/config.toml" ]; then
            CONFIG="$SEARCH_DIR/.machtiani/config.toml"
            break
        fi
        SEARCH_DIR="$(dirname "$SEARCH_DIR")"
    done
    if [ -z "$CONFIG" ]; then
        CONFIG="$HOME/.machtiani/config.toml"
    fi
fi
echo "[setup] Config: $CONFIG"

# Resolve API key
MCT_API_KEY_ARG=""
if [ -n "$API_KEY" ]; then
    # --api-key takes precedence
    MCT_API_KEY_ARG="--api-key $API_KEY"
    if [[ "$API_KEY" != *:* ]]; then
        MODEL_PROVIDER=$(derive_model_provider "$MODEL" "$CONFIG")
        if [ -n "$MODEL_PROVIDER" ]; then
            API_KEY="${MODEL_PROVIDER}:${API_KEY}"
            MCT_API_KEY_ARG="--api-key $API_KEY"
        else
            echo "Error: API_KEY does not contain a provider prefix and could not be derived from config" >&2
            exit 1
        fi
    fi
    echo "[setup] API key: from --api-key flag"
elif [ -n "$API_KEY_FILE" ]; then
    API_KEY_PROVIDER="${API_KEY_FILE%%:*}"
    API_KEY_FILEPATH="${API_KEY_FILE#*:}"
    # Expand ~ if present
    API_KEY_FILEPATH="${API_KEY_FILEPATH/#\~/$HOME}"
    if [ -d "$API_KEY_FILEPATH" ]; then
        KEY_DIR="$API_KEY_FILEPATH"
        API_KEY_FILEPATH=""
        for candidate in "work-api-key.txt" "api-key.txt"; do
            if [ -f "$KEY_DIR/$candidate" ]; then
                API_KEY_FILEPATH="$KEY_DIR/$candidate"
                break
            fi
        done
        if [ -z "$API_KEY_FILEPATH" ]; then
            # fall back to first *.txt file
            for f in "$KEY_DIR"/*.txt; do
                if [ -f "$f" ]; then
                    API_KEY_FILEPATH="$f"
                    break
                fi
            done
        fi
        if [ -z "$API_KEY_FILEPATH" ]; then
            echo "Error: no key file found in directory: $KEY_DIR (looked for work-api-key.txt, api-key.txt, *.txt)" >&2
            exit 1
        fi
    elif [ ! -f "$API_KEY_FILEPATH" ]; then
        echo "Error: API key file not found: $API_KEY_FILEPATH" >&2
        exit 1
    fi
    API_KEY_VALUE="$(tr -d '\n\r' < "$API_KEY_FILEPATH")"
    if [ -z "$API_KEY_VALUE" ]; then
        echo "Error: API key file is empty: $API_KEY_FILEPATH" >&2
        exit 1
    fi
    MCT_API_KEY_ARG="--api-key ${API_KEY_PROVIDER}:${API_KEY_VALUE}"
    echo "[setup] API key: from --api-key-file ($API_KEY_FILEPATH)"
else
    echo "[setup] API key: using config file or environment"
fi

# Resolve sync API key
SYNC_API_KEY_ARG=""
if [ -n "$SYNC_API_KEY" ]; then
    if [[ "$SYNC_API_KEY" != *:* ]]; then
        SYNC_PROVIDER=$(derive_model_provider "$SYNC_MODEL" "$CONFIG")
        if [ -n "$SYNC_PROVIDER" ]; then
            SYNC_API_KEY="${SYNC_PROVIDER}:${SYNC_API_KEY}"
        else
            echo "Error: SYNC_API_KEY does not contain a provider prefix and could not be derived from config" >&2
            exit 1
        fi
    fi
    SYNC_API_KEY_ARG="--api-key $SYNC_API_KEY"
    echo "[setup] Sync API key: from --sync-api-key flag"
elif [ -n "$MCT_API_KEY_ARG" ]; then
    SYNC_API_KEY_ARG="$MCT_API_KEY_ARG"
    echo "[setup] Sync API key: using main API key"
else
    echo "[setup] Sync API key: using config file or environment"
fi

# Derive environment variables for forge from resolved API key
FORGE_ENV="TERM=dumb"
if [ -n "$MCT_API_KEY_ARG" ]; then
    API_KEY_PROVIDER="$(echo "$MCT_API_KEY_ARG" | sed "s/--api-key //" | cut -d: -f1)"
    API_KEY_VALUE="$(echo "$MCT_API_KEY_ARG" | sed "s/--api-key //" | cut -d: -f2-)"
    case "$API_KEY_PROVIDER" in
        deepseek)   FORGE_ENV="TERM=dumb DEEPSEEK_API_KEY=$API_KEY_VALUE" ;;
        openrouter) FORGE_ENV="TERM=dumb OPENROUTER_API_KEY=$API_KEY_VALUE" ;;
        openai)     FORGE_ENV="TERM=dumb OPENAI_API_KEY=$API_KEY_VALUE" ;;
        anthropic)  FORGE_ENV="TERM=dumb ANTHROPIC_API_KEY=$API_KEY_VALUE" ;;
        deepinfra)  FORGE_ENV="TERM=dumb DEEPINFRA_API_KEY=$API_KEY_VALUE" ;;
        *)          FORGE_ENV="TERM=dumb" ;;
    esac
fi

# Sync cache setup
SYNC_CACHE_DIR="$HOME/.cache/mct-eval-sync/$PROJECT_NAME/$EVAL_COMMIT/readme"

# Sync cache: always run mct-agent sync (fast when tag matches, slow on first run)

echo ""
echo "[setup] Running mct-agent sync in worktree..."
cd "$MCT_WORKTREE"
MACHTIANI_CONFIG="$CONFIG" mct-agent sync \
    --model "$SYNC_MODEL" \
    $SYNC_API_KEY_ARG \
    --timeout-per-turn 0 \
    --max-steps 20 || {
    echo "ERROR: mct-agent sync failed" >&2
    exit 1
}
echo "[setup] mct-agent sync completed successfully."

# Cache the sync result for future runs
if [ ! -d "$SYNC_CACHE_DIR" ] && [ -d "$MCT_WORKTREE/.machtiani/artifacts/readme" ]; then
    mkdir -p "$SYNC_CACHE_DIR"
    cp -r "$MCT_WORKTREE/.machtiani/artifacts/readme/"* "$SYNC_CACHE_DIR/"
    echo "[setup] Sync result cached at: $SYNC_CACHE_DIR"
fi

# Copy cached readme to forge worktree (must happen before parallel agents start)
# Forge worktree: sync will be handled by forge agent separately

# ============================================================================
# Phase 1 - Plan
# ============================================================================
echo ""
echo "============================================================"
echo "  Phase 1 - Plan"
echo "============================================================"

# Step 1: Create plan-only prompt
PLAN_PROMPT="$OUTPUT_DIR/plan_prompt.md"
echo "[plan] Creating plan-only prompt at $PLAN_PROMPT..."
{
    echo "IMPORTANT: Do not make any code changes. Produce only a detailed implementation plan. Do not modify any files."
    echo ""
    cat "$PROMPT"
} > "$PLAN_PROMPT"
echo "[plan] Plan prompt written ($(wc -c < "$PLAN_PROMPT") bytes)"

# Generate eval ID for forge conversation (must happen before agents run)
EVAL_ID="$(uuidgen 2>/dev/null || python3 -c "import uuid; print(uuid.uuid4())" 2>/dev/null || cat /proc/sys/kernel/random/uuid 2>/dev/null || printf "%08x-%04x-%04x-%04x-%012x" $(date +%s) 0 0 0 0)"
echo "[forge] Eval ID: $EVAL_ID"

if [ "$SEQUENTIAL" -eq 1 ]; then
    # --- Sequential mode: run mct-agent first, then forge ---
    echo "[plan] Running agents sequentially..."

    # Run mct-agent with plan-only prompt
    cd "$MCT_WORKTREE"
    MCT_PLAN_SUCCESS=false
    for ATTEMPT in 1 2 3; do
        echo "[mct-agent] Plan attempt $ATTEMPT/3: Running mct-agent in plan mode..."
        if MACHTIANI_CONFIG="$CONFIG" mct-agent run \
            --mode code-forge \
            --final-file "$OUTPUT_DIR/mct_plan.md" \
            --model "$MODEL" \
            $MCT_API_KEY_ARG \
            --timeout-per-turn 0 \
            --max-steps 20 \
            --file "$PLAN_PROMPT"; then
            MCT_PLAN_SUCCESS=true
            break
        fi
        echo "[mct-agent] Plan attempt $ATTEMPT failed. Waiting 30 seconds before retry..." >&2
        sleep 30
    done
    if ! $MCT_PLAN_SUCCESS; then
        echo "[mct-agent] ERROR: mct-agent plan phase failed after 3 attempts" >&2
        echo "mct-agent plan phase failed after 3 retry attempts" > "$OUTPUT_DIR/mct_plan.md"
    fi

    if [ -s "$OUTPUT_DIR/mct_plan.md" ]; then
        echo "[mct-agent] Plan: $OUTPUT_DIR/mct_plan.md ($(wc -c < "$OUTPUT_DIR/mct_plan.md") bytes, $(wc -l < "$OUTPUT_DIR/mct_plan.md") lines)"
    else
        echo "[mct-agent] WARNING: No plan produced (file empty or missing)" >&2
    fi

    # Capture mct-agent session ID for later continuation
    MCT_SESSION_ID=$(ls -t "$MCT_WORKTREE/.machtiani/sessions/" 2>/dev/null | head -1)
    if [ -n "$MCT_SESSION_ID" ]; then
        echo "[mct-agent] Captured session ID: $MCT_SESSION_ID"
    else
        echo "[mct-agent] WARNING: Could not capture session ID; implementation will use fresh session" >&2
    fi

    # Run forge muse with plan-only prompt
    cd "$OUTPUT_DIR"
    echo "[forge] Phase 1: Planning with muse..."
    MUSE_PLAN_RC=0
    env $FORGE_ENV forge --agent muse -C "$FORGE_WORKTREE" --conversation-id "$EVAL_ID" < "$PLAN_PROMPT" || MUSE_PLAN_RC=$?

    if [ "$MUSE_PLAN_RC" -ne 0 ]; then
        echo "[forge] WARNING: muse planning exited with code $MUSE_PLAN_RC" >&2
    else
        echo "[forge] Muse planning completed successfully."
    fi
else
    # --- Parallel mode: run both agents concurrently ---
    echo "[plan] Running mct-agent and forge in parallel..."
    echo "[plan] mct-agent log: $OUTPUT_DIR/mct_plan.log"
    echo "[plan] forge log: $OUTPUT_DIR/forge_plan.log"

    # Define runner for mct-agent plan with retry logic
    _mct_plan_runner() {
        cd "$MCT_WORKTREE"
        for ATTEMPT in 1 2 3; do
            echo "[mct-agent] Plan attempt $ATTEMPT/3: Running mct-agent in plan mode..."
            if MACHTIANI_CONFIG="$CONFIG" mct-agent run \
                --mode code-forge \
                --final-file "$OUTPUT_DIR/mct_plan.md" \
                --model "$MODEL" \
                $MCT_API_KEY_ARG \
                --timeout-per-turn 0 \
                --max-steps 20 \
                --file "$PLAN_PROMPT"; then
                return 0
            fi
            echo "[mct-agent] Plan attempt $ATTEMPT failed. Waiting 30 seconds before retry..." >&2
            sleep 30
        done
        echo "[mct-agent] ERROR: mct-agent plan phase failed after 3 attempts" >&2
        echo "mct-agent plan phase failed after 3 retry attempts" > "$OUTPUT_DIR/mct_plan.md"
        return 1
    }

    PARALLEL_RC=0
    run_parallel \
        "_mct_plan_runner" \
        "$OUTPUT_DIR/mct_plan.log" \
        "cd \"$OUTPUT_DIR\" && env $FORGE_ENV forge --agent muse -C \"$FORGE_WORKTREE\" --conversation-id \"$EVAL_ID\" < \"$PLAN_PROMPT\"" \
        "$OUTPUT_DIR/forge_plan.log" || PARALLEL_RC=$?

    if [ "$PARALLEL_RC" -ne 0 ]; then
        echo "[plan] WARNING: one or both agents exited with non-zero status" >&2
    fi

    # Capture mct-agent session ID for later continuation
    MCT_SESSION_ID=$(ls -t "$MCT_WORKTREE/.machtiani/sessions/" 2>/dev/null | head -1)
    if [ -n "$MCT_SESSION_ID" ]; then
        echo "[mct-agent] Captured session ID: $MCT_SESSION_ID"
    else
        echo "[mct-agent] WARNING: Could not capture session ID; implementation will use fresh session" >&2
    fi

    if [ -s "$OUTPUT_DIR/mct_plan.md" ]; then
        echo "[mct-agent] Plan: $OUTPUT_DIR/mct_plan.md ($(wc -c < "$OUTPUT_DIR/mct_plan.md") bytes, $(wc -l < "$OUTPUT_DIR/mct_plan.md") lines)"
    else
        echo "[mct-agent] WARNING: No plan produced (file empty or missing)" >&2
    fi
fi

# Extract forge plan
echo "[forge] Extracting forge plan..."
(cd "$OUTPUT_DIR" && forge conversation dump "$EVAL_ID" 2>/dev/null)
DUMP_FILE="$(ls -t "$OUTPUT_DIR"/*-dump.json 2>/dev/null | head -1)"
if [ -n "$DUMP_FILE" ] && [ -f "$DUMP_FILE" ]; then
    jq -r '[.conversation.context.messages[] | select(.text.role == "Assistant") | .text.content | select(length > 0)] | last' \
        "$DUMP_FILE" > "$OUTPUT_DIR/forge_plan.md"
    rm -f "$DUMP_FILE"
    echo "[forge] Plan extracted to: $OUTPUT_DIR/forge_plan.md"
else
    echo "[forge] WARNING: forge conversation dump did not produce a dump file for $EVAL_ID" >&2
    echo "Forge did not produce a plan (conversation dump failed)." > "$OUTPUT_DIR/forge_plan.md"
fi

if [ -s "$OUTPUT_DIR/forge_plan.md" ]; then
    echo "[forge] Plan: $OUTPUT_DIR/forge_plan.md ($(wc -c < "$OUTPUT_DIR/forge_plan.md") bytes, $(wc -l < "$OUTPUT_DIR/forge_plan.md") lines)"
else
    echo "[forge] WARNING: No forge plan produced (file empty or missing)" >&2
fi

# Step 5: Verify worktrees are clean after plan phase
echo "[plan] Verifying worktrees are clean after plan phase..."
MCT_CLEAN=true
FORGE_CLEAN=true
if ! git -C "$MCT_WORKTREE" diff --exit-code > /dev/null 2>&1; then
    echo "[plan] WARNING: mct worktree is dirty after plan phase!" >&2
    MCT_CLEAN=false
fi
if ! git -C "$FORGE_WORKTREE" diff --exit-code > /dev/null 2>&1; then
    echo "[plan] WARNING: forge worktree is dirty after plan phase!" >&2
    FORGE_CLEAN=false
fi
if $MCT_CLEAN && $FORGE_CLEAN; then
    echo "[plan] Both worktrees are clean."
fi
echo "[plan] Plan phase complete."

# ============================================================================
# Phase 2 - Implement
# ============================================================================
echo ""
echo "============================================================"
echo "  Phase 2 - Implement"
echo "============================================================"

# Step 6: Create implementation prompt
IMPL_PROMPT="$OUTPUT_DIR/impl_prompt.md"
echo "[impl] Creating implementation prompt at $IMPL_PROMPT..."
echo "Implement the plan from the previous step. Make all necessary code changes to resolve the issue." > "$IMPL_PROMPT"
echo "[impl] Implementation prompt written ($(wc -c < "$IMPL_PROMPT") bytes)"

if [ "$SEQUENTIAL" -eq 1 ]; then
    # --- Sequential mode: run mct-agent first, then forge ---
    echo "[impl] Running agents sequentially..."

    # Run mct-agent with implementation prompt
    cd "$MCT_WORKTREE"
    MCT_IMPL_SUCCESS=false
    MCT_SESSION_ID_ARG=""
    if [ -n "$MCT_SESSION_ID" ]; then
        MCT_SESSION_ID_ARG="--session-id $MCT_SESSION_ID"
        echo "[mct-agent] Continuing session $MCT_SESSION_ID for implementation..."
    else
        echo "[mct-agent] Starting fresh session for implementation (no session ID captured)..."
    fi

    for ATTEMPT in 1 2 3; do
        echo "[mct-agent] Impl attempt $ATTEMPT/3: Running mct-agent..."
        if MACHTIANI_CONFIG="$CONFIG" mct-agent run \
            --mode code-forge \
            --final-file "$OUTPUT_DIR/mct_answer.md" \
            --model "$MODEL" \
            $MCT_API_KEY_ARG \
            $MCT_SESSION_ID_ARG \
            --timeout-per-turn 0 \
            --max-steps 20 \
            --file "$IMPL_PROMPT"; then
            MCT_IMPL_SUCCESS=true
            break
        fi
        echo "[mct-agent] Impl attempt $ATTEMPT failed. Waiting 30 seconds before retry..." >&2
        sleep 30
    done
    if ! $MCT_IMPL_SUCCESS; then
        echo "[mct-agent] ERROR: mct-agent implementation phase failed after 3 attempts" >&2
        echo "mct-agent implementation phase failed after 3 retry attempts" > "$OUTPUT_DIR/mct_answer.md"
    fi

    if [ -s "$OUTPUT_DIR/mct_answer.md" ]; then
        echo "[mct-agent] Implementation: $OUTPUT_DIR/mct_answer.md ($(wc -c < "$OUTPUT_DIR/mct_answer.md") bytes, $(wc -l < "$OUTPUT_DIR/mct_answer.md") lines)"
    else
        echo "[mct-agent] WARNING: No implementation produced (file empty or missing)" >&2
    fi

    # Run forge with --agent forge --conversation-id for implementation
    echo "[forge] Phase 2: Implementing with forge..."
    cd "$OUTPUT_DIR"
    FORGE_RC=0
    env $FORGE_ENV forge --agent forge --conversation-id "$EVAL_ID" -C "$FORGE_WORKTREE" < "$IMPL_PROMPT" || FORGE_RC=$?

    if [ "$FORGE_RC" -ne 0 ]; then
        echo "[forge] WARNING: forge exited with code $FORGE_RC" >&2
    else
        echo "[forge] Completed successfully."
    fi
else
    # --- Parallel mode: run both agents concurrently ---
    echo "[impl] Running mct-agent and forge in parallel..."
    echo "[impl] mct-agent log: $OUTPUT_DIR/mct_implement.log"
    echo "[impl] forge log: $OUTPUT_DIR/forge_implement.log"

    # Define runner for mct-agent implementation with retry logic
    _mct_impl_runner() {
        cd "$MCT_WORKTREE"
        local session_arg=""
        if [ -n "$MCT_SESSION_ID" ]; then
            session_arg="--session-id $MCT_SESSION_ID"
            echo "[mct-agent] Continuing session $MCT_SESSION_ID for implementation..."
        else
            echo "[mct-agent] Starting fresh session for implementation (no session ID captured)..."
        fi
        for ATTEMPT in 1 2 3; do
            echo "[mct-agent] Impl attempt $ATTEMPT/3: Running mct-agent..."
            if MACHTIANI_CONFIG="$CONFIG" mct-agent run \
                --mode code-forge \
                --final-file "$OUTPUT_DIR/mct_answer.md" \
                --model "$MODEL" \
                $MCT_API_KEY_ARG \
                $session_arg \
                --timeout-per-turn 0 \
                --max-steps 20 \
                --file "$IMPL_PROMPT"; then
                return 0
            fi
            echo "[mct-agent] Impl attempt $ATTEMPT failed. Waiting 30 seconds before retry..." >&2
            sleep 30
        done
        echo "[mct-agent] ERROR: mct-agent implementation phase failed after 3 attempts" >&2
        echo "mct-agent implementation phase failed after 3 retry attempts" > "$OUTPUT_DIR/mct_answer.md"
        return 1
    }

    PARALLEL_RC=0
    run_parallel \
        "_mct_impl_runner" \
        "$OUTPUT_DIR/mct_implement.log" \
        "cd \"$OUTPUT_DIR\" && env $FORGE_ENV forge --agent forge --conversation-id \"$EVAL_ID\" -C \"$FORGE_WORKTREE\" < \"$IMPL_PROMPT\"" \
        "$OUTPUT_DIR/forge_implement.log" || PARALLEL_RC=$?

    if [ "$PARALLEL_RC" -ne 0 ]; then
        echo "[impl] WARNING: one or both agents exited with non-zero status" >&2
    fi

    if [ -s "$OUTPUT_DIR/mct_answer.md" ]; then
        echo "[mct-agent] Implementation: $OUTPUT_DIR/mct_answer.md ($(wc -c < "$OUTPUT_DIR/mct_answer.md") bytes, $(wc -l < "$OUTPUT_DIR/mct_answer.md") lines)"
    else
        echo "[mct-agent] WARNING: No implementation produced (file empty or missing)" >&2
    fi
fi

# Step 9: Capture git diffs from each worktree
echo "[impl] Capturing git diffs..."
git -C "$MCT_WORKTREE" diff > "$OUTPUT_DIR/mct_changes.patch"
echo "[impl] MCT changes: $OUTPUT_DIR/mct_changes.patch ($(wc -c < "$OUTPUT_DIR/mct_changes.patch") bytes, $(wc -l < "$OUTPUT_DIR/mct_changes.patch") lines)"

git -C "$FORGE_WORKTREE" diff > "$OUTPUT_DIR/forge_changes.patch"
echo "[impl] Forge changes: $OUTPUT_DIR/forge_changes.patch ($(wc -c < "$OUTPUT_DIR/forge_changes.patch") bytes, $(wc -l < "$OUTPUT_DIR/forge_changes.patch") lines)"

# Step 10: Extract forge answer from conversation dump
echo "[forge] Extracting forge implementation answer..."
(cd "$OUTPUT_DIR" && forge conversation dump "$EVAL_ID" 2>/dev/null)
DUMP_FILE="$(ls -t "$OUTPUT_DIR"/*-dump.json 2>/dev/null | head -1)"
if [ -n "$DUMP_FILE" ] && [ -f "$DUMP_FILE" ]; then
    jq -r '[.conversation.context.messages[] | select(.text.role == "Assistant") | .text.content | select(length > 0)] | last' \
        "$DUMP_FILE" > "$OUTPUT_DIR/forge_answer.md"
    rm -f "$DUMP_FILE"
    echo "[forge] Answer extracted to: $OUTPUT_DIR/forge_answer.md"
else
    echo "[forge] WARNING: forge conversation dump did not produce a dump file for $EVAL_ID" >&2
    echo "Forge did not produce output (conversation dump failed)." > "$OUTPUT_DIR/forge_answer.md"
fi

if [ -s "$OUTPUT_DIR/forge_answer.md" ]; then
    echo "[forge] Answer: $OUTPUT_DIR/forge_answer.md ($(wc -c < "$OUTPUT_DIR/forge_answer.md") bytes, $(wc -l < "$OUTPUT_DIR/forge_answer.md") lines)"
else
    echo "[forge] WARNING: No forge answer produced (file empty or missing)" >&2
fi

# ============================================================================
# Step 3 - Capture ground truth
# ============================================================================
echo ""
echo "============================================================"
echo "  Step 3 - Capture ground truth diff"
echo "============================================================"

git -C "$REPO" diff "$EVAL_COMMIT_FULL".."$GROUND_TRUTH_FULL" > "$OUTPUT_DIR/ground_truth.patch"

if [ -s "$OUTPUT_DIR/ground_truth.patch" ]; then
    echo "[ground-truth] Diff: $OUTPUT_DIR/ground_truth.patch ($(wc -c < "$OUTPUT_DIR/ground_truth.patch") bytes, $(wc -l < "$OUTPUT_DIR/ground_truth.patch") lines)"
else
    echo "[ground-truth] WARNING: ground truth diff is empty" >&2
fi

# ============================================================================
# Step 4 - Run forge as judge
# ============================================================================
echo ""
echo "============================================================"
echo "  Step 4 - Run forge as judge"
echo "============================================================"

# Generate judge ID
JUDGE_ID="$(uuidgen 2>/dev/null || python3 -c "import uuid; print(uuid.uuid4())" 2>/dev/null || cat /proc/sys/kernel/random/uuid 2>/dev/null || printf "%08x-%04x-%04x-%04x-%012x" $(date +%s) 0 0 0 0)"
echo "[judge] Judge ID: $JUDGE_ID"

# Generate the judge prompt
JUDGE_PROMPT_FILE="$OUTPUT_DIR/judge_prompt.md"
echo "[judge] Generating judge prompt at $JUDGE_PROMPT_FILE..."

# Locate judge prompt template
JUDGE_TEMPLATE=""
if [ -f "$SCRIPT_DIR/judge_prompt_template.md" ]; then
    JUDGE_TEMPLATE="$SCRIPT_DIR/judge_prompt_template.md"
else
    # Search relative to the script location
    JUDGE_TEMPLATE="$(find "$SCRIPT_DIR" -name "judge_prompt_template.md" -print -quit 2>/dev/null)"
fi

if [ -z "$JUDGE_TEMPLATE" ] || [ ! -f "$JUDGE_TEMPLATE" ]; then
    echo "Error: judge_prompt_template.md not found" >&2
    exit 1
fi
echo "[judge] Using template: $JUDGE_TEMPLATE"

# Substitute placeholders (project and commit references only)
sed \
    -e "s|{{PROJECT_NAME}}|$PROJECT_NAME|g" \
    -e "s|{{EVAL_COMMIT}}|$EVAL_COMMIT_FULL|g" \
    -e "s|{{GROUND_TRUTH_COMMIT}}|$GROUND_TRUTH_FULL|g" \
    "$JUDGE_TEMPLATE" > "$JUDGE_PROMPT_FILE"

# Append mct-agent Plan
{
    echo ""
    echo "## mct-agent Plan"
    echo ""
    if [ -s "$OUTPUT_DIR/mct_plan.md" ]; then
        cat "$OUTPUT_DIR/mct_plan.md"
    else
        echo "(mct-agent did not produce a plan)"
    fi
    echo ""
} >> "$JUDGE_PROMPT_FILE"

# Append Forge Plan
{
    echo ""
    echo "## Forge Plan"
    echo ""
    if [ -s "$OUTPUT_DIR/forge_plan.md" ]; then
        cat "$OUTPUT_DIR/forge_plan.md"
    else
        echo "(Forge did not produce a plan)"
    fi
    echo ""
} >> "$JUDGE_PROMPT_FILE"

# Append mct-agent Implementation
{
    echo ""
    echo "## mct-agent Implementation"
    echo ""
    if [ -s "$OUTPUT_DIR/mct_answer.md" ]; then
        cat "$OUTPUT_DIR/mct_answer.md"
    else
        echo "(mct-agent did not produce an implementation)"
    fi
    echo ""
} >> "$JUDGE_PROMPT_FILE"

# Append Forge Implementation
{
    echo ""
    echo "## Forge Implementation"
    echo ""
    if [ -s "$OUTPUT_DIR/forge_answer.md" ]; then
        cat "$OUTPUT_DIR/forge_answer.md"
    else
        echo "(Forge did not produce an implementation)"
    fi
    echo ""
} >> "$JUDGE_PROMPT_FILE"

# Append mct-agent Changes
{
    echo ""
    echo "## mct-agent Changes"
    echo ""
    echo '```diff'
    if [ -s "$OUTPUT_DIR/mct_changes.patch" ]; then
        cat "$OUTPUT_DIR/mct_changes.patch"
    else
        echo "(mct-agent produced no changes)"
    fi
    echo '```'
    echo ""
} >> "$JUDGE_PROMPT_FILE"

# Append Forge Changes
{
    echo ""
    echo "## Forge Changes"
    echo ""
    echo '```diff'
    if [ -s "$OUTPUT_DIR/forge_changes.patch" ]; then
        cat "$OUTPUT_DIR/forge_changes.patch"
    else
        echo "(Forge produced no changes)"
    fi
    echo '```'
    echo ""
} >> "$JUDGE_PROMPT_FILE"

# Append Ground Truth Diff
{
    echo ""
    echo "## Ground Truth Diff"
    echo ""
    echo '```diff'
    if [ -s "$OUTPUT_DIR/ground_truth.patch" ]; then
        cat "$OUTPUT_DIR/ground_truth.patch"
    else
        echo "(ground truth diff is empty)"
    fi
    echo '```'
    echo ""
} >> "$JUDGE_PROMPT_FILE"

echo "[judge] Judge prompt written ($(wc -c < "$JUDGE_PROMPT_FILE") bytes, $(wc -l < "$JUDGE_PROMPT_FILE") lines)"
# Run forge as judge
echo "[judge] Running forge as judge..."
cd "$OUTPUT_DIR"
JUDGE_RC=0
env $FORGE_ENV forge --agent muse -C "$JUDGE_WORKTREE" --conversation-id "$JUDGE_ID" < "$JUDGE_PROMPT_FILE" || JUDGE_RC=$?

if [ "$JUDGE_RC" -ne 0 ]; then
    echo "[judge] WARNING: judge forge exited with code $JUDGE_RC" >&2
else
    echo "[judge] Judge forge completed successfully."
fi

# Extract judgment
echo "[judge] Extracting judgment..."
echo "[judge] Dumping conversation $JUDGE_ID..."
(cd "$OUTPUT_DIR" && forge conversation dump "$JUDGE_ID" 2>/dev/null)
DUMP_FILE="$(ls -t "$OUTPUT_DIR"/*-dump.json 2>/dev/null | head -1)"
if [ -n "$DUMP_FILE" ] && [ -f "$DUMP_FILE" ]; then
    jq -r '[.conversation.context.messages[] | select(.text.role == "Assistant") | .text.content | select(length > 0)] | last' \
        "$DUMP_FILE" > "$OUTPUT_DIR/judgment.md"
    rm -f "$DUMP_FILE"
    echo "[judge] Judgment extracted to: $OUTPUT_DIR/judgment.md"
else
    echo "[judge] WARNING: judge conversation dump did not produce a dump file for $JUDGE_ID" >&2
    echo "Judge forge did not produce output (conversation dump failed)." > "$OUTPUT_DIR/judgment.md"
fi

if [ -s "$OUTPUT_DIR/judgment.md" ]; then
    echo "[judge] Judgment: $OUTPUT_DIR/judgment.md ($(wc -c < "$OUTPUT_DIR/judgment.md") bytes, $(wc -l < "$OUTPUT_DIR/judgment.md") lines)"
else
    echo "[judge] WARNING: No judgment produced (file empty or missing)" >&2
fi

# ============================================================================
# Step 4b - Generate evaluation report
# ============================================================================
echo ""
echo "============================================================"
echo "  Step 4b - Evaluation Report"
echo "============================================================"

if [ -s "$OUTPUT_DIR/judgment.md" ]; then
    # ---- Plan Quality Axis ----
    # Extract Plan Quality section
    PLAN_SECTION=$(sed -n '/^#.* Plan Quality/,/^#.* Implementation Quality/p' "$OUTPUT_DIR/judgment.md")

    # mct-agent Plan scores
    MCT_PLAN_SUB=$(echo "$PLAN_SECTION" | sed -n '/^#.* mct-agent Plan/,/^#.* Forge Plan/p')
    MCT_PLAN_ACC=$(echo "$MCT_PLAN_SUB" | grep -oP 'Accuracy\s*\(\K\d+' | head -1 || echo "N/A")
    MCT_PLAN_COMP=$(echo "$MCT_PLAN_SUB" | grep -oP 'Completeness\s*\(\K\d+' | head -1 || echo "N/A")
    MCT_PLAN_SPEC=$(echo "$MCT_PLAN_SUB" | grep -oP 'Specificity\s*\(\K\d+' | head -1 || echo "N/A")
    MCT_PLAN_TOTAL=$(echo "$MCT_PLAN_SUB" | grep -oP '\*\*Total:\*\*\s*\K\d+' | head -1 || echo "N/A")

    # Forge Plan scores
    FORGE_PLAN_SUB=$(echo "$PLAN_SECTION" | sed -n '/^#.* Forge Plan/,/^#.* Plan Winner/p')
    FORGE_PLAN_ACC=$(echo "$FORGE_PLAN_SUB" | grep -oP 'Accuracy\s*\(\K\d+' | head -1 || echo "N/A")
    FORGE_PLAN_COMP=$(echo "$FORGE_PLAN_SUB" | grep -oP 'Completeness\s*\(\K\d+' | head -1 || echo "N/A")
    FORGE_PLAN_SPEC=$(echo "$FORGE_PLAN_SUB" | grep -oP 'Specificity\s*\(\K\d+' | head -1 || echo "N/A")
    FORGE_PLAN_TOTAL=$(echo "$FORGE_PLAN_SUB" | grep -oP '\*\*Total:\*\*\s*\K\d+' | head -1 || echo "N/A")

    # Plan Winner
    PLAN_WINNER=$(echo "$PLAN_SECTION" | sed -n '/^#.* Plan Winner/,$p' | sed '1d' | grep -m1 '.' || echo "N/A")
    PLAN_WINNER=$(echo "$PLAN_WINNER" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')

    # ---- Implementation Quality Axis ----
    # Extract Implementation Quality section
    IMPL_SECTION=$(sed -n '/^#.* Implementation Quality/,/^#.* Overall Assessment/p' "$OUTPUT_DIR/judgment.md")

    # mct-agent Implementation scores
    MCT_IMPL_SUB=$(echo "$IMPL_SECTION" | sed -n '/^#.* mct-agent Implementation/,/^#.* Forge Implementation/p')
    MCT_IMPL_CORR=$(echo "$MCT_IMPL_SUB" | grep -oP 'Correctness\s*\(\K\d+' | head -1 || echo "N/A")
    MCT_IMPL_PREC=$(echo "$MCT_IMPL_SUB" | grep -oP 'Precision\s*\(\K\d+' | head -1 || echo "N/A")
    MCT_IMPL_COMP=$(echo "$MCT_IMPL_SUB" | grep -oP 'Completeness\s*\(\K\d+' | head -1 || echo "N/A")
    MCT_IMPL_TOTAL=$(echo "$MCT_IMPL_SUB" | grep -oP '\*\*Total:\*\*\s*\K\d+' | head -1 || echo "N/A")

    # Forge Implementation scores
    FORGE_IMPL_SUB=$(echo "$IMPL_SECTION" | sed -n '/^#.* Forge Implementation/,/^#.* Implementation Winner/p')
    FORGE_IMPL_CORR=$(echo "$FORGE_IMPL_SUB" | grep -oP 'Correctness\s*\(\K\d+' | head -1 || echo "N/A")
    FORGE_IMPL_PREC=$(echo "$FORGE_IMPL_SUB" | grep -oP 'Precision\s*\(\K\d+' | head -1 || echo "N/A")
    FORGE_IMPL_COMP=$(echo "$FORGE_IMPL_SUB" | grep -oP 'Completeness\s*\(\K\d+' | head -1 || echo "N/A")
    FORGE_IMPL_TOTAL=$(echo "$FORGE_IMPL_SUB" | grep -oP '\*\*Total:\*\*\s*\K\d+' | head -1 || echo "N/A")

    # Implementation Winner
    IMPL_WINNER=$(echo "$IMPL_SECTION" | sed -n '/^#.* Implementation Winner/,$p' | sed '1d' | grep -m1 '.' || echo "N/A")
    IMPL_WINNER=$(echo "$IMPL_WINNER" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')

    # ---- Overall Assessment ----
    OVERALL_SECTION=$(sed -n '/^#.* Overall Assessment/,$p' "$OUTPUT_DIR/judgment.md" | sed '1d')
    OVERALL_WINNER=$(echo "$OVERALL_SECTION" | grep -m1 '.' | head -1 || echo "N/A")
    OVERALL_WINNER=$(echo "$OVERALL_WINNER" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')

    # Print two-axis comparative table to stdout
    echo ""
    echo "============================================================"
    echo "  Evaluation Report — Two-Axis"
    echo "============================================================"
    echo ""
    echo "  --- Plan Quality (each /10, total /30) ---"
    echo ""
    printf "  %-20s %-12s %-12s\\n" "Dimension" "mct-agent" "Forge"
    printf "  %-20s %-12s %-12s\\n" "--------------------" "------------" "------------"
    printf "  %-20s %-12s %-12s\\n" "Accuracy" "$MCT_PLAN_ACC" "$FORGE_PLAN_ACC"
    printf "  %-20s %-12s %-12s\\n" "Completeness" "$MCT_PLAN_COMP" "$FORGE_PLAN_COMP"
    printf "  %-20s %-12s %-12s\\n" "Specificity" "$MCT_PLAN_SPEC" "$FORGE_PLAN_SPEC"
    printf "  %-20s %-12s %-12s\\n" "--------------------" "------------" "------------"
    printf "  %-20s %-12s %-12s\\n" "PLAN TOTAL" "$MCT_PLAN_TOTAL" "$FORGE_PLAN_TOTAL"
    echo ""
    echo "  Plan Winner: $PLAN_WINNER"
    echo ""
    echo "  --- Implementation Quality (each /10, total /30) ---"
    echo ""
    printf "  %-20s %-12s %-12s\\n" "Dimension" "mct-agent" "Forge"
    printf "  %-20s %-12s %-12s\\n" "--------------------" "------------" "------------"
    printf "  %-20s %-12s %-12s\\n" "Correctness" "$MCT_IMPL_CORR" "$FORGE_IMPL_CORR"
    printf "  %-20s %-12s %-12s\\n" "Precision" "$MCT_IMPL_PREC" "$FORGE_IMPL_PREC"
    printf "  %-20s %-12s %-12s\\n" "Completeness" "$MCT_IMPL_COMP" "$FORGE_IMPL_COMP"
    printf "  %-20s %-12s %-12s\\n" "--------------------" "------------" "------------"
    printf "  %-20s %-12s %-12s\\n" "IMPL TOTAL" "$MCT_IMPL_TOTAL" "$FORGE_IMPL_TOTAL"
    echo ""
    echo "  Implementation Winner: $IMPL_WINNER"
    echo ""
    echo "  --- Overall ---"
    echo ""
    echo "  Overall Winner: $OVERALL_WINNER"
    echo ""

    # Write report.md
    REPORT_FILE="$OUTPUT_DIR/report.md"
    {
        echo "# Evaluation Report"
        echo ""
        echo "## Plan Quality (each /10, total /30)"
        echo ""
        echo "| Dimension | mct-agent | Forge |"
        echo "|---------------|-----------|-------|"
        echo "| Accuracy | $MCT_PLAN_ACC/10 | $FORGE_PLAN_ACC/10 |"
        echo "| Completeness | $MCT_PLAN_COMP/10 | $FORGE_PLAN_COMP/10 |"
        echo "| Specificity | $MCT_PLAN_SPEC/10 | $FORGE_PLAN_SPEC/10 |"
        echo "| **Plan Total** | **$MCT_PLAN_TOTAL/30** | **$FORGE_PLAN_TOTAL/30** |"
        echo ""
        echo "**Plan Winner:** $PLAN_WINNER"
        echo ""
        echo "## Implementation Quality (each /10, total /30)"
        echo ""
        echo "| Dimension | mct-agent | Forge |"
        echo "|---------------|-----------|-------|"
        echo "| Correctness | $MCT_IMPL_CORR/10 | $FORGE_IMPL_CORR/10 |"
        echo "| Precision | $MCT_IMPL_PREC/10 | $FORGE_IMPL_PREC/10 |"
        echo "| Completeness | $MCT_IMPL_COMP/10 | $FORGE_IMPL_COMP/10 |"
        echo "| **Impl Total** | **$MCT_IMPL_TOTAL/30** | **$FORGE_IMPL_TOTAL/30** |"
        echo ""
        echo "**Implementation Winner:** $IMPL_WINNER"
        echo ""
        echo "## Overall Winner"
        echo ""
        echo "$OVERALL_WINNER"
        echo ""
        echo "## Artifact Paths"
        echo ""
        echo "- mct_plan.md: \`$OUTPUT_DIR/mct_plan.md\`"
        echo "- forge_plan.md: \`$OUTPUT_DIR/forge_plan.md\`"
        echo "- mct_answer.md: \`$OUTPUT_DIR/mct_answer.md\`"
        echo "- forge_answer.md: \`$OUTPUT_DIR/forge_answer.md\`"
        echo "- mct_changes.patch: \`$OUTPUT_DIR/mct_changes.patch\`"
        echo "- forge_changes.patch: \`$OUTPUT_DIR/forge_changes.patch\`"
        echo "- ground_truth.patch: \`$OUTPUT_DIR/ground_truth.patch\`"
        echo "- judgment.md: \`$OUTPUT_DIR/judgment.md\`"
        echo "- judge_prompt.md: \`$OUTPUT_DIR/judge_prompt.md\`"
    } > "$REPORT_FILE"
    echo "[report] Report written to: $REPORT_FILE"
else
    echo "[report] No judgment to parse; report skipped."
fi

# ============================================================================
# Step 5 - Teardown
# ============================================================================
echo ""
echo "============================================================"
echo "  Step 5 - Teardown"
echo "============================================================"

if [ "$KEEP" = true ]; then
    echo "[teardown] --keep set; preserving worktrees and artifacts."
    echo "[teardown] Worktree (mct): $MCT_WORKTREE"
    echo "[teardown] Worktree (forge): $FORGE_WORKTREE"
    echo "[teardown] Worktree (judge): $JUDGE_WORKTREE"
    echo "[teardown] Artifacts: $OUTPUT_DIR"
else
    echo "[teardown] Removing worktrees..."
    rm -rf "$MCT_WORKTREE" "$FORGE_WORKTREE" "$JUDGE_WORKTREE"
    git -C "$REPO" worktree prune 2>/dev/null || true
    echo "[teardown] Worktrees removed."
    echo "[teardown] Artifacts preserved at: $OUTPUT_DIR"
fi

# ============================================================================
# Summary
# ============================================================================
echo ""
echo "============================================================"
echo "  Evaluation Pipeline Complete"
echo "============================================================"
echo ""
echo "  Repository:    $REPO ($PROJECT_NAME)"
echo "  Eval commit:   $EVAL_COMMIT_FULL"
echo "  Ground truth:  $GROUND_TRUTH_FULL"
echo "  Output dir:    $OUTPUT_DIR"
echo ""
echo "  Artifacts:"

print_artifact() {
    local label="$1"
    local path="$2"
    if [ -f "$path" ] && [ -s "$path" ]; then
        printf "    %-20s %s  (%s bytes, %s lines)\n" \
            "$label" "$path" \
            "$(wc -c < "$path")" \
            "$(wc -l < "$path")"
    else
        printf "    %-20s (not produced)\n" "$label"
    fi
}

print_artifact "mct_plan.md:" "$OUTPUT_DIR/mct_plan.md"
print_artifact "forge_plan.md:" "$OUTPUT_DIR/forge_plan.md"
print_artifact "mct_answer.md:" "$OUTPUT_DIR/mct_answer.md"
print_artifact "forge_answer.md:" "$OUTPUT_DIR/forge_answer.md"
print_artifact "mct_changes.patch:" "$OUTPUT_DIR/mct_changes.patch"
print_artifact "forge_changes.patch:" "$OUTPUT_DIR/forge_changes.patch"
print_artifact "ground_truth.patch:" "$OUTPUT_DIR/ground_truth.patch"
print_artifact "judgment.md:" "$OUTPUT_DIR/judgment.md"
print_artifact "judge_prompt.md:" "$OUTPUT_DIR/judge_prompt.md"

echo ""
echo "[$(date '+%Y-%m-%d %H:%M:%S')] run_eval.sh finished."
