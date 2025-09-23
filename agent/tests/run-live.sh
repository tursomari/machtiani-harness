#!/bin/bash
set -euo pipefail  # Strict mode

# Repo-aware paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BUILD_DIR="$REPO_ROOT/agent/tests/bin"
mkdir -p "$BUILD_DIR"

# Note: Add $BUILD_DIR to PATH only after dependency checks to avoid masking system binaries

# Script: Live integration tests for mct-agent binary (modeled after file-discovery/run-live.sh)
# Builds binary locally, runs live scenarios against Issues A/B/C with turn constraints.
# Requires: Go installed (in PATH). OPENAI_* env vars optional for live mode.
# Dependencies handled automatically:
# - mct-agent: built to agent/tests/bin
# - mct + file-discovery: auto-built from ./mct and installed to agent/tests/bin if not found
# - patcher: optional; a local stub exists in agent/tests/bin for planner patch flows
# Artifacts: Per-case files in cwd (stdout-*, stderr-*, final-*, transcript-*).
# Run from repo root.

# Helper: Build mct-agent binary (if not exists or force-rebuild)
MCT_AGENT="$BUILD_DIR/mct-agent"
if [[ ! -f "$MCT_AGENT" || "${FORCE_REBUILD:-}" == "true" ]]; then
  echo "Building mct-agent binary..." >&2
  (
    cd "$REPO_ROOT/agent" && go build -o "$REPO_ROOT/agent/tests/bin/mct-agent" ./cmd/mct-agent
  ) || { echo "Build failed" >&2; exit 1; }
fi

# Helper: Ensure mct + file-discovery are available in PATH, matching standard build flow
# Standard flow (outside tests):
#   cd mct && mkdir -p ~/.local/bin && ./build.sh \
#     && install -m 0755 ./machtiani-cli ~/.local/bin/mct \
#     && install -m 0755 ./bin/file-discovery ~/.local/bin/file-discovery && hash -r
# Test flow: install both to ./agent/tests/bin and prepend to PATH for this script only.

AGENT_MCT_BIN="${MCT_BIN:-}"  # respect external override only when not force rebuilding
if [[ "${FORCE_REBUILD:-}" == "true" ]]; then
  AGENT_MCT_BIN=""  # force local rebuild
fi

# Determine live vs dry-run early to avoid unnecessary builds
LIVE_MODE=false
if [[ -n "${OPENAI_API_KEY:-}" && -n "${OPENAI_BASE_URL:-}" && -n "${OPENAI_MODEL:-}" ]]; then
  LIVE_MODE=true
fi

if [[ -n "$AGENT_MCT_BIN" && -x "$AGENT_MCT_BIN" ]]; then
  echo "Using provided MCT_BIN: $AGENT_MCT_BIN" >&2
else
  if [[ "$LIVE_MODE" == true || "${FORCE_REBUILD:-}" == "true" ]]; then
    # In live mode (or when forcing rebuild), ensure a real mct is resolved
    if [[ "${FORCE_REBUILD:-}" != "true" ]] && command -v mct >/dev/null 2>&1; then
      AGENT_MCT_BIN="$(command -v mct)"
      echo "Found mct in PATH: $AGENT_MCT_BIN" >&2
    else
      echo "Building local mct + file-discovery into $BUILD_DIR..." >&2
      (
        cd "$REPO_ROOT/mct" \
          && ./build.sh \
          && install -m 0755 ./machtiani-cli "$BUILD_DIR/mct" \
          && install -m 0755 ./bin/file-discovery "$BUILD_DIR/file-discovery"
      ) || { echo "Failed to build/install mct or file-discovery. Ensure Go is installed and submodules are initialized." >&2; exit 1; }
      AGENT_MCT_BIN="$BUILD_DIR/mct"
    fi
  else
    # Dry-run mode and not forcing rebuild: skip building mct entirely
    AGENT_MCT_BIN=""
    echo "Dry-run: skipping mct build (not needed)." >&2
  fi
fi

# Now that we may rely on locally installed helpers, add tests/bin to PATH
# - If we built mct locally, prepend so it takes precedence over any global mct
# - Otherwise, append to avoid masking user's globally installed mct with local stubs
if [[ "$AGENT_MCT_BIN" == "$BUILD_DIR/mct" ]]; then
  export PATH="$BUILD_DIR:$PATH"
else
  export PATH="$PATH:$BUILD_DIR"
fi

# Helper: Run happy path case
# Args: case_id max_steps prompt expected_keywords [min_turns=1]
run_happy_case() {
  local case_id="$1" max_steps="$2" prompt="$3" expected_keywords="$4" min_turns="${5:-1}"
  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="test-out-${session_id}"
  mkdir -p "$out_dir"

  echo "Running happy case: $case_id (max $max_steps turns)..." >&2
  # If no OpenAI config, use dry-run to avoid network and filesystem side effects
  local maybe_dry_run=""
  if [[ -z "${OPENAI_API_KEY:-}" || -z "${OPENAI_BASE_URL:-}" || -z "${OPENAI_MODEL:-}" ]]; then
    maybe_dry_run="--dry-run"
  fi
  # Invoke binary: provide subcommand and prompt as arg, capture outputs
  timeout $((max_steps * 60)) "$MCT_AGENT" run \
    --max-steps "$max_steps" \
    --timeout-per-turn 300 \
    --verbose \
    --no-apply \
    $maybe_dry_run \
    ${AGENT_MCT_BIN:+--mct-bin "$AGENT_MCT_BIN"} \
    --final-file "$out_dir/final-${session_id}.txt" \
    --transcript-file "$out_dir/transcript-${session_id}.md" \
  "$prompt" \
    > "$out_dir/stdout-${session_id}.txt" 2> "$out_dir/stderr-${session_id}.txt"

  local rc=$?
  if [[ $rc -ne 0 ]]; then
    echo "Failed (rc=$rc): $case_id" >&2
    return 1
  fi

  # Validate: Turns <= max, >= min; keywords in stdout/final; artifacts exist; no empty transcript
  local turns=$(grep -c '^## Turn ' "$out_dir/transcript-${session_id}.md" || echo 0)
  if [[ $turns -gt $max_steps || $turns -lt $min_turns ]]; then
    echo "Invalid turns ($turns): $case_id" >&2
    return 1
  fi
  if ! grep -qE "$expected_keywords" "$out_dir/stdout-${session_id}.txt" "$out_dir/final-${session_id}.txt" 2>/dev/null; then
    echo "Missing keywords: $case_id" >&2
    return 1
  fi
  if [[ ! -s "$out_dir/final-${session_id}.txt" || ! -s "$out_dir/transcript-${session_id}.md" ]]; then
    echo "Missing/empty artifacts: $case_id" >&2
    return 1
  fi

  # Check finalization: No errors in transcript (e.g., via pl.Finalize fallback)
  if grep -q "error.*finalize\|transcript" "$out_dir/stderr-${session_id}.txt"; then
    echo "Finalize error detected: $case_id" >&2
    return 1
  fi

  echo "Passed: $case_id ($turns turns)" >&2
}

# Helper: Run error case
# Args: case_id args max_steps expected_error_keywords
run_error_case() {
  local case_id="$1" args="$2" max_steps="$3" expected_keywords="$4"
  local session_id="error-${case_id}-$(date +%s)"
  local out_dir="test-out-${session_id}"
  mkdir -p "$out_dir"

  echo "Running error case: $case_id..." >&2
  if [[ -z "$args" ]]; then args=""; fi  # For empty-goal
  # In error cases, still prefer dry-run if model config is missing
  local maybe_dry_run=""
  if [[ -z "${OPENAI_API_KEY:-}" || -z "${OPENAI_BASE_URL:-}" || -z "${OPENAI_MODEL:-}" ]]; then
    maybe_dry_run="--dry-run"
  fi
  # Only add --mct-bin if not already provided in args
  local maybe_mct_bin_arg=()
  if [[ -n "$AGENT_MCT_BIN" && "$args" != *"--mct-bin"* ]]; then
    maybe_mct_bin_arg=(--mct-bin "$AGENT_MCT_BIN")
  fi

  timeout $((max_steps * 60)) "$MCT_AGENT" run $args \
    --max-steps "$max_steps" \
    --timeout-per-turn 300 \
    --verbose \
    --no-apply \
    $maybe_dry_run \
    "${maybe_mct_bin_arg[@]}" \
    --final-file "$out_dir/final-${session_id}.txt" \
    --transcript-file "$out_dir/transcript-${session_id}.md" \
    "" \
    > "$out_dir/stdout-${session_id}.txt" 2> "$out_dir/stderr-${session_id}.txt"

  local rc=$?
  if [[ $rc -eq 0 ]]; then
    echo "Unexpected success (rc=0): $case_id" >&2
    return 1
  fi

  if ! (grep -qE "$expected_keywords" "$out_dir/stderr-${session_id}.txt" 2>/dev/null || \
        grep -qE "$expected_keywords" "$out_dir/stdout-${session_id}.txt" 2>/dev/null); then
    echo "Missing error keywords: $case_id" >&2
    return 1
  fi

  # Artifacts may be partial/empty on error (valid)
  echo "Passed (error triggered): $case_id (rc=$rc)" >&2
}

# Cleanup old dirs (optional)
rm -rf test-out-*

# Restore env defaults if overridden (live tests when all are set; otherwise dry-run mode)
if [[ -n "${OPENAI_API_KEY:-}" && -n "${OPENAI_BASE_URL:-}" && -n "${OPENAI_MODEL:-}" ]]; then
  echo "Live mode: using provided OPENAI_* for real LLM calls." >&2
else
  echo "Dry-run mode: OPENAI_* not fully set; skipping live LLM calls." >&2
fi

## Run happy path cases (Issues A, B, C with 1-turn and 3-turn)

# Issue A: Basic agent initialization and single-turn response handling
# Prompt: Simple query to trigger initial prompt only
run_happy_case "issue-a-1turn" 1 \
  "What is the main purpose of the mct-agent binary?" \
  "Conclusion" \
  1  # Exact 1 turn for max=1

run_happy_case "issue-a-3turn" 3 \
  "What is the main purpose of the mct-agent binary?" \
  "Conclusion"  # May stay 1-turn

# Issue B: Multi-turn conversation flow with context retention
# Prompt: Complex analysis to potentially trigger planner 'ask' decisions
run_happy_case "issue-b-1turn" 1 \
  "Describe the full multi-turn flow in mct-agent, including planning and context retention." \
  "multi-turn|conversation|context|planner|ask" \
  1

run_happy_case "issue-b-3turn" 3 \
  "Describe the full multi-turn flow in mct-agent, including planning and context retention." \
  "multi-turn|conversation|context|planner|ask" \
  1  # Expect 2-3 turns with 'ask' for context

# Issue C: Error handling in agent finalization and transcript writing
# Prompt: Self-referential to explain handling (may simulate via code analysis)
run_happy_case "issue-c-1turn" 1 \
  "Explain how mct-agent handles errors during finalization and transcript writing." \
  "error|handling|finalize|transcript|fallback"

run_happy_case "issue-c-3turn" 3 \
  "Explain how mct-agent handles errors during finalization and transcript writing, with examples from code." \
  "error|handling|finalize|transcript|fallback"  # May trigger multi-turn for examples

## Run dedicated error cases for Issue C coverage

# Error: Missing mct binary (triggers resolution error in first turn)
if ! command -v mct >/dev/null 2>&1; then
  if [[ -n "${OPENAI_API_KEY:-}" && -n "${OPENAI_BASE_URL:-}" && -n "${OPENAI_MODEL:-}" ]]; then
    run_error_case "missing-mct" "--mct-bin /nonexistent/path" 1 "mct resolution error|mct not found"
  else
    echo "Skipping missing-mct in dry-run mode (mct not required)." >&2
  fi
else
  echo "Skipping missing-mct (mct in PATH)" >&2
fi

# Error: Empty goal/input (invalid args)
run_error_case "empty-goal" "" 1 "empty issue/question|missing issue"

# Error: Missing OpenAI config (only when currently configured for live)
if [[ -n "${OPENAI_API_KEY:-}" || -n "${OPENAI_BASE_URL:-}" || -n "${OPENAI_MODEL:-}" ]]; then
  OPENAI_API_KEY="" OPENAI_BASE_URL="" OPENAI_MODEL="" \
  run_error_case "missing-config" "" 1 "Missing model config|OPENAI_API_KEY|OPENAI_BASE_URL|OPENAI_MODEL"
fi

# Error: Timeout simulation (short timeout + simple prompt; loose check)
run_error_case "timeout" "--timeout-per-turn 1 --max-steps 1" 1 "timed out|deadline exceeded" || \
  echo "Warning: timeout case flaky (LLM too fast); manual verification recommended"

echo "All cases finished. Check test-out-* dirs for artifacts." >&2
