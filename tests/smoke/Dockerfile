# Smoketest Dockerfile for mct-agent
# Multi-stage: builder compiles the binary, runtime is a clean minimal image.
#
# Build:
#   docker build -f tests/smoke/Dockerfile -t mct-agent-smoke .
#
# Run (no secrets in build layers):
#   docker run --rm -e TEST_API_KEY=... mct-agent-smoke bash /tests/smoke/container.sh

# ---------------------------------------------------------------------------
# Builder stage: compile mct-agent and any peripherals
# ---------------------------------------------------------------------------
FROM golang:1.23-alpine AS builder

# System dependencies for the build: git (version stamping), build-base (cgo
# support when needed), bash (to run install.sh), and ripgrep (install.sh
# preflight check).
RUN apk add --no-cache git build-base bash ripgrep

WORKDIR /src

# Layer go.mod/go.sum first so dependency download is cached.
COPY agent/go.mod agent/go.sum ./agent/
WORKDIR /src/agent
RUN go mod download

# Now copy the rest of the source tree.
WORKDIR /src
COPY . .

# Build binaries.
#  1. Try the canonical install script with PREFIX=/out.
#  2. If it fails (e.g. missing runtime deps), fall back to a direct go build
#     of mct-agent.
RUN mkdir -p /out/bin && \
    (PREFIX=/out bash scripts/install.sh 2>/dev/null || \
     (cd agent && go build -buildvcs=false -o /out/bin/mct-agent ./cmd/mct-agent))

# Build the deterministic local ChatCompletion overflow fixture. It exercises
# production request/retry/config-persistence code without remote credentials.
RUN cd agent && go build -buildvcs=false -o /out/bin/context-overflow-smoke ./tests/context-overflow-smoke

# ---------------------------------------------------------------------------
# Runtime stage: lean production image with only the tools mct-agent needs
# ---------------------------------------------------------------------------
FROM alpine:3.20

# Runtime system dependencies.
#   git       – workspace operations and repository introspection
#   ripgrep   – file-discovery engine
#   curl/jq   – smoke-test harness helpers
#   bash/sed  – shell scripting in the smoke-test entrypoint
#   expect    – deterministic pseudo-terminal coverage for interactive menus
RUN apk add --no-cache git ripgrep curl jq bash sed expect

# Managed updates build from source inside the runtime environment.
COPY --from=builder /usr/local/go /usr/local/go
COPY --from=builder /go/pkg/mod /go/pkg/mod
ENV PATH="/usr/local/go/bin:${PATH}" \
    GOMODCACHE="/go/pkg/mod"

# Pull built binaries from the builder stage.
COPY --from=builder /out/bin /usr/local/bin/

# Smoke-test script (created in a later phase; referenced here so the
# Dockerfile is self-documenting).
COPY tests/smoke/container.sh /tests/smoke/container.sh
COPY tests/smoke/config-crud.sh /tests/smoke/config-crud.sh
COPY tests/smoke/update-container.sh /tests/smoke/update-container.sh
COPY . /fixtures/mct-source

WORKDIR /workspace

# .machtiani/ is deliberately NOT copied – no runtime state leaks into the
# image. Credentials and configuration are injected at container start via
# environment variables or volume mounts.
