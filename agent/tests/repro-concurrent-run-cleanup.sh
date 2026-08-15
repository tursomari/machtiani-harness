#!/usr/bin/env bash
set -euo pipefail

# Reproduces and verifies the concurrent startup cleanup bug where a second
# `machtiani run` could remove the first session's live `workspace-*` tree.
#
# Modes:
# - EXPECT_REPRO=true: succeed only if the old bug is reproduced.
# - EXPECT_REPRO=false: succeed only if the workspace survives concurrent start.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PYTHON_BIN="${PYTHON_BIN:-python3}"
TMP_ROOT="${TMP_ROOT:-$SCRIPT_DIR/tmp}"
KEEP_REPRO_ARTIFACTS="${KEEP_REPRO_ARTIFACTS:-false}"
STUB_DELAY_SECONDS="${STUB_DELAY_SECONDS:-12}"
EXPECT_REPRO="${EXPECT_REPRO:-true}"
mkdir -p "$TMP_ROOT"

case "$EXPECT_REPRO" in
  true|false) ;;
  *)
    echo "EXPECT_REPRO must be true or false (got: $EXPECT_REPRO)" >&2
    exit 1
    ;;
esac

if ! command -v machtiani >/dev/null 2>&1; then
  echo "machtiani not found on PATH" >&2
  exit 1
fi

WORK_DIR="$(mktemp -d "$TMP_ROOT/repro-concurrent-run-cleanup.XXXXXX")"
PORT_FILE="$WORK_DIR/stub.port"
STUB_LOG="$WORK_DIR/stub.log"
CONFIG_ROOT="$WORK_DIR/config/.machtiani"
CONFIG_FILE="$CONFIG_ROOT/config.toml"
STUB_ALIAS="stub-model"
STUB_PID=""
RUN_A_PID=""
RUN_B_PID=""

cleanup() {
  set +e
  if [[ -n "$RUN_A_PID" ]]; then
    kill "$RUN_A_PID" 2>/dev/null || true
    wait "$RUN_A_PID" 2>/dev/null || true
  fi
  if [[ -n "$RUN_B_PID" ]]; then
    kill "$RUN_B_PID" 2>/dev/null || true
    wait "$RUN_B_PID" 2>/dev/null || true
  fi
  if [[ -n "$STUB_PID" ]]; then
    kill "$STUB_PID" 2>/dev/null || true
    wait "$STUB_PID" 2>/dev/null || true
  fi
  if [[ "$KEEP_REPRO_ARTIFACTS" != "true" ]]; then
    rm -rf "$WORK_DIR"
  fi
}
trap cleanup EXIT

cat > "$WORK_DIR/stub.py" <<'PY'
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

port_path = sys.argv[1]
delay = float(sys.argv[2])

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
        time.sleep(delay)
        payload = {
            "id": "stub-1",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": data.get("model", "stub-model"),
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": "Stub response."},
                    "finish_reason": "stop",
                }
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
server.serve_forever()
PY

"$PYTHON_BIN" "$WORK_DIR/stub.py" "$PORT_FILE" "$STUB_DELAY_SECONDS" >"$STUB_LOG" 2>&1 &
STUB_PID=$!

for _ in $(seq 1 100); do
  [[ -s "$PORT_FILE" ]] && break
  sleep 0.1
done

if [[ ! -s "$PORT_FILE" ]]; then
  echo "stub server failed to start; log follows:" >&2
  cat "$STUB_LOG" >&2 || true
  exit 1
fi

STUB_PORT="$(cat "$PORT_FILE")"
mkdir -p "$CONFIG_ROOT"
cp "$REPO_ROOT/.machtiani/config.toml" "$CONFIG_FILE"

"$PYTHON_BIN" - "$CONFIG_FILE" "$STUB_ALIAS" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
alias = sys.argv[2]
lines = path.read_text(encoding="utf-8").splitlines()
for idx, line in enumerate(lines):
    if re.match(r"^\s*default_model\s*=", line):
        lines[idx] = f'default_model = "{alias}"'
        break
else:
    insert_idx = 0
    while insert_idx < len(lines):
        stripped = lines[insert_idx].strip()
        if stripped and not stripped.startswith("#"):
            break
        insert_idx += 1
    lines.insert(insert_idx, f'default_model = "{alias}"')
path.write_text("\n".join(lines) + "\n", encoding="utf-8")
PY

cat >> "$CONFIG_FILE" <<EOF

[providers.concurrent_repro_stub]
base_url = "http://127.0.0.1:${STUB_PORT}/v1"
api_key = "stub-key"
endpoint = "/chat/completions"

[models."${STUB_ALIAS}"]
provider = "concurrent_repro_stub"
model = "${STUB_ALIAS}"
EOF

LAST_PID=""

launch_run() {
  local label="$1"
  local prompt="$2"
  local stdout_file="$WORK_DIR/${label}.stdout"
  local stderr_file="$WORK_DIR/${label}.stderr"
  (
    trap - EXIT
    cd "$REPO_ROOT"
    MACHTIANI_CONFIG="$CONFIG_FILE" \
      timeout 180 machtiani run \
      --max-turns 1 \
      --turn-timeout 120 \
      --persist-tmp-data \
      --patch-no-apply \
      --verbose \
      --model "$STUB_ALIAS" \
      --orch-model "$STUB_ALIAS" \
      --file-discovery-model "$STUB_ALIAS" \
      -p "$prompt" \
      >"$stdout_file" 2>"$stderr_file"
  ) &
  LAST_PID=$!
}

wait_for_session_id() {
  local stderr_file="$1"
  local session_id=""
  for _ in $(seq 1 200); do
    session_id="$(grep -m1 '^Session:' "$stderr_file" 2>/dev/null | awk '{print $2}')"
    if [[ -n "$session_id" ]]; then
      printf '%s\n' "$session_id"
      return 0
    fi
    sleep 0.1
  done
  return 1
}

wait_for_dir() {
  local path="$1"
  for _ in $(seq 1 200); do
    [[ -d "$path" ]] && return 0
    sleep 0.1
  done
  return 1
}

launch_run a 'What is the main purpose of the machtiani binary?'
RUN_A_PID="$LAST_PID"
SESSION_A="$(wait_for_session_id "$WORK_DIR/a.stderr")"
WORKSPACE_A="$REPO_ROOT/.machtiani/tmp/workspace-${SESSION_A}"
LOCK_A="$REPO_ROOT/.machtiani/tmp/${SESSION_A}/session.lock"

if ! wait_for_dir "$WORKSPACE_A"; then
  echo "session A workspace was never created: $WORKSPACE_A" >&2
  cat "$WORK_DIR/a.stderr" >&2 || true
  exit 1
fi

if ! wait_for_dir "$(dirname "$LOCK_A")"; then
  echo "session A scratch dir was never created: $(dirname "$LOCK_A")" >&2
  cat "$WORK_DIR/a.stderr" >&2 || true
  exit 1
fi

launch_run b 'Describe the full multi-turn flow in machtiani.'
RUN_B_PID="$LAST_PID"
SESSION_B="$(wait_for_session_id "$WORK_DIR/b.stderr")"

deleted_workspace=false
session_a_alive_when_deleted=false
for _ in $(seq 1 200); do
  if [[ ! -d "$WORKSPACE_A" ]]; then
    deleted_workspace=true
    if kill -0 "$RUN_A_PID" 2>/dev/null; then
      session_a_alive_when_deleted=true
    fi
    break
  fi
  sleep 0.1
done

echo "stub_delay_seconds=$STUB_DELAY_SECONDS"
echo "expect_repro=$EXPECT_REPRO"
echo "session_a=$SESSION_A"
echo "session_b=$SESSION_B"
echo "workspace_a=$WORKSPACE_A"
echo "lock_a=$LOCK_A"
echo "workspace_a_exists=$( [[ -d "$WORKSPACE_A" ]] && echo true || echo false )"
echo "lock_a_exists=$( [[ -f "$LOCK_A" ]] && echo true || echo false )"
echo "deleted_workspace=$deleted_workspace"
echo "session_a_alive_when_deleted=$session_a_alive_when_deleted"
echo "artifacts_dir=$WORK_DIR"

if [[ "$deleted_workspace" == "true" && "$session_a_alive_when_deleted" == "true" ]]; then
  if [[ "$EXPECT_REPRO" == "true" ]]; then
    echo "REPRODUCED: session B startup cleanup removed session A workspace while session A was still running."
    exit 0
  fi
  echo "UNEXPECTED REPRODUCTION: session B removed session A workspace after fix was expected." >&2
  echo "--- session A stderr ---" >&2
  cat "$WORK_DIR/a.stderr" >&2 || true
  echo "--- session B stderr ---" >&2
  cat "$WORK_DIR/b.stderr" >&2 || true
  exit 1
fi

if [[ "$EXPECT_REPRO" == "false" ]]; then
  echo "FIX CONFIRMED: concurrent session startup did not remove the live workspace."
  exit 0
fi

echo "REPRO NOT CONFIRMED" >&2
echo "--- session A stderr ---" >&2
cat "$WORK_DIR/a.stderr" >&2 || true
echo "--- session B stderr ---" >&2
cat "$WORK_DIR/b.stderr" >&2 || true
exit 1
