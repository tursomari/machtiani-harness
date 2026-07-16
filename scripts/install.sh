#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PREFIX="${PREFIX:-$HOME/.local}"
BIN_DIR="${PREFIX}/bin"
MANAGED_ROOT="$HOME/.machtiani/installations/mct-agent"
MANAGED_SOURCE="$MANAGED_ROOT/source"
BUILD_SUBMODULE_PATH="agent/internal/shell-agent"

usage() {
  cat <<EOF
Usage: $(basename "$0") [--managed | --install-peripherals]

Installs the mct-agent binary by default. Pass --install-peripherals to also
build and install mct, file-discovery, snippet-discovery, and shell-agent
(for development and debugging only; most users should use mct-agent
directly).
Pass --managed from a clean Git clone to bootstrap an updater-owned clone from
the same origin, then build and register that managed source for updates. Clean
means no tracked changes or untracked files. Required submodules are initialized
in the managed source automatically. Managed mode installs only mct-agent and
the bootstrap clone may be removed after installation.
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

git_full_commit() {
  local dir="$1"
  git -C "$dir" rev-parse HEAD 2>/dev/null || echo "unknown"
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

origin_has_http_userinfo() {
  local remote="$1"
  local authority
  case "$remote" in
    http://*|https://*)
      authority="${remote#*://}"
      authority="${authority%%/*}"
      [[ "$authority" == *"@"* ]]
      ;;
    *)
      return 1
      ;;
  esac
}

prepare_build_submodule() {
  local source="$1"
  local entry
  entry="$(git -C "$source" ls-tree HEAD -- "$BUILD_SUBMODULE_PATH")"
  if [[ "$entry" != 160000\ * ]]; then
    return 0
  fi
  git -C "$source" submodule sync -- "$BUILD_SUBMODULE_PATH"
  git -C "$source" submodule update --init --recursive --depth 1 -- "$BUILD_SUBMODULE_PATH"
}

resolve_remote_head() {
  local remote="$1"
  local output first second
  REMOTE_HEAD_BRANCH=""
  REMOTE_HEAD_COMMIT=""
  output="$(git ls-remote --symref "$remote" HEAD)" || return 1
  while IFS=$'\t' read -r first second; do
    if [[ "$first" == "ref: refs/heads/"* && "$second" == "HEAD" ]]; then
      REMOTE_HEAD_BRANCH="${first#ref: refs/heads/}"
    elif [[ "$second" == "HEAD" && "$first" =~ ^[0-9a-fA-F]{40,64}$ ]]; then
      REMOTE_HEAD_COMMIT="$first"
    fi
  done <<<"$output"
  if [[ -z "$REMOTE_HEAD_BRANCH" || -z "$REMOTE_HEAD_COMMIT" ]]; then
    echo "Error: managed installation origin does not advertise a symbolic default branch." >&2
    return 1
  fi
}

sync_managed_source() {
  local remote="$1"
  local managed_remote fetched refspec
  if ! git -C "$MANAGED_SOURCE" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "Error: managed source exists but is not a Git checkout: $MANAGED_SOURCE" >&2
    return 1
  fi
  managed_remote="$(git -C "$MANAGED_SOURCE" remote get-url origin 2>/dev/null || true)"
  if [[ -z "$managed_remote" ]]; then
    echo "Error: managed source does not have an origin remote." >&2
    return 1
  fi
  if origin_has_http_userinfo "$managed_remote"; then
    echo "Error: managed source origin contains embedded credentials; use SSH or a Git credential helper." >&2
    return 1
  fi
  if [[ "$managed_remote" != "$remote" ]]; then
    echo "Error: managed source origin differs from the invoking checkout; refusing to repoint it." >&2
    return 1
  fi
  if [[ -n "$(git -C "$MANAGED_SOURCE" status --porcelain --untracked-files=all)" ]]; then
    echo "Error: managed source checkout contains local changes; preserve or remove them before installing." >&2
    return 1
  fi
  resolve_remote_head "$remote"
  refspec="+refs/heads/$REMOTE_HEAD_BRANCH:refs/remotes/origin/$REMOTE_HEAD_BRANCH"
  git -C "$MANAGED_SOURCE" fetch --depth 1 origin "$refspec"
  fetched="$(git -C "$MANAGED_SOURCE" rev-parse "refs/remotes/origin/$REMOTE_HEAD_BRANCH")"
  if [[ "$fetched" != "$REMOTE_HEAD_COMMIT" ]]; then
    echo "Error: managed installation origin moved while synchronizing; retry." >&2
    return 1
  fi
  git -C "$MANAGED_SOURCE" checkout -B "$REMOTE_HEAD_BRANCH" "$REMOTE_HEAD_COMMIT"
  prepare_build_submodule "$MANAGED_SOURCE"
}

bootstrap_managed_source() {
  local remote="$1"
  local staging_source=""
  mkdir -p "$MANAGED_ROOT"
  chmod 0700 "$MANAGED_ROOT"
  if [[ -e "$MANAGED_SOURCE" ]]; then
    sync_managed_source "$remote"
    return
  fi
  staging_source="$(mktemp -d "$MANAGED_ROOT/.source.bootstrap.XXXXXX")"
  cleanup_bootstrap_source() {
    rm -rf "${staging_source:-}"
  }
  trap cleanup_bootstrap_source EXIT
  git clone --depth 1 --single-branch "$remote" "$staging_source"
  prepare_build_submodule "$staging_source"
  mv "$staging_source" "$MANAGED_SOURCE"
  staging_source=""
  trap - EXIT
}

INSTALL_PERIPHERALS=false
MANAGED_INSTALL=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-peripherals|--all-binaries)
      INSTALL_PERIPHERALS=true
      ;;
    --managed)
      MANAGED_INSTALL=true
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

if $MANAGED_INSTALL && $INSTALL_PERIPHERALS; then
  echo "Error: --managed and --install-peripherals cannot be combined." >&2
  exit 2
fi

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

if $MANAGED_INSTALL; then
  MANAGED_ORIGIN="$(git -C "$REPO_ROOT" remote get-url origin 2>/dev/null || true)"
  if [[ -z "$MANAGED_ORIGIN" ]]; then
    echo "Error: managed installation requires an origin remote." >&2
    exit 1
  fi
  if origin_has_http_userinfo "$MANAGED_ORIGIN"; then
    echo "Error: managed installation origin contains embedded credentials; use SSH or a Git credential helper." >&2
    exit 1
  fi
  if [[ -n "$(git -C "$REPO_ROOT" status --porcelain --untracked-files=all 2>/dev/null)" ]]; then
    echo "Error: managed installation requires a clean source checkout." >&2
    exit 1
  fi
  REPO_PHYSICAL="$(cd "$REPO_ROOT" && pwd -P)"
  MANAGED_PHYSICAL="$MANAGED_SOURCE"
  if [[ -d "$MANAGED_SOURCE" ]]; then
    MANAGED_PHYSICAL="$(cd "$MANAGED_SOURCE" && pwd -P)"
  fi
  if [[ "$REPO_PHYSICAL" != "$MANAGED_PHYSICAL" ]]; then
    log "Bootstrapping managed source from the invoking checkout's origin"
    bootstrap_managed_source "$MANAGED_ORIGIN"
    exec bash "$MANAGED_SOURCE/scripts/install.sh" --managed
  fi
fi

mkdir -p "$BIN_DIR"
log "Installing binaries into $BIN_DIR"

BUILD_AT="$(date -u +%Y%m%dT%H%M%SZ 2>/dev/null || date -u +%Y%m%d%H%M%S)"

if [[ -z "${GOCACHE:-}" ]]; then
  export GOCACHE="$REPO_ROOT/.gocache"
fi
mkdir -p "$GOCACHE"

log "Building mct-agent"
AGENT_COMMIT="$(git_full_commit "$REPO_ROOT")"
AGENT_SHORT_COMMIT="$(git_short_commit "$REPO_ROOT")"
AGENT_DIRTY="$(git_dirty_flag "$REPO_ROOT/agent")"
AGENT_VERSION="dev-${AGENT_SHORT_COMMIT}"
if [ "$AGENT_DIRTY" = "dirty" ]; then
  AGENT_VERSION="${AGENT_VERSION}-dirty"
fi
AGENT_LDFLAGS="-X main.Version=${AGENT_VERSION} -X main.Commit=${AGENT_COMMIT} -X main.BuiltAt=${BUILD_AT} -X main.Dirty=${AGENT_DIRTY}"
AGENT_TMP="$(mktemp "$BIN_DIR/.mct-agent.XXXXXX")"
AGENT_BACKUP=""
cleanup_agent_install() {
  rm -f "${AGENT_TMP:-}" "${AGENT_BACKUP:-}"
}
trap cleanup_agent_install EXIT
(
  cd "$REPO_ROOT/agent"
  go build -buildvcs=false -ldflags "$AGENT_LDFLAGS" -o "$AGENT_TMP" ./cmd/mct-agent
)
chmod 0755 "$AGENT_TMP"
"$AGENT_TMP" --version >/dev/null
if [[ -f "$BIN_DIR/mct-agent" ]]; then
  AGENT_BACKUP="$(mktemp "$BIN_DIR/.mct-agent.previous.XXXXXX")"
  cp -p "$BIN_DIR/mct-agent" "$AGENT_BACKUP"
fi
mv -f "$AGENT_TMP" "$BIN_DIR/mct-agent"
AGENT_TMP=""

if $MANAGED_INSTALL; then
  if ! "$BIN_DIR/mct-agent" update register --source "$REPO_ROOT" --prefix "$PREFIX"; then
    if [[ -n "$AGENT_BACKUP" && -f "$AGENT_BACKUP" ]]; then
      mv -f "$AGENT_BACKUP" "$BIN_DIR/mct-agent"
      AGENT_BACKUP=""
    else
      rm -f "$BIN_DIR/mct-agent"
    fi
    echo "Error: managed installation registration failed; previous binary restored." >&2
    exit 1
  fi
fi
rm -f "${AGENT_BACKUP:-}"
AGENT_BACKUP=""

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
