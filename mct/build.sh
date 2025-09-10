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
  GOOS=darwin GOARCH=amd64 go build -ldflags "$LD_FLAGS" -o machtiani-darwin-amd64 ./cmd/mct

  # Build for macOS (Apple Silicon)
  GOOS=darwin GOARCH=arm64 go build -ldflags "$LD_FLAGS" -o machtiani-darwin-arm64 ./cmd/mct

  # Build for Linux (x86_64)
  GOOS=linux GOARCH=amd64 go build -ldflags "$LD_FLAGS" -o machtiani-linux-amd64 ./cmd/mct

else
  # Generate ldflags (dev mode)
  LD_FLAGS=$(run_generate_ldflags)

  # Build the main application with ldflags
  go build -ldflags "$LD_FLAGS" -o machtiani-cli ./cmd/mct

  # Build local file-discovery helper (from submodule)
  mkdir -p bin
  # Try to respect a local GOCACHE if present
  if [ -d .gocache ]; then
    (cd submodules/file-discovery && GOCACHE=$(pwd)/../../.gocache go build -o ../../bin/file-discovery ./cmd/file-discovery)
  else
    (cd submodules/file-discovery && go build -o ../../bin/file-discovery ./cmd/file-discovery)
  fi
fi
