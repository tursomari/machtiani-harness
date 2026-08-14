#!/bin/bash
# print-mode-test.sh — e2e smoke test for `mct-agent run --exec` (R7).
#
# Verifies that `mct-agent run --shell-agent --prompt <goal> --exec` writes ONLY
# the raw final answer to stdout (byte-for-byte equal to the session's
# agent-final-answer.md after trailing-newline normalization) and that no
# styled/banner content leaks to stdout or stderr.
#
# Gating: this test needs a live LLM (no offline shell-agent fixture exists;
# agent/tests/fixtures/synthetic_shell_goal.txt is optional and the goal is
# otherwise created inline). It therefore runs only when MCT_LIVE_PRINT_TEST=1
# is set, following the skip convention used by agent/tests/run-live.sh
# ("Skipping <case> (needs ...)"), and prints a clear SKIP otherwise.
set -euo pipefail

# Unset ambient session env vars that can cause the runner to attempt
# resuming an inherited DearMachine session, which fails with a "session
# already active" lock error.
unset MACHTIANI_SESSION_ID MACHTIANI_SESSION_TEMP_ROOT MINISWE_FINAL_DIR 2>/dev/null || true

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

if [[ "${MCT_LIVE_PRINT_TEST:-}" != "1" ]]; then
  echo "SKIP: print-mode-test (needs MCT_LIVE_PRINT_TEST=1; live LLM required, no offline fixture exists)" >&2
  exit 0
fi

MCT_AGENT="${MCT_AGENT_BIN:-$REPO_ROOT/mct-agent}"
if [[ ! -x "$MCT_AGENT" ]]; then
  echo "ERROR: mct-agent binary not found or not executable: $MCT_AGENT" >&2
  exit 1
fi

CONFIG="$REPO_ROOT/.machtiani/config.toml"
if [[ ! -f "$CONFIG" ]]; then
  CONFIG="$HOME/.machtiani/config.toml"
fi
if [[ -f "$CONFIG" ]]; then
  export MACHTIANI_CONFIG="$CONFIG"
fi

TIMESTAMP=$(date +%s)
OUT_DIR="$SCRIPT_DIR/tmp/print-mode-test-$TIMESTAMP"
mkdir -p "$OUT_DIR"
STDOUT_LOG="$OUT_DIR/stdout.log"
STDERR_LOG="$OUT_DIR/stderr.log"

GOAL_FILE="$SCRIPT_DIR/fixtures/synthetic_shell_goal.txt"
if [[ -f "$GOAL_FILE" ]]; then
  GOAL="$(cat "$GOAL_FILE")"
  echo "Goal file: $GOAL_FILE"
else
  # No offline fixture exists; use a short trivial shell-agent goal inline.
  echo "NOTE: fixture file not found ($GOAL_FILE); using inline goal fallback" >&2
  GOAL="Run the shell command printf print-mode-smoke-ok and report its output."
fi

echo "=== print-mode smoke test ==="
echo "Output dir: $OUT_DIR"
echo ""

set +e
"$MCT_AGENT" run \
  --shell-agent \
  --prompt "$GOAL" \
  --exec \
  --max-turns 30 \
  --turn-timeout 180 \
  1>"$STDOUT_LOG" 2>"$STDERR_LOG"
rc=$?
set -e

echo "Exit code: $rc"

# Resolve the sessions root: project store first, then legacy local, then home.
SESSIONS_ROOT=""
if command -v python3 >/dev/null 2>&1; then
  STORE=$("$MCT_AGENT" project show --json 2>/dev/null |
    python3 -c 'import json, sys; print(json.load(sys.stdin)["store"])' 2>/dev/null || true)
  if [[ -n "$STORE" && -d "$STORE/sessions" ]]; then
    SESSIONS_ROOT="$STORE/sessions"
  fi
fi
if [[ -z "$SESSIONS_ROOT" && -d "$REPO_ROOT/.machtiani/sessions" ]]; then
  SESSIONS_ROOT="$REPO_ROOT/.machtiani/sessions"
fi
if [[ -z "$SESSIONS_ROOT" && -d "$HOME/.machtiani/sessions" ]]; then
  SESSIONS_ROOT="$HOME/.machtiani/sessions"
fi

# Parse the session id from stderr; fall back to the newest session dir.
SID=""
if [[ -f "$STDERR_LOG" ]]; then
  SID=$(grep -oP 'session-id: \K[^"]+' "$STDERR_LOG" 2>/dev/null | head -1 || true)
  if [[ -z "$SID" ]]; then
    SID=$(grep -oP 'session_id[:\"]+\K[^",}]+' "$STDERR_LOG" 2>/dev/null | head -1 || true)
  fi
  if [[ -z "$SID" ]]; then
    SID=$(grep -oP 'Session: \K\S+' "$STDERR_LOG" 2>/dev/null | head -1 || true)
  fi
fi
if [[ -z "$SID" && -d "$SESSIONS_ROOT" ]]; then
  SID=$(ls -1t "$SESSIONS_ROOT" 2>/dev/null | grep -E '^agent-' | head -1 || true)
  if [[ -z "$SID" ]]; then
    SID=$(ls -1t "$SESSIONS_ROOT" 2>/dev/null | head -1 || true)
  fi
fi
if [[ -n "$SID" ]]; then
  echo "Session ID: $SID"
fi

SESSION_DIR="$SESSIONS_ROOT/$SID"
FINAL_FILE="$SESSION_DIR/chat/agent-final-answer.md"
TRANSCRIPT_FILE="$SESSION_DIR/chat/agent-transcript.adoc"

fail=0

# (4) process exit code must be 0
if [[ "$rc" -ne 0 ]]; then
  echo "FAIL: mct-agent run exit code = $rc, want 0" >&2
  fail=1
fi

if [[ -z "$SID" ]]; then
  echo "FAIL: could not determine session id" >&2
  fail=1
fi

# (3) session dir must contain transcript and final answer
if [[ ! -f "$FINAL_FILE" ]]; then
  echo "FAIL: missing final answer file: $FINAL_FILE" >&2
  fail=1
fi
if [[ ! -f "$TRANSCRIPT_FILE" ]]; then
  echo "FAIL: missing transcript file: $TRANSCRIPT_FILE" >&2
  fail=1
fi

# (1) stdout.log must byte-for-byte equal agent-final-answer.md
if [[ -f "$FINAL_FILE" ]]; then
  if diff -q "$STDOUT_LOG" "$FINAL_FILE" >/dev/null 2>&1; then
    echo "PASS: stdout.log byte-for-byte equals agent-final-answer.md"
  else
    # Normalize trailing-newline differences only (printf '%s\n' "$(cat ...)"
    # collapses a trailing run of newlines to exactly one).
    norm_stdout="$(printf '%s\n' "$(cat "$STDOUT_LOG")")"
    norm_file="$(printf '%s\n' "$(cat "$FINAL_FILE")")"
    if [[ "$norm_stdout" == "$norm_file" ]]; then
      echo "NOTE: stdout.log and agent-final-answer.md differ only in trailing newlines;"
      echo "      normalized both with: printf '%s\\n' \"\$(cat ...)\" before comparing"
      echo "PASS: stdout.log equals agent-final-answer.md after trailing-newline normalization"
    else
      echo "FAIL: stdout.log differs from agent-final-answer.md" >&2
      diff "$STDOUT_LOG" "$FINAL_FILE" | head -40 >&2 || true
      fail=1
    fi
  fi
fi

# (2) stderr.log must contain no styled/banner content
for needle in "━" "Answer saved to:" "Resume this session:"; do
  if grep -qF "$needle" "$STDERR_LOG" 2>/dev/null; then
    echo "FAIL: stderr.log contains styled/banner content: $needle" >&2
    fail=1
  fi
done

# (5) stdout.log must not contain styled block artifacts
for needle in "━" "Answer saved to:" "Resume this session:" "mct-agent run"; do
  if grep -qF "$needle" "$STDOUT_LOG" 2>/dev/null; then
    echo "FAIL: stdout.log contains styled block artifact: $needle" >&2
    fail=1
  fi
done

# (6) Byte-level ANSI/escape rejection: stdout must contain no ESC byte.
# This catches any terminal-control sequence like ResetTerminal that
# leaks onto stdout (e.g. ESC[s, ESC[r).
if LC_ALL=C grep -q "$(printf '\033')" "$STDOUT_LOG" 2>/dev/null; then
  echo "FAIL: stdout.log contains ANSI escape bytes (ESC). Actual bytes:" >&2
  od -An -tx1 "$STDOUT_LOG" | head -5 >&2 || true
  fail=1
fi

echo ""
if [[ "$fail" -eq 0 ]]; then
  echo "PASS: print-mode smoke test"
  exit 0
fi
echo "FAIL: print-mode smoke test" >&2
exit 1
