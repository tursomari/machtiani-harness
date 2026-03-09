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

SHELL_AGENT_MARKER_PREFIX="mct-swe-agent-finale"
SHELL_AGENT_MARKER_SUFFIX=".txt"

# Marker cleanup runs per case; we avoid traps so the harness stays safe if
# cases are later executed concurrently in subshells.
shell_marker_checks_enabled() {
  case "${CHECK_SHELL_AGENT_MARKERS:-true}" in
    1|true|TRUE|yes|YES|on|ON) return 0 ;;
    0|false|FALSE|no|NO|off|OFF) return 1 ;;
    *) return 0 ;;
  esac
}

duration_to_seconds() {
  local duration="$1"
  "$PYTHON_BIN" - "$duration" <<'PY'
import re
import sys

duration = sys.argv[1]
total = 0.0
pattern = re.compile(r'(\d+(?:\.\d+)?)([hms])')
matches = pattern.findall(duration)
if not matches:
    print(0)
    sys.exit(0)

for value, unit in matches:
    value = float(value)
    if unit == "h":
        total += value * 3600
    elif unit == "m":
        total += value * 60
    elif unit == "s":
        total += value

print(int(total))
PY
}

seed_marker_with_age() {
  local marker_dir="$1"
  local marker_name="$2"
  local age_seconds="$3"

  mkdir -p "$marker_dir"
  local marker_path="$marker_dir/$marker_name"
  printf 'stale' > "$marker_path"
  "$PYTHON_BIN" - "$marker_path" "$age_seconds" <<'PY'
import os
import sys
import time

path = sys.argv[1]
age = int(sys.argv[2])
old = time.time() - age
os.utime(path, (old, old))
PY
  echo "$marker_path"
}

assert_shell_agent_marker_cleanup() {
  local marker_dir="$1"
  local stale_marker="$2"
  local recent_marker="$3"
  local case_id="$4"

  if [[ -e "$stale_marker" ]]; then
    echo "Stale shell-agent marker not removed: $stale_marker ($case_id)" >&2
    if [[ -d "$marker_dir" ]]; then
      ls -al "$marker_dir" >&2 || true
    fi
    return 1
  fi

  if [[ -n "$recent_marker" && ! -e "$recent_marker" ]]; then
    echo "Recent shell-agent marker unexpectedly removed: $recent_marker ($case_id)" >&2
    if [[ -d "$marker_dir" ]]; then
      ls -al "$marker_dir" >&2 || true
    fi
    return 1
  fi

  if [[ -d "$marker_dir" ]]; then
    local -a leftover=()
    while IFS= read -r -d '' path; do
      if [[ -n "$recent_marker" && "$path" == "$recent_marker" ]]; then
        continue
      fi
      leftover+=("$path")
    done < <(find "$marker_dir" -maxdepth 1 -type f -name "${SHELL_AGENT_MARKER_PREFIX}*${SHELL_AGENT_MARKER_SUFFIX}" -print0)
    if (( ${#leftover[@]} )); then
      echo "Shell-agent markers still present after run: $marker_dir ($case_id)" >&2
      ls -al "$marker_dir" >&2 || true
      return 1
    fi
  fi

  return 0
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

shell_agent_available() {
  command -v shell-agent >/dev/null 2>&1
}

start_llm_stub_server() {
  local state_file="$1"
  local port_file="$2"

  "$PYTHON_BIN" - "$state_file" "$port_file" <<'PY' &
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

state_path = sys.argv[1]
port_path = sys.argv[2]

counts = {
    "plan": 0,
    "ask": 0,
    "monitor": 0,
    "mixed_monitor": 0,
    "preflight": 0,
    "other": 0,
}

def write_state():
    try:
        with open(state_path, "w", encoding="utf-8") as fh:
            json.dump(counts, fh)
    except OSError:
        pass

class Handler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        return

    def do_POST(self):
        length = int(self.headers.get("content-length", "0"))
        raw = self.rfile.read(length)
        try:
            data = json.loads(raw.decode("utf-8"))
        except Exception:
            self.send_response(400)
            self.end_headers()
            return
        messages = data.get("messages", [])
        content = "\n".join(m.get("content", "") for m in messages if isinstance(m, dict))

        reply = "Stub response."
        if "Decision menu (choose exactly one)" in content:
            counts["plan"] += 1
            reply = "Decision: ask"
        elif "You are generating the next Ask for mct." in content:
            counts["ask"] += 1
            if "Guardrail:" in content:
                reply = "Ask Mode: no-shell\nAsk: Summarize the staged planner menu flow."
            else:
                reply = "Ask Mode: both\nNo-shell: Summarize the staged planner menu flow.\nShell: Run `git diff --stat` and report the output."
        elif "You are a guard that checks whether an ask mixes no-shell and shell actions." in content:
            counts["mixed_monitor"] += 1
            reply = "{\"is_mixed\":false,\"reason\":\"already split\",\"rewrite\":\"\"}"
        elif "You are a guard that checks whether an ask is requesting file changes or patches." in content:
            counts["monitor"] += 1
            if counts["monitor"] == 1:
                reply = "{\"has_patch_intent\":true,\"reason\":\"mentions running git diff and could lead to patches\"}"
            else:
                reply = "{\"has_patch_intent\":false,\"reason\":\"no patch intent\"}"
        elif "You classify user requests for a developer assistant" in content:
            counts["preflight"] += 1
            reply = "content"
        else:
            counts["other"] += 1

        write_state()
        payload = {
            "id": "stub-1",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": data.get("model", "stub-model"),
            "choices": [
                {"index": 0, "message": {"role": "assistant", "content": reply}, "finish_reason": "stop"}
            ],
            "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
        }
        encoded = json.dumps(payload).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

server = HTTPServer(("127.0.0.1", 0), Handler)
with open(port_path, "w", encoding="utf-8") as fh:
    fh.write(str(server.server_address[1]))
    fh.flush()
write_state()
server.serve_forever()
PY
  STUB_SERVER_PID=$!

  local waited=0
  while [[ ! -s "$port_file" && $waited -lt 50 ]]; do
    sleep 0.1
    waited=$((waited + 1))
  done
  if [[ ! -s "$port_file" ]]; then
    echo "ERROR: stub server failed to write port file" >&2
    return 1
  fi
}

stop_llm_stub_server() {
  if [[ -n "${STUB_SERVER_PID:-}" ]]; then
    kill "$STUB_SERVER_PID" 2>/dev/null || true
    wait "$STUB_SERVER_PID" 2>/dev/null || true
    STUB_SERVER_PID=""
  fi
}

assert_stub_counts() {
  local state_file="$1"
  local min_plan="$2"
  local min_ask="$3"
  local min_monitor="$4"
  local min_diff="$5"
  "$PYTHON_BIN" - "$state_file" "$min_plan" "$min_ask" "$min_monitor" "$min_diff" <<'PY'
import json
import sys

path = sys.argv[1]
min_plan = int(sys.argv[2])
min_ask = int(sys.argv[3])
min_monitor = int(sys.argv[4])
min_diff = int(sys.argv[5])

try:
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
except Exception:
    print("ERROR: failed to read stub state", file=sys.stderr)
    sys.exit(1)

def get(key):
    return int(data.get(key, 0))

plan = get("plan")
ask = get("ask")
monitor = get("monitor")
mixed = get("mixed_monitor")

if plan < min_plan or ask < min_ask or monitor < min_monitor or mixed < min_diff:
    print(f"ERROR: stub counts too low (plan={plan}, ask={ask}, monitor={monitor}, mixed_monitor={mixed})", file=sys.stderr)
    sys.exit(1)
PY
}

regex_escape() {
  local text="$1"
  "$PYTHON_BIN" - "$text" <<'PY'
import re
import sys

if len(sys.argv) != 2:
    sys.exit(1)

print(re.escape(sys.argv[1]))
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

echo "== Preflight: verifying mct-agent binary ==" >&2
check_bin MCT_AGENT_BIN mct-agent "$REPO_ROOT/agent" "--version"
echo "Preflight OK" >&2

echo >&2

LIVE_MODE=false
if [[ -n "${OPENAI_API_KEY:-}" && -n "${OPENAI_BASE_URL:-}" && -n "${OPENAI_MODEL:-}" ]]; then
  LIVE_MODE=true
fi

TEST_MODEL_ALIAS=""
ORCH_MODEL_ALIAS=""
PATCHER_MODEL_ALIAS=""
FILE_DISCOVERY_MODEL_ALIAS=""
TMP_ROOT="$SCRIPT_DIR/tmp"
mkdir -p "$TMP_ROOT"
CONFIG_ROOT="$(mktemp -d "$TMP_ROOT/config.XXXXXX")"
TEST_CONFIG_FILE=""

generate_test_config() {
  local config_dir="$CONFIG_ROOT/.machtiani"
  local config_file="$config_dir/config.toml"
  mkdir -p "$config_dir"

  local repo_config="$REPO_ROOT/.machtiani/config.toml"
  if [[ ! -f "$repo_config" ]]; then
    echo "Missing repository config: $repo_config" >&2
    exit 1
  fi

  local test_provider_name="run-live-provider"

  local provider_base_url="https://api.openai.com/v1"
  local provider_api_key="sk-test-key-fake"
  local provider_endpoint="/chat/completions"

  local orch_remote_model="gpt-4o-mini"
  local patcher_remote_model="gpt-4o-mini"
  local fd_remote_model="gpt-4o-mini"

  if [[ "$LIVE_MODE" == true ]]; then
    provider_base_url="${OPENAI_BASE_URL}"
    provider_api_key="${OPENAI_API_KEY}"
    orch_remote_model="${OPENAI_ORCH_MODEL:-${OPENAI_MODEL}}"
    patcher_remote_model="${OPENAI_PATCHER_MODEL:-${orch_remote_model}}"
    fd_remote_model="${OPENAI_FILE_DISCOVERY_MODEL:-${patcher_remote_model}}"
  fi

  ORCH_MODEL_ALIAS="${OPENAI_ORCH_MODEL_ALIAS:-${orch_remote_model}}"
  PATCHER_MODEL_ALIAS="${OPENAI_PATCHER_MODEL_ALIAS:-${patcher_remote_model}}"
  FILE_DISCOVERY_MODEL_ALIAS="${OPENAI_FILE_DISCOVERY_MODEL_ALIAS:-${fd_remote_model}}"

  if [[ "$LIVE_MODE" == true ]]; then
    TEST_MODEL_ALIAS="${OPENAI_MODEL:-${ORCH_MODEL_ALIAS}}"
  else
    TEST_MODEL_ALIAS="test-model"
    ORCH_MODEL_ALIAS="${TEST_MODEL_ALIAS}"
    PATCHER_MODEL_ALIAS="${TEST_MODEL_ALIAS}"
    FILE_DISCOVERY_MODEL_ALIAS="${TEST_MODEL_ALIAS}"
    orch_remote_model="gpt-4o-mini"
    patcher_remote_model="${orch_remote_model}"
    fd_remote_model="${patcher_remote_model}"
  fi

  cp "$repo_config" "$config_file"
  if [[ -d "$REPO_ROOT/.machtiani/templates" ]]; then
    cp -R "$REPO_ROOT/.machtiani/templates" "$config_dir/"
  fi
  if [[ -d "$REPO_ROOT/.machtiani/meta-orchestrator/custom-instructions" ]]; then
    mkdir -p "$config_dir/meta-orchestrator"
    cp -R "$REPO_ROOT/.machtiani/meta-orchestrator/custom-instructions" "$config_dir/meta-orchestrator/"
  fi

  "$PYTHON_BIN" - "$config_file" "$TEST_MODEL_ALIAS" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
alias = sys.argv[2]
lines = path.read_text(encoding="utf-8").splitlines()
replaced = False
for idx, line in enumerate(lines):
    if re.match(r"^\s*default_model\s*=", line):
        lines[idx] = f"default_model = \"{alias}\""
        replaced = True
        break
if not replaced:
    insert_idx = 0
    while insert_idx < len(lines):
        stripped = lines[insert_idx].strip()
        if stripped and not stripped.startswith("#"):
            break
        insert_idx += 1
    lines.insert(insert_idx, f"default_model = \"{alias}\"")
path.write_text("\n".join(lines) + "\n", encoding="utf-8")
PY

  cat >> "$config_file" <<EOF

[providers.${test_provider_name}]
base_url = "${provider_base_url}"
api_key = "${provider_api_key}"
endpoint = "${provider_endpoint}"
EOF

  local -a declared_aliases=()
  write_model_block() {
    local alias="$1"
    local remote="$2"
    if [[ -z "$alias" ]]; then
      return
    fi
    local already=false
    for existing in "${declared_aliases[@]:-}"; do
      if [[ "$existing" == "$alias" ]]; then
        already=true
        break
      fi
    done
    if [[ "$already" == true ]]; then
      return
    fi
    if grep -Fq "[models.\"${alias}\"]" "$config_file" || grep -Fq "[models.${alias}]" "$config_file"; then
      return
    fi
    cat >> "$config_file" <<EOF

[models."${alias}"]
provider = "${test_provider_name}"
model = "${remote}"
EOF
    declared_aliases+=("$alias")
  }

  write_model_block "$TEST_MODEL_ALIAS" "$orch_remote_model"
  write_model_block "$ORCH_MODEL_ALIAS" "$orch_remote_model"
  write_model_block "$PATCHER_MODEL_ALIAS" "$patcher_remote_model"
  write_model_block "$FILE_DISCOVERY_MODEL_ALIAS" "$fd_remote_model"

  unset -f write_model_block

  if [[ "${TRACE_TEST_CONFIG:-}" == "true" ]]; then
    echo "Generated test config ($config_file):" >&2
    cat "$config_file" >&2
  fi

  TEST_CONFIG_FILE="$config_file"
  echo "$config_file"
}

generate_stub_config() {
  local base_url="$1"
  local alias="$2"
  local config_root
  config_root="$(mktemp -d "$TMP_ROOT/config-stub.XXXXXX")"
  local config_dir="$config_root/.machtiani"
  local config_file="$config_dir/config.toml"
  mkdir -p "$config_dir"

  local repo_config="$REPO_ROOT/.machtiani/config.toml"
  if [[ ! -f "$repo_config" ]]; then
    echo "Missing repository config: $repo_config" >&2
    exit 1
  fi

  cp "$repo_config" "$config_file"
  if [[ -d "$REPO_ROOT/.machtiani/templates" ]]; then
    cp -R "$REPO_ROOT/.machtiani/templates" "$config_dir/"
  fi
  if [[ -d "$REPO_ROOT/.machtiani/meta-orchestrator/custom-instructions" ]]; then
    mkdir -p "$config_dir/meta-orchestrator"
    cp -R "$REPO_ROOT/.machtiani/meta-orchestrator/custom-instructions" "$config_dir/meta-orchestrator/"
  fi

  "$PYTHON_BIN" - "$config_file" "$alias" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
alias = sys.argv[2]
lines = path.read_text(encoding="utf-8").splitlines()
replaced = False
for idx, line in enumerate(lines):
    if re.match(r"^\s*default_model\s*=", line):
        lines[idx] = f"default_model = \"{alias}\""
        replaced = True
        break
if not replaced:
    insert_idx = 0
    while insert_idx < len(lines):
        stripped = lines[insert_idx].strip()
        if stripped and not stripped.startswith("#"):
            break
        insert_idx += 1
    lines.insert(insert_idx, f"default_model = \"{alias}\"")
path.write_text("\n".join(lines) + "\n", encoding="utf-8")
PY

  cat >> "$config_file" <<EOF

[providers.run_live_stub]
base_url = "${base_url}"
api_key = "stub-key"
endpoint = "/chat/completions"

[models."${alias}"]
provider = "run_live_stub"
model = "${alias}"
EOF

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

COMMON_AGENT_ARGS=()
if [[ "$LIVE_MODE" != true ]]; then
  COMMON_AGENT_ARGS+=(--dry-run)
fi

DEFAULT_MODEL_ARGS=(--model "$TEST_MODEL_ALIAS")
PER_COMPONENT_MODEL_ARGS=(
  --orch-model "$ORCH_MODEL_ALIAS"
  --patcher-model "$PATCHER_MODEL_ALIAS"
  --file-discovery-model "$FILE_DISCOVERY_MODEL_ALIAS"
)

MCT_AGENT="$MCT_AGENT_BIN"

assert_snippet_single_file() {
  local traj_path="$1"
  local expected_path="$2"
  "$PYTHON_BIN" - "$traj_path" "$expected_path" <<'PY'
import json
import sys

traj_path = sys.argv[1]
expected = sys.argv[2]
paths = None

try:
    with open(traj_path, "r", encoding="utf-8") as fh:
        for line in fh:
            try:
                obj = json.loads(line)
            except Exception:
                continue
            if obj.get("type") == "final_output":
                candidate = obj.get("paths")
                if isinstance(candidate, list):
                    paths = candidate
except OSError as exc:
    print(f"ERROR: unable to read snippet-discovery trajectory: {exc}", file=sys.stderr)
    sys.exit(1)

if not isinstance(paths, list) or not paths:
    print("ERROR: missing final_output paths in snippet-discovery trajectory", file=sys.stderr)
    sys.exit(1)

if paths != [expected]:
    print(f"ERROR: expected snippet tightness [{expected}], got {paths}", file=sys.stderr)
    sys.exit(1)
PY
}

run_happy_case() {
  local case_id="$1"
  local max_steps="$2"
  local prompt="$3"
  local expected_keywords="$4"
  shift 4
  local min_turns=1
  if (( $# > 0 )) && [[ "${1}" != --* ]]; then
    min_turns="$1"
    shift
  fi
  local -a runtime_args=("$@")
  local has_timeout=false
  for arg in "${runtime_args[@]}"; do
    case "$arg" in
      --timeout-per-turn|--timeout-per-turn=*)
        has_timeout=true
        ;;
    esac
  done
  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_file="$out_dir/stdout-${session_id}.txt"
  local stderr_file="$out_dir/stderr-${session_id}.txt"
  local marker_checks=false
  local session_temp_root=""
  local marker_dir=""
  local stale_marker=""
  local recent_marker=""
  local marker_max_age=""
  local marker_max_age_seconds=0

  cleanup_marker_root() {
    if [[ "$marker_checks" == true && -n "${session_temp_root:-}" && -d "$session_temp_root" ]]; then
      rm -rf "$session_temp_root"
    fi
  }
  return_with_cleanup() {
    local rc="${1:-0}"
    cleanup_marker_root
    return "$rc"
  }

  if shell_marker_checks_enabled; then
    marker_checks=true
    marker_max_age="${MACHTIANI_SHELL_AGENT_MARKER_MAX_AGE:-1h}"
    marker_max_age_seconds="$(duration_to_seconds "$marker_max_age")"
    if [[ "$marker_max_age_seconds" -le 0 ]]; then
      marker_max_age="1h"
      marker_max_age_seconds=3600
    fi
    session_temp_root="$(mktemp -d "$TMP_ROOT/session-temp-${session_id}.XXXXXX")"
    marker_dir="$session_temp_root/shell-agent/markers"
    local stale_age=$((marker_max_age_seconds + 3600))
    local recent_age=$((marker_max_age_seconds / 2))
    if (( recent_age < 60 )); then
      recent_age=60
    fi
    if (( recent_age >= marker_max_age_seconds )); then
      if (( marker_max_age_seconds > 1 )); then
        recent_age=$((marker_max_age_seconds - 1))
      else
        recent_age=1
      fi
    fi
    local stale_name="${SHELL_AGENT_MARKER_PREFIX}-stale-${session_id}${SHELL_AGENT_MARKER_SUFFIX}"
    local recent_name="${SHELL_AGENT_MARKER_PREFIX}-recent-${session_id}${SHELL_AGENT_MARKER_SUFFIX}"
    stale_marker="$(seed_marker_with_age "$marker_dir" "$stale_name" "$stale_age")"
    recent_marker="$(seed_marker_with_age "$marker_dir" "$recent_name" "$recent_age")"
  fi

  echo "Running happy case: $case_id (max $max_steps turns)..." >&2
  local -a cmd=(
    timeout $((max_steps * 180)) "$MCT_AGENT" run
    --max-steps "$max_steps"
    --verbose
    --patch-no-apply
  )
  if [[ "$has_timeout" == false ]]; then
    cmd+=(--timeout-per-turn 300)
  fi
  if ((${#COMMON_AGENT_ARGS[@]})); then
    cmd+=("${COMMON_AGENT_ARGS[@]}")
  fi
  if ((${#runtime_args[@]})); then
    cmd+=("${runtime_args[@]}")
  fi
  cmd+=(-t "$prompt")

  pushd "$REPO_ROOT" >/dev/null
  set +e
  if [[ "$marker_checks" == true ]]; then
    MACHTIANI_SESSION_TEMP_ROOT="$session_temp_root" \
      MACHTIANI_SHELL_AGENT_MARKER_MAX_AGE="$marker_max_age" \
      "${cmd[@]}" > "$stdout_file" 2> "$stderr_file"
  else
    "${cmd[@]}" > "$stdout_file" 2> "$stderr_file"
  fi
  local rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed (rc=$rc): $case_id" >&2
    return_with_cleanup 1
  fi

  local agent_session
  agent_session=$(grep -m1 '^Session:' "$stderr_file" | awk '{print $2}')
  if [[ -z "$agent_session" ]]; then
    echo "Failed to parse session ID from stderr for $case_id" >&2
    return_with_cleanup 1
  fi

  local sessions_root="$REPO_ROOT/.machtiani/sessions"
  local session_dir="$sessions_root/$agent_session"
  if [[ ! -d "$session_dir" ]]; then
    echo "Session directory missing: $session_dir" >&2
    return_with_cleanup 1
  fi

  local chat_dir="$session_dir/chat"
  if [[ ! -d "$chat_dir" ]]; then
    echo "Chat directory missing: $chat_dir" >&2
    return_with_cleanup 1
  fi

  local transcript_path="$chat_dir/agent-transcript.adoc"
  if [[ ! -s "$transcript_path" ]]; then
    echo "Transcript missing or empty: $transcript_path" >&2
    return_with_cleanup 1
  fi

  local mode_file_oriented=false
  local mode_shell=false

  cp -f "$transcript_path" "$out_dir/transcript-${session_id}.adoc"

  local turns
  turns=$(awk 'BEGIN { c = 0 } /^== Turn / {
      if ($3 ~ /^[0-9]+$/ && ($3 + 0) > 0) {
        c++
      }
    } END { print c }' "$transcript_path")
  if [[ $turns -eq 0 ]]; then
    turns=$(grep -E -c '^Step [0-9]+ decision: ' "$stderr_file" 2>/dev/null || echo 0)
  fi
  if [[ $turns -gt $max_steps || $turns -lt $min_turns ]]; then
    echo "Invalid turns ($turns): $case_id" >&2
    return_with_cleanup 1
  fi
  local -a keyword_files=("$stdout_file" "$transcript_path")

  local final_path=""
  local fd_path=""
  if [[ "$LIVE_MODE" == true ]]; then
    final_path="$chat_dir/agent-final-answer.md"
    if [[ ! -s "$final_path" ]]; then
      echo "Missing final artifact in session directory: $final_path" >&2
      return_with_cleanup 1
    fi
    cp -f "$final_path" "$out_dir/final-${session_id}.md"
    keyword_files+=("$final_path")

    local -a mode_indicator_files=("$stdout_file" "$transcript_path" "$final_path")
    if grep -qE '\[mct:(file|both)\]' "${mode_indicator_files[@]}" 2>/dev/null; then
      mode_file_oriented=true
    fi
    if grep -qE '\[mct:shell\]' "${mode_indicator_files[@]}" 2>/dev/null; then
      mode_shell=true
    fi

    fd_path="$session_dir/artifacts/file-discovery.jsonl"
    local require_fd_artifact=false
    if [[ "$mode_file_oriented" == true && "$mode_shell" != true ]]; then
      require_fd_artifact=true
    fi
    if [[ "$require_fd_artifact" == true ]] && grep -qE '^Step [0-9]+ decision: ask' "$stderr_file" 2>/dev/null; then
      if [[ ! -f "$fd_path" ]]; then
        echo "Missing file-discovery trajectory: $fd_path" >&2
        return_with_cleanup 1
      fi
      keyword_files+=("$fd_path")
    fi
  else
    rm -f "$out_dir/final-${session_id}.md"
  fi

  local patches_dir="$session_dir/artifacts/patches"
  if [[ -e "$patches_dir" && ! -d "$patches_dir" ]]; then
    echo "Patches path exists but is not a directory: $patches_dir" >&2
    return_with_cleanup 1
  fi

  if [[ "$LIVE_MODE" == true ]]; then
    keyword_files+=("$out_dir/final-${session_id}.md")
  fi
  if ! contains_keywords "$expected_keywords" "${keyword_files[@]}"; then
    echo "Missing keywords: $case_id" >&2
    return_with_cleanup 1
  fi
  if [[ ! -s "$out_dir/transcript-${session_id}.adoc" ]]; then
    echo "Missing transcript: $case_id" >&2
    return_with_cleanup 1
  fi
  if [[ "$LIVE_MODE" == true && ! -s "$out_dir/final-${session_id}.md" ]]; then
    echo "Missing final artifact: $case_id" >&2
    return_with_cleanup 1
  fi
  if grep -qE "(Finalizer|Transcript write|Final file write) error:" "$stderr_file"; then
    echo "Finalize error detected: $case_id" >&2
    return_with_cleanup 1
  fi
  if [[ "$marker_checks" == true ]]; then
    if ! assert_shell_agent_marker_cleanup "$marker_dir" "$stale_marker" "$recent_marker" "$case_id"; then
      return_with_cleanup 1
    fi
  fi

  echo "Passed: $case_id ($turns turns)" >&2
  return_with_cleanup 0
}

run_menu_flow_case() {
  local case_id="planner-menu-flow"
  local stub_dir="$TMP_ROOT/stub-${case_id}-$(date +%s)"
  mkdir -p "$stub_dir"
  local state_file="$stub_dir/state.json"
  local port_file="$stub_dir/port.txt"
  start_llm_stub_server "$state_file" "$port_file"
  local stub_port
  stub_port="$(cat "$port_file")"
  local base_url="http://127.0.0.1:${stub_port}/v1"
  local stub_alias="stub-model"
  local stub_config
  stub_config="$(generate_stub_config "$base_url" "$stub_alias")"

  local rc=0
  set +e
  (
    export MACHTIANI_CONFIG="$stub_config"
    COMMON_AGENT_ARGS=()
    run_happy_case "$case_id" 1 \
      "Explain the planner menu flow and what happens after an ask is selected." \
      "(?s)(?=.*Planner decision: ask)(?=.*No-shell:)(?=.*Shell:)" \
      1 \
      --model "$stub_alias" \
      --orch-model "$stub_alias" \
      --patcher-model "$stub_alias" \
      --file-discovery-model "$stub_alias"
  )
  rc=$?
  set -e

  stop_llm_stub_server
  if [[ $rc -ne 0 ]]; then
    return "$rc"
  fi

  assert_stub_counts "$state_file" 1 2 2 1

  if [[ "${KEEP_TEST_CONFIG:-}" != "true" ]]; then
    rm -rf "$stub_dir"
    rm -rf "$(dirname "$stub_config")"
  fi
}

run_menu_flow_live_case() {
  local case_id="planner-menu-flow-live"

  run_happy_case "$case_id" 2 \
    "Explain the planner menu flow and what happens after an ask is selected. Also run \`git diff --stat\` and report the output." \
    "(?s)(?=.*No-shell:)(?=.*Shell:)(?=.*(Planner decision: ask|Step 1 decision: ask))" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

run_file_discovery_live_case() {
  local case_id="routing-file-discovery-live"

  run_happy_case "$case_id" 2 \
    "Explain how the planner chooses ask vs patch vs finalize." \
    "(?s)(?=.*\\[mct:shell\\])" \
    1 \
    --timeout-per-turn 600 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

run_snippet_discovery_tightness_live_case() {
  local case_id="snippet-discovery-tight-readme"
  local out_dir="$(pwd)/test-out-${case_id}-$(date +%s)"
  mkdir -p "$out_dir"
  local stdout_file="$out_dir/stdout.txt"
  local stderr_file="$out_dir/stderr.txt"
  local traj_file="$out_dir/snippet-trajectory.jsonl"
  local snippet_bin="$out_dir/snippet-discovery-bin"

  echo "Running snippet tightness case: $case_id..." >&2

  pushd "$REPO_ROOT/agent" >/dev/null
  if ! GOCACHE="$(pwd)/.gocache" go build -o "$snippet_bin" ./internal/snippet-discovery/cmd/snippet-discovery >/dev/null 2>> "$stderr_file"; then
    popd >/dev/null
    echo "Failed to build snippet-discovery binary: $case_id" >&2
    cat "$stderr_file" >&2 || true
    return 1
  fi
  popd >/dev/null

  pushd "$REPO_ROOT" >/dev/null
  set +e
  timeout 180 "$snippet_bin" \
    --model "$TEST_MODEL_ALIAS" \
    --trajectory "$traj_file" \
    -timeout 120 \
    -max-rounds 8 \
    -r "Find the line ranges in the repository-root README.md that contain HTML tags needing conversion to Markdown. Only return ranges for the relevant file." \
    -f README.md \
    -f agent/README.md \
    < /dev/null > "$stdout_file" 2>> "$stderr_file"
  local rc=$?
  set -e
  popd >/dev/null

  if [[ $rc -ne 0 ]]; then
    echo "Failed (rc=$rc): $case_id" >&2
    cat "$stderr_file" >&2 || true
    return 1
  fi
  if [[ ! -s "$traj_file" ]]; then
    echo "Missing snippet-discovery trajectory: $traj_file" >&2
    return 1
  fi
  if ! assert_snippet_single_file "$traj_file" "README.md"; then
    echo "Snippet-discovery tightness check failed: $case_id" >&2
    return 1
  fi

  echo "Passed: $case_id" >&2
}

run_show_live_case() {
  local case_id="routing-show-live"

  run_happy_case "$case_id" 2 \
    "Explain the purpose and structure of agent/internal/workspace/repo_snapshot.go." \
    "(?s)(?=.*\\[mct:shell\\])(?!.*\\[mct:show\\])" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

run_show_range_live_case() {
  local case_id="routing-show-range-live"

  run_happy_case "$case_id" 2 \
    "Explain what agent/internal/workspace/repo_snapshot.go is doing around lines 10-20." \
    "(?s)(?=.*\\[mct:shell\\])(?!.*\\[mct:show\\])" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

run_shell_live_case() {
  local case_id="routing-shell-live"

  run_happy_case "$case_id" 2 \
    "Run `git status -sb` and report the output." \
    "(?s)(?=.*\\[mct:shell\\])" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

run_error_case() {
  local case_id="$1"
  local args="$2"
  local max_steps="$3"
  local expected_keywords="$4"
  shift 4
  local prompt_override=""
  if (( $# > 0 )) && [[ "${1}" != --* ]]; then
    prompt_override="$1"
    shift
  fi
  local -a runtime_args=("$@")
  local session_id="error-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_file="$out_dir/stdout-${session_id}.txt"
  local stderr_file="$out_dir/stderr-${session_id}.txt"

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
  if ((${#COMMON_AGENT_ARGS[@]})); then
    cmd+=("${COMMON_AGENT_ARGS[@]}")
  fi
  if ((${#runtime_args[@]})); then
    cmd+=("${runtime_args[@]}")
  fi
  cmd+=(
    --verbose
    --patch-no-apply
  )
  if [[ -n "$prompt_override" ]]; then
    cmd+=(-t "$prompt_override")
  fi

  pushd "$REPO_ROOT" >/dev/null
  set +e
  "${cmd[@]}" > "$stdout_file" 2> "$stderr_file"
  local rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -eq 0 ]]; then
    echo "Unexpected success (rc=0): $case_id" >&2
    return 1
  fi

  if ! contains_keywords "$expected_keywords" \
      "$stderr_file" \
      "$stdout_file"; then
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

ORCH_LABEL_REGEX="$(regex_escape "$ORCH_MODEL_ALIAS")"
FILE_DISCOVERY_LABEL_REGEX="$(regex_escape "$FILE_DISCOVERY_MODEL_ALIAS")"
DEFAULT_LABEL_REGEX="$(regex_escape "$TEST_MODEL_ALIAS")"

PER_COMPONENT_LABEL_PATTERN="(?s)(?=.*orchestrator model: ${ORCH_LABEL_REGEX})(?=.*file discovery model: ${FILE_DISCOVERY_LABEL_REGEX})"
MIXED_LABEL_PATTERN="(?s)(?=.*orchestrator model: ${ORCH_LABEL_REGEX})(?=.*file discovery model: ${ORCH_LABEL_REGEX})"
CATCH_ALL_LABEL_PATTERN="(?s)(?=.*orchestrator model: ${DEFAULT_LABEL_REGEX})(?=.*file discovery model: ${DEFAULT_LABEL_REGEX})"

INVALID_ALIAS="does-not-exist-alias"
INVALID_ALIAS_REGEX="$(regex_escape "$INVALID_ALIAS")"
MODEL_ALIAS_NOT_FOUND_PATTERN="model alias \"${INVALID_ALIAS_REGEX}\" not found"

if [[ "$LIVE_MODE" != true ]]; then
  run_menu_flow_case
else
  run_snippet_discovery_tightness_live_case
  run_menu_flow_live_case
  run_file_discovery_live_case
  run_show_live_case
  run_show_range_live_case
  if shell_agent_available; then
    run_shell_live_case
  else
    echo "Skipping shell-only live case: shell-agent not found on PATH." >&2
  fi
fi

# Per-component flag coverage.
run_happy_case "models-per-component" 3 \
  "Outline how the orchestrator, patcher, and file discovery collaborators interact." \
  "$PER_COMPONENT_LABEL_PATTERN" \
  1 \
  --patch \
  "${PER_COMPONENT_MODEL_ARGS[@]}"

run_happy_case "models-mixed-fallback" 3 \
  "Summarize how the patcher falls back to the orchestrator model when unspecified." \
  "$MIXED_LABEL_PATTERN" \
  1 \
  --patch \
  --orch-model "$ORCH_MODEL_ALIAS"

run_happy_case "models-catch-all" 1 \
  "Describe the default model wiring for orchestrator and file discovery." \
  "$CATCH_ALL_LABEL_PATTERN" \
  1 \
  "${DEFAULT_MODEL_ARGS[@]}"

run_happy_case "issue-a-1turn" 1 \
  "What is the main purpose of the mct-agent binary?" \
  "===> FINAL RESPONSE <===" \
  1 \
  "${DEFAULT_MODEL_ARGS[@]}"

run_happy_case "issue-a-3turn" 3 \
  "What is the main purpose of the mct-agent binary?" \
  "===> FINAL RESPONSE <===" \
  "${DEFAULT_MODEL_ARGS[@]}"

run_happy_case "issue-b-1turn" 1 \
  "Describe the full multi-turn flow in mct-agent, including planning and context retention." \
  "multi-turn|conversation|context|planner|ask" \
  1 \
  "${DEFAULT_MODEL_ARGS[@]}"

run_happy_case "issue-b-3turn" 3 \
  "Describe the full multi-turn flow in mct-agent, including planning and context retention." \
  "multi-turn|conversation|context|planner|ask" \
  1 \
  "${DEFAULT_MODEL_ARGS[@]}"

run_happy_case "planner-ask-monitor" 2 \
  "Explain the planner ask monitor guardrail flow and when it retries. If you need repository context, ask for it." \
  "(?s)(?=.*ask)(?=.*monitor)" \
  1 \
  "${DEFAULT_MODEL_ARGS[@]}"

run_happy_case "issue-c-1turn" 1 \
  "Explain how mct-agent handles errors during finalization and transcript writing." \
  "error|handling|finalize|transcript|fallback" \
  "${DEFAULT_MODEL_ARGS[@]}"

run_happy_case "issue-c-3turn" 3 \
  "Explain how mct-agent handles errors during finalization and transcript writing, with examples from code." \
  "error|handling|finalize|transcript|fallback" \
  "${DEFAULT_MODEL_ARGS[@]}"

run_error_case "empty-goal" "" 1 "empty issue/question|missing issue" "${DEFAULT_MODEL_ARGS[@]}"

run_error_case "invalid-orch-model" "" 1 "$MODEL_ALIAS_NOT_FOUND_PATTERN" \
  "Trigger orchestrator alias failure" \
  --orch-model "$INVALID_ALIAS"

run_error_case "invalid-patcher-model" "" 1 "$MODEL_ALIAS_NOT_FOUND_PATTERN" \
  "Trigger patcher alias failure" \
  --patcher-model "$INVALID_ALIAS" \
  "${DEFAULT_MODEL_ARGS[@]}"

run_error_case "invalid-file-discovery-model" "" 1 "$MODEL_ALIAS_NOT_FOUND_PATTERN" \
  "Trigger file discovery alias failure" \
  --file-discovery-model "$INVALID_ALIAS" \
  "${DEFAULT_MODEL_ARGS[@]}"

if [[ "$LIVE_MODE" == true ]]; then
  MACHTIANI_CONFIG="/nonexistent/machtiani-config.toml" OPENAI_API_KEY="" OPENAI_BASE_URL="" OPENAI_MODEL="" \
  run_error_case "missing-config" "" 1 "Missing model config|Model resolution error|MACHTIANI_CONFIG" \
    "Explain how the agent chooses its model runtime."
else
  echo "Skipping missing-config error case in dry-run mode (requires live env)." >&2
fi

echo "Skipping missing-mct and timeout simulations: preflight ensures PATH binaries and no stub overrides." >&2

echo "All cases finished. Check test-out-* dirs for artifacts." >&2
