#!/bin/bash
set -euo pipefail
unset MACHTIANI_SESSION_ID

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="${REPO_ROOT:-$(cd "$SCRIPT_DIR/../.." && pwd)}"

# The integration suite creates sessions and other project-scoped state. Keep
# that state out of the developer's checkout by running the suite from a
# detached worktree at committed HEAD with a disposable HOME. The inner run is
# selected explicitly so invoking the copied script does not recurse.
if [[ "${MCT_RUN_LIVE_INNER:-}" != "1" ]]; then
  SOURCE_ROOT="$REPO_ROOT"
  RUN_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/mct-run-live.XXXXXX")
  WORKTREE="$RUN_ROOT/repo"
  TEST_HOME="$RUN_ROOT/home"

  cleanup_isolated_run() {
    git -C "$SOURCE_ROOT" worktree remove --force "$WORKTREE" >/dev/null 2>&1 || true
    rm -rf "$RUN_ROOT"
  }
  trap cleanup_isolated_run EXIT

  mkdir -p "$TEST_HOME"
  git -C "$SOURCE_ROOT" worktree add --detach "$WORKTREE" HEAD >/dev/null
  mkdir -p "$WORKTREE/.machtiani"
  if [[ -f "$SOURCE_ROOT/.machtiani/config.toml" ]]; then
    install -m 0600 "$SOURCE_ROOT/.machtiani/config.toml" "$WORKTREE/.machtiani/config.toml"
  fi
  if [[ -d "$SOURCE_ROOT/.machtiani/templates" ]]; then
    cp -R "$SOURCE_ROOT/.machtiani/templates" "$WORKTREE/.machtiani/templates"
  fi

  set +e
  (
    cd "$WORKTREE"
    HOME="$TEST_HOME" \
      REPO_ROOT="$WORKTREE" \
      MCT_RUN_LIVE_INNER=1 \
      bash "$WORKTREE/agent/tests/run-live.sh" "$@"
  )
  isolated_status=$?
  set -e

  find "$WORKTREE" -maxdepth 1 -type d -name 'test-out-*' -exec cp -R {} "$SOURCE_ROOT/" \;
  exit "$isolated_status"
fi

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
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

state_path = sys.argv[1]
port_path = sys.argv[2]

counts = {
    "plan": 0,
    "ask_worker": 0,
    "monitor": 0,
    "mixed_monitor": 0,
    "user_directed_monitor": 0,
    "user_directed_purifier": 0,
    "preflight": 0,
    "other": 0,
    "last_plan_content": "",
    "shell_agent": 0,
    "shell_agent_code_mode": 0,
    "last_shell_agent_content": "",
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
        last_user_content = None
        for m in messages:
            if isinstance(m, dict) and m.get("role", "").lower() == "user":
                last_user_content = m.get("content", "")
        match_content = last_user_content if last_user_content is not None else "\n".join(m.get("content", "") for m in messages if isinstance(m, dict))

        reply = os.environ.get("STUB_FORCE_REPLY")
        if reply is None:
            reply = "Stub response."
            if "Decision menu (choose exactly one)" in match_content:
                counts["plan"] += 1
                counts["last_plan_content"] = "\n".join(m.get("content", "") for m in messages)
                if "Use the safer fix that preserves behavior." in match_content:
                    reply = "Decision: answer_user\nFinalize: Proceed with the safer fix and summarize the chosen direction."
                elif "planner prompt layers" in match_content:
                    reply = "Decision: answer_user\nReason: The planner prompt layers for code mode are organized with CORE_SAFETY_RULES, PLANNER_OPERATING_RULES, and REPO_MODE_GUIDANCE sections."
                else:
                    reply = "Decision: ask_worker"
            elif "You are generating the next Ask for mct." in match_content:
                counts["plan"] += 1
                if "tradeoff I prefer" in match_content:
                    reply = "Ask Mode: no-shell\nAsk: Ask the user whether they want the safer fix that preserves behavior or the faster fix that may slightly change behavior before you continue."
                elif "Guardrail:" in match_content:
                    reply = "Ask Mode: no-shell\nAsk: Summarize the staged planner menu flow."
                else:
                    reply = "Ask Mode: both\nAsk: Summarize the staged planner menu flow, then run `git diff --stat` and report the output."
            elif "You are a guard that checks whether an ask mixes no-shell and shell actions." in match_content:
                counts["mixed_monitor"] += 1
                reply = "{\"is_mixed\":false,\"reason\":\"already split\",\"rewrite\":\"\"}"
            elif "You are a guard that checks whether an ask is requesting file changes or patches." in match_content:
                counts["monitor"] += 1
                if counts["monitor"] == 1:
                    reply = "{\"has_patch_intent\":true,\"reason\":\"mentions running git diff and could lead to patches\"}"
                else:
                    reply = "{\"has_patch_intent\":false,\"reason\":\"no patch intent\"}"
            elif "You are a guard for user-directed asks." in match_content:
                counts["user_directed_monitor"] += 1
                if "Ask the user whether they want the safer fix" in match_content:
                    reply = "{\"is_user_directed\":true,\"reason\":\"asks the user to choose the preferred tradeoff\"}"
                else:
                    reply = "{\"is_user_directed\":false,\"reason\":\"no user-owned authority boundary\"}"
            elif "You are a purifier for flagged user-directed asks." in match_content:
                counts["user_directed_purifier"] += 1
                if "Ask the user whether they want the safer fix" in match_content:
                    reply = "{\"should_suspend\":true,\"purified_question\":\"Do you want the safer fix that preserves behavior, or the faster fix that may slightly change behavior?\",\"context\":\"\",\"reason\":\"extracted the user-owned tradeoff\"}"
                else:
                    reply = "{\"should_suspend\":false,\"purified_question\":\"\",\"context\":\"\",\"reason\":\"no clean user-owned question\"}"
            elif "You classify user requests for a developer assistant" in match_content:
                counts["preflight"] += 1
                reply = "content"
            elif "You are the action-execution layer of the Machtiani shell agent." in match_content:
                counts["shell_agent"] += 1
                counts["last_shell_agent_content"] = match_content
                if "You MUST use forge" in match_content:
                    counts["shell_agent_code_mode"] += 1
                reply = "<answer>\nStub shell-agent final answer.\n</answer>"
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

assert_stub_plan_prompt_layers() {
  local state_file="$1"
  "$PYTHON_BIN" - "$state_file" <<'PY'
import json
import sys

path = sys.argv[1]

try:
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
except Exception:
    print("ERROR: failed to read stub state", file=sys.stderr)
    sys.exit(1)

content = str(data.get("last_plan_content") or "")
system_prompt = content.split("Explain how the planner prompt layers are organized for code mode.", 1)[0]
required = [
    "<CORE_SAFETY_RULES>",
    "<PLANNER_OPERATING_RULES>",
    "<REPO_MODE_GUIDANCE>",
    "Goal Adherence",
]

missing = [item for item in required if item not in system_prompt]
if missing:
    print(f"ERROR: stub planner prompt missing sections: {missing}", file=sys.stderr)
    sys.exit(1)

if "Task-Specific Guidance" in system_prompt:
    print("ERROR: stub planner prompt still uses deprecated Task-Specific Guidance heading", file=sys.stderr)
    sys.exit(1)
PY
}

assert_stub_shell_agent_code_mode() {
  local state_file="$1"
  local phase_label="$2"
  local min_count="${3:-1}"
  "$PYTHON_BIN" - "$state_file" "$phase_label" "$min_count" <<'PY'
import json
import sys

path = sys.argv[1]
phase = sys.argv[2]
min_count = int(sys.argv[3])

try:
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
except Exception:
    print(f"ERROR: failed to read stub state for {phase}", file=sys.stderr)
    sys.exit(1)

count = int(data.get("shell_agent_code_mode", 0))
if count < min_count:
    print(f"ERROR: [{phase}] shell_agent_code_mode={count}, expected at least {min_count}", file=sys.stderr)
    sys.exit(1)

content = str(data.get("last_shell_agent_content") or "")
if "You MUST use forge" not in content:
    print(f"ERROR: [{phase}] last_shell_agent_content missing 'You MUST use forge'", file=sys.stderr)
    sys.exit(1)
PY
}

assert_file_contains() {
  local file="$1"
  local pattern="$2"
  local label="${3:-}"
  local msg=""
  if [[ -n "$label" ]]; then
    msg=" ($label)"
  fi
  if [[ ! -f "$file" ]]; then
    echo "ERROR: file missing for assert_file_contains: $file${msg}" >&2
    return 1
  fi
  if ! grep -qF "$pattern" "$file"; then
    echo "ERROR: pattern not found in $file${msg}: $pattern" >&2
    return 1
  fi
}

assert_file_not_contains() {
  local file="$1"
  local pattern="$2"
  local label="${3:-}"
  local msg=""
  if [[ -n "$label" ]]; then
    msg=" ($label)"
  fi
  if [[ ! -f "$file" ]]; then
    echo "ERROR: file missing for assert_file_not_contains: $file${msg}" >&2
    return 1
  fi
  if grep -qF "$pattern" "$file"; then
    echo "ERROR: pattern found in $file${msg} when it should not be: $pattern" >&2
    return 1
  fi
}

extract_agent_session_id() {
  local stdout_file="$1"
  local stderr_file="$2"
  local since_epoch="${3:-0}"
  local sid=""

  sid=$(grep -m1 '^Session ID:' "$stdout_file" 2>/dev/null | awk '{print $NF}' || true)
	if [[ -z "$sid" ]]; then
		sid=$(grep -oE -- '--session-id agent-[0-9TZ]+-[0-9]+' "$stdout_file" 2>/dev/null \
			| head -1 | awk '{print $2}' || true)
	fi
  if [[ -z "$sid" ]]; then
    sid=$(grep -m1 '^Session:' "$stderr_file" 2>/dev/null | awk '{print $2}' || true)
  fi
  if [[ -z "$sid" ]]; then
    sid=$(grep -oE 'session agent-[0-9TZ]+-[0-9]+' "$stdout_file" "$stderr_file" 2>/dev/null \
      | head -1 | awk '{print $2}' || true)
  fi
  if [[ -z "$sid" ]]; then
    sid=$(grep -oE '/sessions/(agent-[0-9TZ]+-[0-9]+)/' "$stdout_file" "$stderr_file" 2>/dev/null \
      | head -1 | sed -E 's#.*/sessions/(agent-[0-9TZ]+-[0-9]+)/.*#\1#' || true)
  fi
  if [[ -z "$sid" && "$since_epoch" != "0" ]]; then
    sid=$("$PYTHON_BIN" - "$REPO_ROOT/.machtiani/sessions" "$since_epoch" <<'PY'
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
since = float(sys.argv[2])
best = None
if root.exists():
    for path in root.glob("agent-*"):
        conv = path / "artifacts" / "conversation.json"
        if not conv.exists():
            continue
        mtime = conv.stat().st_mtime
        if mtime + 2 < since:
            continue
        if best is None or mtime > best[0]:
            best = (mtime, path.name)
if best:
    print(best[1])
PY
    )
  fi
  printf '%s\n' "$sid"
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
ask_worker = get("ask_worker")
monitor = get("monitor")
mixed = get("mixed_monitor")

if plan < min_plan or ask < min_ask or monitor < min_monitor or mixed < min_diff:
    print(f"ERROR: stub counts too low (plan={plan}, ask={ask}, monitor={monitor}, mixed_monitor={mixed})", file=sys.stderr)
    sys.exit(1)
PY
}

assert_stub_user_directed_counts() {
  local state_file="$1"
  local min_plan="$2"
  local min_user_directed_monitor="$3"
  local min_user_directed_purifier="$4"
  "$PYTHON_BIN" - "$state_file" "$min_plan" "$min_user_directed_monitor" "$min_user_directed_purifier" <<'PY'
import json
import sys

path = sys.argv[1]
min_plan = int(sys.argv[2])
min_monitor = int(sys.argv[3])
min_purifier = int(sys.argv[4])

try:
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
except Exception:
    print("ERROR: failed to read stub state", file=sys.stderr)
    sys.exit(1)

def get(key):
    return int(data.get(key, 0))

plan = get("plan")
monitor = get("user_directed_monitor")
purifier = get("user_directed_purifier")

if plan < min_plan or monitor < min_monitor or purifier < min_purifier:
    print(
        f"ERROR: user-directed stub counts too low (plan={plan}, user_directed_monitor={monitor}, user_directed_purifier={purifier})",
        file=sys.stderr,
    )
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
      mct-agent)
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
if [ -n "${MCT_AGENT_BIN:-}" ]; then
  echo "Using MCT_AGENT_BIN from environment: $MCT_AGENT_BIN" >&2
else
  check_bin MCT_AGENT_BIN mct-agent "$REPO_ROOT/agent" "--version"
fi
echo "Preflight OK" >&2

echo >&2

LIVE_MODE=false
if [[ -n "${TEST_API_KEY:-${OPENAI_API_KEY:-}}" && \
      -n "${TEST_BASE_URL:-${OPENAI_BASE_URL:-}}" && \
      -n "${TEST_MODEL:-${OPENAI_MODEL:-}}" ]]; then
  LIVE_MODE=true
fi

TEST_MODEL_ALIAS=""
ORCH_MODEL_ALIAS=""
FILE_DISCOVERY_MODEL_ALIAS=""
TMP_ROOT="$SCRIPT_DIR/tmp"
mkdir -p "$TMP_ROOT"
# Remove stale shell-agent marker files from prior crashed runs (older than 1 hour)
find agent/tests/tmp -name "mct-swe-agent-finale-*.txt" -mmin +60 -delete 2>/dev/null || true
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
  local fd_remote_model="gpt-4o-mini"

  if [[ "$LIVE_MODE" == true ]]; then
    provider_base_url="${TEST_BASE_URL:-${OPENAI_BASE_URL}}"
    provider_api_key="${TEST_API_KEY:-${OPENAI_API_KEY}}"
    orch_remote_model="${TEST_ORCH_MODEL:-${OPENAI_ORCH_MODEL:-${TEST_MODEL:-${OPENAI_MODEL}}}}"
    fd_remote_model="${TEST_FILE_DISCOVERY_MODEL:-${OPENAI_FILE_DISCOVERY_MODEL:-${orch_remote_model}}}"
  fi

  ORCH_MODEL_ALIAS="${OPENAI_ORCH_MODEL_ALIAS:-${orch_remote_model}}"
  FILE_DISCOVERY_MODEL_ALIAS="${OPENAI_FILE_DISCOVERY_MODEL_ALIAS:-${fd_remote_model}}"

  if [[ "$LIVE_MODE" == true ]]; then
    # TEST_MODEL_ALIAS is already set by the caller, do nothing here
    ORCH_MODEL_ALIAS="${TEST_MODEL_ALIAS}"
    FILE_DISCOVERY_MODEL_ALIAS="${TEST_MODEL_ALIAS}"
  else
    TEST_MODEL_ALIAS="test-model"
    ORCH_MODEL_ALIAS="${TEST_MODEL_ALIAS}"
    FILE_DISCOVERY_MODEL_ALIAS="${TEST_MODEL_ALIAS}"
    orch_remote_model="gpt-4o-mini"
    fd_remote_model="${orch_remote_model}"
  fi

  cp "$repo_config" "$config_file"
  if [[ -d "$REPO_ROOT/.machtiani/templates" ]]; then
    cp -R "$REPO_ROOT/.machtiani/templates" "$config_dir/"
  fi
  if [[ -d "$REPO_ROOT/.machtiani/modes" ]]; then
    cp -R "$REPO_ROOT/.machtiani/modes" "$config_dir/"
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

if [[ "$LIVE_MODE" == true ]]; then
  "$PYTHON_BIN" - "$config_file" "$TEST_MODEL_ALIAS" "$provider_api_key" "$provider_base_url" <<'PY'
import pathlib
import re
import sys

try:
    import tomllib
except ModuleNotFoundError:
    import tomli as tomllib

config_path = pathlib.Path(sys.argv[1])
alias = sys.argv[2]
api_key = sys.argv[3]
base_url = sys.argv[4]

# 1. Parse TOML to find which provider the model alias references.
with config_path.open('rb') as fh:
    data = tomllib.load(fh)

models = data.get('models', {})
if alias not in models:
    print(f"ERROR: model alias '{alias}' not found in config", file=sys.stderr)
    sys.exit(1)

provider_name = models[alias].get('provider', '')
if not provider_name:
    print(f"ERROR: model alias '{alias}' has no provider field", file=sys.stderr)
    sys.exit(1)

# 2. Line-based edit: set api_key and base_url in the provider section.
lines = config_path.read_text(encoding='utf-8').splitlines()

def replace_section_key(src_lines, section_name, key, value):
    out = []
    in_section = False
    found_section = False
    wrote_key = False
    for line in src_lines:
        stripped = line.strip()
        if stripped.startswith('[') and stripped.endswith(']'):
            if in_section and not wrote_key:
                out.append(f'{key} = "{value}"')
                wrote_key = True
            in_section = stripped == f'[{section_name}]'
            if in_section:
                found_section = True
            out.append(line)
            continue
        if in_section and re.match(rf'^\s*{re.escape(key)}\s*=', line):
            out.append(f'{key} = "{value}"')
            wrote_key = True
            continue
        out.append(line)
    if in_section and not wrote_key:
        out.append(f'{key} = "{value}"')
    if not found_section:
        if out and out[-1] != '':
            out.append('')
        out.append(f'[{section_name}]')
        out.append(f'{key} = "{value}"')
    return out

section = f'providers.{provider_name}'
lines = replace_section_key(lines, section, 'api_key', api_key)
lines = replace_section_key(lines, section, 'base_url', base_url)

config_path.write_text('\n'.join(lines) + '\n', encoding='utf-8')
PY
else
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
  write_model_block "$FILE_DISCOVERY_MODEL_ALIAS" "$fd_remote_model"

  unset -f write_model_block
fi

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
  if [[ -d "$REPO_ROOT/.machtiani/modes" ]]; then
    cp -R "$REPO_ROOT/.machtiani/modes" "$config_dir/"
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


generate_local_test_config() {
  local config_root
  config_root="$(mktemp -d "$TMP_ROOT/config-local.XXXXXX")"
  cp -R "$(dirname "$TEST_CONFIG_FILE")" "$config_root/"

  local config_file="$config_root/.machtiani/config.toml"
  "$PYTHON_BIN" - "$config_file" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
lines = path.read_text(encoding="utf-8").splitlines()

def replace_section_key(src_lines, section_name, key, value):
    out = []
    in_section = False
    found_section = False
    wrote_key = False
    for line in src_lines:
        stripped = line.strip()
        if stripped.startswith("[") and stripped.endswith("]"):
            if in_section and not wrote_key:
                out.append(f"{key} = {value}")
                wrote_key = True
            in_section = stripped == f"[{section_name}]"
            if in_section:
                found_section = True
            out.append(line)
            continue
        if in_section and re.match(rf"^\s*{re.escape(key)}\s*=", line):
            out.append(f"{key} = {value}")
            wrote_key = True
            continue
        out.append(line)
    if in_section and not wrote_key:
        out.append(f"{key} = {value}")
    if not found_section:
        if out and out[-1] != "":
            out.append("")
        out.extend([f"[{section_name}]", f"{key} = {value}"])
    return out

def replace_environment_type(src_lines):
    out = []
    in_environment = False
    found_environment = False
    wrote_type = False
    for line in src_lines:
        stripped = line.strip()
        if stripped.startswith("[") and stripped.endswith("]"):
            if in_environment and not wrote_type:
                out.append('type = "local"')
                wrote_type = True
            in_environment = stripped == "[environment]"
            if in_environment:
                found_environment = True
            out.append(line)
            continue
        if in_environment and re.match(r"^\s*type\s*=", line):
            out.append('type = "local"')
            wrote_type = True
            continue
        out.append(line)
    if in_environment and not wrote_type:
        out.append('type = "local"')
    if not found_environment:
        if out and out[-1] != "":
            out.append("")
        out.extend(["[environment]", 'type = "local"'])
    return out

lines = replace_environment_type(lines)
lines = replace_section_key(lines, "environment", "command_timeout", "600")
lines = replace_section_key(lines, "shell-agent", "max_steps", "2")
lines = replace_section_key(lines, "shell-agent", "finalize_remaining_steps", "1")
path.write_text("\n".join(lines) + "\n", encoding="utf-8")
PY

  echo "$config_file"
}

resolve_live_workspace_root() {
  local config_file="$1"
  local agent_session="$2"

  "$PYTHON_BIN" - "$config_file" "$REPO_ROOT" "$agent_session" <<'PY'
import pathlib
import sys

try:
    import tomllib
except ModuleNotFoundError:  # pragma: no cover
    import tomli as tomllib

config_path = pathlib.Path(sys.argv[1])
repo_root = pathlib.Path(sys.argv[2]).resolve()
session_id = sys.argv[3].strip()

with config_path.open('rb') as fh:
    data = tomllib.load(fh)

environment = data.get('environment') or {}
tmp_root = str(environment.get('tmp_root') or '').strip()
if not tmp_root:
    tmp_root = '.machtiani/tmp'

tmp_path = pathlib.Path(tmp_root)
if not tmp_path.is_absolute():
    tmp_path = (repo_root / tmp_path).resolve()
else:
    tmp_path = tmp_path.resolve()

print(tmp_path / f'workspace-{session_id}')
PY
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

# Resolve model aliases in the parent shell so they are available
# both inside generate_test_config (subshell) and to subsequent commands.
if [[ "$LIVE_MODE" == true ]]; then
  if [[ -n "${TEST_MODEL:-}" ]]; then
    TEST_MODEL_ALIAS="$TEST_MODEL"
  elif [[ -n "${TEST_MODEL_ALIAS:-}" ]]; then
    : # already set
  else
    echo "Error: TEST_MODEL and TEST_MODEL_ALIAS are both unset" >&2
    exit 1
  fi
  ORCH_MODEL_ALIAS="${TEST_MODEL_ALIAS}"
  FILE_DISCOVERY_MODEL_ALIAS="${TEST_MODEL_ALIAS}"
else
  TEST_MODEL_ALIAS="test-model"
  ORCH_MODEL_ALIAS="${TEST_MODEL_ALIAS}"
  FILE_DISCOVERY_MODEL_ALIAS="${TEST_MODEL_ALIAS}"
  orch_remote_model="gpt-4o-mini"
  fd_remote_model="${orch_remote_model}"
fi

TEST_CONFIG_FILE="$(generate_test_config)"
export MACHTIANI_CONFIG="$TEST_CONFIG_FILE"

COMMON_AGENT_ARGS=()
if [[ "$LIVE_MODE" != true ]]; then
  COMMON_AGENT_ARGS+=(--dry-run)
fi

DEFAULT_MODEL_ARGS=(--model "$TEST_MODEL_ALIAS")

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

validate_turn_counts() {
  local case_id="$1"
  local transcript_path="$3"
  local session_dir="$4"

  # 1. Check if jq is available
  if ! command -v jq >/dev/null 2>&1; then
    printf "WARNING: jq not found, skipping turn-count cross-validation for %s\n" "$case_id" >&2
    return 0
  fi

  # 2. Read turns_completed from conversation.json. Normal output intentionally
  # omits the redundant completion summary unless --verbose is enabled.
  local conv_turns
  conv_turns=$(jq -r ".turns_completed" "$session_dir/artifacts/conversation.json" 2>/dev/null)
  if [[ -z "$conv_turns" || "$conv_turns" == "null" ]]; then
    printf "ERROR: unable to read turns_completed from conversation.json for %s\n" "$case_id" >&2
    return 1
  fi

  # 3. Count unique turn numbers in the transcript
  local trans_turns
  trans_turns=$(grep -oP "^== TURN \K[0-9]+" "$transcript_path" 2>/dev/null | sort -n -u | wc -l)
  if [[ -z "$trans_turns" || "$trans_turns" -eq 0 ]]; then
    printf "ERROR: unable to count turn headers in transcript for %s\n" "$case_id" >&2
    return 1
  fi
  # If Turn 0 is included, subtract 1
  if grep -q "^== TURN 0$" "$transcript_path" 2>/dev/null; then
    trans_turns=$((trans_turns - 1))
  fi

  # 4. Assert the persisted state and transcript agree.
  if [[ "$conv_turns" -eq "$trans_turns" ]]; then
    printf "Turn count cross-validation PASSED: %s (conv=%s, trans=%s)\n" "$case_id" "$conv_turns" "$trans_turns" >&2
    return 0
  else
    printf "ERROR: Turn count mismatch for %s: conv=%s, trans=%s\n" "$case_id" "$conv_turns" "$trans_turns" >&2
    return 1
  fi
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
  local skip_turn_check=false
  if (( $# > 0 )) && [[ "${1}" == "--skip-turn-check" ]]; then
    skip_turn_check=true
    shift
  fi
  local -a runtime_args=("$@")
  local has_timeout=false
  for arg in "${runtime_args[@]}"; do
    case "$arg" in
      --turn-timeout|--turn-timeout=*)
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
  local harness_input="${HARNESS_STDIN_INPUT:-}"

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
  local tmp_root="${TMP_ROOT:-}"
  local scratch_root=$(mktemp -d "$tmp_root/scratch-${case_id:-default}.XXXXXX")
  local -a cmd=(
    timeout $((max_steps * 180)) "$MCT_AGENT" run
    --max-turns "$max_steps"
    
  )
  if [[ "$has_timeout" == false ]]; then
    cmd+=(--turn-timeout 300)
  fi
  if ((${#COMMON_AGENT_ARGS[@]})); then
    cmd+=("${COMMON_AGENT_ARGS[@]}")
  fi
  if ((${#runtime_args[@]})); then
    cmd+=("${runtime_args[@]}")
  fi
  cmd+=(--text "$prompt")

  pushd "$REPO_ROOT" >/dev/null
  set +e
  if [[ "$marker_checks" == true ]]; then
    if [[ -n "$harness_input" ]]; then
      MACHTIANI_TMP_ROOT="$scratch_root" \
      MACHTIANI_SESSION_ID="$session_id" \
      MACHTIANI_SESSION_TEMP_ROOT="$session_temp_root" \
      MACHTIANI_SHELL_AGENT_MARKER_MAX_AGE="$marker_max_age" \
      MINISWE_FINAL_DIR="$marker_dir" \
        "${cmd[@]}" < <(printf '%b' "$harness_input") > "$stdout_file" 2> "$stderr_file"
    else
      MACHTIANI_TMP_ROOT="$scratch_root" \
      MACHTIANI_SESSION_ID="$session_id" \
      MACHTIANI_SESSION_TEMP_ROOT="$session_temp_root" \
      MACHTIANI_SHELL_AGENT_MARKER_MAX_AGE="$marker_max_age" \
      MINISWE_FINAL_DIR="$marker_dir" \
        "${cmd[@]}" > "$stdout_file" 2> "$stderr_file"
    fi
  else
    if [[ -n "$harness_input" ]]; then
      MACHTIANI_TMP_ROOT="$scratch_root" \
      MACHTIANI_SESSION_ID="$session_id" \
      "${cmd[@]}" < <(printf '%b' "$harness_input") > "$stdout_file" 2> "$stderr_file"
    else
      MACHTIANI_TMP_ROOT="$scratch_root" \
      MACHTIANI_SESSION_ID="$session_id" \
      "${cmd[@]}" > "$stdout_file" 2> "$stderr_file"
    fi
  fi
  local rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed (rc=$rc): $case_id" >&2
    return_with_cleanup 1 || return 1
  fi

  local agent_session="$session_id"

  local sessions_root="$REPO_ROOT/.machtiani/sessions"
  local session_dir="$sessions_root/$agent_session"
  if [[ ! -d "$session_dir" ]]; then
    echo "Session directory missing: $session_dir" >&2
    return_with_cleanup 1 || return 1
  fi

  local chat_dir="$session_dir/chat"
  if [[ ! -d "$chat_dir" ]]; then
    echo "Chat directory missing: $chat_dir" >&2
    return_with_cleanup 1 || return 1
  fi

  local transcript_path="$chat_dir/agent-transcript.adoc"
  if [[ ! -s "$transcript_path" ]]; then
    echo "Transcript missing or empty: $transcript_path" >&2
    return_with_cleanup 1 || return 1
  fi

  local mode_file_oriented=false
  local mode_shell=false

  cp -f "$transcript_path" "$out_dir/transcript-${session_id}.adoc"

  local turns
  turns=$(jq -r "select(.kind == \"agent.turn.start\") | .kind" "$session_dir/trajectory/agent.jsonl" 2>/dev/null | wc -l)
  if [[ $turns -eq 0 ]]; then
    turns=$(awk 'BEGIN { c = 0 } /^== TURN / { c++ } END { print c }' "$transcript_path")
  fi
  if [[ $turns -eq 0 ]]; then
    turns=$(grep -E '^Step [0-9]+ decision: ' "$stderr_file" "$stdout_file" 2>/dev/null | wc -l)
  fi
  if [[ "$skip_turn_check" != true ]]; then
    if [[ $turns -gt $max_steps || $turns -lt $min_turns ]]; then
      echo "Invalid turns ($turns): $case_id" >&2
      return_with_cleanup 1 || return 1
    fi
  fi
  local -a keyword_files=("$stdout_file" "$transcript_path")

  local final_path=""
  local fd_path=""
  if [[ "$LIVE_MODE" == true ]]; then
    final_path="$chat_dir/agent-final-answer.md"
    if [[ ! -s "$final_path" ]]; then
      echo "Missing final artifact in session directory: $final_path" >&2
      return_with_cleanup 1 || return 1
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
        return_with_cleanup 1 || return 1
      fi
      keyword_files+=("$fd_path")
    fi
  else
    rm -f "$out_dir/final-${session_id}.md"
  fi

  local patches_dir="$session_dir/artifacts/patches"
  if [[ -e "$patches_dir" && ! -d "$patches_dir" ]]; then
    echo "Patches path exists but is not a directory: $patches_dir" >&2
    return_with_cleanup 1 || return 1
  fi

  if [[ "$LIVE_MODE" == true ]]; then
    keyword_files+=("$out_dir/final-${session_id}.md")
  fi
  if ! contains_keywords "$expected_keywords" "${keyword_files[@]}"; then
    echo "Missing keywords: $case_id" >&2
    return_with_cleanup 1 || return 1
  fi
  if [[ ! -s "$out_dir/transcript-${session_id}.adoc" ]]; then
    echo "Missing transcript: $case_id" >&2
    return_with_cleanup 1 || return 1
  fi
  if [[ "$LIVE_MODE" == true && ! -s "$out_dir/final-${session_id}.md" ]]; then
    echo "Missing final artifact: $case_id" >&2
    return_with_cleanup 1 || return 1
  fi
  if grep -qE "^[0-9]{4}/[0-9]{2}/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}.* (Finalizer|Transcript write|Final file write) error:" "$stderr_file"; then
    echo "Finalize error detected: $case_id" >&2
    return_with_cleanup 1 || return 1
  fi
  if [[ "$marker_checks" == true ]]; then
    if ! assert_shell_agent_marker_cleanup "$marker_dir" "$stale_marker" "$recent_marker" "$case_id"; then
      return_with_cleanup 1 || return 1
    fi
  fi

  if ! validate_turn_counts "$case_id" "$stdout_file" "$transcript_path" "$session_dir"; then
    return_with_cleanup 1 || return 1
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
      "Identify the functions in \`agent/internal/planner/planner.go\` and \`agent/internal/session/runner.go\` that handle ask selection and ask execution, then summarize the control flow after an ask is chosen." \
      "(?s)(?=.*Summarize the staged planner menu flow)(?=.*git diff --stat)(?!.*\bNo-shell:)(?!.*\bShell:)" \
      1 \
      --model "$stub_alias" \
      --orch-model "$stub_alias" \
      --file-discovery-model "$stub_alias"
  )
  rc=$?
  set -e

  stop_llm_stub_server
  if [[ $rc -ne 0 ]]; then
    return "$rc"
  fi

  assert_stub_counts "$state_file" 1 2 2 0

  if [[ "${KEEP_TEST_CONFIG:-}" != "true" ]]; then
    rm -rf "$stub_dir"
    rm -rf "$(dirname "$stub_config")"
  fi
}

run_menu_flow_live_case() {
  local case_id="planner-menu-flow-live"

  run_happy_case "$case_id" 2 \
    'Identify the functions in `agent/internal/planner/planner.go` and `agent/internal/session/runner.go` that handle ask selection and ask execution, then summarize the control flow after an ask is chosen.' \
    "(?s)(?=.*\[mct:shell\])(?!.*\bNo-shell:)(?!.*\bShell:)" \
    1 \
    --turn-timeout 600 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

run_mode_prompt_layers_case() {
  local case_id="mode-prompt-layers"
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
    HARNESS_STDIN_INPUT=$'c\n' run_happy_case "$case_id" 2 \
      "Explain how the planner prompt layers are organized for code mode." \
      "." \
      0 \
      --skip-turn-check \
      --mode code \
      --model "$stub_alias" \
      --orch-model "$stub_alias" \
      --file-discovery-model "$stub_alias"
  )
  rc=$?
  set -e

  stop_llm_stub_server
  if [[ $rc -ne 0 ]]; then
    return "$rc"
  fi

  assert_stub_plan_prompt_layers "$state_file"

  if [[ "${KEEP_TEST_CONFIG:-}" != "true" ]]; then
    rm -rf "$stub_dir"
    rm -rf "$(dirname "$stub_config")"
  fi
}

run_mode_live_case() {
  local case_id="mode-code-live"

  HARNESS_STDIN_INPUT=$'c\n' run_happy_case "$case_id" 2 \
    'Using only `docs/mct-agent-runbook.md`, summarize the recommended `--mode code` workflow in this repository and name the most useful artifacts written under `.machtiani/sessions/<session-id>/`.' \
    'mode-plan.json|agent-transcript.adoc|agent-final-answer.md' \
    1 \
    --mode code \
    --turn-timeout 600 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

run_file_discovery_live_case() {
  local case_id="routing-file-discovery-live"

  run_happy_case "$case_id" 2 \
    'In `agent/internal/planner/planner.go`, what function generates the ask prompt? In `agent/internal/session/runner.go`, what function executes planner asks? Return the two function names and one sentence connecting them.' \
    "(?s)(?=.*\\[mct:shell\\])" \
    1 \
    --turn-timeout 600 \
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
    -r 'Find the line ranges containing the exact phrase "Search current model catalogue". Only return ranges for files that contain that exact phrase.' \
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
    "Explain the purpose and structure of agent/internal/session/runner.go." \
    "(?s)(?=.*\\[mct:shell\\])(?!.*\\[mct:show\\])" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

run_show_range_live_case() {
  local case_id="routing-show-range-live"

  run_happy_case "$case_id" 2 \
    "Explain what agent/internal/session/runner.go is doing around lines 10-20." \
    "(?s)(?=.*\\[mct:shell\\])(?!.*\\[mct:show\\])" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

run_shell_live_case() {
  local case_id="routing-shell-live"

  run_happy_case "$case_id" 2 \
    'Run `git status -sb` and report the output.' \
    "(?s)(?=.*\\[mct:shell\\])" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

run_local_tmp_root_unset_live_case() {
  local case_id="local-tmp-root-unset-live"
  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_file="$out_dir/stdout-${session_id}.txt"
  local stderr_file="$out_dir/stderr-${session_id}.txt"
  local session_temp_root=""
  local marker_checks=false
  local marker_dir=""
  local stale_marker=""
  local recent_marker=""
  local marker_max_age=""
  local marker_max_age_seconds=0
  local local_config=""
  local prompt
  prompt=$(cat <<'EOF'
Run this exact command and report the exact output token only: `python3 -c 'import os; print("MCT_LOCAL_TMP_ROOT=" + (os.environ.get("MACHTIANI_TMP_ROOT") or "UNSET"))'`
EOF
)

  if [[ "$LIVE_MODE" != true ]]; then
    echo "Skipping $case_id: requires live mode (TEST_API_KEY/TEST_BASE_URL/TEST_MODEL set)" >&2
    return 0
  fi

  cleanup_case_root() {
    if [[ -n "$local_config" && "${KEEP_TEST_CONFIG:-}" != "true" ]]; then
      rm -rf "$(dirname "$(dirname "$local_config")")"
    fi
    if [[ -n "$session_temp_root" && -d "$session_temp_root" ]]; then
      rm -rf "$session_temp_root"
    fi
  }
  return_with_cleanup() {
    local rc="${1:-0}"
    cleanup_case_root
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

  local_config="$(generate_local_test_config)"

  echo "Running happy case: $case_id (local env tmp-root regression)..." >&2
  local -a cmd=(
    timeout 240 "$MCT_AGENT" run
    --max-turns 2
    
    --turn-timeout 300
  )
  if ((${#COMMON_AGENT_ARGS[@]})); then
    cmd+=("${COMMON_AGENT_ARGS[@]}")
  fi
  cmd+=(
    --model "$TEST_MODEL_ALIAS"
    --text "$prompt"
  )

  pushd "$REPO_ROOT" >/dev/null
  set +e
  if [[ "$marker_checks" == true ]]; then
    MACHTIANI_CONFIG="$local_config" \
      MACHTIANI_SESSION_TEMP_ROOT="$session_temp_root" \
      MACHTIANI_SHELL_AGENT_MARKER_MAX_AGE="$marker_max_age" \
      MINISWE_FINAL_DIR="$marker_dir" \
      "${cmd[@]}" > "$stdout_file" 2> "$stderr_file"
  else
    MACHTIANI_CONFIG="$local_config" \
      "${cmd[@]}" > "$stdout_file" 2> "$stderr_file"
  fi
  local rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed (rc=$rc): $case_id" >&2
    return_with_cleanup 1 || return 1
  fi

  local agent_session
  agent_session=$(extract_agent_session_id "$stdout_file" "$stderr_file")
  if [[ -z "$agent_session" ]]; then
    echo "Failed to parse session ID for $case_id" >&2
    return_with_cleanup 1 || return 1
  fi

  local session_dir="$REPO_ROOT/.machtiani/sessions/$agent_session"
  local transcript_path="$session_dir/chat/agent-transcript.adoc"
  local final_path="$session_dir/chat/agent-final-answer.md"
  if [[ ! -s "$transcript_path" || ! -s "$final_path" ]]; then
    echo "Missing live artifacts for $case_id" >&2
    return_with_cleanup 1 || return 1
  fi
  cp -f "$transcript_path" "$out_dir/transcript-${session_id}.adoc"
  cp -f "$final_path" "$out_dir/final-${session_id}.md"

  if ! contains_keywords "(?s)(?=.*\[mct:shell\])(?=.*MCT_LOCAL_TMP_ROOT=UNSET)" \
      "$stdout_file" "$transcript_path" "$final_path"; then
    echo "Missing local tmp-root unset proof: $case_id" >&2
    return_with_cleanup 1 || return 1
  fi

  local live_workspace_root
  live_workspace_root="$(resolve_live_workspace_root "$local_config" "$agent_session")"
  if [[ -e "$live_workspace_root" ]]; then
    echo "Unexpected snapshot workspace created for local env: $live_workspace_root ($case_id)" >&2
    find "$live_workspace_root" -maxdepth 2 -mindepth 0 2>/dev/null | sort >&2 || true
    return_with_cleanup 1 || return 1
  fi

  if [[ "$marker_checks" == true ]]; then
    if ! assert_shell_agent_marker_cleanup "$marker_dir" "$stale_marker" "$recent_marker" "$case_id"; then
      return_with_cleanup 1 || return 1
    fi
  fi

  echo "Passed: $case_id (local env leaves MACHTIANI_TMP_ROOT unset)" >&2
  return_with_cleanup 0
}


run_shell_command_trajectory_live_case() {
  # --- verbose sub-test ---
  local case_id="shell-command-trajectory-live"
  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_file="$out_dir/stdout-${session_id}.txt"
  local stderr_file="$out_dir/stderr-${session_id}.txt"

  echo "Running sub-test: $case_id (verbose)..." >&2
  local -a cmd=(
    timeout 240 "$MCT_AGENT" run
    --max-turns 3
    --turn-timeout 120
    --mode code
  )
  if ((${#COMMON_AGENT_ARGS[@]})); then
    cmd+=("${COMMON_AGENT_ARGS[@]}")
  fi
  cmd+=(
    --verbose
    --model "$TEST_MODEL_ALIAS"
    --text "Run the command echo hello and report the output."
  )

  pushd "$REPO_ROOT" >/dev/null
  set +e
  "${cmd[@]}" > "$stdout_file" 2> "$stderr_file"
  local rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed (rc=$rc): $case_id (verbose)" >&2
    return 1
  fi

  local agent_session
  agent_session=$(extract_agent_session_id "$stdout_file" "$stderr_file")
  if [[ -z "$agent_session" ]]; then
    echo "Failed to parse session ID for $case_id (verbose)" >&2
    return 1
  fi

  local session_dir="$REPO_ROOT/.machtiani/sessions/$agent_session"
  if [[ ! -d "$session_dir" ]]; then
    echo "Session directory missing: $session_dir ($case_id verbose)" >&2
    return 1
  fi

  local traj_file="$session_dir/trajectory/agent.jsonl"
  if [[ ! -f "$traj_file" ]]; then
    echo "Trajectory file missing: $traj_file ($case_id verbose)" >&2
    return 1
  fi

  local shell_count
  shell_count=$("$PYTHON_BIN" - "$traj_file" <<'PY'
import json
import sys

traj_path = sys.argv[1]
count = 0
with open(traj_path, 'r') as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if obj.get("kind") == "shell.command":
            payload = obj.get("payload", {})
            if "command" in payload and "return_code" in payload:
                count += 1
print(count)
PY
  )

  if [[ -z "$shell_count" || "$shell_count" -lt 1 ]]; then
    echo "Expected at least 1 shell.command event in verbose mode, got $shell_count ($case_id)" >&2
    return 1
  fi
  echo "PASSED: shell-command-trajectory-live case (verbose: $shell_count shell.command events)" >&2

  # --- non-verbose sub-test ---
  local case_id2="shell-command-trajectory-live"
  local session_id2="test-${case_id2}-noverbose-$(date +%s)"
  local out_dir2="$(pwd)/test-out-${session_id2}"
  mkdir -p "$out_dir2"
  local stdout_file2="$out_dir2/stdout-${session_id2}.txt"
  local stderr_file2="$out_dir2/stderr-${session_id2}.txt"

  echo "Running sub-test: $case_id (non-verbose)..." >&2
  local -a cmd2=(
    timeout 240 "$MCT_AGENT" run
    --max-turns 3
    --turn-timeout 120
    --mode code
  )
  if ((${#COMMON_AGENT_ARGS[@]})); then
    cmd2+=("${COMMON_AGENT_ARGS[@]}")
  fi
  cmd2+=(
    --model "$TEST_MODEL_ALIAS"
    --text "Run the command echo hello and report the output."
  )

  pushd "$REPO_ROOT" >/dev/null
  set +e
  "${cmd2[@]}" > "$stdout_file2" 2> "$stderr_file2"
  local rc2=$?
  set -e
  popd >/dev/null
  if [[ $rc2 -ne 0 ]]; then
    echo "Failed (rc=$rc2): $case_id (non-verbose)" >&2
    return 1
  fi

  local agent_session2
  agent_session2=$(extract_agent_session_id "$stdout_file2" "$stderr_file2")
  if [[ -z "$agent_session2" ]]; then
    echo "Failed to parse session ID for $case_id (non-verbose)" >&2
    return 1
  fi

  local session_dir2="$REPO_ROOT/.machtiani/sessions/$agent_session2"
  if [[ ! -d "$session_dir2" ]]; then
    echo "Session directory missing: $session_dir2 ($case_id non-verbose)" >&2
    return 1
  fi

  local traj_file2="$session_dir2/trajectory/agent.jsonl"
  if [[ ! -f "$traj_file2" ]]; then
    echo "Trajectory file missing: $traj_file2 ($case_id non-verbose)" >&2
    return 1
  fi

  local shell_count2
  shell_count2=$("$PYTHON_BIN" - "$traj_file2" <<'PY'
import json
import sys

traj_path = sys.argv[1]
count = 0
with open(traj_path, 'r') as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if obj.get("kind") == "shell.command":
            payload = obj.get("payload", {})
            if "command" in payload and "return_code" in payload:
                count += 1
print(count)
PY
  )

  if [[ -z "$shell_count2" || "$shell_count2" -ne 0 ]]; then
    echo "Expected 0 shell.command events in non-verbose mode, got $shell_count2 ($case_id)" >&2
    return 1
  fi
  echo "PASSED: shell-command-trajectory-live case (non-verbose: $shell_count2 shell.command events)" >&2

  echo "PASSED: shell-command-trajectory-live case" >&2
}


run_shell_agent_subcommand_live_case() {
    shell_agent_available || { echo "SKIP: shell-agent binary not found"; return 0; }

    local prompt="List the files in the current working directory, then report how many there are."

    local output
    output=$(${MCT_AGENT} shell-agent \
        --model "${TEST_SHELL_AGENT_MODEL:-${TEST_MODEL}}" \
        --text "${prompt}" \
        2>&1)
    local exit_code=$?

    [ "$exit_code" -eq 0 ] || {
        echo "FAIL: mct-agent shell-agent exit ${exit_code}, output: ${output}"
        return 1
    }

    echo "$output" | grep -q "Exit Status: Submitted" || {
        echo "FAIL: mct-agent shell-agent output missing submitted status"
        return 1
    }

    if [[ -n "${TEST_STUB_SERVER:-}" ]]; then
        echo "$output" | grep -q "Stub shell-agent final answer" || {
            echo "FAIL: mct-agent shell-agent output missing final answer text (stub mode)"
            return 1
        }
    else
        if [[ -z "$(echo "$output" | grep -v '^Exit Status:' | tr -d '[:space:]')" ]]; then
            echo "FAIL: mct-agent shell-agent output missing final answer text (live mode)"
            return 1
        fi
    fi

    if echo "$output" | grep -q "## Answer\|<answer>"; then
        echo "FAIL: mct-agent shell-agent output should not expose answer control markers"
        return 1
    fi

    echo "PASS: run_shell_agent_subcommand_live_case"
}

# run_resume_without_mode_case verifies that --mode code writes the
# expected modes to artifacts/conversation.json.
# It starts a session with --mode code and checks that the resulting
# conversation.json contains modes=["code"].
run_resume_without_mode_case() {
  local case_id="resume-without-mode"
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

  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_init="$out_dir/stdout-init-${session_id}.txt"
  local stderr_init="$out_dir/stderr-init-${session_id}.txt"

  echo "Running resume-without-mode case: $case_id..." >&2

  # Phase 1: Start a session with --mode code.
  local rc=0
  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$stub_config" \
  timeout 120 "$MCT_AGENT" run \
    --max-turns 2 \
    \
    --turn-timeout 300 \
    --mode code \
    --model "$stub_alias" \
    --orch-model "$stub_alias" \
    --file-discovery-model "$stub_alias" \
    --text "Explain how to modify files in this project." \
    > "$stdout_init" 2> "$stderr_init"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed initial run (rc=$rc): $case_id" >&2
    cat "$stderr_init" >&2 || true
    stop_llm_stub_server
    return 1
  fi

  # Extract the session ID from stderr.
  local agent_session
  agent_session=$(extract_agent_session_id "$stdout_init" "$stderr_init")
  if [[ -z "$agent_session" ]]; then
    echo "Failed to parse session ID for $case_id" >&2
    cat "$stderr_init" >&2 || true
    stop_llm_stub_server
    return 1
  fi

  local conv_path="$REPO_ROOT/.machtiani/sessions/$agent_session/artifacts/conversation.json"
  if [[ ! -f "$conv_path" ]]; then
    echo "conversation.json missing after initial run: $conv_path" >&2
    stop_llm_stub_server
    return 1
  fi
  if ! "$PYTHON_BIN" -c "
import json, sys
try:
    with open(\"$conv_path\") as f:
        state = json.load(f)
    modes = state.get(\"modes\", [])
    if \"code\" not in modes:
        print(\"ERROR: modes={modes}, expected code to be present\", file=sys.stderr)
        sys.exit(1)
    print(f\"OK: modes={modes}\")
except Exception as e:
    print(f\"ERROR: {e}\", file=sys.stderr)
    sys.exit(1)
" 2>&1; then
    echo "Phase 1: session state verification failed for $case_id" >&2
    stop_llm_stub_server
    return 1
  fi

  stop_llm_stub_server
  if [[ "${KEEP_TEST_CONFIG:-}" != "true" ]]; then
    rm -rf "$stub_dir"
    rm -rf "$(dirname "$stub_config")"
  fi
  echo "Passed: $case_id" >&2
}

# test_code_no_forge verifies that --mode code does NOT inject the forge
# instruction into the shell-agent system prompt.  It asserts mode-plan.json
# mode is "code" and inputs.jsonl does not contain "You MUST use forge".
test_code_no_forge() {
  local case_id="code-no-forge"
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

  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_file="$out_dir/stdout-${session_id}.txt"
  local stderr_file="$out_dir/stderr-${session_id}.txt"

  echo "Running code-no-forge case..." >&2

  local rc=0
  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$stub_config" \
  timeout 120 "$MCT_AGENT" run \
    --max-turns 2 \
    \
    --turn-timeout 300 \
    --mode code \
    --model "$stub_alias" \
    --orch-model "$stub_alias" \
    --file-discovery-model "$stub_alias" \
    --text "confirm the full path to README.md in the cwd" \
    > "$stdout_file" 2> "$stderr_file"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed (rc=$rc): $case_id" >&2
    cat "$stderr_file" >&2 || true
    stop_llm_stub_server

    return 1
  fi

  local agent_session
  agent_session=$(extract_agent_session_id "$stdout_file" "$stderr_file")
  if [[ -z "$agent_session" ]]; then
    echo "Failed to parse session ID for $case_id" >&2
    stop_llm_stub_server

    return 1
  fi

  local sessions_root="$REPO_ROOT/.machtiani/sessions"
  local session_dir="$sessions_root/$agent_session"
  local meta_plan="$session_dir/mode-plan.json"
  local inputs_jsonl="$session_dir/artifacts/llm/inputs.jsonl"

  # Assert mode-plan.json mode is "code".
  if ! assert_file_contains "$meta_plan" '"mode": "code"' "$case_id mode-plan.json"; then
    stop_llm_stub_server

    return 1
  fi

  # Assert inputs.jsonl does NOT contain the forge instruction.
  if ! assert_file_not_contains "$inputs_jsonl" "You MUST use forge" "$case_id inputs.jsonl"; then
    stop_llm_stub_server

    return 1
  fi

  stop_llm_stub_server

  if [[ "${KEEP_TEST_CONFIG:-}" != "true" ]]; then
    rm -rf "$stub_dir"
    rm -rf "$(dirname "$stub_config")"
  fi
  echo "Passed: $case_id" >&2
}

# run_enforce_early_commands_case verifies that the EnforceEarlyCommands
# feature flag is plumbed through the shell-agent library path end-to-end
# without regressing the agent loop.
#
# The test runs the agent with MACHTIANI_SHELL_AGENT_ENFORCE_EARLY_COMMANDS=true
# using the LLM stub server (no live API key required). The stub returns a
# well-formed <command> followed by a final answer, so neither the bash
# syntax validator nor the stricter format-error message should engage.
# The test asserts that:
#   * the agent completes successfully with the flag on, and
#   * the trajectory contains at least one shell-agent invocation, and
#   * no FormatErrorLoop status appears in the session's shell-agent state.
# This catches regressions in the flag-threading plumbing (env-var read,
# ShellAgentLibrary field, Request field, DefaultAgent field) without
# requiring a live LLM or a malformed response fixture. The full A/B
# comparison is in agent/tests/enforce_early_commands_test.sh.
run_enforce_early_commands_case() {
  local case_id="enforce-early-commands"
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

  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_file="$out_dir/stdout-${session_id}.txt"
  local stderr_file="$out_dir/stderr-${session_id}.txt"

  echo "Running enforce-early-commands case: $case_id..." >&2

  # Phase 1: experimental run (flag on).
  local rc=0
  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$stub_config" \
  MACHTIANI_SHELL_AGENT_ENFORCE_EARLY_COMMANDS=true \
  STUB_FORCE_REPLY=ask_worker \
    timeout 120 "$MCT_AGENT" run \
      --max-turns 2 \
      \
      --turn-timeout 300 \
      --mode code \
      --model "$stub_alias" \
      --orch-model "$stub_alias" \
      --file-discovery-model "$stub_alias" \
      --text "List the README.md file path under the cwd." \
      > "$stdout_file" 2> "$stderr_file"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed experimental run (rc=$rc): $case_id" >&2
    cat "$stderr_file" >&2 || true
    stop_llm_stub_server
    return 1
  fi

  local agent_session
  agent_session=$(extract_agent_session_id "$stdout_file" "$stderr_file")
  if [[ -z "$agent_session" ]]; then
    echo "Failed to parse session ID for $case_id" >&2
    stop_llm_stub_server
    return 1
  fi

  local sessions_root="$REPO_ROOT/.machtiani/sessions"
  local session_dir="$sessions_root/$agent_session"
  local traj_file="$session_dir/trajectory/agent.jsonl"
  if [[ ! -f "$traj_file" ]]; then
    echo "Trajectory file missing: $traj_file ($case_id)" >&2
    stop_llm_stub_server
    return 1
  fi

  # The shell-agent library should have been invoked at least once. We
  # check the trajectory for any shell-agent-related event so the test
  # does not depend on the exact event name in the unified stream.
  if ! "$PYTHON_BIN" - "$traj_file" <<'PY'
import json
import sys

traj_path = sys.argv[1]
shell_hits = 0
with open(traj_path, 'r', encoding='utf-8') as fh:
    for line in fh:
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        kind = str(obj.get("kind") or "")
        payload = obj.get("payload") or {}
        if (
            "shell" in kind.lower()
            or "shell_agent" in str(payload).lower()
            or "mct-swe-agent" in str(payload).lower()
        ):
            shell_hits += 1
if shell_hits < 1:
    print(f"ERROR: no shell-agent events found in trajectory ({shell_hits})", file=sys.stderr)
    sys.exit(1)
print(f"OK: {shell_hits} shell-agent related events")
PY
  then
    stop_llm_stub_server
    return 1
  fi

  # Phase 2: control run (flag off).  The session must also complete
  # successfully; otherwise the flag has not been plumbed as a no-op
  # default. We keep this lightweight by reusing the same stub server.
  local control_session="test-${case_id}-control-$(date +%s)"
  local control_out="$(pwd)/test-out-${control_session}"
  mkdir -p "$control_out"
  local control_stdout="$control_out/stdout-${control_session}.txt"
  local control_stderr="$control_out/stderr-${control_session}.txt"

  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$stub_config" \
  MACHTIANI_SHELL_AGENT_ENFORCE_EARLY_COMMANDS=false \
  STUB_FORCE_REPLY=ask_worker \
    timeout 120 "$MCT_AGENT" run \
      --max-turns 2 \
      \
      --turn-timeout 300 \
      --mode code \
      --model "$stub_alias" \
      --orch-model "$stub_alias" \
      --file-discovery-model "$stub_alias" \
      --text "List the README.md file path under the cwd." \
      > "$control_stdout" 2> "$control_stderr"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed control run (rc=$rc): $case_id" >&2
    cat "$control_stderr" >&2 || true
    stop_llm_stub_server
    return 1
  fi

  # Both sessions should reach a Submitted (or comparable non-error)
  # status; neither should hit FormatErrorLoop on a well-formed response.
  if grep -q "FormatErrorLoop" "$stderr_file" "$control_stderr" 2>/dev/null; then
    echo "Unexpected FormatErrorLoop status in $case_id" >&2
    stop_llm_stub_server
    return 1
  fi

  unset STUB_FORCE_REPLY
  stop_llm_stub_server
  if [[ "${KEEP_TEST_CONFIG:-}" != "true" ]]; then
    rm -rf "$stub_dir"
    rm -rf "$(dirname "$stub_config")"
  fi
  echo "Passed: $case_id (flag plumbed through end-to-end without regression)" >&2
}

# test_code_forge_initial verifies that --mode code-forge injects the forge
# instruction into the shell-agent system prompt.  It asserts mode-plan.json
# mode is "code-forge" and inputs.jsonl contains "You MUST use forge".
test_code_forge_initial() {
  local case_id="code-forge-initial"
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

  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_file="$out_dir/stdout-${session_id}.txt"
  local stderr_file="$out_dir/stderr-${session_id}.txt"

  echo "Running code-forge-initial case..." >&2

  local rc=0
  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$stub_config" \
  timeout 120 "$MCT_AGENT" run \
    --max-turns 2 \
    \
    --turn-timeout 300 \
    --mode code-forge \
    --model "$stub_alias" \
    --orch-model "$stub_alias" \
    --file-discovery-model "$stub_alias" \
    --text "confirm the full path to README.md in the cwd" \
    > "$stdout_file" 2> "$stderr_file"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed (rc=$rc): $case_id" >&2
    cat "$stderr_file" >&2 || true
    stop_llm_stub_server

    return 1
  fi

  local agent_session
  agent_session=$(extract_agent_session_id "$stdout_file" "$stderr_file")
  if [[ -z "$agent_session" ]]; then
    echo "Failed to parse session ID for $case_id" >&2
    stop_llm_stub_server

    return 1
  fi

  local sessions_root="$REPO_ROOT/.machtiani/sessions"
  local session_dir="$sessions_root/$agent_session"
  local meta_plan="$session_dir/mode-plan.json"
  local inputs_jsonl="$session_dir/artifacts/llm/inputs.jsonl"

  # Assert mode-plan.json mode is "code-forge".
  if ! assert_file_contains "$meta_plan" '"mode": "code-forge"' "$case_id mode-plan.json"; then
    stop_llm_stub_server

    return 1
  fi

  # Assert inputs.jsonl DOES contain the forge instruction.
  if ! assert_file_contains "$inputs_jsonl" "You MUST use forge" "$case_id inputs.jsonl"; then
    stop_llm_stub_server

    return 1
  fi

  stop_llm_stub_server

  if [[ "${KEEP_TEST_CONFIG:-}" != "true" ]]; then
    rm -rf "$stub_dir"
    rm -rf "$(dirname "$stub_config")"
  fi
  echo "Passed: $case_id" >&2
}

# test_code_forge_resume_with_mode starts a session with --mode code-forge,
# captures the session ID, then resumes with --session-id and --mode code-forge,
# asserting mode-plan.json mode and forge instruction presence in both phases.
test_code_forge_resume_with_mode() {
  local case_id="code-forge-resume-with-mode"
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

  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_init="$out_dir/stdout-init-${session_id}.txt"
  local stderr_init="$out_dir/stderr-init-${session_id}.txt"
  local stdout_resume="$out_dir/stdout-resume-${session_id}.txt"
  local stderr_resume="$out_dir/stderr-resume-${session_id}.txt"

  echo "Running code-forge-resume-with-mode case..." >&2

  # Phase 1: Start a session with --mode code-forge.
  local rc=0
  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$stub_config" \
  timeout 120 "$MCT_AGENT" run \
    --max-turns 2 \
    \
    --turn-timeout 300 \
    --mode code-forge \
    --model "$stub_alias" \
    --orch-model "$stub_alias" \
    --file-discovery-model "$stub_alias" \
    --text "confirm the full path to README.md in the cwd" \
    > "$stdout_init" 2> "$stderr_init"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed initial run (rc=$rc): $case_id" >&2
    cat "$stderr_init" >&2 || true
    stop_llm_stub_server

    return 1
  fi

  local agent_session
  agent_session=$(extract_agent_session_id "$stdout_init" "$stderr_init")
  if [[ -z "$agent_session" ]]; then
    echo "Failed to parse session ID for $case_id" >&2
    stop_llm_stub_server

    return 1
  fi

  local sessions_root="$REPO_ROOT/.machtiani/sessions"
  local session_dir="$sessions_root/$agent_session"
  local meta_plan="$session_dir/mode-plan.json"
  local inputs_jsonl="$session_dir/artifacts/llm/inputs.jsonl"

  # Phase 1 assertions.
  if ! assert_file_contains "$meta_plan" '"mode": "code-forge"' "$case_id phase1 mode-plan.json"; then
    stop_llm_stub_server

    return 1
  fi
  if ! assert_file_contains "$inputs_jsonl" "You MUST use forge" "$case_id phase1 inputs.jsonl"; then
    stop_llm_stub_server

    return 1
  fi

  # Phase 2: Resume the session WITH --mode code-forge.
  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$stub_config" \
  timeout 120 "$MCT_AGENT" run \
    --max-turns 2 \
    \
    --turn-timeout 300 \
    --session-id "$agent_session" \
    --mode code-forge \
    --model "$stub_alias" \
    --orch-model "$stub_alias" \
    --file-discovery-model "$stub_alias" \
    --text "Continue." \
    > "$stdout_resume" 2> "$stderr_resume"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed resume run (rc=$rc): $case_id" >&2
    cat "$stderr_resume" >&2 || true
    stop_llm_stub_server

    return 1
  fi

  # Phase 2 assertions.
  if ! assert_file_contains "$meta_plan" '"mode": "code-forge"' "$case_id phase2 mode-plan.json"; then
    stop_llm_stub_server

    return 1
  fi
  if ! assert_file_contains "$inputs_jsonl" "You MUST use forge" "$case_id phase2 inputs.jsonl"; then
    stop_llm_stub_server

    return 1
  fi

  stop_llm_stub_server

  if [[ "${KEEP_TEST_CONFIG:-}" != "true" ]]; then
    rm -rf "$stub_dir"
    rm -rf "$(dirname "$stub_config")"
  fi
  echo "Passed: $case_id" >&2
}

# test_code_forge_resume_without_mode starts a session with --mode code-forge,
# captures the session ID, then resumes WITHOUT --mode.  It asserts that
# mode-plan.json mode remains "code-forge" and the forge instruction is still
# present in inputs.jsonl.
test_code_forge_resume_without_mode() {
  local case_id="code-forge-resume-without-mode"
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

  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_init="$out_dir/stdout-init-${session_id}.txt"
  local stderr_init="$out_dir/stderr-init-${session_id}.txt"
  local stdout_resume="$out_dir/stdout-resume-${session_id}.txt"
  local stderr_resume="$out_dir/stderr-resume-${session_id}.txt"

  echo "Running code-forge-resume-without-mode case..." >&2

  # Phase 1: Start a session with --mode code-forge.
  local rc=0
  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$stub_config" \
  timeout 120 "$MCT_AGENT" run \
    --max-turns 2 \
    \
    --turn-timeout 300 \
    --mode code-forge \
    --model "$stub_alias" \
    --orch-model "$stub_alias" \
    --file-discovery-model "$stub_alias" \
    --text "confirm the full path to README.md in the cwd" \
    > "$stdout_init" 2> "$stderr_init"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed initial run (rc=$rc): $case_id" >&2
    cat "$stderr_init" >&2 || true
    stop_llm_stub_server

    return 1
  fi

  local agent_session
  agent_session=$(extract_agent_session_id "$stdout_init" "$stderr_init")
  if [[ -z "$agent_session" ]]; then
    echo "Failed to parse session ID for $case_id" >&2
    stop_llm_stub_server

    return 1
  fi

  local sessions_root="$REPO_ROOT/.machtiani/sessions"
  local session_dir="$sessions_root/$agent_session"
  local meta_plan="$session_dir/mode-plan.json"
  local inputs_jsonl="$session_dir/artifacts/llm/inputs.jsonl"

  # Phase 1 assertions.
  if ! assert_file_contains "$meta_plan" '"mode": "code-forge"' "$case_id phase1 mode-plan.json"; then
    stop_llm_stub_server

    return 1
  fi
  if ! assert_file_contains "$inputs_jsonl" "You MUST use forge" "$case_id phase1 inputs.jsonl"; then
    stop_llm_stub_server

    return 1
  fi

  # Phase 2: Resume the session WITHOUT --mode.
  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$stub_config" \
  timeout 120 "$MCT_AGENT" run \
    --max-turns 2 \
    \
    --turn-timeout 300 \
    --session-id "$agent_session" \
    --model "$stub_alias" \
    --orch-model "$stub_alias" \
    --file-discovery-model "$stub_alias" \
    --text "Continue." \
    > "$stdout_resume" 2> "$stderr_resume"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed resume run (rc=$rc): $case_id" >&2
    cat "$stderr_resume" >&2 || true
    stop_llm_stub_server

    return 1
  fi

  # Phase 2 assertions: mode must still be "code-forge".
  if ! assert_file_contains "$meta_plan" '"mode": "code-forge"' "$case_id phase2 mode-plan.json"; then
    stop_llm_stub_server

    return 1
  fi
  if ! assert_file_contains "$inputs_jsonl" "You MUST use forge" "$case_id phase2 inputs.jsonl"; then
    stop_llm_stub_server

    return 1
  fi

  stop_llm_stub_server

  if [[ "${KEEP_TEST_CONFIG:-}" != "true" ]]; then
    rm -rf "$stub_dir"
    rm -rf "$(dirname "$stub_config")"
  fi
  echo "Passed: $case_id" >&2
}

# test_code_resume_without_mode_no_forge starts a session with --mode code
# (no forge), captures the session ID, then resumes WITHOUT --mode.  It asserts
# mode-plan.json mode is still "code" and inputs.jsonl still does NOT contain
# the forge instruction.
test_code_resume_without_mode_no_forge() {
  local case_id="code-resume-without-mode-no-forge"
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

  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_init="$out_dir/stdout-init-${session_id}.txt"
  local stderr_init="$out_dir/stderr-init-${session_id}.txt"
  local stdout_resume="$out_dir/stdout-resume-${session_id}.txt"
  local stderr_resume="$out_dir/stderr-resume-${session_id}.txt"

  echo "Running code-resume-without-mode-no-forge case..." >&2

  # Phase 1: Start a session with --mode code (no forge).
  local rc=0
  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$stub_config" \
  timeout 120 "$MCT_AGENT" run \
    --max-turns 2 \
    \
    --turn-timeout 300 \
    --mode code \
    --model "$stub_alias" \
    --orch-model "$stub_alias" \
    --file-discovery-model "$stub_alias" \
    --text "confirm the full path to README.md in the cwd" \
    > "$stdout_init" 2> "$stderr_init"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed initial run (rc=$rc): $case_id" >&2
    cat "$stderr_init" >&2 || true
    stop_llm_stub_server

    return 1
  fi

  local agent_session
  agent_session=$(extract_agent_session_id "$stdout_init" "$stderr_init")
  if [[ -z "$agent_session" ]]; then
    echo "Failed to parse session ID for $case_id" >&2
    stop_llm_stub_server

    return 1
  fi

  local sessions_root="$REPO_ROOT/.machtiani/sessions"
  local session_dir="$sessions_root/$agent_session"
  local meta_plan="$session_dir/mode-plan.json"
  local inputs_jsonl="$session_dir/artifacts/llm/inputs.jsonl"

  # Phase 1 assertions.
  if ! assert_file_contains "$meta_plan" '"mode": "code"' "$case_id phase1 mode-plan.json"; then
    stop_llm_stub_server

    return 1
  fi
  if ! assert_file_not_contains "$inputs_jsonl" "You MUST use forge" "$case_id phase1 inputs.jsonl"; then
    stop_llm_stub_server

    return 1
  fi

  # Phase 2: Resume the session WITHOUT --mode.
  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$stub_config" \
  timeout 120 "$MCT_AGENT" run \
    --max-turns 2 \
    \
    --turn-timeout 300 \
    --session-id "$agent_session" \
    --model "$stub_alias" \
    --orch-model "$stub_alias" \
    --file-discovery-model "$stub_alias" \
    --text "Continue." \
    > "$stdout_resume" 2> "$stderr_resume"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed resume run (rc=$rc): $case_id" >&2
    cat "$stderr_resume" >&2 || true
    stop_llm_stub_server

    return 1
  fi

  # Phase 2 assertions: mode must still be "code" and no forge.
  if ! assert_file_contains "$meta_plan" '"mode": "code"' "$case_id phase2 mode-plan.json"; then
    stop_llm_stub_server

    return 1
  fi
  if ! assert_file_not_contains "$inputs_jsonl" "You MUST use forge" "$case_id phase2 inputs.jsonl"; then
    stop_llm_stub_server

    return 1
  fi

  stop_llm_stub_server

  if [[ "${KEEP_TEST_CONFIG:-}" != "true" ]]; then
    rm -rf "$stub_dir"
    rm -rf "$(dirname "$stub_config")"
  fi
  echo "Passed: $case_id" >&2
}

# test_finalize_reminder verifies that the shell-agent respects
# finalize_remaining_steps by issuing a reminder when remaining steps
# fall at or below the threshold.
test_finalize_reminder() {
  local case_id="finalize-reminder"

  if [[ "$LIVE_MODE" != true ]]; then
    echo "Skipping $case_id (needs LIVE_MODE=true)" >&2
    return 0
  fi

  local config_root
  config_root="$(mktemp -d "$TMP_ROOT/config-finalize.XXXXXX")"
  cp -R "$(dirname "$TEST_CONFIG_FILE")" "$config_root/"

  local local_config="$config_root/.machtiani/config.toml"
  "$PYTHON_BIN" - "$local_config" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
lines = path.read_text(encoding="utf-8").splitlines()

def replace_section_key(src_lines, section_name, key, value):
    out = []
    in_section = False
    found_section = False
    wrote_key = False
    for line in src_lines:
        stripped = line.strip()
        if stripped.startswith("[") and stripped.endswith("]"):
            if in_section and not wrote_key:
                out.append(f"{key} = {value}")
                wrote_key = True
            in_section = stripped == f"[{section_name}]"
            if in_section:
                found_section = True
            out.append(line)
            continue
        if in_section and re.match(rf"^\s*{re.escape(key)}\s*=", line):
            out.append(f"{key} = {value}")
            wrote_key = True
            continue
        out.append(line)
    if in_section and not wrote_key:
        out.append(f"{key} = {value}")
    if not found_section:
        if out and out[-1] != "":
            out.append("")
        out.extend([f"[{section_name}]", f"{key} = {value}"])
    return out

lines = replace_section_key(lines, "shell-agent", "max_steps", "10")
lines = replace_section_key(lines, "shell-agent", "finalize_remaining_steps", "6")
path.write_text("\n".join(lines) + "\n", encoding="utf-8")
PY

  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_file="$out_dir/stdout-${session_id}.txt"
  local stderr_file="$out_dir/stderr-${session_id}.txt"

  echo "Running finalize-reminder case..." >&2

  local rc=0
  pushd "$REPO_ROOT" >/dev/null
  set +e
  MACHTIANI_CONFIG="$local_config" \
  MACHTIANI_SESSION_ID="$session_id" \
  timeout 600 "$MCT_AGENT" run \
    --max-turns 15 \
    --turn-timeout 300 \
    "${DEFAULT_MODEL_ARGS[@]}" \
    --text "Investigate the repository structure: find the main Go package for the mct-agent binary, list its key source files, and identify what Go version is required in go.mod. Report your findings step by step." \
    > "$stdout_file" 2> "$stderr_file"
  rc=$?
  set -e
  popd >/dev/null

  if [[ $rc -ne 0 ]]; then
    echo "Failed run (rc=$rc): $case_id" >&2
    cat "$stderr_file" >&2 || true
    rm -rf "$config_root"
    return 1
  fi

  local agent_session="$session_id"

  local sessions_root="$REPO_ROOT/.machtiani/sessions"
  local session_dir="$sessions_root/$agent_session"
  if [[ ! -d "$session_dir" ]]; then
    echo "Session directory missing: $session_dir" >&2
    rm -rf "$config_root"
    return 1
  fi

  local chat_dir="$session_dir/chat"
  if [[ ! -d "$chat_dir" ]]; then
    echo "Chat directory missing: $chat_dir" >&2
    rm -rf "$config_root"
    return 1
  fi

  local final_path="$chat_dir/agent-final-answer.md"
  if [[ ! -s "$final_path" ]]; then
    echo "Missing final artifact in session directory: $final_path" >&2
    rm -rf "$config_root"
    return 1
  fi

  if grep -qF "LimitsExceeded" "$stdout_file"; then
    echo "Unexpected 'LimitsExceeded' in stdout for $case_id" >&2
    rm -rf "$config_root"
    return 1
  fi

  rm -rf "$config_root"
  echo "Passed: $case_id" >&2
}

run_resume_from_conversation_json_case() {
  local case_id="resume-from-conversation-json"
  local session_id="test-${case_id}-$(date +%s)"
  local out_dir="$(pwd)/test-out-${session_id}"
  mkdir -p "$out_dir"
  local stdout_interrupt="$out_dir/stdout-interrupt-${session_id}.txt"
  local stderr_interrupt="$out_dir/stderr-interrupt-${session_id}.txt"
  local stdout_resume="$out_dir/stdout-resume-${session_id}.txt"
  local stderr_resume="$out_dir/stderr-resume-${session_id}.txt"

  echo "Running resume-from-conversation-json case: $case_id..." >&2

  # Run 1: start a session that completes 1-2 turns, then SIGINT it via timeout.
  # The agent handles SIGTERM gracefully and saves session state.
  local rc=0
  local run_started_epoch
  run_started_epoch=$(date +%s)
  pushd "$REPO_ROOT" >/dev/null
  set +e
  timeout 420 "$MCT_AGENT" run \
    --max-turns 3 \
    \
    --turn-timeout 300 \
    "${DEFAULT_MODEL_ARGS[@]}" \
    --text "Identify the main components of the mct-agent binary by reading agent/README.md and agent/cmd/mct-agent/main.go. List them." \
    > "$stdout_interrupt" 2> "$stderr_interrupt"
  rc=$?
  set -e
  popd >/dev/null
  # rc 124 = timeout (expected), rc 0 = completed early (also fine)
  if [[ $rc -ne 124 && $rc -ne 0 ]]; then
    echo "Failed initial interrupt run (rc=$rc): $case_id" >&2
    return 1
  fi

  local agent_session
  agent_session=$(extract_agent_session_id "$stdout_interrupt" "$stderr_interrupt" "$run_started_epoch")
  if [[ -z "$agent_session" ]]; then
    echo "Failed to parse session ID for interrupted run in $case_id" >&2
    return 1
  fi

  local session_dir="$REPO_ROOT/.machtiani/sessions/$agent_session"
  local conv_path="$session_dir/artifacts/conversation.json"
  local state_path="$session_dir/session-state.json"
  local transcript_path="$session_dir/chat/agent-transcript.adoc"

  # Assert conversation.json exists (the canonical source of truth)
  if [[ ! -s "$conv_path" ]]; then
    echo "conversation.json missing or empty after interrupt: $conv_path" >&2
    return 1
  fi

  # If session-state.json exists, check for legacy fields (it may not exist under the current architecture)
  if [[ -e "$state_path" ]]; then
    if ! "$PYTHON_BIN" - "$state_path" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, 'r', encoding='utf-8') as fh:
    data = json.load(fh)

legacy_fields = ['transcript', 'conversation_json', 'transcript_path', 'conversation_path']
present = [f for f in legacy_fields if f in data and data[f]]
if present:
    print(f"ERROR: legacy fields present in session-state.json: {present}", file=sys.stderr)
    sys.exit(1)
PY
    then
      return 1
    fi
  fi

  # Assert .machtiani-session.json does not exist in repo root
  if [[ -e "$REPO_ROOT/.machtiani-session.json" ]]; then
    echo "ERROR: retired .machtiani-session.json exists in repo root" >&2
    return 1
  fi

  # Run 2: resume the session
  pushd "$REPO_ROOT" >/dev/null
  set +e
  timeout 420 "$MCT_AGENT" run \
    --max-turns 2 \
    \
    --turn-timeout 300 \
    --session-id "$agent_session" \
    "${DEFAULT_MODEL_ARGS[@]}" \
    --text "Continue." \
    > "$stdout_resume" 2> "$stderr_resume"
  rc=$?
  set -e
  popd >/dev/null
  if [[ $rc -ne 0 ]]; then
    echo "Failed resume run (rc=$rc): $case_id" >&2
    return 1
  fi

  # After resume: final artifact must exist
  local final_path="$session_dir/chat/agent-final-answer.md"
  if [[ ! -s "$final_path" ]]; then
    echo "Missing final artifact after resume: $final_path" >&2
    return 1
  fi

  # Transcript must exist and contain expected turns
  if [[ ! -s "$transcript_path" ]]; then
    echo "Transcript missing after resume: $transcript_path" >&2
    return 1
  fi
  local turns
  turns=$(awk 'BEGIN { c = 0 } /^== TURN / {
      if ($3 ~ /^[0-9]+$/ && ($3 + 0) > 0) {
        c++
      }
    } END { print c }' "$transcript_path")
  if [[ $turns -eq 0 ]]; then
    turns=$(grep -E -c '^Step [0-9]+ decision: ' "$stderr_resume" 2>/dev/null; true)
  fi
  if [[ $turns -lt 1 ]]; then
    echo "Transcript has fewer than 1 turn after resume: $turns turns" >&2
    return 1
  fi

  # Assert .machtiani-session.json still does not exist
  if [[ -e "$REPO_ROOT/.machtiani-session.json" ]]; then
    echo "ERROR: retired .machtiani-session.json exists after resume" >&2
    return 1
  fi

  # Assert session-state.json after resume is still slim (no legacy fields)
  if [[ -e "$state_path" ]]; then
    if ! "$PYTHON_BIN" - "$state_path" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, 'r', encoding='utf-8') as fh:
    data = json.load(fh)

legacy_fields = ['transcript', 'conversation_json', 'transcript_path', 'conversation_path']
present = [f for f in legacy_fields if f in data and data[f]]
if present:
    print(f"ERROR: legacy fields present after resume: {present}", file=sys.stderr)
    sys.exit(1)
PY
    then
      return 1
    fi
  fi

  cp -f "$transcript_path" "$out_dir/transcript-resume-${session_id}.adoc"
  cp -f "$final_path" "$out_dir/final-resume-${session_id}.md"
  cp -f "$conv_path" "$out_dir/conversation-${session_id}.json"

  echo "Passed: $case_id (conversation.json is source of truth, $turns turns)" >&2
  return 0
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
      --turn-timeout|--turn-timeout=*)
        has_timeout=true
        ;;
      --max-turns|--max-turns=*)
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
    cmd+=(--max-turns "$max_steps")
  fi
  if [[ "$has_timeout" == false ]]; then
    cmd+=(--turn-timeout 300)
  fi
  if ((${#COMMON_AGENT_ARGS[@]})); then
    cmd+=("${COMMON_AGENT_ARGS[@]}")
  fi
  if ((${#runtime_args[@]})); then
    cmd+=("${runtime_args[@]}")
  fi
  cmd+=(
    
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
  echo "Live mode: using generated config.toml from TEST_* / OPENAI_* env." >&2
else
  echo "Dry-run mode: using generated config.toml with stubbed provider; --dry-run enabled." >&2
fi

ORCH_LABEL_REGEX="$(regex_escape "$ORCH_MODEL_ALIAS")"
FILE_DISCOVERY_LABEL_REGEX="$(regex_escape "$FILE_DISCOVERY_MODEL_ALIAS")"
DEFAULT_LABEL_REGEX="$(regex_escape "$TEST_MODEL_ALIAS")"

MIXED_LABEL_PATTERN="(?s)(?=.*shell.agent)(?=.*fall.?back)(?=.*orchestrator)(?=.*model)"
CATCH_ALL_LABEL_PATTERN="(?s)(?=.*orchestrator)(?=.*file.discovery)(?=.*default)(?=.*model)"

INVALID_ALIAS="does-not-exist-alias"
INVALID_ALIAS_REGEX="$(regex_escape "$INVALID_ALIAS")"
MODEL_ALIAS_NOT_FOUND_PATTERN="model alias \"${INVALID_ALIAS_REGEX}\" not found"

if [[ $# -eq 0 ]]; then
declare -a PASS_LIST=()
declare -a FAIL_LIST=()
global_failed=0

run_test_case() {
  local name="$1"
  shift
  echo "Running $name..." >&2
  if "$@"; then
    PASS_LIST+=("$name")
    echo "Passed: $name" >&2
  else
    FAIL_LIST+=("$name")
    global_failed=1
    echo "FAILED: $name" >&2
  fi
}

if [[ "$LIVE_MODE" != true ]]; then
  # Temporarily disable the legacy `both` routing dry-run case while
  # `Ask Mode: both` is treated as a single shell ask.
  :
else
  if shell_agent_available; then
    :
  else
    echo "Skipping local tmp-root live case: shell-agent not found on PATH." >&2
  fi
  run_test_case "shell_agent_subcommand_live" run_shell_agent_subcommand_live_case
  run_test_case "enforce_early_commands" run_enforce_early_commands_case
  run_test_case "snippet_discovery_tightness" run_snippet_discovery_tightness_live_case
  run_test_case "menu_flow_live" run_menu_flow_live_case
  run_test_case "mode_live" run_mode_live_case
  run_test_case "file_discovery_live" run_file_discovery_live_case
  run_test_case "show_live" run_show_live_case
  run_test_case "show_range_live" run_show_range_live_case
  run_test_case "resume_from_conversation_json" run_resume_from_conversation_json_case
  run_test_case "shell_command_trajectory_live" run_shell_command_trajectory_live_case
  if shell_agent_available; then
    run_test_case "shell_live" run_shell_live_case
  else
    echo "Skipping shell-only live case: shell-agent not found on PATH." >&2
  fi
fi
fi  # $# -eq 0 guard

# --- Named test dispatch -------------------------------------------------
# When called with one or more test names (e.g. ./run-live.sh test_code_no_forge),
# run only the requested tests in order.  Each name must map to a registered
# function in the TESTS array.

test_models_mixed_fallback() {
  run_happy_case "models-mixed-fallback" 3 \
    "Summarize how shell-agent falls back to the orchestrator model when unspecified." \
    "$MIXED_LABEL_PATTERN" \
    1 \
    \
    --orch-model "$ORCH_MODEL_ALIAS"
}

test_models_catch_all() {
  run_happy_case "models-catch-all" 1 \
    "Describe the default model wiring for orchestrator and file discovery." \
    "$CATCH_ALL_LABEL_PATTERN" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

test_issue_a_1turn() {
  run_happy_case "issue-a-1turn" 1 \
    "What is the main purpose of the mct-agent binary?" \
    "===> FINAL RESPONSE <===" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

test_issue_a_3turn() {
  run_happy_case "issue-a-3turn" 3 \
    "What is the main purpose of the mct-agent binary?" \
    "===> FINAL RESPONSE <===" \
    "${DEFAULT_MODEL_ARGS[@]}"
}

test_issue_b_1turn() {
  run_happy_case "issue-b-1turn" 1 \
    "Describe the full multi-turn flow in mct-agent, including planning and context retention." \
    "multi-turn|conversation|context|planner|ask" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

test_issue_b_3turn() {
  run_happy_case "issue-b-3turn" 3 \
    "Describe the full multi-turn flow in mct-agent, including planning and context retention." \
    "multi-turn|conversation|context|planner|ask" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

test_planner_ask_monitor() {
  run_happy_case "planner-ask-monitor" 2 \
    "Explain the planner ask monitor guardrail flow and when it retries. If you need repository context, ask for it." \
    "(?s)(?=.*ask)(?=.*monitor)" \
    1 \
    "${DEFAULT_MODEL_ARGS[@]}"
}

test_issue_c_1turn() {
  run_happy_case "issue-c-1turn" 1 \
    "Explain how mct-agent handles errors during finalization and transcript writing." \
    "error|handling|finalize|transcript|fallback" \
    "${DEFAULT_MODEL_ARGS[@]}"
}

test_issue_c_3turn() {
  run_happy_case "issue-c-3turn" 3 \
    "Explain how mct-agent handles errors during finalization and transcript writing, with examples from code." \
    "error|handling|finalize|transcript|fallback" \
    "${DEFAULT_MODEL_ARGS[@]}"
}

test_empty_goal() {
  run_error_case "empty-goal" "" 1 "one of --text or --file is required" "${DEFAULT_MODEL_ARGS[@]}"
}

test_invalid_orch_model() {
  run_error_case "invalid-orch-model" "" 1 "$MODEL_ALIAS_NOT_FOUND_PATTERN" \
    "Trigger orchestrator alias failure" \
    --orch-model "$INVALID_ALIAS"
}

test_invalid_file_discovery_model() {
  run_error_case "invalid-file-discovery-model" "" 1 "$MODEL_ALIAS_NOT_FOUND_PATTERN" \
    "Trigger file discovery alias failure" \
    --file-discovery-model "$INVALID_ALIAS" \
    "${DEFAULT_MODEL_ARGS[@]}"
}

test_missing_config() {
  MACHTIANI_CONFIG="/nonexistent/machtiani-config.toml" OPENAI_API_KEY="" OPENAI_BASE_URL="" OPENAI_MODEL="" \
  run_error_case "missing-config" "" 1 "Missing model config|Model resolution error|MACHTIANI_CONFIG" \
    "Explain how the agent chooses its model runtime."
}

declare -A TESTS=(
  ["test_local_tmp_root_unset_live"]="run_local_tmp_root_unset_live_case"
  ["test_code_no_forge"]="test_code_no_forge"
  ["test_code_forge_initial"]="test_code_forge_initial"
  ["test_code_forge_resume_with_mode"]="test_code_forge_resume_with_mode"
  ["test_code_forge_resume_without_mode"]="test_code_forge_resume_without_mode"
  ["test_code_resume_without_mode_no_forge"]="test_code_resume_without_mode_no_forge"
  ["test_finalize_reminder"]="test_finalize_reminder"
  ["test_mode_prompt_layers"]="run_mode_prompt_layers_case"
  ["test_enforce_early_commands"]="run_enforce_early_commands_case"
  ["shell_agent_subcommand_live"]="run_shell_agent_subcommand_live_case"
  ["snippet_discovery_tightness_live"]="run_snippet_discovery_tightness_live_case"
  ["menu_flow_live"]="run_menu_flow_live_case"
  ["mode_live"]="run_mode_live_case"
  ["file_discovery_live"]="run_file_discovery_live_case"
  ["show_live"]="run_show_live_case"
  ["show_range_live"]="run_show_range_live_case"
  ["resume_from_conversation_json"]="run_resume_from_conversation_json_case"
  ["shell_command_trajectory_live"]="run_shell_command_trajectory_live_case"
  ["shell_live"]="run_shell_live_case"
  ["resume_without_mode"]="run_resume_without_mode_case"
  ["models-mixed-fallback"]="test_models_mixed_fallback"
  ["models-catch-all"]="test_models_catch_all"
  ["issue-a-1turn"]="test_issue_a_1turn"
  ["issue-a-3turn"]="test_issue_a_3turn"
  ["issue-b-1turn"]="test_issue_b_1turn"
  ["issue-b-3turn"]="test_issue_b_3turn"
  ["planner-ask-monitor"]="test_planner_ask_monitor"
  ["issue-c-1turn"]="test_issue_c_1turn"
  ["issue-c-3turn"]="test_issue_c_3turn"
  ["empty-goal"]="test_empty_goal"
  ["invalid-orch-model"]="test_invalid_orch_model"
  ["invalid-file-discovery-model"]="test_invalid_file_discovery_model"
  ["missing-config"]="test_missing_config"
)

if [[ $# -gt 0 ]]; then
  # Named-test mode: run each argument as a test name, exit on first failure.
  failed=0
  for tname in "$@"; do
    tfunc="${TESTS[$tname]:-}"
    if [[ -z "$tfunc" ]]; then
      echo "ERROR: unknown test name '$tname'" >&2
      echo "Available: ${!TESTS[*]}" >&2
      exit 1
    fi
    if ! "$tfunc"; then
      echo "FAILED: $tname" >&2
      failed=1
    else
      echo "OK: $tname" >&2
    fi
  done
  if [[ $failed -ne 0 ]]; then
    echo "One or more named tests failed." >&2
    exit 1
  fi
  echo "All requested tests passed." >&2
  exit 0
fi

if [[ $# -eq 0 ]]; then
# Per-component flag coverage.
run_test_case "mode_prompt_layers" run_mode_prompt_layers_case

# Resume without mode: verify shell-agent system prompt survives
# a mode-less resume (uses stub server).
run_test_case "resume_without_mode" run_resume_without_mode_case

if [[ "$LIVE_MODE" == true ]]; then
  run_test_case "code_no_forge" test_code_no_forge
  run_test_case "code_forge_initial" test_code_forge_initial
  run_test_case "code_forge_resume_with_mode" test_code_forge_resume_with_mode
  run_test_case "code_forge_resume_without_mode" test_code_forge_resume_without_mode
  run_test_case "code_resume_without_mode_no_forge" test_code_resume_without_mode_no_forge
  run_test_case "finalize_reminder" test_finalize_reminder
fi

run_test_case "models-mixed-fallback" run_happy_case "models-mixed-fallback" 3 \
  "Summarize how shell-agent falls back to the orchestrator model when unspecified." \
  "$MIXED_LABEL_PATTERN" \
  1 \
  \
  --orch-model "$ORCH_MODEL_ALIAS"

run_test_case "models-catch-all" run_happy_case "models-catch-all" 1 \
  "Describe the default model wiring for orchestrator and file discovery." \
  "$CATCH_ALL_LABEL_PATTERN" \
  1 \
  "${DEFAULT_MODEL_ARGS[@]}"

run_test_case "issue-a-1turn" run_happy_case "issue-a-1turn" 1 \
  "What is the main purpose of the mct-agent binary?" \
  "===> FINAL RESPONSE <===" \
  1 \
  "${DEFAULT_MODEL_ARGS[@]}"

run_test_case "issue-a-3turn" run_happy_case "issue-a-3turn" 3 \
  "What is the main purpose of the mct-agent binary?" \
  "===> FINAL RESPONSE <===" \
  "${DEFAULT_MODEL_ARGS[@]}"

run_test_case "issue-b-1turn" run_happy_case "issue-b-1turn" 1 \
  "Describe the full multi-turn flow in mct-agent, including planning and context retention." \
  "multi-turn|conversation|context|planner|ask" \
  1 \
  "${DEFAULT_MODEL_ARGS[@]}"

run_test_case "issue-b-3turn" run_happy_case "issue-b-3turn" 3 \
  "Describe the full multi-turn flow in mct-agent, including planning and context retention." \
  "multi-turn|conversation|context|planner|ask" \
  1 \
  "${DEFAULT_MODEL_ARGS[@]}"

run_test_case "planner-ask-monitor" run_happy_case "planner-ask-monitor" 2 \
  "Explain the planner ask monitor guardrail flow and when it retries. If you need repository context, ask for it." \
  "(?s)(?=.*ask)(?=.*monitor)" \
  1 \
  "${DEFAULT_MODEL_ARGS[@]}"



run_test_case "issue-c-1turn" run_happy_case "issue-c-1turn" 1 \
  "Explain how mct-agent handles errors during finalization and transcript writing." \
  "error|handling|finalize|transcript|fallback" \
  "${DEFAULT_MODEL_ARGS[@]}"

run_test_case "issue-c-3turn" run_happy_case "issue-c-3turn" 3 \
  "Explain how mct-agent handles errors during finalization and transcript writing, with examples from code." \
  "error|handling|finalize|transcript|fallback" \
  "${DEFAULT_MODEL_ARGS[@]}"

run_test_case "empty-goal" run_error_case "empty-goal" "" 1 "one of --text or --file is required" "${DEFAULT_MODEL_ARGS[@]}"

run_test_case "invalid-orch-model" run_error_case "invalid-orch-model" "" 1 "$MODEL_ALIAS_NOT_FOUND_PATTERN" \
  "Trigger orchestrator alias failure" \
  --orch-model "$INVALID_ALIAS"


run_test_case "invalid-file-discovery-model" run_error_case "invalid-file-discovery-model" "" 1 "$MODEL_ALIAS_NOT_FOUND_PATTERN" \
  "Trigger file discovery alias failure" \
  --file-discovery-model "$INVALID_ALIAS" \
  "${DEFAULT_MODEL_ARGS[@]}"

if [[ "$LIVE_MODE" == true ]]; then
  MACHTIANI_CONFIG="/nonexistent/machtiani-config.toml" OPENAI_API_KEY="" OPENAI_BASE_URL="" OPENAI_MODEL="" \
  run_test_case "missing-config" run_error_case "missing-config" "" 1 "Missing model config|Model resolution error|MACHTIANI_CONFIG" \
    "Explain how the agent chooses its model runtime."

else
  echo "Skipping missing-config error case in dry-run mode (requires live env)." >&2
fi


echo "Skipping missing-mct and timeout simulations: preflight ensures PATH binaries and no stub overrides." >&2

  echo "=== Test Summary ===" >&2
  echo "Total: $(( ${#PASS_LIST[@]} + ${#FAIL_LIST[@]} )) | Passed: ${#PASS_LIST[@]} | Failed: ${#FAIL_LIST[@]}" >&2
  if [[ ${#FAIL_LIST[@]} -gt 0 ]]; then
    echo "Failed tests:" >&2
    for t in "${FAIL_LIST[@]}"; do echo "  - $t" >&2; done
  fi
fi  # $# -eq 0 guard
exit $global_failed
