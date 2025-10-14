#!/usr/bin/env bash
set -euo pipefail

# Accept optional session id flag (ignored for flag-focused tests)
SESSION_ID=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -s|--session-id)
      if [[ $# -lt 2 ]]; then
        echo "Missing value for $1" >&2
        exit 2
      fi
      SESSION_ID="$2"; shift 2 ;;
    --)
      shift; break ;;
    *)
      echo "Unknown option: $1" >&2
      exit 2 ;;
  esac
done

# Run flag-focused tests inside Docker image
cd /workspace
export GOCACHE=${GOCACHE:-/workspace/.gocache}
if [[ -n "$SESSION_ID" ]]; then
  echo "Note: -s/--session-id ($SESSION_ID) is ignored in run-flags.sh" >&2
fi
echo "Running e2e and unit tests for flags..."
go test ./e2e -v
go test ./internal/... -v

if [ "${RUN_SLOW:-}" = "1" ] || [ "${RUN_SLOW:-}" = "true" ]; then
  echo "Running slow-command timeout test (e2e_slow_rg)..."
  go test -tags e2e_slow_rg ./e2e -run CmdTimeout -v
fi

echo "OK"
