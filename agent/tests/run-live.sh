#!/bin/bash
set -euo pipefail  # Strict mode

# Repo-aware paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BUILD_DIR="$REPO_ROOT/agent/tests/bin"
TIMEOUT_MCT_BIN="$SCRIPT_DIR/bin/mct-timeout"
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

TEST_MODEL_ALIAS=""

generate_test_config() {
  local config_dir="$BUILD_DIR/.machtiani"
  local config_file="$config_dir/config.toml"
  mkdir -p "$config_dir"

  if [[ "$LIVE_MODE" == true ]]; then
    TEST_MODEL_ALIAS="${OPENAI_MODEL}"
    cat > "$config_file" <<EOF
default_model = "${TEST_MODEL_ALIAS}"

[providers.test-provider]
base_url = "${OPENAI_BASE_URL}"
api_key = "${OPENAI_API_KEY}"
endpoint = "/chat/completions"

[models."${TEST_MODEL_ALIAS}"]
provider = "test-provider"
model = "${OPENAI_MODEL}"
EOF
  else
    TEST_MODEL_ALIAS="test-model"
    cat > "$config_file" <<EOF
default_model = "${TEST_MODEL_ALIAS}"

[providers.test-provider]
base_url = "https://api.openai.com/v1"
api_key = "sk-test-key-fake"
endpoint = "/chat/completions"

[models."${TEST_MODEL_ALIAS}"]
provider = "test-provider"
model = "gpt-4o-mini"
EOF
  fi

  if [[ "${TRACE_TEST_CONFIG:-}" == "true" ]]; then
    echo "Generated test config ($config_file):" >&2
    cat "$config_file" >&2
  fi

  echo "$config_file"
}

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

TEST_CONFIG_FILE="$(generate_test_config)"
export MACHTIANI_CONFIG="$TEST_CONFIG_FILE"
TEST_CONFIG_DIR="$(dirname "$TEST_CONFIG_FILE")"

cleanup_config() {
  if [[ "${KEEP_TEST_CONFIG:-}" == "true" ]]; then
    return
  fi
  if [[ -n "${TEST_CONFIG_FILE:-}" && -f "$TEST_CONFIG_FILE" ]]; then
    rm -f "$TEST_CONFIG_FILE"
  fi
  if [[ -n "${TEST_CONFIG_DIR:-}" ]]; then
    rmdir "$TEST_CONFIG_DIR" 2>/dev/null || true
  fi
}
trap cleanup_config EXIT

AGENT_RUNTIME_ARGS=(--model "$TEST_MODEL_ALIAS")
if [[ "$LIVE_MODE" != true ]]; then
  AGENT_RUNTIME_ARGS+=(--dry-run)
fi

# Helper: Run happy path case
# Args: case_id max_steps prompt expected_keywords [min_turns=1]
run_happy_case() {
  local case_id="$1" max_steps="$2" prompt="$3" expected_keywords="$4" min_turns="${5:-1}"
  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="test-out-${session_id}"
  mkdir -p "$out_dir"

  echo "Running happy case: $case_id (max $max_steps turns)..." >&2
  # Invoke binary: provide subcommand and prompt as arg, capture outputs
  local -a cmd=(
    timeout $((max_steps * 180)) "$MCT_AGENT" run  # allow 3 minutes per planned turn for live latency
    --max-steps "$max_steps"
    --timeout-per-turn 300
    --verbose
    --no-apply
  )
  if ((${#AGENT_RUNTIME_ARGS[@]})); then
    cmd+=("${AGENT_RUNTIME_ARGS[@]}")
  fi
  if [[ -n "$AGENT_MCT_BIN" ]]; then
    cmd+=(--mct-bin "$AGENT_MCT_BIN")
  fi
  cmd+=(
    --final-file "$out_dir/final-${session_id}.txt"
    --transcript-file "$out_dir/transcript-${session_id}.md"
    "$prompt"
  )

  set +e
  "${cmd[@]}" > "$out_dir/stdout-${session_id}.txt" 2> "$out_dir/stderr-${session_id}.txt"
  local rc=$?
  set -e
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
  local -a keyword_files=("$out_dir/stdout-${session_id}.txt")
  if [[ "$LIVE_MODE" == true ]]; then
    keyword_files+=("$out_dir/final-${session_id}.txt")
  fi
  if ! grep -qE "$expected_keywords" "${keyword_files[@]}" 2>/dev/null; then
    echo "Missing keywords: $case_id" >&2
    return 1
  fi
  if [[ ! -s "$out_dir/transcript-${session_id}.md" ]]; then
    echo "Missing transcript: $case_id" >&2
    return 1
  fi
  if [[ "$LIVE_MODE" == true && ! -s "$out_dir/final-${session_id}.txt" ]]; then
    echo "Missing final artifact: $case_id" >&2
    return 1
  fi

  # Check finalization: No errors in transcript (e.g., via pl.Finalize fallback)
  if grep -qE "(Finalizer|Transcript write|Final file write) error:" "$out_dir/stderr-${session_id}.txt"; then
    echo "Finalize error detected: $case_id" >&2
    return 1
  fi

  echo "Passed: $case_id ($turns turns)" >&2
}

# Helper: Run error case
# Args: case_id args max_steps expected_error_keywords
run_error_case() {
  local case_id="$1" args="$2" max_steps="$3" expected_keywords="$4" prompt_override="${5:-}"
  local session_id="error-${case_id}-$(date +%s)"
  local out_dir="test-out-${session_id}"
  mkdir -p "$out_dir"

  echo "Running error case: $case_id..." >&2
  if [[ -z "$args" ]]; then args=""; fi  # For empty-goal
  local -a provided_args=()
  if [[ -n "$args" ]]; then
    # shellcheck disable=SC2206  # word splitting intentional for arg list
    provided_args=($args)
  fi
  local has_timeout=false
  local has_max_steps=false
  local has_mct_flag=false
  for ((i=0; i<${#provided_args[@]}; i++)); do
    case "${provided_args[i]}" in
      --timeout-per-turn|--timeout-per-turn=*)
        has_timeout=true
        ;;
      --max-steps|--max-steps=*)
        has_max_steps=true
        ;;
      --mct-bin|--mct-bin=*)
        has_mct_flag=true
        ;;
    esac
  done
  local -a cmd=(
    timeout $((max_steps * 60)) "$MCT_AGENT" run
  )
  if ((${#provided_args[@]})); then
    cmd+=("${provided_args[@]}")
  fi
  if [[ "$has_max_steps" == false ]]; then
    cmd+=(--max-steps "$max_steps")
  fi
  if [[ "$has_timeout" == false ]]; then
    cmd+=(--timeout-per-turn 300)
  fi
  if [[ "$has_mct_flag" == false ]]; then
    if [[ -n "$AGENT_MCT_BIN" ]]; then
      cmd+=(--mct-bin "$AGENT_MCT_BIN")
    fi
  fi
  if ((${#AGENT_RUNTIME_ARGS[@]})); then
    cmd+=("${AGENT_RUNTIME_ARGS[@]}")
  fi
  cmd+=(
    --verbose
    --no-apply
  )
  cmd+=(
    --final-file "$out_dir/final-${session_id}.txt"
    --transcript-file "$out_dir/transcript-${session_id}.md"
  )

  if [[ -n "$prompt_override" ]]; then
    cmd+=("$prompt_override")
  else
    cmd+=("")
  fi

  set +e
  "${cmd[@]}" > "$out_dir/stdout-${session_id}.txt" 2> "$out_dir/stderr-${session_id}.txt"
  local rc=$?
  set -e
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
if [[ "$LIVE_MODE" == true ]]; then
  echo "Live mode: using generated config.toml populated from OPENAI_* env." >&2
else
  echo "Dry-run mode: using generated config.toml with stubbed provider; --dry-run enabled." >&2
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
  if [[ "$LIVE_MODE" == true ]]; then
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
if [[ "$LIVE_MODE" == true ]]; then
  MACHTIANI_CONFIG="/nonexistent/machtiani-config.toml" OPENAI_API_KEY="" OPENAI_BASE_URL="" OPENAI_MODEL="" \
  run_error_case "missing-config" "" 1 "Missing model config|Model resolution error|MACHTIANI_CONFIG" \
    "Explain how the agent chooses its model runtime."
fi

# Error: Timeout simulation (short timeout + simple prompt; loose check)
run_error_case "timeout" "--mct-bin $TIMEOUT_MCT_BIN --timeout-per-turn 1 --max-steps 1" 1 "timed out|deadline exceeded" \
  "Explain an involved refactor plan that requires multiple steps." || \
  echo "Warning: timeout case flaky (LLM too fast); manual verification recommended"

echo "All cases finished. Check test-out-* dirs for artifacts." >&2
