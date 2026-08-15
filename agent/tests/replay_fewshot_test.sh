#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
GOAL_FILE="$SCRIPT_DIR/fixtures/synthetic_shell_goal.txt"
TIMESTAMP=$(date +%s)
OUT_DIR="$SCRIPT_DIR/tmp/fewshot-ab-$TIMESTAMP"
mkdir -p "$OUT_DIR"

MCT_AGENT="$REPO_ROOT/machtiani"
CONFIG="$REPO_ROOT/.machtiani/config.toml"
export MACHTIANI_CONFIG="$CONFIG"

echo "=== A/B Few-Shot Injection Test ==="
echo "Output dir: $OUT_DIR"
echo "Goal file: $GOAL_FILE"
echo ""

run_session() {
  local label="$1"
  local variant="$2"
  local out="$OUT_DIR/${label}"
  mkdir -p "$out"
  echo "--- Running $label (variant=$variant) ---"

  local stdout_log="$out/stdout.log"
  local stderr_log="$out/stderr.log"

  MACHTIANI_SHELL_AGENT_FEW_SHOT_VARIANT="$variant" \
    "$MCT_AGENT" run \
      --prompt "$(cat "$GOAL_FILE")" \
      --shell-agent \
      --max-turns 20 \
      --turn-timeout 180 \
      1>"$stdout_log" 2>"$stderr_log"

  local rc=$?

  local sid
  sid=$(grep -oP "session-id: \K[^\"]+" "$stderr_log" 2>/dev/null | head -1 || true)
  if [[ -z "$sid" ]]; then
    sid=$(grep -oP "session_id[:\"]+\K[^\",}]+" "$stderr_log" 2>/dev/null | head -1 || true)
  fi

  echo "Exit code: $rc"
  if [[ -n "$sid" ]]; then
    echo "Session ID: $sid"
    local traj_file="$REPO_ROOT/.machtiani/sessions/$sid/trajectory/agent.jsonl"
    if [[ -f "$traj_file" ]]; then
      cp "$traj_file" "$out/trajectory.jsonl"
      echo "Trajectory copied to $out/trajectory.jsonl"
    else
      echo "WARNING: trajectory not found at $traj_file"
    fi
  else
    echo "WARNING: could not determine session ID"
  fi
  echo ""
}

analyze_trajectory() {
  local label="$1"
  local traj="$2"
  local format_errors=0
  local command_emits=0
  local tot_shell_turns=0

  if [[ -f "$traj" ]]; then
    format_errors=$(grep -c "FormatError" "$traj" 2>/dev/null || echo 0)
    command_emits=$(grep -c "<command>" "$traj" 2>/dev/null || echo 0)
    tot_shell_turns=$(grep -c "shell.agent" "$traj" 2>/dev/null || echo 0)
  fi

  echo "--- Analysis for $label ---"
  echo "  FormatError events:    $format_errors"
  echo "  command emits:         $command_emits"
  echo "  Shell-agent events:    $tot_shell_turns"
  echo ""
}

run_session "control" "none"
run_session "experimental" "user"

echo ""
echo "=== Results ==="
analyze_trajectory "control"       "$OUT_DIR/control/trajectory.jsonl"
analyze_trajectory "experimental" "$OUT_DIR/experimental/trajectory.jsonl"
echo "Done. Full output in $OUT_DIR"
