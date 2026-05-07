#!/usr/bin/env bash
set -euo pipefail
PROJECT_ROOT="${1:?missing project root}"
PROMPT_FILE="${2:?missing prompt file}"
OUTPUT="${3:-/tmp/comparison_runbook.md}"
PROJECT_ROOT="$(realpath "$PROJECT_ROOT")"
PROMPT_FILE="$(realpath "$PROMPT_FILE")"
PROJECT_NAME="$(basename "$PROJECT_ROOT")"
GEN_DATE="$(date -u +"%Y-%m-%dTH%M:%M:%SZ")"
sed -e "s|{{PROJECT_ROOT}}|$PROJECT_ROOT|g" \
    -e "s|{{PROJECT_NAME}}|$PROJECT_NAME|g" \
    -e "s|{{PROMPT_FILE}}|$PROMPT_FILE|g" \
    -e "s|{{GENERATION_DATE}}|$GEN_DATE|g" \
    scripts/runbook_template.md > "$OUTPUT"
echo "Runbook written to $OUTPUT"