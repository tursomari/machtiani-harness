#!/usr/bin/env bash
set -euo pipefail

usage() {
    cat <<'EOF'
Usage: generate_comparison.sh --root <path> --prompt <path> [--output <path>] [--model <alias>] [--api-key <provider:key>] [--api-key-file <provider:path>] [--config <path>] [--ground-truth <oid>] [--eval-commit <oid>]
EOF
    exit 1
}

PROJECT_ROOT=""
PROMPT_FILE=""
OUTPUT="/tmp/comparison_runbook.md"
MCT_MODEL="glm-5-high-deepinfra"
MCT_API_KEY=""
GROUND_TRUTH_COMMIT=""
EVAL_COMMIT=""
API_KEY_FILE=""
CONFIG_FILE=""

while [ $# -gt 0 ]; do
    case "$1" in
        --root)
            PROJECT_ROOT="$2"
            shift 2
            ;;
        --prompt)
            PROMPT_FILE="$2"
            shift 2
            ;;
        --output)
            OUTPUT="$2"
            shift 2
            ;;
        --model)
            MCT_MODEL="$2"
            shift 2
            ;;
        --api-key)
            MCT_API_KEY="$2"
            shift 2
            ;;
        --api-key-file)
            API_KEY_FILE="$2"
            shift 2
            ;;
        --config)
            CONFIG_FILE="$2"
            shift 2
            ;;
        --ground-truth)
            GROUND_TRUTH_COMMIT="$2"
            shift 2
            ;;
        --eval-commit)
            EVAL_COMMIT="$2"
            shift 2
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

if [ -z "$PROJECT_ROOT" ]; then
    echo "Error: --root is required" >&2
    usage
fi
if [ -z "$PROMPT_FILE" ]; then
    echo "Error: --prompt is required" >&2
    usage
fi

if [ -n "$API_KEY_FILE" ]; then
    API_KEY_PROVIDER="${API_KEY_FILE%%:*}"
    API_KEY_FILEPATH="${API_KEY_FILE#*:}"
    API_KEY_FILEPATH="${API_KEY_FILEPATH/#\~/$HOME}"
    if [ -d "$API_KEY_FILEPATH" ]; then
        KEY_DIR="$API_KEY_FILEPATH"
        API_KEY_FILEPATH=""
        for candidate in "work-api-key.txt" "api-key.txt" "key.txt"; do
            if [ -f "$KEY_DIR/$candidate" ]; then
                API_KEY_FILEPATH="$KEY_DIR/$candidate"
                break
            fi
        done
        if [ -z "$API_KEY_FILEPATH" ]; then
            for f in "$KEY_DIR"/*.txt; do
                if [ -f "$f" ]; then
                    API_KEY_FILEPATH="$f"
                    break
                fi
            done
        fi
        if [ -z "$API_KEY_FILEPATH" ]; then
            echo "Error: no key file found in directory: $KEY_DIR (looked for work-api-key.txt, api-key.txt, key.txt, *.txt)" >&2
            exit 1
        fi
    elif [ ! -f "$API_KEY_FILEPATH" ]; then
        echo "Error: API key file not found: $API_KEY_FILEPATH" >&2
        exit 1
    fi
    API_KEY_VALUE="$(tr -d "\n\r" < "$API_KEY_FILEPATH")"
    if [ -z "$API_KEY_VALUE" ]; then
        echo "Error: API key file is empty: $API_KEY_FILEPATH" >&2
        exit 1
    fi
    if [ -n "$MCT_API_KEY" ]; then
        echo "Warning: both --api-key and --api-key-file provided; --api-key takes precedence" >&2
    else
        MCT_API_KEY="${API_KEY_PROVIDER}:${API_KEY_VALUE}"
    fi
fi

if [ -z "$CONFIG_FILE" ]; then
    SEARCH_DIR="$PROJECT_ROOT"
    while [ "$SEARCH_DIR" != "/" ] && [ "$SEARCH_DIR" != "." ]; do
        if [ -f "$SEARCH_DIR/.machtiani/config.toml" ]; then
            CONFIG_FILE="$SEARCH_DIR/.machtiani/config.toml"
            break
        fi
        SEARCH_DIR="$(dirname "$SEARCH_DIR")"
    done
    if [ -z "$CONFIG_FILE" ]; then
        CONFIG_FILE="$HOME/.machtiani/config.toml"
    fi
fi

PROJECT_ROOT="$(realpath "$PROJECT_ROOT")"
PROMPT_FILE="$(realpath "$PROMPT_FILE")"
PROJECT_NAME="$(basename "$PROJECT_ROOT")"
GEN_DATE="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
if [ -n "$EVAL_COMMIT" ]; then
    ARTIFACT_SUFFIX="_${EVAL_COMMIT}"
else
    ARTIFACT_SUFFIX=""
fi
if [ -n "$EVAL_COMMIT" ]; then
    WORKTREE_DIR="/tmp/${PROJECT_NAME}_eval_${EVAL_COMMIT}"
else
    WORKTREE_DIR=""
fi
if [ -n "$MCT_API_KEY" ]; then
    MCT_API_KEY_ARG="--api-key $MCT_API_KEY"
else
    MCT_API_KEY_ARG=""
fi
if [ -n "$CONFIG_FILE" ]; then
    MCT_CONFIG_ARG="--config $CONFIG_FILE"
else
    MCT_CONFIG_ARG=""
fi
MCT_CONFIG_PATH="$(realpath "$CONFIG_FILE")"
sed -e "s|{{PROJECT_ROOT}}|$PROJECT_ROOT|g" \
    -e "s|{{PROJECT_NAME}}|$PROJECT_NAME|g" \
    -e "s|{{PROMPT_FILE}}|$PROMPT_FILE|g" \
    -e "s|{{GENERATION_DATE}}|$GEN_DATE|g" \
    -e "s|{{MCT_MODEL}}|$MCT_MODEL|g" \
    -e "s|{{MCT_API_KEY}}|$MCT_API_KEY|g" \
    -e "s|{{MCT_API_KEY_ARG}}|$MCT_API_KEY_ARG|g" \
    -e "s|{{MCT_CONFIG_ARG}}|$MCT_CONFIG_ARG|g" \
    -e "s|{{MCT_CONFIG_PATH}}|$MCT_CONFIG_PATH|g" \
    -e "s|{{GROUND_TRUTH_COMMIT}}|$GROUND_TRUTH_COMMIT|g" \
    -e "s|{{EVAL_COMMIT}}|$EVAL_COMMIT|g" \
    -e "s|{{ARTIFACT_SUFFIX}}|$ARTIFACT_SUFFIX|g" \
    -e "s|{{WORKTREE_DIR}}|$WORKTREE_DIR|g" \
    scripts/runbook_template.md > "$OUTPUT"
if [ -z "$EVAL_COMMIT" ]; then
    sed -i "/<!-- IF_EVAL -->/,/<!-- END_IF_EVAL -->/d" "$OUTPUT"
else
    sed -i "/<!-- IF_EVAL -->/d; /<!-- END_IF_EVAL -->/d" "$OUTPUT"
fi
if [ -z "$EVAL_COMMIT" ]; then
    sed -i "/<!-- IF_NOT_EVAL -->/d; /<!-- END_IF_NOT_EVAL -->/d" "$OUTPUT"
else
    sed -i "/<!-- IF_NOT_EVAL -->/,/<!-- END_IF_NOT_EVAL -->/d" "$OUTPUT"
fi
echo "Runbook written to $OUTPUT"
