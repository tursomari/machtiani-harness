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
# run_eval.sh — Automated eval pipeline comparing mct-agent vs Forge
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
  --model <alias>          mct-agent model alias (default: deepseek-v4-pro)
  --sync-model <alias>     model alias for the sync step (default: glm-5-high)
  --api-key-file <provider:path>
                           API key file in provider:path format
  --api-key <provider:key>
                           API key in provider:key format
  --config <path>          Path to machtiani config.toml (auto-discovered if not set)
  --output-dir <path>      Directory for all artifacts (default: /tmp/eval_<timestamp>)
  --keep                   Keep worktree and artifacts after completion (for debugging)
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
SYNC_MODEL="glm-5-high"
MODEL="deepseek-v4-pro"
API_KEY_FILE=""
API_KEY=""
CONFIG=""
OUTPUT_DIR=""
KEEP=false

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

if [ -d "$SYNC_CACHE_DIR" ]; then
    echo "[sync] Cache hit for $EVAL_COMMIT, skipping sync"
    mkdir -p "$MCT_WORKTREE/.machtiani/artifacts/readme"
    cp -r "$SYNC_CACHE_DIR/"* "$MCT_WORKTREE/.machtiani/artifacts/readme/"
else
    echo ""
    echo "[setup] Running mct-agent sync in worktree..."
    cd "$MCT_WORKTREE"
    MACHTIANI_CONFIG="$CONFIG" mct-agent sync \
        --model "$SYNC_MODEL" \
        --timeout-per-turn 0 \
        --max-steps 20 || {
        echo "ERROR: mct-agent sync failed" >&2
        exit 1
    }
    echo "[setup] mct-agent sync completed successfully."

    # Cache the sync result
    mkdir -p "$SYNC_CACHE_DIR"
    if [ -d "$MCT_WORKTREE/.machtiani/artifacts/readme" ]; then
        cp -r "$MCT_WORKTREE/.machtiani/artifacts/readme/"* "$SYNC_CACHE_DIR/"
        echo "[setup] Sync result cached at: $SYNC_CACHE_DIR"
    fi
fi

# ============================================================================
# Step 1 - Run mct-agent
# ============================================================================
echo ""
echo "============================================================"
echo "  Step 1 - Run mct-agent (code mode)"
echo "============================================================"

cd "$MCT_WORKTREE"
MCT_RC=0
MCT_SUCCESS=false
for ATTEMPT in 1 2 3; do
    echo "[mct-agent] Attempt $ATTEMPT/3: Running mct-agent..."
    if MACHTIANI_CONFIG="$CONFIG" mct-agent run \
        --mode code \
        --final-file "$OUTPUT_DIR/mct_answer.md" \
        --model "$MODEL" \
        $MCT_API_KEY_ARG \
        --timeout-per-turn 0 \
        --max-steps 20 \
        --file "$PROMPT"; then
        MCT_SUCCESS=true
        break
    fi
    echo "[mct-agent] Attempt $ATTEMPT failed. Waiting 30 seconds before retry..." >&2
    sleep 30
done
if ! $MCT_SUCCESS; then
    echo "[mct-agent] ERROR: mct-agent failed after 3 attempts" >&2
    echo "mct-agent failed after 3 retry attempts" > "$OUTPUT_DIR/mct_answer.md"
fi

if [ "$MCT_RC" -ne 0 ]; then
    echo "[mct-agent] WARNING: mct-agent exited with code $MCT_RC" >&2
else
    echo "[mct-agent] Completed successfully."
fi

if [ -s "$OUTPUT_DIR/mct_answer.md" ]; then
    echo "[mct-agent] Answer: $OUTPUT_DIR/mct_answer.md ($(wc -c < "$OUTPUT_DIR/mct_answer.md") bytes, $(wc -l < "$OUTPUT_DIR/mct_answer.md") lines)"
else
    echo "[mct-agent] WARNING: No answer produced (file empty or missing)" >&2
fi

# ============================================================================
# Step 2 - Run Forge (muse-forge handoff)
# ============================================================================
echo ""
echo "============================================================"
echo "  Step 2 - Run Forge (muse-forge handoff)"
echo "============================================================"

EVAL_ID="$(uuidgen 2>/dev/null || python3 -c "import uuid; print(uuid.uuid4())" 2>/dev/null || cat /proc/sys/kernel/random/uuid 2>/dev/null || printf "%08x-%04x-%04x-%04x-%012x" $(date +%s) 0 0 0 0)"
echo "[forge] Eval ID: $EVAL_ID"

JUDGE_ID="$(uuidgen 2>/dev/null || python3 -c "import uuid; print(uuid.uuid4())" 2>/dev/null || cat /proc/sys/kernel/random/uuid 2>/dev/null || printf "%08x-%04x-%04x-%04x-%012x" $(date +%s) 0 0 0 0)"
echo "[judge] Judge ID: $JUDGE_ID"

# Copy cached readme to forge worktree if available
if [ -n "${SYNC_CACHE_DIR:-}" ] && [ -d "$SYNC_CACHE_DIR" ]; then
    mkdir -p "$FORGE_WORKTREE/.machtiani/artifacts/readme"
    cp -r "$SYNC_CACHE_DIR/"* "$FORGE_WORKTREE/.machtiani/artifacts/readme/"
    echo "[forge] Cached readme copied to forge worktree"
fi

# Step 2a: Plan with muse
cd "$OUTPUT_DIR"
echo "[forge] Step 2a: Planning with muse..."
MUSE_RC=0
env $FORGE_ENV forge --agent muse -C "$FORGE_WORKTREE" --conversation-id "$EVAL_ID" < "$PROMPT" || MUSE_RC=$?

if [ "$MUSE_RC" -ne 0 ]; then
    echo "[forge] WARNING: muse planning exited with code $MUSE_RC" >&2
else
    echo "[forge] Muse planning completed successfully."
fi

# Step 2b: Implement with forge
echo "[forge] Step 2b: Implementing with forge..."
FORGE_RC=0
echo "Implement the plan from the previous step. Make all necessary code changes to resolve the issue." | env $FORGE_ENV forge --agent forge --conversation-id "$EVAL_ID" -C "$FORGE_WORKTREE" || FORGE_RC=$?

if [ "$FORGE_RC" -ne 0 ]; then
    echo "[forge] WARNING: forge exited with code $FORGE_RC" >&2
else
    echo "[forge] Completed successfully."
fi

echo "[forge] Extracting assistant answer..."
echo "[forge] Dumping conversation $EVAL_ID..."
forge conversation dump "$EVAL_ID" 2>/dev/null
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

# Substitute placeholders
sed \
    -e "s|{{PROJECT_NAME}}|$PROJECT_NAME|g" \
    -e "s|{{EVAL_COMMIT}}|$EVAL_COMMIT_FULL|g" \
    -e "s|{{GROUND_TRUTH_COMMIT}}|$GROUND_TRUTH_FULL|g" \
    -e "s|{{MCT_ANSWER_PATH}}|$OUTPUT_DIR/mct_answer.md|g" \
    -e "s|{{FORGE_ANSWER_PATH}}|$OUTPUT_DIR/forge_answer.md|g" \
    -e "s|{{GROUND_TRUTH_PATCH_PATH}}|$OUTPUT_DIR/ground_truth.patch|g" \
    "$JUDGE_TEMPLATE" > "$JUDGE_PROMPT_FILE"

# Append the original task
{
    echo ""
    echo "## Original Task"
    echo ""
    cat "$PROMPT"
    echo ""
} >> "$JUDGE_PROMPT_FILE"

# Append the mct-agent answer
{
    echo ""
    echo "## mct-agent Answer"
    echo ""
    if [ -s "$OUTPUT_DIR/mct_answer.md" ]; then
        cat "$OUTPUT_DIR/mct_answer.md"
    else
        echo "(mct-agent did not produce an answer)"
    fi
    echo ""
} >> "$JUDGE_PROMPT_FILE"

# Append the forge answer
{
    echo ""
    echo "## Forge Answer"
    echo ""
    if [ -s "$OUTPUT_DIR/forge_answer.md" ]; then
        cat "$OUTPUT_DIR/forge_answer.md"
    else
        echo "(forge did not produce an answer)"
    fi
    echo ""
} >> "$JUDGE_PROMPT_FILE"

# Append the ground truth diff
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
forge conversation dump "$JUDGE_ID" 2>/dev/null
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
    # Extract mct-agent evaluation section
    MCT_SECTION=$(sed -n '/### mct-agent Evaluation/,/### Forge Evaluation/p' "$OUTPUT_DIR/judgment.md")

    # Extract forge evaluation section
    FORGE_SECTION=$(sed -n '/### Forge Evaluation/,/### Winner/p' "$OUTPUT_DIR/judgment.md")
    if [ -z "$FORGE_SECTION" ]; then
        FORGE_SECTION=$(sed -n '/### Forge Evaluation/,$p' "$OUTPUT_DIR/judgment.md")
    fi

    # Extract scores for mct-agent
    MCT_ACC=$(echo "$MCT_SECTION" | grep -oP 'Accuracy\s*\(\K\d+' | head -1 || echo "N/A")
    MCT_COMP=$(echo "$MCT_SECTION" | grep -oP 'Completeness\s*\(\K\d+' | head -1 || echo "N/A")
    MCT_CLR=$(echo "$MCT_SECTION" | grep -oP 'Clarity\s*\(\K\d+' | head -1 || echo "N/A")
    MCT_ACT=$(echo "$MCT_SECTION" | grep -oP 'Actionability\s*\(\K\d+' | head -1 || echo "N/A")
    MCT_TOTAL=$(echo "$MCT_SECTION" | grep -oP '\*\*Total:\*\*\s*\K\d+' | head -1 || echo "N/A")

    # Extract scores for forge
    FORGE_ACC=$(echo "$FORGE_SECTION" | grep -oP 'Accuracy\s*\(\K\d+' | head -1 || echo "N/A")
    FORGE_COMP=$(echo "$FORGE_SECTION" | grep -oP 'Completeness\s*\(\K\d+' | head -1 || echo "N/A")
    FORGE_CLR=$(echo "$FORGE_SECTION" | grep -oP 'Clarity\s*\(\K\d+' | head -1 || echo "N/A")
    FORGE_ACT=$(echo "$FORGE_SECTION" | grep -oP 'Actionability\s*\(\K\d+' | head -1 || echo "N/A")
    FORGE_TOTAL=$(echo "$FORGE_SECTION" | grep -oP '\*\*Total:\*\*\s*\K\d+' | head -1 || echo "N/A")

    # Extract winner
    WINNER=$(sed -n '/### Winner/,$p' "$OUTPUT_DIR/judgment.md" | sed '1d' | grep -m1 '.' || echo "N/A")
    WINNER=$(echo "$WINNER" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')

    echo ""
    echo "============================================================"
    echo "  Evaluation Report"
    echo "============================================================"
    echo ""
    printf "  %-20s %-12s %-12s\\n" "Dimension" "mct-agent" "Forge"
    printf "  %-20s %-12s %-12s\\n" "--------------------" "------------" "------------"
    printf "  %-20s %-12s %-12s\\n" "Accuracy" "$MCT_ACC" "$FORGE_ACC"
    printf "  %-20s %-12s %-12s\\n" "Completeness" "$MCT_COMP" "$FORGE_COMP"
    printf "  %-20s %-12s %-12s\\n" "Clarity" "$MCT_CLR" "$FORGE_CLR"
    printf "  %-20s %-12s %-12s\\n" "Actionability" "$MCT_ACT" "$FORGE_ACT"
    printf "  %-20s %-12s %-12s\\n" "--------------------" "------------" "------------"
    printf "  %-20s %-12s %-12s\\n" "TOTAL" "$MCT_TOTAL" "$FORGE_TOTAL"
    echo ""
    echo "  Winner: $WINNER"
    echo ""

    # Write report.md
    REPORT_FILE="$OUTPUT_DIR/report.md"
    {
        echo "# Evaluation Report"
        echo ""
        echo "## Comparative Scores"
        echo ""
        echo "| Dimension | mct-agent | Forge |"
        echo "|---------------|-----------|-------|"
        echo "| Accuracy | $MCT_ACC/10 | $FORGE_ACC/10 |"
        echo "| Completeness | $MCT_COMP/10 | $FORGE_COMP/10 |"
        echo "| Clarity | $MCT_CLR/10 | $FORGE_CLR/10 |"
        echo "| Actionability | $MCT_ACT/10 | $FORGE_ACT/10 |"
        echo "| **TOTAL** | **$MCT_TOTAL/40** | **$FORGE_TOTAL/40** |"
        echo ""
        echo "## Winner"
        echo ""
        echo "$WINNER"
        echo ""
        echo "## Artifact Paths"
        echo ""
        echo "- mct_answer.md: \`$OUTPUT_DIR/mct_answer.md\`"
        echo "- forge_answer.md: \`$OUTPUT_DIR/forge_answer.md\`"
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

print_artifact "mct_answer.md:" "$OUTPUT_DIR/mct_answer.md"
print_artifact "forge_answer.md:" "$OUTPUT_DIR/forge_answer.md"
print_artifact "ground_truth.patch:" "$OUTPUT_DIR/ground_truth.patch"
print_artifact "judgment.md:" "$OUTPUT_DIR/judgment.md"
print_artifact "judge_prompt.md:" "$OUTPUT_DIR/judge_prompt.md"

echo ""
echo "[$(date '+%Y-%m-%d %H:%M:%S')] run_eval.sh finished."
