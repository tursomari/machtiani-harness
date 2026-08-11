# Registering a Custom Backend Agent

This runbook explains how to register your own agent as a DearMachine backend agent — without modifying any DearMachine source code. Use the TOML-based custom backend configuration described in the [custom backend design document](https://github.com/lessweb/deepcode-cli) (~/projects/pm/docs/custom-backend-design.md). PM ticket: `7fe89894`.

## Quick Reference: TOML Schema

Custom backends are defined in `~/.dearmachine/config/custom-backends.toml`. Each backend is a TOML table keyed by its unique ID.

```toml
[my-unique-agent-id]
executable = "/absolute/path/to/executable"    # required
output_format = "plain"                         # required: "plain" or "json-stream"
name = "My Agent Display Name"                  # optional, defaults to ID
arguments = ["--flag", "$WRITABLE_DIR"]         # optional; $WRITABLE_DIR = ticket sandbox
environment = { KEY = "value" }                 # optional; extra env vars
install_help = "Install instructions"           # optional; shown when missing
```

**Required fields:**
- `executable` — absolute path or `$PATH`-resolvable name. The agent-manager calls this binary for health checks and ticket dispatch.
- `output_format` — how to parse stdout:
  - `"plain"` — capture all stdout text (up to 64 KiB) as the agent reply.
  - `"json-stream"` — parse JSON Lines with Codex-style `type`/`item` events.

**Optional fields:**
- `name` — display name (defaults to the ID).
- `arguments` — extra CLI arguments appended to the command. `$WRITABLE_DIR` is substituted with the ticket sandbox path at launch time.
- `environment` — extra `KEY=VALUE` pairs added to the process environment.
- `install_help` — human-readable message shown by `backend list` when the executable is not found on `PATH`.

After defining your backend, approve it by listing its ID in the device configuration:

```toml
# ~/.dearmachine/config/device-client.toml
backends = ["codex", "forge", "my-unique-agent-id"]
```

Or set the `DEARMACHINE_BACKENDS` environment variable:

```bash
export DEARMACHINE_BACKENDS='["codex", "my-unique-agent-id"]'
```

## Walkthrough: Registering a Deepseek-backed Agent

This example registers a Python script that calls the Deepseek API — a lightweight "DeepCode" backend compatible with DearMachine.

### 1. Prepare your agent executable

Create `/home/david/bin/deepcode-backend` (or any path you prefer):

```python
#!/usr/bin/env python3
"""DeepCode Backend — calls Deepseek API as a stdin/stdout agent."""
import sys, os, json, re, requests

work_request = sys.stdin.read().strip()

# Handle Close-Path from ticket-open format
close_path = None
cp_match = re.search(r'^\s*#\s*Close-Path:\s*(\S+)', work_request, re.MULTILINE)
if cp_match:
    close_path = cp_match.group(1)
    parts = work_request.split('\n\n', 2)
    work_request = parts[-1].strip() if len(parts) >= 2 else work_request

# Handle health-check probe
probe_match = re.search(r'Write a file at exactly (\S+) containing exactly this line: (.+?)\.\s', work_request)
if probe_match:
    with open(probe_match.group(1), 'w') as f:
        f.write(probe_match.group(2) + '\n')
    print("Probe file written successfully. The probe is complete.")
    sys.exit(0)

# Load config from ~/.deepcode/settings.json
settings = {}
if os.path.exists(os.path.expanduser("~/.deepcode/settings.json")):
    with open(os.path.expanduser("~/.deepcode/settings.json")) as f:
        settings = json.load(f)
env_s = settings.get("env", {})

API_KEY = os.environ.get("DEEPCODE_API_KEY", env_s.get("API_KEY", ""))
MODEL = os.environ.get("DEEPCODE_MODEL", env_s.get("MODEL", "deepseek-v4-flash"))
BASE_URL = os.environ.get("DEEPCODE_BASE_URL", env_s.get("BASE_URL", "https://api.deepseek.com"))

resp = requests.post(f"{BASE_URL}/chat/completions",
    headers={"Authorization": f"Bearer {API_KEY}"},
    json={"model": MODEL, "messages": [{"role":"user","content":work_request}], "max_tokens":4096},
    timeout=120)
resp.raise_for_status()
reply = resp.json()["choices"][0]["message"]["content"]
print(reply)
if close_path:
    with open(close_path, 'w') as f:
        f.write(reply + '\n')
```

Make it executable: `chmod +x /home/david/bin/deepcode-backend`

### 2. Configure API credentials

Create `~/.deepcode/settings.json` (or set `DEEPCODE_API_KEY` in the environment):

```json
{
  "env": {
    "MODEL": "deepseek-v4-flash",
    "BASE_URL": "https://api.deepseek.com",
    "API_KEY": "sk-your-key-here"
  }
}
```

### 3. Register the backend

Create `~/.dearmachine/config/custom-backends.toml`:

```toml
[deepcode]
name = "DeepCode (Deepseek v4 Flash)"
executable = "/home/david/bin/deepcode-backend"
output_format = "plain"
install_help = "Requires Python3 + requests, and ~/.deepcode/settings.json with API_KEY"
```

### 4. Approve the backend

Edit `~/.dearmachine/config/device-client.toml` and add `"deepcode"` to the `backends` array, or use the environment variable:

```bash
export DEARMACHINE_BACKENDS='["codex", "deepcode"]'
```

### 5. Verify with health check

```bash
cd ~/projects/DearMachine/device-client
DEARMACHINE_BACKENDS='["deepcode"]' ./agent-manager backend health deepcode
```

Expected output:

```
backend=deepcode
probe="Write a file at exactly ..."
reply="Probe file written successfully. The probe is complete."
result=ok
```

### 6. Dispatch a test ticket

```bash
echo "Say hello in French, just one short phrase." > /tmp/test-request.md
DEARMACHINE_BACKENDS='["deepcode"]' ./agent-manager ticket send \
  --backend deepcode --file /tmp/test-request.md --cwd /tmp
# Returns ticket ID, e.g. 20260811T201221-ca06bb82

./agent-manager ticket status 20260811T201221-ca06bb82
# Wait for status=closed

./agent-manager ticket view 20260811T201221-ca06bb82
# Shows the AI reply in ticket-close.md
```

## Common Output-Format Considerations

| Format | Best for | Agent protocol |
|--------|----------|----------------|
| `"plain"` | Simple agents that print the reply directly to stdout. | Agent writes the full reply text; agent-manager captures up to 64 KiB. |
| `"json-stream"` | Agents that emit JSON Lines (like Codex). Each line is a JSON object with `type` and `item.text` fields. | Use this if your agent already produces Codex-compatible output. |

To determine which format to use, run your agent with a simple prompt and inspect stdout:

```bash
echo "test" | /path/to/your-agent
```

- If it prints JSON objects with `"type": "item.completed"`, use `"json-stream"`.
- Otherwise (plain text, Markdown, etc.), use `"plain"`.

## Troubleshooting

### `result=fail reason=unavailable` or `result=fail reason=not-approved`

- **Unavailable**: The executable is not found on `PATH`. Use an absolute path, or ensure the binary directory is in the `PATH` of the process running agent-manager.
- **Not approved**: The backend ID is not in the `backends` array (config file or `DEARMACHINE_BACKENDS` env var).

### `result=fail reason=file-not-written`

Your agent did not write the probe file. The health check sends a message like:

> Write a file at exactly `/tmp/.healthcheck-probe-123.txt` containing exactly this line: DearMachine backend health probe. ...

Your agent must parse this, create the file with the exact content, and print a confirmation. See the example Python backend above for a working pattern.

### Ticket crashes immediately (`status=crashed`)

This almost always means the agent exited without writing `ticket-close.md`. The agent-manager feeds the ticket content (which includes `Close-Path: /path/to/ticket-close.md`) to the agent's stdin. The agent **must** open stdin as a pipe, read the content, extract the `Close-Path`, call the API, and write the reply to that path. The agent does **not** need to run commands or modify files in the working directory — it just needs to produce a reply and save it to the close path.

Check:
- Your agent reads **all of stdin** (not just a line).
- Your agent writes the reply to the path specified in `Close-Path:` in the ticket header.
- Your agent prints the reply to stdout (so the observation is captured).
- The agent exits with code 0 after writing.

### `output_format` mismatch

If the agent prints valid JSON but you chose `"plain"`, the observation will contain the raw JSON text — still functional but noisy. If the agent prints plain text but `output_format` is `"json-stream"`, the observation will be empty because the parser skips non-JSON lines. Match the actual stdout format.

### `$WRITABLE_DIR` substitution

The string `$WRITABLE_DIR` in `arguments` is replaced literally with the ticket sandbox directory at launch time. No shell expansion is performed — it's a simple string replace. If your agent reads this argument, use it as an absolute directory path.

### ID conflicts

Custom backend IDs cannot override built-in backend IDs (`codex`, `forge`). If you define `[codex]` in `custom-backends.toml`, it will be silently ignored — the hardcoded adapter wins.

### Missing `BurntSushi/toml` dependency

If you get a build error about `BurntSushi/toml`, ensure the dependency is in `go.mod`:
```bash
cd ~/projects/DearMachine/device-client
go get github.com/BurntSushi/toml@latest
```

## Reference

- [Custom Backend Configuration Design Document](~/projects/pm/docs/custom-backend-design.md)
- PM Ticket: `7fe89894` in `~/projects/pm/.issues/`
- [DearMachine Architecture](~/projects/DearMachine/device-client/)
