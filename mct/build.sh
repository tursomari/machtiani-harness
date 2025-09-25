#!/bin/bash

# Function to display usage information
usage() {
  echo "Usage: $0 [--release]"
  exit 1
}

# Check for the --release flag
RELEASE=false

for arg in "$@"; do
  case $arg in
    --release)
      RELEASE=true
      shift # Remove --release from the arguments
      ;;
    *)
      usage
      ;;
  esac
done

run_generate_ldflags() {
  # Prefer prebuilt helper inside the folder
  if [ -x ./generate_ldflags/generate_ldflags ]; then
    ./generate_ldflags/generate_ldflags "$@"
    return $?
  fi
  # If a top-level file exists and is executable (not a dir), use it
  if [ -f ./generate_ldflags ] && [ -x ./generate_ldflags ]; then
    ./generate_ldflags "$@"
    return $?
  fi
  # Otherwise try to build it into ./generate_ldflags/generate_ldflags
  mkdir -p generate_ldflags
  if [ -d .gocache ]; then
    GOCACHE=$(pwd)/.gocache go build -o generate_ldflags/generate_ldflags ./generate_ldflags
  else
    go build -o generate_ldflags/generate_ldflags ./generate_ldflags
  fi
  ./generate_ldflags/generate_ldflags "$@"
}

if [ "$RELEASE" = true ]; then
  # Generate ldflags (release mode)
  LD_FLAGS=$(run_generate_ldflags --release "https://machtiani2.p.rapidapi.com https://machtiani2.p.rapidapi.com")

  # Build for macOS (Intel)
  GOOS=darwin GOARCH=amd64 go build -buildvcs=true -ldflags "$LD_FLAGS" -o machtiani-darwin-amd64 ./cmd/mct

  # Build for macOS (Apple Silicon)
  GOOS=darwin GOARCH=arm64 go build -buildvcs=true -ldflags "$LD_FLAGS" -o machtiani-darwin-arm64 ./cmd/mct

  # Build for Linux (x86_64)
  GOOS=linux GOARCH=amd64 go build -buildvcs=true -ldflags "$LD_FLAGS" -o machtiani-linux-amd64 ./cmd/mct

else
  # Generate ldflags (dev mode)
  LD_FLAGS=$(run_generate_ldflags)

  # Build the main application with ldflags
  go build -buildvcs=true -ldflags "$LD_FLAGS" -o machtiani-cli ./cmd/mct

  # Build local file-discovery helper (from submodule)
  mkdir -p bin
  FD_COMMIT=$(git -C submodules/file-discovery rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
  [ -z "$FD_COMMIT" ] && FD_COMMIT=unknown
  FD_TIME=$(git -C submodules/file-discovery log -1 --format=%cd --date=format:%Y%m%dT%H%M%SZ -- . 2>/dev/null)
  if [ -z "$FD_TIME" ]; then
    FD_TIME=$(date -u +%Y%m%dT%H%M%SZ 2>/dev/null || date -u +%Y%m%d%H%M%S)
  fi
  FD_BUILD_AT=$(git -C submodules/file-discovery log -1 --format=%cd --date=format:%Y-%m-%dT%H:%M:%SZ -- . 2>/dev/null)
  if [ -z "$FD_BUILD_AT" ]; then
    FD_BUILD_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u +%Y%m%d%H%M%S)
  fi
  FD_DIRTY_STATE="unknown"
  if git -C submodules/file-discovery status --porcelain --untracked-files=no >/dev/null 2>&1; then
    if [ -n "$(git -C submodules/file-discovery status --porcelain --untracked-files=no 2>/dev/null)" ]; then
      FD_DIRTY_STATE="dirty"
    else
      FD_DIRTY_STATE="clean"
    fi
  fi
  FD_SUFFIX=""
  if [ "$FD_DIRTY_STATE" = "dirty" ]; then
    FD_SUFFIX="-dirty"
  fi
  FD_VERSION="dev-${FD_COMMIT}-${FD_TIME}${FD_SUFFIX}"
  FD_LDFLAGS="-X main.version=${FD_VERSION} -X main.commit=${FD_COMMIT} -X main.builtAt=${FD_BUILD_AT} -X main.dirty=${FD_DIRTY_STATE}"
  # Try to respect a local GOCACHE if present
  if [ -d .gocache ]; then
    (cd submodules/file-discovery && GOCACHE=$(pwd)/../../.gocache go build -buildvcs=true -ldflags "$FD_LDFLAGS" -o ../../bin/file-discovery ./cmd/file-discovery)
  else
    (cd submodules/file-discovery && go build -buildvcs=true -ldflags "$FD_LDFLAGS" -o ../../bin/file-discovery ./cmd/file-discovery)
  fi
fi
