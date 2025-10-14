FROM golang:1.23-alpine

# Install required packages for runtime and TLS
RUN apk add --no-cache ripgrep bash ca-certificates git \
    && update-ca-certificates

# Use a stable workspace path
WORKDIR /workspace

# Copy test fixture early to benefit from Docker layer caching
COPY tests/undici/ /workspace/undici/
COPY e2e/ /workspace/e2e/

# Copy module file to enable future dependency caching (no external deps now)
COPY go.mod ./
RUN go mod download || true

# Copy app code last for fast rebuilds
COPY cmd/ cmd/
COPY internal/ internal/

# Build the binary into PATH
RUN go build -o /usr/local/bin/file-discovery ./cmd/file-discovery

# Copy the live test runner
COPY tests/run-live.sh /usr/local/bin/run-live.sh
RUN chmod +x /usr/local/bin/run-live.sh

# Copy the flags test runner
COPY tests/run-flags.sh /usr/local/bin/run-flags.sh
RUN chmod +x /usr/local/bin/run-flags.sh

# Default working directory is the test fixture
WORKDIR /workspace/undici

ENTRYPOINT ["/usr/local/bin/run-live.sh"]
