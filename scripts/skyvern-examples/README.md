# Skyvern Local Server API

This document explains how to use the Skyvern local server API for browser automation tasks.

## Prerequisites

- The Skyvern server must be running locally. Start it with the environment variable `LLM_KEY=DEEPSEEK_V4`.
- DeepSeek credentials are read from `.machtiani/config.toml`.
- An API key is auto-generated on first run and stored in `third_party/skyvern/.env` as `SKYVERN_API_KEY`.

## API Overview

The API accepts JSON payloads with **natural-language prompts** — it does *not* use a YAML script format. All requests require an `x-api-key` header set to the value of `SKYVERN_API_KEY`.

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/tasks` | Create a new browser automation task |
| `GET` | `/api/v1/tasks/{task_id}` | Poll the status and result of a task |

## Request Fields for POST /api/v1/tasks

| Field | Required | Description |
|-------|----------|-------------|
| `url` | Yes | The target URL to navigate to |
| `prompt` | Yes | Natural-language instructions describing what the agent should do |
| `engine` | Yes | Set to `"skyvern-1.0"` |
| `data_extraction_schema` | No | Optional JSON Schema describing the shape of structured output to extract |
| `max_steps` | No | Optional cap on the number of browser steps (defaults to server-side limit) |

### Example: curl

```bash
curl -sS -X POST http://localhost:8000/api/v1/tasks \
  -H "Content-Type: application/json" \
  -H "x-api-key: $SKYVERN_API_KEY" \
  -d '{
    "url": "https://example.com",
    "prompt": "Load the homepage and extract all visible text and links.",
    "engine": "skyvern-1.0"
  }'
```

### Example: Python with urllib

```python
import json
import os
import urllib.request

api_key = os.environ["SKYVERN_API_KEY"]
url = "http://localhost:8000/api/v1/tasks"

payload = json.dumps({
    "url": "https://example.com",
    "prompt": "Load the homepage and extract all visible text and links.",
    "engine": "skyvern-1.0"
}).encode("utf-8")

req = urllib.request.Request(url, data=payload, method="POST")
req.add_header("Content-Type", "application/json")
req.add_header("x-api-key", api_key)

with urllib.request.urlopen(req) as resp:
    task = json.loads(resp.read().decode("utf-8"))
    print("Task ID:", task.get("task_id"))
```

### Polling for results

```bash
curl -sS http://localhost:8000/api/v1/tasks/{task_id} \
  -H "x-api-key: $SKYVERN_API_KEY"
```

A successful task has `"status": "completed"` and populates the response fields based on the prompt and any `data_extraction_schema` provided.

## Obstacle Handling

When the agent encounters obstacles such as authentication dialogs or login walls, the task status becomes `"failed"` and a `failure_reason` is returned. A common category is `NAVIGATION_FAILURE` (e.g., HTTP basic auth prompts producing `net::ERR_INVALID_AUTH_CREDENTIALS`).

For self-hosted deployments, **captcha solving is not automatic** — the agent will pause and wait for manual intervention. See `obstacle-detection.md` in this directory for detailed response shapes and the recommended handling workflow.

## Additional Resources

- Skyvern documentation is located at `third_party/skyvern/docs/`.
- Example payloads for specific sites are in this directory.
