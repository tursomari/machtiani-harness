#!/usr/bin/env bash
# run-live-docker.sh — Docker-based live integration test for mct-code.
#
# Builds a Docker image containing mct-code and runs it against the
# undici test repository with a read-only handoff prompt.
#
# Environment variables (all optional):
#   DEEPSEEK_API_KEY   API key for the DeepSeek provider
#   DEEPSEEK_BASE_URL  Base URL for the DeepSeek provider

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../../" && pwd)"

echo "=== Building Docker image mct-code-test:latest ..."
docker build -t mct-code-test:latest \
	-f "$SCRIPT_DIR/Dockerfile" \
	"$REPO_ROOT"

echo "=== Running mct-code in container ..."
docker run --rm \
	-e DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-}" \
	-e DEEPSEEK_BASE_URL="${DEEPSEEK_BASE_URL:-}" \
	mct-code-test:latest

EXIT_CODE=$?

if [[ $EXIT_CODE -eq 0 ]]; then
	echo "=== PASS (exit code: $EXIT_CODE)"
else
	echo "=== FAIL (exit code: $EXIT_CODE)"
fi

exit $EXIT_CODE
