#!/usr/bin/env bash
set -euo pipefail

MCT_AGENT_BIN="${MCT_AGENT_BIN:-mct-agent}"
SMOKE_REPO="${MCT_SUPERVISOR_SMOKE_REPO:-$PWD}"

if [[ "$MCT_AGENT_BIN" != /* ]]; then
  MCT_AGENT_BIN="$(command -v "$MCT_AGENT_BIN")"
fi
if [[ ! -x "$MCT_AGENT_BIN" ]]; then
  echo "ERROR: command-supervisor smoke binary is not executable: $MCT_AGENT_BIN" >&2
  exit 1
fi
if ! git -C "$SMOKE_REPO" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "ERROR: command-supervisor smoke requires a Git repository: $SMOKE_REPO" >&2
  exit 1
fi

smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/mct-command-supervisor-smoke.XXXXXX")
server_pid=""
cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf "$smoke_root"
}
trap cleanup EXIT

port_file="$smoke_root/port"
state_file="$smoke_root/state.json"
python3 - "$port_file" "$state_file" <<'PY' &
import json
import pathlib
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

port_path = pathlib.Path(sys.argv[1])
state_path = pathlib.Path(sys.argv[2])
state = {"parent_requests": 0, "supervisor_requests": 0}

def save():
    state_path.write_text(json.dumps(state), encoding="utf-8")

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        return

    def do_POST(self):
        length = int(self.headers.get("content-length", "0"))
        try:
            body = json.loads(self.rfile.read(length).decode("utf-8"))
        except Exception:
            self.send_error(400)
            return
        messages = body.get("messages") or []
        content = "\n".join(str(message.get("content") or "") for message in messages if isinstance(message, dict))
        if "You are the command supervisor for a shell-agent task." in content:
            state["supervisor_requests"] += 1
            reply = '<answer>{"disposition":"cancel","summary":"the planted go command is intentionally blocked"}</answer>'
        else:
            state["parent_requests"] += 1
            if "command stopped by supervisor" in content:
                reply = "<answer>Command supervisor smoke recovered after stopping the planted blocker.</answer>"
            else:
                reply = "<command>go test ./...</command>"
        save()
        payload = {
            "id": "command-supervisor-smoke",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": body.get("model", "supervisor-smoke"),
            "choices": [{"index": 0, "message": {"role": "assistant", "content": reply}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
        }
        encoded = json.dumps(payload).encode("utf-8")
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
port_path.write_text(str(server.server_address[1]), encoding="utf-8")
save()
server.serve_forever()
PY
server_pid=$!

for _ in $(seq 1 100); do
  [[ -s "$port_file" ]] && break
  sleep 0.05
done
if [[ ! -s "$port_file" ]]; then
  echo "ERROR: command-supervisor smoke server did not start" >&2
  exit 1
fi
port=$(cat "$port_file")

config_file="$smoke_root/config.toml"
cat >"$config_file" <<EOF
default_model = "supervisor-smoke"

[planner]
max_turns = 3
turn_timeout = 0

[shell-agent]
max_steps = 8
finalize_remaining_steps = 2
command_supervisor_after = 1
command_supervisor_timeout = 2
command_supervisor_failure_limit = 2
command_supervisor_max_steps = 4
command_supervisor_deadline_buffer = 2

[environment]
type = "local"
command_timeout = 10
max_command_output_bytes = 65536

[providers.supervisor-smoke]
base_url = "http://127.0.0.1:${port}/v1"
api_key = "smoke-key"
endpoint = "/chat/completions"

[models.supervisor-smoke]
provider = "supervisor-smoke"
model = "supervisor-smoke"
context_length = 128000

[ui]
theme = "none"
EOF

blocker_dir="$smoke_root/bin"
blocker_pid_file="$smoke_root/blocker.pid"
mkdir -p "$blocker_dir"
cat >"$blocker_dir/go" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$$" >"$MCT_BLOCKER_PID_FILE"
printf 'planted go blocker pid=%s args=%s\n' "$$" "$*"
while :; do
  sleep 1
done
EOF
chmod +x "$blocker_dir/go"

stdout_file="$smoke_root/stdout"
stderr_file="$smoke_root/stderr"
started=$(date +%s)
set +e
(
  cd "$SMOKE_REPO"
  PATH="$blocker_dir:$PATH" \
    MCT_BLOCKER_PID_FILE="$blocker_pid_file" \
    MACHTIANI_CONFIG="$config_file" \
    timeout 25 "$MCT_AGENT_BIN" shell-agent \
      --model supervisor-smoke \
      --text "Run go test ./... exactly once and report the result."
) >"$stdout_file" 2>"$stderr_file"
status=$?
set -e
elapsed=$(( $(date +%s) - started ))
if (( status != 0 )); then
  echo "ERROR: command-supervisor smoke failed with status $status" >&2
  cat "$stdout_file" >&2 || true
  cat "$stderr_file" >&2 || true
  exit 1
fi
if (( elapsed < 1 || elapsed >= 15 )); then
  echo "ERROR: command-supervisor smoke elapsed ${elapsed}s, expected 1-14s" >&2
  exit 1
fi
grep -Fq "Command supervisor smoke recovered" "$stdout_file"
test -s "$blocker_pid_file"

python3 - "$state_file" "$blocker_pid_file" <<'PY'
import json
import os
import pathlib
import sys
import time

state = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
if state.get("parent_requests", 0) < 2:
    raise SystemExit(f"expected at least two parent requests, got {state}")
if state.get("supervisor_requests") != 1:
    raise SystemExit(f"expected exactly one supervisor request, got {state}")

pid = int(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8").strip())
deadline = time.time() + 2
while time.time() < deadline:
    stat = pathlib.Path(f"/proc/{pid}/stat")
    if not stat.exists():
        break
    fields = stat.read_text(encoding="utf-8").split()
    if len(fields) > 2 and fields[2] == "Z":
        break
    time.sleep(0.02)
else:
    raise SystemExit(f"planted blocker PID {pid} is still running")
PY

echo "COMMAND SUPERVISOR SMOKE PASSED: planted go blocker stopped in ${elapsed}s"
