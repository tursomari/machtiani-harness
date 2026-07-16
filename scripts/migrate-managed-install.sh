#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"
MANAGED_ROOT="$HOME/.machtiani/installations/mct-agent"
MANAGED_SOURCE="$MANAGED_ROOT/source"
PREFIX="${PREFIX:-$HOME/.local}"
BINARY="$PREFIX/bin/mct-agent"

usage() {
  cat <<EOF
Usage: $(basename "$0")

Adds the managed mct-agent source installation to an existing Machtiani home.
Run this script from a clean Git clone with the origin that should supply
updates. Clean means no tracked changes or untracked files. Required submodules
are initialized in the managed source automatically. Existing configuration,
project stores, sessions, and artifacts under \$HOME/.machtiani are left in
place; the bootstrap clone may be removed after migration.

Environment:
  PREFIX   Destination prefix for mct-agent (default: \$HOME/.local)
EOF
}

if [[ $# -gt 1 || ( $# -eq 1 && "$1" != "-h" && "$1" != "--help" ) ]]; then
  usage >&2
  exit 2
fi
if [[ $# -eq 1 ]]; then
  usage
  exit 0
fi

echo "[migrate] Preserving existing Machtiani configuration and project data" >&2
HOME="$HOME" PREFIX="$PREFIX" bash "$REPO_ROOT/scripts/install.sh" --managed

if [[ ! -f "$MANAGED_ROOT/receipt.json" || ! -d "$MANAGED_SOURCE/.git" ]]; then
  echo "Error: managed installation migration did not create the expected source and receipt." >&2
  exit 1
fi
if [[ ! -x "$BINARY" ]]; then
  echo "Error: managed installation migration did not install mct-agent at $BINARY." >&2
  exit 1
fi

source_commit="$(git -C "$MANAGED_SOURCE" rev-parse HEAD)"
binary_commit="$(HOME="$HOME" "$BINARY" --version | sed -n 's/^commit: //p')"
if [[ "$source_commit" != "$binary_commit" ]]; then
  echo "Error: installed mct-agent does not match the managed source checkout." >&2
  exit 1
fi

echo "[migrate] Managed updates are enabled from $MANAGED_SOURCE" >&2
