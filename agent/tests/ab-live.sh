#!/usr/bin/env bash
set -euo pipefail

# Parse per-test-case PASS/FAIL/SKIP output captured from agent/tests/run-live.sh.
#
# Usage:
#   agent/tests/ab-live.sh <run-output-dir>
#   agent/tests/ab-live.sh --tsv-out <path> <run-output-dir>
#
# The run-output-dir must be a directory produced by scripts/ab-dev.sh for a
# single container run, containing stdout.log and optionally stderr.log. Results
# are emitted as TSV columns: case_id, status, detail. This script always exits
# 0; it reports what it can parse without failing the caller.

sanitize_detail() {
    local detail="$1"
    detail="${detail//$'\t'/ }"
    detail="${detail//$'\r'/ }"
    detail="${detail//$'\n'/ }"
    detail="$(printf '%s' "$detail" | tr -s '[:space:]' ' ' | sed 's/^ //; s/ $//')"
    printf '%s' "${detail:0:200}"
}

record_result() {
    local case_id="$1"
    local status="$2"
    local detail="$3"

    if [[ -n "${RESULT_STATUS[$case_id]+set}" ]]; then
        if [[ "${RESULT_STATUS[$case_id]}" == "FAIL" && "$status" == "PASS" ]]; then
            RESULT_STATUS[$case_id]="$status"
            RESULT_DETAIL[$case_id]="$detail"
        fi
        return 0
    fi

    RESULT_ORDER+=("$case_id")
    RESULT_STATUS[$case_id]="$status"
    RESULT_DETAIL[$case_id]="$detail"
}

parse_line() {
    local line="$1"
    local detail
    detail="$(sanitize_detail "$line")"

    if [[ "$line" =~ ^[[:space:]]*Passed:[[:space:]]+([^[:space:]]+) ]]; then
        record_result "${BASH_REMATCH[1]}" "PASS" "$detail"
        return 0
    fi

    if [[ "$line" =~ ^[[:space:]]*PASS[[:space:]]*:?[[:space:]]+(run_shell_agent_subcommand_live_case) ]]; then
        record_result "run_shell_agent_subcommand_live_case" "PASS" "$detail"
        return 0
    fi

    if [[ "$line" =~ ^[[:space:]]*PASSED:[[:space:]]+shell-command-trajectory-live[[:space:]]case ]]; then
        record_result "shell-command-trajectory-live" "PASS" "$detail"
        return 0
    fi

    if [[ "$line" =~ ^[[:space:]]*Failed[[:space:]].*[[:space:]]([A-Za-z0-9_.-]+)[[:space:]]*$ ]]; then
        record_result "${BASH_REMATCH[1]}" "FAIL" "$detail"
        return 0
    fi

    if [[ "$line" =~ ^[[:space:]]*FAIL[[:space:]]*:[[:space:]]+mct-agent[[:space:]]shell-agent[[:space:]][^[:space:]]+ ]]; then
        record_result "shell-agent-subcommand" "FAIL" "$detail"
        return 0
    fi

    if [[ "$line" =~ ^[[:space:]]*FAILED[[:space:]]*\([[:space:]]*non-fatal[[:space:]]*\)[[:space:]]*:[[:space:]]+([^[:space:]]+) ]]; then
        record_result "${BASH_REMATCH[1]}" "FAIL" "$detail"
        return 0
    fi

    if [[ "$line" =~ ^[[:space:]]*FATAL[[:space:]]*: ]]; then
        FATAL_SEEN=true
        FATAL_DETAIL="$detail"
        record_result "fatal-preflight" "SKIP" "$detail"
    fi
}

emit_tsv() {
    printf 'case_id\tstatus\tdetail\n'
    local case_id
    for case_id in "${RESULT_ORDER[@]}"; do
        printf '%s\t%s\t%s\n' "$case_id" "${RESULT_STATUS[$case_id]}" "${RESULT_DETAIL[$case_id]}"
    done
}

OUT_PATH=""
RUN_DIR=""

if [[ $# -eq 1 ]]; then
    RUN_DIR="$1"
elif [[ $# -eq 3 && "$1" == "--tsv-out" ]]; then
    OUT_PATH="$2"
    RUN_DIR="$3"
else
    echo "Usage: agent/tests/ab-live.sh [--tsv-out PATH] <run-output-dir>" >&2
    exit 0
fi

declare -a RESULT_ORDER=()
declare -A RESULT_STATUS=()
declare -A RESULT_DETAIL=()
FATAL_SEEN=false
FATAL_DETAIL=""

if [[ -f "$RUN_DIR/stdout.log" ]]; then
    while IFS= read -r line || [[ -n "$line" ]]; do
        parse_line "$line"
    done < "$RUN_DIR/stdout.log"
fi

if [[ -f "$RUN_DIR/stderr.log" ]]; then
    while IFS= read -r line || [[ -n "$line" ]]; do
        parse_line "$line"
    done < "$RUN_DIR/stderr.log"
fi

if [[ "${#RESULT_ORDER[@]}" -eq 0 && "$FATAL_SEEN" == true ]]; then
    record_result "fatal-preflight" "SKIP" "$FATAL_DETAIL"
fi

if [[ -n "$OUT_PATH" ]]; then
    emit_tsv > "$OUT_PATH"
    rows=$(( ${#RESULT_ORDER[@]} ))
    echo "Wrote $rows case results to $OUT_PATH" >&2
else
    emit_tsv
fi

exit 0
