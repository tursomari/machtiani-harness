#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PREFIX="${PREFIX:-$HOME/.local}"
BIN_DIR="${PREFIX}/bin"

usage() {
  cat <<EOF
Usage: $(basename "$0") [--install-peripherals]

Installs the mct-agent binary by default. Pass --install-peripherals to also
build and install mct, file-discovery, snippet-discovery, and shell-agent
(for development and debugging only; most users should use mct-agent
directly).
Environment:
  PREFIX   Destination prefix for the install (default: \$HOME/.local)
EOF
}

log() {
  echo "[install] $*" >&2
}

git_short_commit() {
  local dir="$1"
  git -C "$dir" rev-parse --short=12 HEAD 2>/dev/null || echo "unknown"
}

git_dirty_flag() {
  local dir="$1"
  if git -C "$dir" status --porcelain --untracked-files=no >/dev/null 2>&1; then
    if [ -n "$(git -C "$dir" status --porcelain --untracked-files=no 2>/dev/null)" ]; then
      echo "dirty"
    else
      echo "clean"
    fi
  else
    echo "unknown"
  fi
}

INSTALL_PERIPHERALS=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-peripherals|--all-binaries)
      INSTALL_PERIPHERALS=true
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
  shift
done

check_prereq() {
  local cmd="$1"
  local name="$2"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "Error: $name is required but not found on PATH." >&2
    exit 1
  fi
}

check_prereq go "Go"
check_prereq git "Git"
check_prereq rg "ripgrep (rg)"

mkdir -p "$BIN_DIR"
log "Installing binaries into $BIN_DIR"

BUILD_AT="$(date -u +%Y%m%dT%H%M%SZ 2>/dev/null || date -u +%Y%m%d%H%M%S)"

if [[ -z "${GOCACHE:-}" ]]; then
  export GOCACHE="$REPO_ROOT/.gocache"
fi
mkdir -p "$GOCACHE"

log "Building mct-agent"
AGENT_COMMIT="$(git_short_commit "$REPO_ROOT/agent")"
AGENT_DIRTY="$(git_dirty_flag "$REPO_ROOT/agent")"
AGENT_VERSION="dev-${AGENT_COMMIT}"
if [ "$AGENT_DIRTY" = "dirty" ]; then
  AGENT_VERSION="${AGENT_VERSION}-dirty"
fi
AGENT_LDFLAGS="-X main.Version=${AGENT_VERSION} -X main.Commit=${AGENT_COMMIT} -X main.BuiltAt=${BUILD_AT} -X main.Dirty=${AGENT_DIRTY}"
(
  cd "$REPO_ROOT/agent"
  go build -buildvcs=false -ldflags "$AGENT_LDFLAGS" -o "$BIN_DIR/mct-agent" ./cmd/mct-agent
)

if $INSTALL_PERIPHERALS; then
  log "Building mct and file-discovery"
  (
    cd "$REPO_ROOT/agent/internal/mct"
    ./build.sh
  )
  if [ ! -f "$REPO_ROOT/agent/internal/mct/bin/mct" ]; then
    echo "mct build did not produce bin/mct" >&2
    exit 1
  fi
  if [ ! -f "$REPO_ROOT/agent/internal/mct/bin/file-discovery" ]; then
    echo "mct build did not produce file-discovery" >&2
    exit 1
  fi
  install -m 0755 "$REPO_ROOT/agent/internal/mct/bin/mct" "$BIN_DIR/mct"
  install -m 0755 "$REPO_ROOT/agent/internal/mct/bin/file-discovery" "$BIN_DIR/file-discovery"

  log "Building snippet-discovery"
  SNIPPET_COMMIT="$(git_short_commit "$REPO_ROOT/agent/internal/snippet-discovery")"
  SNIPPET_DIRTY="$(git_dirty_flag "$REPO_ROOT/agent/internal/snippet-discovery")"
  SNIPPET_VERSION="dev-${SNIPPET_COMMIT}"
  if [ "$SNIPPET_DIRTY" = "dirty" ]; then
    SNIPPET_VERSION="${SNIPPET_VERSION}-dirty"
  fi
  SNIPPET_LDFLAGS="-X main.version=${SNIPPET_VERSION} -X main.commit=${SNIPPET_COMMIT} -X main.builtAt=${BUILD_AT} -X main.dirty=${SNIPPET_DIRTY}"
  (
    cd "$REPO_ROOT/agent/internal/snippet-discovery"
    go build -buildvcs=false -ldflags "$SNIPPET_LDFLAGS" -o "$BIN_DIR/snippet-discovery" ./cmd/snippet-discovery
  )

  log "Building shell-agent"
  (
    cd "$REPO_ROOT/agent/internal/shell-agent"
    go build -buildvcs=false -o "$BIN_DIR/shell-agent" ./cmd/shell-agent
  )
fi

hash -r 2>/dev/null || true

log "Done. Binaries available on PATH if $BIN_DIR is included"
log "mct-agent --version =>"
"$BIN_DIR/mct-agent" --version 2>/dev/null || log "  (mct-agent not executable?)"

if $INSTALL_PERIPHERALS; then
  log "mct --version =>"
  "$BIN_DIR/mct" --version 2>/dev/null || log "  (mct not executable?)"
  log "file-discovery -version =>"
  "$BIN_DIR/file-discovery" -version 2>/dev/null || log "  (file-discovery not executable?)"
  log "snippet-discovery -version =>"
  "$BIN_DIR/snippet-discovery" -version 2>/dev/null || log "  (snippet-discovery not executable?)"
  log "shell-agent --help (usage) =>"
  "$BIN_DIR/shell-agent" --help 2>/dev/null | head -n 1 || log "  (shell-agent not executable?)"
fi
