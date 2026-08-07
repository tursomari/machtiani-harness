#!/usr/bin/env bash
set -euo pipefail

mkdir -p /workspace

echo "==> Building committed mct-agent for command-supervisor smoke..."
(
  cd /fixtures/mct-source/agent
  CGO_ENABLED=0 go build -trimpath -o /tmp/mct-command-supervisor-agent ./cmd/mct-agent
)

cd /workspace
git init --quiet --initial-branch=main
git config user.email "command-supervisor-smoke@example.invalid"
git config user.name "command supervisor smoke"
git commit --quiet --allow-empty -m "initial smoke commit"

MCT_AGENT_BIN=/tmp/mct-command-supervisor-agent \
  MCT_SUPERVISOR_SMOKE_REPO=/workspace \
  bash /fixtures/mct-source/agent/tests/command-supervisor-smoke.sh
