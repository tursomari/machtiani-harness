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
build and install mct, file-discovery, and patcher.
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
  go build -buildvcs=true -ldflags "$AGENT_LDFLAGS" -o "$BIN_DIR/mct-agent" ./cmd/mct-agent
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

  log "Building patcher"
  PATCHER_COMMIT="$(git_short_commit "$REPO_ROOT/agent/internal/patcher")"
  PATCHER_DIRTY="$(git_dirty_flag "$REPO_ROOT/agent/internal/patcher")"
  PATCHER_VERSION="dev-${PATCHER_COMMIT}"
  if [ "$PATCHER_DIRTY" = "dirty" ]; then
    PATCHER_VERSION="${PATCHER_VERSION}-dirty"
  fi
  PATCHER_LDFLAGS="-X main.Version=${PATCHER_VERSION} -X main.Commit=${PATCHER_COMMIT} -X main.BuiltAt=${BUILD_AT} -X main.Dirty=${PATCHER_DIRTY}"
  (
    cd "$REPO_ROOT/agent/internal/patcher"
    go build -buildvcs=true -ldflags "$PATCHER_LDFLAGS" -o "$BIN_DIR/patcher" ./cmd/patcher
  )
fi

hash -r 2>/dev/null || true

log "Done. Binaries available on PATH if $BIN_DIR is included"
log "mct-agent --version =>"
"$BIN_DIR/mct-agent" --version 2>/dev/null || log "  (mct-agent not executable?)"

if $INSTALL_PERIPHERALS; then
  log "mct --version =>"
  "$BIN_DIR/mct" --version 2>/dev/null || log "  (mct not executable?)"
  log "patcher --version =>"
  "$BIN_DIR/patcher" --version 2>/dev/null || log "  (patcher not executable?)"
  log "file-discovery -version =>"
  "$BIN_DIR/file-discovery" -version 2>/dev/null || log "  (file-discovery not executable?)"
fi
