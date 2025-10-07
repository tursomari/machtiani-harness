#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
hash -r 2>/dev/null || true

PYTHON_BIN="${PYTHON_BIN:-python3}"
if ! command -v "$PYTHON_BIN" >/dev/null 2>&1; then
  if command -v python >/dev/null 2>&1; then
    PYTHON_BIN="python"
  fi
fi

stat_mtime() {
  local path="$1"
  if stat -c %Y "$path" >/dev/null 2>&1; then
    stat -c %Y "$path"
  elif stat -f %m "$path" >/dev/null 2>&1; then
    stat -f %m "$path"
  else
    echo 0
  fi
}

git_head() {
  local dir="$1"
  git -C "$dir" rev-parse --short=12 HEAD 2>/dev/null || echo ""
}

git_dirty() {
  local dir="$1"
  if git -C "$dir" status --porcelain >/dev/null 2>&1; then
    if [ -n "$(git -C "$dir" status --porcelain 2>/dev/null)" ]; then
      echo "dirty"
    else
      echo "clean"
    fi
  else
    echo "unknown"
  fi
}

git_last_change() {
  local dir="$1"
  git -C "$dir" log -1 --format=%ct -- . 2>/dev/null || echo 0
}

normalize_dirty() {
  case "$1" in
    dirty|Dirty|DIRTY|true|TRUE|1) echo "dirty" ;;
    clean|Clean|CLEAN|false|FALSE|0) echo "clean" ;;
    unknown|UNKNOWN) echo "unknown" ;;
    *) echo "" ;;
  esac
}

contains_keywords() {
  local pattern="$1"
  shift || true
  if (( $# == 0 )); then
    return 1
  fi
  "$PYTHON_BIN" - "$pattern" "$@" <<'PY'
import pathlib
import re
import sys

pattern = sys.argv[1]
files = sys.argv[2:]
if not files:
    sys.exit(1)

regex = re.compile(pattern)
ansi_csi = re.compile(r'\x1B\[[0-9;?]*[ -/]*[@-~]')
ansi_osc = re.compile(r'\x1B\][^\x07]*\x07')

for name in files:
    path = pathlib.Path(name)
    if not path.exists():
        continue
    try:
        data = path.read_bytes()
    except OSError:
        continue
    text = data.decode('utf-8', errors='ignore')
    text = ansi_csi.sub('', text)
    text = ansi_osc.sub('', text)
    text = text.replace('\r', '')
    if regex.search(text):
        sys.exit(0)

sys.exit(1)
PY
}

check_bin() {
  local out_var="$1"
  local name="$2"
  local src_dir="$3"
  local version_flag="${4:-}"

  local path
  if ! path="$(command -v "$name" 2>/dev/null)"; then
    echo "ERROR: $name not found in PATH" >&2
    exit 1
  fi
  printf -v "$out_var" '%s' "$path"

  echo "Resolved $name: $path" >&2

  local version_output=""
  if [ -n "$version_flag" ]; then
    if version_output="$("$path" "$version_flag" 2>/dev/null)"; then
      if [ -n "$version_output" ]; then
        echo "$version_output" >&2
      fi
    fi
  fi

  local go_output=""
  if go_output="$(go version -m "$path" 2>/dev/null)"; then
    if [ -n "$go_output" ]; then
      echo "$go_output" >&2
    fi
  fi

  local got_commit=""
  local got_dirty_raw=""
  if [ -n "$version_output" ]; then
    case "$name" in
      mct-agent|patcher)
        got_commit="$(printf '%s\n' "$version_output" | awk -F': ' '/^commit:/ {print $2; exit}')"
        got_commit="${got_commit:0:12}"
        got_dirty_raw="$(printf '%s\n' "$version_output" | awk -F': ' '/^dirty:/ {print $2; exit}')"
        ;;
      file-discovery)
        got_commit="$(printf '%s\n' "$version_output" | awk -F': ' '/^commit:/ {print $2; exit}')"
        if [ -z "$got_commit" ]; then
          got_commit="$(printf '%s\n' "$version_output" | sed -n 's/^dev-\([0-9a-fA-F]\{7,40\}\).*/\1/p' | head -n1)"
        fi
        got_commit="${got_commit:0:12}"
        got_dirty_raw="$(printf '%s\n' "$version_output" | awk -F': ' '/^dirty:/ {print $2; exit}')"
        ;;
    esac
  fi
  if [ -z "$got_commit" ] && [ -n "$go_output" ]; then
    got_commit="$(printf '%s\n' "$go_output" | awk '{
      for (i = 1; i <= NF; i++) {
        if ($i ~ /^vcs\.revision/) {
          if (index($i, "=") > 0) {
            split($i, parts, "=");
            print substr(parts[2], 1, 12);
            exit;
          } else if (i + 1 <= NF) {
            print substr($(i+1), 1, 12);
            exit;
          }
        }
      }
    }')"
  fi
  if [ -z "$got_dirty_raw" ] && [ -n "$go_output" ]; then
    got_dirty_raw="$(printf '%s\n' "$go_output" | awk '{
      for (i = 1; i <= NF; i++) {
        if ($i ~ /^vcs\.modified/) {
          if (index($i, "=") > 0) {
            split($i, parts, "=");
            print parts[2];
            exit;
          } else if (i + 1 <= NF) {
            print $(i+1);
            exit;
          }
        }
      }
    }')"
  fi

  local want_commit
  want_commit="$(git_head "$src_dir")"
  if [ -n "$want_commit" ] && [ -n "$got_commit" ] && [ "$want_commit" != "$got_commit" ]; then
    echo "ERROR: $name commit mismatch (got $got_commit, want $want_commit)" >&2
    exit 1
  fi

  local want_dirty
  want_dirty="$(git_dirty "$src_dir")"
  local got_dirty
  got_dirty="$(normalize_dirty "$got_dirty_raw")"
  if [ -n "$got_dirty" ] && [ "$want_dirty" != "unknown" ] && [ -n "$want_dirty" ]; then
    if [ "$want_dirty" = "clean" ] && [ "$got_dirty" = "dirty" ]; then
      echo "ERROR: $name dirty flag mismatch (binary $got_dirty vs repo $want_dirty)" >&2
      exit 1
    elif [ "$want_dirty" = "dirty" ] && [ "$got_dirty" = "clean" ]; then
      echo "NOTE: $name dirty flag mismatch (binary clean vs repo dirty); continuing" >&2
    elif [ "$want_dirty" != "$got_dirty" ]; then
      echo "ERROR: $name dirty flag mismatch (binary $got_dirty vs repo $want_dirty)" >&2
      exit 1
    fi
  fi

  local bin_mtime
  bin_mtime="$(stat_mtime "$path")"
  local last_change
  last_change="$(git_last_change "$src_dir")"
  if [ "$last_change" -gt 0 ] && [ "$bin_mtime" -lt "$last_change" ]; then
    echo "ERROR: $name binary appears stale (mtime older than last source change)" >&2
    exit 1
  fi

  echo "--" >&2
}

echo "== Preflight: verifying PATH binaries ==" >&2
check_bin MCT_AGENT_BIN mct-agent "$REPO_ROOT/agent" "--version"
check_bin MCT_PATH mct "$REPO_ROOT/mct" "--version"
check_bin FILE_DISCOVERY_BIN file-discovery "$REPO_ROOT/mct/submodules/file-discovery" "-version"
check_bin PATCHER_BIN patcher "$REPO_ROOT/patcher" "--version"
echo "Preflight OK" >&2

echo >&2

LIVE_MODE=false
if [[ -n "${OPENAI_API_KEY:-}" && -n "${OPENAI_BASE_URL:-}" && -n "${OPENAI_MODEL:-}" ]]; then
  LIVE_MODE=true
fi

TEST_MODEL_ALIAS=""
TMP_ROOT="$SCRIPT_DIR/tmp"
mkdir -p "$TMP_ROOT"
CONFIG_ROOT="$(mktemp -d "$TMP_ROOT/config.XXXXXX")"
TEST_CONFIG_FILE=""

generate_test_config() {
  local config_dir="$CONFIG_ROOT/.machtiani"
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

  TEST_CONFIG_FILE="$config_file"
  echo "$config_file"
}

cleanup_config() {
  if [[ "${KEEP_TEST_CONFIG:-}" == "true" ]]; then
    return
  fi
  if [[ -n "${CONFIG_ROOT:-}" && -d "$CONFIG_ROOT" ]]; then
    rm -rf "$CONFIG_ROOT"
  fi
}
trap cleanup_config EXIT

TEST_CONFIG_FILE="$(generate_test_config)"
export MACHTIANI_CONFIG="$TEST_CONFIG_FILE"

AGENT_RUNTIME_ARGS=(--model "$TEST_MODEL_ALIAS")
if [[ "$LIVE_MODE" != true ]]; then
  AGENT_RUNTIME_ARGS+=(--dry-run)
fi

MCT_AGENT="$MCT_AGENT_BIN"

run_happy_case() {
  local case_id="$1" max_steps="$2" prompt="$3" expected_keywords="$4" min_turns="${5:-1}"
  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="test-out-${session_id}"
  mkdir -p "$out_dir"

  echo "Running happy case: $case_id (max $max_steps turns)..." >&2
  local -a cmd=(
    timeout $((max_steps * 180)) "$MCT_AGENT" run
    --max-steps "$max_steps"
    --timeout-per-turn 300
    --verbose
    --no-apply
  )
  if ((${#AGENT_RUNTIME_ARGS[@]})); then
    cmd+=("${AGENT_RUNTIME_ARGS[@]}")
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

  local turns=$(grep -c '^## Turn ' "$out_dir/transcript-${session_id}.md" || echo 0)
  if [[ $turns -gt $max_steps || $turns -lt $min_turns ]]; then
    echo "Invalid turns ($turns): $case_id" >&2
    return 1
  fi
  local -a keyword_files=("$out_dir/stdout-${session_id}.txt")
  if [[ "$LIVE_MODE" == true ]]; then
    keyword_files+=("$out_dir/final-${session_id}.txt")
  fi
  if ! contains_keywords "$expected_keywords" "${keyword_files[@]}"; then
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
  if grep -qE "(Finalizer|Transcript write|Final file write) error:" "$out_dir/stderr-${session_id}.txt"; then
    echo "Finalize error detected: $case_id" >&2
    return 1
  fi

  echo "Passed: $case_id ($turns turns)" >&2
}

run_error_case() {
  local case_id="$1" args="$2" max_steps="$3" expected_keywords="$4" prompt_override="${5:-}"
  local session_id="error-${case_id}-$(date +%s)"
  local out_dir="test-out-${session_id}"
  mkdir -p "$out_dir"

  echo "Running error case: $case_id..." >&2
  local -a provided_args=()
  if [[ -n "$args" ]]; then
    # shellcheck disable=SC2206
    provided_args=($args)
  fi
  local has_timeout=false
  local has_max_steps=false
  for ((i=0; i<${#provided_args[@]}; i++)); do
    case "${provided_args[i]}" in
      --timeout-per-turn|--timeout-per-turn=*)
        has_timeout=true
        ;;
      --max-steps|--max-steps=*)
        has_max_steps=true
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

  if ! contains_keywords "$expected_keywords" \
      "$out_dir/stderr-${session_id}.txt" \
      "$out_dir/stdout-${session_id}.txt"; then
    echo "Missing error keywords: $case_id" >&2
    return 1
  fi

  echo "Passed (error triggered): $case_id (rc=$rc)" >&2
}

rm -rf test-out-*

if [[ "$LIVE_MODE" == true ]]; then
  echo "Live mode: using generated config.toml from OPENAI_* env." >&2
else
  echo "Dry-run mode: using generated config.toml with stubbed provider; --dry-run enabled." >&2
fi

run_happy_case "issue-a-1turn" 1 \
  "What is the main purpose of the mct-agent binary?" \
  "===> FINAL RESPONSE <===" \
  1

run_happy_case "issue-a-3turn" 3 \
  "What is the main purpose of the mct-agent binary?" \
  "===> FINAL RESPONSE <==="

run_happy_case "issue-b-1turn" 1 \
  "Describe the full multi-turn flow in mct-agent, including planning and context retention." \
  "multi-turn|conversation|context|planner|ask" \
  1

run_happy_case "issue-b-3turn" 3 \
  "Describe the full multi-turn flow in mct-agent, including planning and context retention." \
  "multi-turn|conversation|context|planner|ask" \
  1

run_happy_case "issue-c-1turn" 1 \
  "Explain how mct-agent handles errors during finalization and transcript writing." \
  "error|handling|finalize|transcript|fallback"

run_happy_case "issue-c-3turn" 3 \
  "Explain how mct-agent handles errors during finalization and transcript writing, with examples from code." \
  "error|handling|finalize|transcript|fallback"

run_error_case "empty-goal" "" 1 "empty issue/question|missing issue"

if [[ "$LIVE_MODE" == true ]]; then
  MACHTIANI_CONFIG="/nonexistent/machtiani-config.toml" OPENAI_API_KEY="" OPENAI_BASE_URL="" OPENAI_MODEL="" \
  run_error_case "missing-config" "" 1 "Missing model config|Model resolution error|MACHTIANI_CONFIG" \
    "Explain how the agent chooses its model runtime."
else
  echo "Skipping missing-config error case in dry-run mode (requires live env)." >&2
fi

echo "Skipping missing-mct and timeout simulations: preflight ensures PATH binaries and no stub overrides." >&2

echo "All cases finished. Check test-out-* dirs for artifacts." >&2
