#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 <source-root> <output-directory>" >&2
}

if [[ $# -ne 2 ]]; then
  usage
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SOURCE_ROOT="$(cd "$1" && pwd)"
mkdir -p "$2"
OUTPUT_DIR="$(cd "$2" && pwd)"

if [[ ! -d "$SOURCE_ROOT/agent/cmd/machtiani" ]]; then
  echo "Error: machtiani source not found under $SOURCE_ROOT" >&2
  exit 1
fi

export BENCH_SOURCE_ROOT="$SOURCE_ROOT"
export BENCH_OUTPUT_DIR="$OUTPUT_DIR"
nix develop "path:${REPO_ROOT}#bench" -c bash -c '
  set -euo pipefail
  export GOTOOLCHAIN=local CGO_ENABLED=0
  cd "$BENCH_SOURCE_ROOT/agent"
  go build -trimpath -o "$BENCH_OUTPUT_DIR/machtiani" ./cmd/machtiani
  if [[ -d cmd/meta-orchestrator ]]; then
    go build -trimpath -o "$BENCH_OUTPUT_DIR/meta-orchestrator" ./cmd/meta-orchestrator
  else
    rm -f "$BENCH_OUTPUT_DIR/meta-orchestrator"
  fi
'

test -x "$OUTPUT_DIR/machtiani"
echo "Built benchmark binaries in $OUTPUT_DIR"
