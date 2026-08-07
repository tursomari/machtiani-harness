#!/usr/bin/env bash
set -euo pipefail

export HOME=/tmp/mct-command-supervisor-home
export PATH="$HOME/.local/bin:$PATH"
mkdir -p "$HOME" /workspace

echo "==> Installing committed mct-agent for command-supervisor smoke..."
(cd /fixtures/mct-source && nix run path:.#install -- --prefix "$HOME/.local")

cd /workspace
git init --quiet --initial-branch=main
git config user.email "command-supervisor-smoke@example.invalid"
git config user.name "command supervisor smoke"
git commit --quiet --allow-empty -m "initial smoke commit"

wrapper_agent=$(readlink -f "$(command -v mct-agent)")
native_agent="$(dirname "$wrapper_agent")/.mct-agent-wrapped"
test -x "$native_agent"

MCT_AGENT_BIN="$native_agent" \
  MCT_SUPERVISOR_SMOKE_REPO=/workspace \
  bash /fixtures/mct-source/agent/tests/command-supervisor-smoke.sh
