#!/usr/bin/env bash
set -euo pipefail

# Detect repo root as dirname of script and cd there.
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Check if Skyvern is already running.
if curl -s --max-time 2 http://localhost:8000/openapi.json >/dev/null 2>&1; then
  SKYVERN_API_KEY=$(grep SKYVERN_API_KEY third_party/skyvern/.env | cut -d= -f2-)
  SKYVERN_API_KEY="${SKYVERN_API_KEY//\'/}"
  SKYVERN_API_KEY="${SKYVERN_API_KEY//\"/}"
  SKYVERN_API_KEY="${SKYVERN_API_KEY## }"
  SKYVERN_API_KEY="${SKYVERN_API_KEY%% }"
  echo "$SKYVERN_API_KEY"
  exit 0
fi

# Kill any process on port 8000.
fuser -k 8000/tcp 2>/dev/null || {
  PID=$(lsof -ti:8000 2>/dev/null)
  if [ -n "${PID:-}" ]; then
    kill -9 $PID 2>/dev/null || true
  fi
}

# Small delay to let the port be released.
sleep 1

# Optionally reset the database.
if [ "${1:-}" = "--reset-db" ]; then
  rm -f third_party/skyvern/skyvern.db
fi

# Read DeepSeek credentials from .machtiani/config.toml and export them.
eval "$(python3 -c "
import tomllib

with open('.machtiani/config.toml', 'rb') as f:
    cfg = tomllib.load(f)

providers = cfg.get('providers', {})
deepseek = providers.get('deepseek', {})
models = cfg.get('models', {})
ds_model = models.get('deepseek-v4-pro', {})

api_base = deepseek.get('api_base', deepseek.get('base_url', ''))
api_key = deepseek.get('api_key', '')
model_name = ds_model.get('model', '')

print(f'export OPENAI_COMPATIBLE_API_BASE={api_base}')
print(f'export OPENAI_COMPATIBLE_API_KEY={api_key}')
print(f'export OPENAI_COMPATIBLE_MODEL_NAME={model_name}')
")"

export LLM_KEY=DEEPSEEK_V4
export OPENAI_COMPATIBLE_MODEL_KEY=DEEPSEEK_V4

# Launch Skyvern in the background.
cd third_party/skyvern
nohup python3 -m skyvern run server > skyvern.log 2>&1 &
disown
cd "$REPO_ROOT"

# Wait up to 30 seconds for the SQLite bootstrap message.
for i in $(seq 1 30); do
  if tail -100 third_party/skyvern/skyvern.log 2>/dev/null | grep -q "SQLite bootstrap complete"; then
    SKYVERN_API_KEY=$(grep SKYVERN_API_KEY third_party/skyvern/.env | cut -d= -f2-)
    SKYVERN_API_KEY="${SKYVERN_API_KEY//\'/}"
    SKYVERN_API_KEY="${SKYVERN_API_KEY//\"/}"
    SKYVERN_API_KEY="${SKYVERN_API_KEY## }"
    SKYVERN_API_KEY="${SKYVERN_API_KEY%% }"
    echo "$SKYVERN_API_KEY"
    exit 0
  fi
  sleep 1
done

echo "ERROR: Skyvern bootstrap timed out after 30 seconds" >&2
exit 1
