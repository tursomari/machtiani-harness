# Development Workflow

Centralised development workflow documentation for the Machtiani toolchain.

## Quick Start

Build and compare a patch against the current state:

```bash
# Test a patch file
./scripts/ab-dev.sh my-change.patch

# Test changes in the latest commit
./scripts/ab-dev.sh HEAD~1

# Test with a custom command and skip container execution
./scripts/ab-dev.sh --no-run my-change.patch

# Run a custom command inside both images
./scripts/ab-dev.sh --cmd 'mct-agent --version && go test ./...' my-change.patch
```

After the run, inspect the outputs and diff:

```bash
# See the diff summary printed by the script, or examine files directly:
cat /tmp/mct-ab-output/control/stdout.log
cat /tmp/mct-ab-output/treatment/stdout.log
```

## Using code-forge Mode

The Docker-based development workflow supports running mct-agent in code-forge mode. The container includes the mct-forge wrapper script (copied from peripherals/), and the ab-dev.sh script automatically mounts the external forge binary from the host if it is found at ~/.local/bin/forge. If the binary is not found, the script prints a warning and skips the mount; in that case mct-forge will not work inside the container but other commands still execute.

To run a comparison with code-forge mode, use a command like:

./scripts/ab-dev.sh --cmd "mct-agent run --mode code-forge --verbose --api-key deepseek:sk-YOURKEY --max-steps 1000 --timeout-per-turn 0 -t \"Your prompt\"" my-change.patch

Export MACHTIANI_WORKSPACE_DEBUG=1 before running if debug output is desired.

The control and treatment outputs are compared as usual, allowing you to detect regressions from the change when the agent is editing files via forge.

## Prerequisites

| Tool | Min Version | Purpose |
|------|-------------|---------|
| Docker | 23.0+ (with BuildKit) | Containerised build and A/B comparison |
| Git | 2.x | Patch generation from refs and version stamping |

BuildKit is enabled by default in Docker Engine 23.0+. The script explicitly sets `DOCKER_BUILDKIT=1` for backwards compatibility with older installations where the feature was opt-in.

## Project Structure Overview

```
mct/
├── agent/                         # Go module (github.com/tursomari/machtiani/agent)
│   ├── cmd/
│   │   ├── mct-agent/             # Main orchestrator binary
│   │   └── print-config/          # Config debug helper
│   ├── internal/
│   │   ├── file-discovery/        # LLM-guided file discovery (rg protocol)
│   │   ├── mct/                   # Core MCT CLI and README manager
│   │   ├── mct-code/              # Native Go code-editing agent (FSRead/Write/Patch)
│   │   ├── shell-agent/           # Shell command execution agent
│   │   ├── snippet-discovery/     # Snippet-level discovery
│   │   ├── planner/               # Planning layer
│   │   ├── session/               # Session state management
│   │   ├── trajectory/            # Structured telemetry (JSONL)
│   │   ├── llm/                   # LLM client abstraction
│   │   ├── conversation/          # Turn management
│   │   ├── templates/             # Prompt templates
│   │   └── ...
│   ├── go.mod / go.sum
│   └── tests/                     # Agent integration tests
├── scripts/
│   ├── Dockerfile.build           # Multi-stage Docker build (A/B images)
│   ├── Dockerfile.base            # Single-stage base builder (mod download cache)
│   ├── ab-dev.sh                  # A/B build-and-compare entrypoint
│   ├── install.sh                 # Local binary install
│   └── run_eval_head.sh           # HEAD-based evaluation pipeline
├── docs/
│   ├── development-workflow.md    # This file
│   ├── examples/                  # Minimal and comprehensive config references
│   ├── mct-agent-runbook.md       # Repo-local agent operation guide
│   ├── runtime-prerequisites.md   # Dependency and platform notes
│   └── adr/                       # Architecture Decision Records
├── tests/                         # Integration and smoke harnesses
├── .machtiani/
│   └── project.uuid               # Project identity; runtime state lives under ~/.machtiani/<uuid>/
└── README.md
```

All binaries share a single Go module at `agent/`. The `scripts/install.sh` script builds `mct-agent` by default; pass `--install-peripherals` to also build `mct`, `file-discovery`, `snippet-discovery`, and `shell-agent`.

## Docker-Based Development Loop

The `scripts/ab-dev.sh` script provides a containerised A/B comparison workflow. It builds two Docker images from the same `Dockerfile.build`:

- **Control image** (`mct-control`): built from the current working tree without modifications.
- **Treatment image** (`mct-treatment`): built with a patch applied before compilation.

The script then optionally runs a command inside each container and diffs the outputs. This is the primary mechanism for validating that a change does not break the toolchain before committing.

### How It Works

```
                    ┌──────────────────┐
                    │  scripts/ab-dev.sh│
                    └────────┬─────────┘
                             │
              ┌──────────────┴──────────────┐
              │                             │
     ┌────────▼────────┐          ┌─────────▼────────┐
     │ Docker Build #1 │          │ Docker Build #2  │
     │ (no patch)      │          │ (CHANGE_PATCH=X) │
     │ → mct-control   │          │ → mct-treatment  │
     └────────┬────────┘          └─────────┬────────┘
              │                             │
     ┌────────▼────────┐          ┌─────────▼────────┐
     │ Run container   │          │ Run container    │
     │ → /tmp/mct-ab-  │          │ → /tmp/mct-ab-   │
     │   output/control│          │   output/treatment│
     └────────┬────────┘          └─────────┬────────┘
              │                             │
              └──────────────┬──────────────┘
                             │
                    ┌────────▼────────┐
                    │ diff stdout     │
                    │ diff stderr     │
                    │ compare exit    │
                    │ codes           │
                    └────────┬────────┘
                             │
                    ┌────────▼────────┐
                    │ Summary verdict │
                    └─────────────────┘
```

## Multi-Stage Dockerfile.build

`scripts/Dockerfile.build` uses a two-stage pattern:

**Stage 1 — `builder`** (based on `golang:1.23-bookworm`):

```dockerfile
FROM golang:1.23-bookworm AS builder

ARG CHANGE_PATCH                          # Optional patch for treatment builds

# System dependencies
RUN apt-get update && apt-get install -y ripgrep rsync git gcc && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# Layer 1: Go module cache (rarely changes)
COPY --link agent/go.mod agent/go.sum ./
RUN go mod download

# Layer 2: Source tree (changes frequently)
COPY --link agent/ ./agent/
COPY --link scripts/ ./scripts/
COPY --link .machtiani/ ./.machtiani/

# Apply patch for treatment builds
RUN if [ -n "${CHANGE_PATCH}" ]; then \
      cd agent && git apply "${CHANGE_PATCH}"; \
    fi

# Build all binaries
RUN PREFIX=/build ./scripts/install.sh --install-peripherals
```

**Stage 2 — `runtime`** (based on `debian:bookworm-slim`):

```dockerfile
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y ripgrep rsync && rm -rf /var/lib/apt/lists/*

COPY --from=builder /build/bin/ /usr/local/bin/
COPY --from=builder /build/.machtiani/ /.machtiani/

ENV MACHTIANI_CONFIG=/.machtiani/config.toml
ENTRYPOINT ["bash"]
```

### `COPY --link` Caching

Every `COPY` instruction in the builder stage uses the `--link` flag. This is a BuildKit feature that:

- Copies files without creating a new layer dependency on the previous layer.
- Allows the copied content to be fetched directly from the build context rather than chaining through prior layers.
- Means that when `agent/` source changes, the `go mod download` layer is **not invalidated** (it was already copied and cached via `--link` independently).
- Enables parallel copy operations during the build.

Without `--link`, a change to any file in `agent/` would invalidate the `COPY agent/` layer and everything after it, forcing `go mod download` to re-run on every source change.

## Base Builder: Dockerfile.base

`scripts/Dockerfile.base` is a minimal single-stage image used internally for pre-warming the Go module cache:

```dockerfile
FROM golang:1.23-bookworm
RUN apt-get update && apt-get install -y ripgrep rsync git gcc && rm -rf /var/lib/apt/lists/*
WORKDIR /build
COPY agent/go.mod ./
RUN go mod download || true
```

This image acts as a cache seed. When `Dockerfile.build` runs, the `go mod download` layer can reuse the cached modules from `Dockerfile.base` if built beforehand, or from a previously built `mct-control`/`mct-treatment` image. The `|| true` ensures the build succeeds even if the network is unavailable — useful in air-gapped or CI environments where module downloads are handled separately.

## Testing a Patch File

Pass the path to a `.patch` or `.diff` file:

```bash
./scripts/ab-dev.sh path/to/my-change.patch
```

The script detects that the argument is an existing file and uses it directly:

```
[patch] Using existing patch file: /absolute/path/to/my-change.patch

============================================================
  Step 1 — Build control image (version A, no patch)
============================================================
[build:control] Building mct-control from <repo-root>...
[build:control] Image built: mct-control

============================================================
  Step 2 — Build treatment image (version B, with patch)
============================================================
[build:treatment] Building mct-treatment with CHANGE_PATCH=<path>...
[build:treatment] Image built: mct-treatment
```

## Testing a Git Ref

When the argument is not a file on disk, the script treats it as a git ref and generates a patch from `ref..HEAD`:

```bash
# Test the most recent commit
./scripts/ab-dev.sh HEAD~1

# Test all changes since a branch point
./scripts/ab-dev.sh origin/main
```

The resolution logic:

1. If the argument resolves as a local or remote ref, it is used directly.
2. If the ref contains a `/` and is not found locally, the script attempts `git fetch origin` before giving up.
3. `git diff <ref> --` generates the patch into a temporary file.
4. If the generated patch is empty, a warning is printed but the build proceeds (the treatment image will be identical to control).

Example output for a ref:

```
[patch] 'HEAD~1' is not a file; treating as git ref, generating patch...
[patch] Diff range: HEAD~1..HEAD
[patch] Generated patch: /tmp/mct-ab-patch.XXXXXX/change.patch (1234 bytes, 42 lines)
```

## Options `--cmd` and `--no-run`

### `--cmd <command>`

Overrides the default verification command. The command is executed inside each container via `bash -c`. The default command verifies that all five binaries report version or help output:

```default
set -euo pipefail
echo "=== Built binaries ==="
for bin in mct-agent mct file-discovery snippet-discovery shell-agent; do
    if command -v "$bin" &>/dev/null; then
        echo "--- $bin ---"
        "$bin" --version 2>&1 || "$bin" --help 2>&1 | head -5 || echo "(no version/help output)"
    else
        echo "--- $bin: NOT FOUND ---"
    fi
done
echo "=== Done ==="
```

Custom command examples:

```bash
# Run Go unit tests inside the container
./scripts/ab-dev.sh --cmd 'cd /build/agent && go test ./...' HEAD~1

# Verify a specific binary and its flags
./scripts/ab-dev.sh --cmd 'mct-agent --help && mct-agent --version' my-change.patch

# Shell into the container interactively (use docker run manually for this)
docker run --rm -it mct-treatment bash
```

### `--no-run`

Skips container execution entirely. Both images are built, but no command is run and no output diff is performed. Useful when:

- You only want to verify that the build succeeds with a given patch.
- You want to run containers manually afterward for interactive debugging.
- Build caching is the primary goal (warm the layer cache for subsequent runs).

```bash
./scripts/ab-dev.sh --no-run my-change.patch
```

Output when `--no-run` is set:

```
--no-run specified; skipping container execution.
Control image:   mct-control
Treatment image: mct-treatment
```

## Output Directories

All container output lands under `/tmp/mct-ab-output/`:

```
/tmp/mct-ab-output/
├── control/
│   ├── stdout.log      # Control container stdout
│   ├── stderr.log      # Control container stderr
│   └── exit_code       # Control container exit code (marker file)
└── treatment/
    ├── stdout.log      # Treatment container stdout
    ├── stderr.log      # Treatment container stderr
    └── exit_code       # Treatment container exit code (marker file)
```

The directories are cleaned (`rm -rf`) before each run, so only the latest output is preserved. The `exit_code` marker file is written as the last step of the command inside the container — its value takes precedence over the Docker exit code in case of signal-induced exits.

## Interpreting Diff Results

The script prints a structured summary at the end:

```
============================================================
  Summary
============================================================

  Control exit code:   0
  Treatment exit code: 0

  Exit codes match   (both=0)
  stdout: unchanged
  stderr: CHANGED relative to control

  VERDICT: The treatment changed output relative to control.

  Output directories:
    Control:   /tmp/mct-ab-output/control
    Treatment: /tmp/mct-ab-output/treatment
```

The verdict logic:

| Condition | Meaning |
|-----------|---------|
| Exit codes differ | The treatment image exits with a different code than control. The change may introduce a crash or alter error handling. |
| stdout differs | The change produces different standard output. This may be intentional (e.g., new version strings) or unintentional (regression). |
| stderr differs | The change produces different diagnostic output. Log format changes, new warnings, or suppressed errors can cause this. |
| All match | No observable difference between control and treatment. |

The script exits with code `0` when control and treatment match, and code `1` when they differ. This makes it suitable for CI gating (`./scripts/ab-dev.sh ... && echo "No regressions"`).

**Inspecting diffs manually:**

```bash
# Side-by-side comparison (requires diffoscope or similar)
diff -u /tmp/mct-ab-output/control/stdout.log /tmp/mct-ab-output/treatment/stdout.log

# Check if exit codes differ
cat /tmp/mct-ab-output/control/exit_code
cat /tmp/mct-ab-output/treatment/exit_code
```

## CI Integration Notes

The `ab-dev.sh` script is designed to run in CI pipelines. Key considerations:

1. **BuildKit requirement**: Ensure the CI runner has `DOCKER_BUILDKIT=1` support. Most modern CI platforms (GitHub Actions `ubuntu-latest`, GitLab SaaS runners) provide this by default.

2. **Caching between runs**: In CI, leverage Docker layer caching to speed up repeated builds:
   ```bash
   # Example: cache the builder base image
   docker build -f scripts/Dockerfile.base -t mct-base .
   ```

3. **Exit code gating**: Since the script exits `0` for "no diff" and `1` for "diff detected", it can be used directly as a CI step:
   ```yaml
   # GitHub Actions example
   - name: A/B validation
     run: ./scripts/ab-dev.sh origin/main
   ```

4. **Empty patches**: If the generated patch is empty (no changes between ref and HEAD), the script prints a warning but continues. The treatment image will be identical to control, and the diff will show no changes. This is not an error condition.

5. **Remote refs**: When testing against a remote branch that may not be fetched locally, the script attempts `git fetch origin` automatically. In shallow-clone CI environments, ensure the fetch depth is sufficient to resolve the target ref:
   ```yaml
   - uses: actions/checkout@v4
     with:
       fetch-depth: 0  # Full history for git diff
   ```

## Caching Strategy

The Docker build in `scripts/Dockerfile.build` employs a layered caching strategy to minimise rebuild time:

### Go Module Layer

The `go.mod` and `go.sum` files are copied and `go mod download` is executed **before** any source code is copied:

```dockerfile
COPY --link agent/go.mod agent/go.sum ./
RUN go mod download
```

This layer is invalidated only when dependencies change — not when application source changes. In most development cycles, `go.mod`/`go.sum` remain stable, so `go mod download` is served from cache on every build.

### Multi-Stage Build Separation

The builder stage contains all compilation tools (Go compiler, gcc, git). The runtime stage is a minimal `debian:bookworm-slim` image containing only the compiled binaries and runtime dependencies (`ripgrep`, `rsync`). This separation:

- Keeps the runtime image small (no Go toolchain, no git).
- Ensures the builder layer cache is independent of the runtime layer.
- Allows the runtime image to be rebuilt instantly when only source code changes (since `COPY --from=builder` picks up the new binaries from the freshly built builder stage).

### `COPY --link` Independence

Every `COPY` instruction uses `--link`, meaning each copy is an independent operation from the build context rather than a sequential layer dependency. The practical effect:

- Changing `scripts/install.sh` does not invalidate the `agent/` source copy or the `go mod download` cache.
- Changing `.machtiani/config.toml` does not invalidate any source or dependency layers.
- BuildKit can parallelise the independent copy operations.

This is in contrast to traditional `COPY` (without `--link`), where each `COPY` creates a new layer and all subsequent layers are invalidated when any file in the layer changes — even files unrelated to the modification.

### Pre-Warming via Dockerfile.base

For environments where repeated clean builds are needed (CI ephemeral runners), pre-building `Dockerfile.base` warms the Go module download cache:

```bash
docker build -f scripts/Dockerfile.base -t mct-base .
```

Subsequent `Dockerfile.build` runs can reuse the cached Go modules if the base image is referenced or if the module layer hash matches a previously built image.
