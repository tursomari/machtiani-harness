FROM golang:1.23-bookworm

RUN apt-get update && apt-get install -y ripgrep && rm -rf /var/lib/apt/lists/*

WORKDIR /build/agent

# go.sum may not exist in every branch; copy only go.mod in that case
COPY agent/go.mod ./

RUN go mod download || true

COPY agent/ ./

COPY .machtiani/ .machtiani/

WORKDIR /build/agent/internal/mct-code
RUN go build -o /usr/local/bin/mct-code ./cmd/mct-code

WORKDIR /workspace
COPY tests/repositories/undici ./

ENV MCT_DEFAULT_MODEL=deepseek-v4-flash
ENV MACHTIANI_CONFIG=/build/agent/.machtiani/config.toml

CMD ["mct-code", "1. Use FSSearch to find which file defines the function request as module.exports.request with content output and show_line_numbers true. 2. Read that file and find the exact line. 3. Summarize what you found. Do not modify any files."]
