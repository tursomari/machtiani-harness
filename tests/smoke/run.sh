#!/usr/bin/env bash
set -euo pipefail

# ---------------------------------------------------------------------------
# Host-side runner for the mct-agent smoke test.
#
# Prerequisites
#   - Docker
#   - Git
#   - A populated .machtiani/config.toml, or TEST_API_KEY, TEST_BASE_URL,
#     and TEST_MODEL. Provider credentials are passed only through the
#     container environment.
#
# Usage:
#   ./tests/smoke/run.sh
#
# This script builds a clean Docker image from a git worktree at HEAD so
# that only committed sources are tested.  Submodules are populated from
# the host's shared .git/modules/ directory without any network access.
# ---------------------------------------------------------------------------

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKTREE="/tmp/mct-agent-smoke-context"
UPDATE_ONLY=false

if [[ "${1:-}" == "--update-only" ]]; then
  UPDATE_ONLY=true
  shift
fi
if [[ $# -ne 0 ]]; then
  echo "Usage: $0 [--update-only]" >&2
  exit 2
fi

cd "$ROOT"

# Discover the requested live-provider matrix without printing credentials.
# Explicit TEST_* values remain the primary smoke target. Otherwise the local
# repository config supplies the primary target and any additional OpenAI,
# OpenRouter, DeepInfra, and DeepSeek cases that can be configured completely.
mapfile -t discovered_env < <(python3 - "$ROOT/.machtiani/config.toml" <<'PY'
import base64
import os
import re
import sys
import tomllib
from pathlib import Path

def emit(name, value):
    encoded = base64.b64encode(str(value).encode()).decode()
    print(f"{name}\t{encoded}")

path = Path(sys.argv[1])
data = tomllib.loads(path.read_text()) if path.is_file() else {}
providers = data.get("providers", {})
models = data.get("models", {})

def credential(value):
    value = str(value or "").strip()
    match = re.fullmatch(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}", value)
    return os.environ.get(match.group(1), "") if match else value

targets = {}
specs = {
    "openai": "gpt-5.6-luna",
    "openrouter": "deepseek/deepseek-v4-flash",
    "deepseek": "deepseek-v4-flash",
}
for name, model_id in specs.items():
    provider = providers.get(name, {})
    key = credential(provider.get("api_key"))
    url = str(provider.get("base_url", "")).strip()
    if key and url:
        targets[name] = (url, key, model_id)

provider = providers.get("deepinfra", {})
key = credential(provider.get("api_key"))
url = str(provider.get("base_url", "")).strip()
deepinfra_models = [str(v.get("model", "")).strip() for v in models.values() if v.get("provider") == "deepinfra"]
preferred = next((m for m in deepinfra_models if "glm" in m.lower()), deepinfra_models[0] if deepinfra_models else "")
if key and url and preferred:
    targets["deepinfra"] = (url, key, preferred)

explicit = tuple(os.environ.get(name, "").strip() for name in ("TEST_BASE_URL", "TEST_API_KEY", "TEST_MODEL"))
if all(explicit):
    primary = explicit
else:
    primary = targets.get("deepseek") or next(iter(targets.values()), None)
if primary:
    emit("TEST_BASE_URL", primary[0])
    emit("TEST_API_KEY", primary[1])
    emit("TEST_MODEL", primary[2])

matrix = []
for name, values in targets.items():
    if primary and values[0].rstrip("/") == primary[0].rstrip("/") and values[2] == primary[2]:
        continue
    upper = name.upper()
    emit(f"SMOKE_{upper}_BASE_URL", values[0])
    emit(f"SMOKE_{upper}_API_KEY", values[1])
    emit(f"SMOKE_{upper}_MODEL", values[2])
    matrix.append(name)
emit("SMOKE_MATRIX", ",".join(matrix))
PY
)
for assignment in "${discovered_env[@]}"; do
  name=${assignment%%$'\t'*}
  encoded=${assignment#*$'\t'}
  printf -v "$name" '%s' "$(printf '%s' "$encoded" | base64 --decode)"
  export "$name"
done

# --- Ensure the worktree is always cleaned up on exit ----------------------
cleanup() {
  git worktree remove --force "$WORKTREE" 2>/dev/null || true
}
trap cleanup EXIT

# --- Validate required environment variables -------------------------------
missing=false

for var in TEST_API_KEY TEST_BASE_URL TEST_MODEL; do
  if $UPDATE_ONLY; then
    break
  fi
  if [[ -z "${!var:-}" ]]; then
    echo "ERROR: Required environment variable '${var}' is not set or empty." >&2
    missing=true
  fi
done

if [[ "${missing}" == "true" ]]; then
  echo >&2
  echo "Please set the missing variable(s) and try again. Example:" >&2
  echo "  export TEST_API_KEY=sk-..." >&2
  echo "  export TEST_BASE_URL=https://api.openai.com/v1" >&2
  echo "  export TEST_MODEL=gpt-4" >&2
  exit 1
fi

# --- Clean up any stale worktree from a previous run -----------------------
echo "==> Cleaning up stale worktree (if any)..."
git worktree remove --force "$WORKTREE" 2>/dev/null || true

# --- Create a fresh worktree from HEAD -------------------------------------
echo "==> Creating git worktree from HEAD..."
git worktree add --detach "$WORKTREE" HEAD

# --- Copy submodules from host working directory (no network) --------------
echo "==> Copying submodules from host working directory..."
HOST_REPO_ROOT=$(git rev-parse --show-toplevel)
if [[ -z "${HOST_REPO_ROOT:-}" ]]; then
  echo "ERROR: HOST_REPO_ROOT is not set or empty." >&2
  exit 1
fi
grep -E '^\s*path\s*=' "${WORKTREE}/.gitmodules" 2>/dev/null | while IFS= read -r line; do
  submodule_path=$(echo "$line" | sed 's/.*path\s*=\s*//' | xargs)
  if [[ -z "${submodule_path:-}" ]]; then
    continue
  fi
  host_dir="${HOST_REPO_ROOT}/${submodule_path}"
  worktree_dir="${WORKTREE}/${submodule_path}"
  if [[ -d "${host_dir}" ]] && [[ -n "$(ls -A "${host_dir}" 2>/dev/null)" ]]; then
    rm -rf "${worktree_dir}"
    mkdir -p "$(dirname "${worktree_dir}")"
    cp -a "${host_dir}" "${worktree_dir}"
    rm -rf "${worktree_dir}/.git"
  fi
done

# --- Build the Docker image from the worktree ------------------------------
echo "==> Building Docker image 'mct-agent-smoke'..."
docker build -f "$WORKTREE/tests/smoke/Dockerfile" -t mct-agent-smoke "$WORKTREE"

# --- Run the smoke test ----------------------------------------------------
echo "==> Running smoke-test container..."
set +e
container_script=/tests/smoke/container.sh
if $UPDATE_ONLY; then
  container_script=/tests/smoke/update-container.sh
fi
docker run --rm \
  -e TEST_API_KEY \
  -e TEST_BASE_URL \
  -e TEST_MODEL \
  -e SMOKE_MATRIX \
  -e SMOKE_OPENAI_API_KEY -e SMOKE_OPENAI_BASE_URL -e SMOKE_OPENAI_MODEL \
  -e SMOKE_OPENROUTER_API_KEY -e SMOKE_OPENROUTER_BASE_URL -e SMOKE_OPENROUTER_MODEL \
  -e SMOKE_DEEPINFRA_API_KEY -e SMOKE_DEEPINFRA_BASE_URL -e SMOKE_DEEPINFRA_MODEL \
  -e SMOKE_DEEPSEEK_API_KEY -e SMOKE_DEEPSEEK_BASE_URL -e SMOKE_DEEPSEEK_MODEL \
  mct-agent-smoke \
  bash "$container_script"
exit_code=$?
set -e

# --- Summary ----------------------------------------------------------------
echo
if [[ "${exit_code}" -eq 0 ]]; then
  echo "==> SUCCESS: Smoke test passed."
else
  echo "==> FAILURE: Smoke test exited with code ${exit_code}." >&2
fi
exit "${exit_code}"
