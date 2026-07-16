# Testing Guide

This repository groups its tests into three layers: package-level unit tests, integration tests under `tests/integration`, and end-to-end (E2E) scenarios under `tests/e2e`. The suites focus on shell command execution; they never call out to external model providers or require API keys.

## Quick Start

```bash
# From shell-agent/ with a writable cache
GOCACHE=$(pwd)/.gocache go test ./...
```

That command runs every unit, integration, and E2E test in one shot. Drop the `GOCACHE` prefix if your global Go cache is writable.

## Test Layers Overview

- **Unit tests** live alongside their packages (e.g. `internal/*`, `pkg/minisweagent`). These exercise helpers, models, and environments in isolation.
- **Integration tests** (`tests/integration`) mix the shared shell helpers with deterministic mock scripts to validate behaviour without relying on ambient system state.
- **E2E tests** (`tests/e2e`) execute real shell commands via `internal/environments.LocalEnvironment`, covering stdout/stderr handling, retries, metadata, and environment propagation.

## Running Individual Suites

Each suite is a Go package, so you can target it directly:

```bash
# Integration tests
GOCACHE=$(pwd)/.gocache go test ./tests/integration/shell-integration

# Run-live E2E scenarios
GOCACHE=$(pwd)/.gocache go test ./tests/e2e/run-live

# Shell subprocess E2E scenarios
GOCACHE=$(pwd)/.gocache go test ./tests/e2e/shell-subprocess
```

### Integration Suites

- `shell-integration-commands_test.go`: exercises mock script directories and executes the generated commands, asserting stdout and exit codes.
- `shell-integration-error-scenarios_test.go`: drives timeout and non-zero exit conditions through the retry helper and mock scripts.

### E2E Suites

- `run-live/async-run-live-execution_test.go`: ensures asynchronous shell work finishes and metadata captures script details.
- `run-live/sync-run-live-error-handling_test.go`: validates non-zero exit propagation and retry recovery from transient failures.
- `run-live/run-lite-kiosk_test.go`: checks lightweight commands return expected output and environment metadata.
- `shell-subprocess/shell-subprocess-output-validation_test.go`: verifies stdout/stderr separation and fixture-based JSON validation.
- `shell-subprocess/shell-subprocess-exit-codes_test.go`: covers explicit exit codes and timeout sentinel behaviour.
- `shell-subprocess/shell-subprocess-env-integration_test.go`: confirms environment variables from fixtures reach subprocesses.

## Running a Single Test

Use Go's `-run` filter with any package to focus on one test:

```bash
GOCACHE=$(pwd)/.gocache go test ./tests/e2e/run-live -run TestRetryShellCallRecoversAfterTransientFailure
```

## Tips

- The shared helpers auto-detect your shell (`$SHELL`, `bash`, then `sh`). No additional setup is required beyond having a POSIX shell available.

