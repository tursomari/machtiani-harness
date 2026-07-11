#!/bin/bash
#
# A/B test for the EnforceEarlyCommands shell-agent feature.
#
# Compares two sessions that run the same synthetic shell goal:
#   * control:      EnforceEarlyCommands=false (relaxed / legacy)
#   * experimental: EnforceEarlyCommands=true  (stricter on planner turns 0-2)
#
# For each session we copy the trajectory and report:
#   * FormatError event counts       (lower is better in experimental)
#   * <command> emission counts       (higher is better in experimental)
#   * shell-agent turn counts         (proxy for session length)
#
# Both sessions use the same FewShotVariant as the baseline session, so the
# only difference is the EnforceEarlyCommands flag.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
GOAL_FILE="$SCRIPT_DIR/fixtures/synthetic_shell_goal.txt"
TIMESTAMP=$(date +%s)
OUT_DIR="$SCRIPT_DIR/tmp/enforce-early-commands-ab-$TIMESTAMP"
mkdir -p "$OUT_DIR"

MCT_AGENT="$REPO_ROOT/mct-agent"
CONFIG="$REPO_ROOT/.machtiani/config.toml"
export MACHTIANI_CONFIG="$CONFIG"

# Few-shot variant is held constant across both runs so the comparison
# isolates the EnforceEarlyCommands flag.
FEW_SHOT_VARIANT="${MACHTIANI_SHELL_AGENT_FEW_SHOT_VARIANT:-instance}"

echo "=== A/B EnforceEarlyCommands Test ==="
echo "Output dir:        $OUT_DIR"
echo "Goal file:         $GOAL_FILE"
echo "Few-shot variant:  $FEW_SHOT_VARIANT"
echo ""

run_session() {
  local label="$1"
  local enforce="$2"
  local out="$OUT_DIR/${label}"
  mkdir -p "$out"
  echo "--- Running $label (EnforceEarlyCommands=$enforce) ---"

  local stdout_log="$out/stdout.log"
  local stderr_log="$out/stderr.log"

  MACHTIANI_SHELL_AGENT_FEW_SHOT_VARIANT="$FEW_SHOT_VARIANT" \
  MACHTIANI_SHELL_AGENT_ENFORCE_EARLY_COMMANDS="$enforce" \
    "$MCT_AGENT" run \
      --text "$(cat "$GOAL_FILE")" \
      --shell-agent \
      --max-steps 60 \
      --timeout-per-turn 180 \
      1>"$stdout_log" 2>"$stderr_log"

  local rc=$?

  local sid
  # Try several known formats from stdout/stderr.
  sid=$(grep -oE "Session ID: agent-[0-9TZ]+-[0-9]+" "$stdout_log" "$stderr_log" 2>/dev/null \
        | head -1 | awk '{print $NF}' || true)
  if [[ -z "$sid" ]]; then
    sid=$(grep -oE "Session: agent-[0-9TZ]+-[0-9]+" "$stderr_log" 2>/dev/null \
          | head -1 | awk '{print $2}' || true)
  fi
  if [[ -z "$sid" ]]; then
    sid=$(grep -oE "session agent-[0-9TZ]+-[0-9]+" "$stdout_log" "$stderr_log" 2>/dev/null \
          | head -1 | awk '{print $2}' || true)
  fi
  if [[ -z "$sid" ]]; then
    sid=$(grep -oE "/sessions/(agent-[0-9TZ]+-[0-9]+)/trajectory" "$stderr_log" 2>/dev/null \
          | head -1 | awk -F/ '{print $3}' || true)
  fi
  if [[ -z "$sid" ]]; then
    sid=$(grep -oE "session[-_]?id[=:]? *\"?([A-Za-z0-9_-]+)\"?" "$stderr_log" 2>/dev/null \
          | head -1 | sed -E 's/.*["]?([A-Za-z0-9_-]+)"?$/\1/' || true)
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
  local planner_responses=0
  local planner_requests=0
  local turn_starts=0
  local turn_ends=0
  local llm_errors=0
  local early_turn_msgs=0
  local fmt_err_refs=0
  local total_events=0

  if [[ -f "$traj" ]]; then
    # Count each pattern in a single pass; use grep -c which prints the
    # match count and exits 0 when found, 1 when none (we suppress the
    # exit-code so set -e doesn't trip; we just want the count).
    total_events=$(wc -l < "$traj" | tr -d ' ')
    planner_responses=$(grep -c '"kind":"planner.response"' "$traj" 2>/dev/null) || planner_responses=0
    planner_requests=$(grep -c '"kind":"planner.request"'  "$traj" 2>/dev/null) || planner_requests=0
    turn_starts=$(grep -c '"kind":"agent.turn.start"'    "$traj" 2>/dev/null) || turn_starts=0
    turn_ends=$(grep -c '"kind":"agent.turn.end"'        "$traj" 2>/dev/null) || turn_ends=0
    llm_errors=$(grep -c '"kind":"llm.request.error"'     "$traj" 2>/dev/null) || llm_errors=0
    early_turn_msgs=$(grep -c 'On this early step'         "$traj" 2>/dev/null) || early_turn_msgs=0
    fmt_err_refs=$(grep -c 'FormatError'                   "$traj" 2>/dev/null) || fmt_err_refs=0
    # grep -c returns "0" with newline; strip.
    planner_responses=$(printf '%s' "$planner_responses" | tr -d '\n')
    planner_requests=$(printf '%s' "$planner_requests" | tr -d '\n')
    turn_starts=$(printf '%s' "$turn_starts" | tr -d '\n')
    turn_ends=$(printf '%s' "$turn_ends" | tr -d '\n')
    llm_errors=$(printf '%s' "$llm_errors" | tr -d '\n')
    early_turn_msgs=$(printf '%s' "$early_turn_msgs" | tr -d '\n')
    fmt_err_refs=$(printf '%s' "$fmt_err_refs" | tr -d '\n')
  fi

  echo "--- Analysis for $label ---"
  echo "  total events:                $total_events"
  echo "  planner.requests / .response: $planner_requests / $planner_responses"
  echo "  agent.turn.start / .end:     $turn_starts / $turn_ends"
  echo "  llm.request.error:           $llm_errors"
  echo "  early-turn message emits:    $early_turn_msgs"
  echo "  FormatError refs (any):      $fmt_err_refs"
  echo ""
}

run_session "control"      "false"
run_session "experimental" "true"

echo ""
echo "=== Results ==="
analyze_trajectory "control"      "$OUT_DIR/control/trajectory.jsonl"
analyze_trajectory "experimental" "$OUT_DIR/experimental/trajectory.jsonl"

# Side-by-side comparison.
ctrl_traj="$OUT_DIR/control/trajectory.jsonl"
exp_traj="$OUT_DIR/experimental/trajectory.jsonl"
if [[ -f "$ctrl_traj" && -f "$exp_traj" ]]; then
  echo "=== A/B comparison ==="
  ctrl_lines=$(wc -l < "$ctrl_traj" | tr -d ' ')
  exp_lines=$(wc -l < "$exp_traj" | tr -d ' ')
  echo "  Trajectory length: control=$ctrl_lines experimental=$exp_lines"
  if [[ "$ctrl_lines" == "$exp_lines" ]]; then
    echo "  OK: identical trajectory length (no extra retries caused by enforcement)"
  else
    echo "  NOTE: trajectory lengths differ (expected: identical or experimental slightly shorter)"
  fi
fi
echo "Done. Full output in $OUT_DIR"
