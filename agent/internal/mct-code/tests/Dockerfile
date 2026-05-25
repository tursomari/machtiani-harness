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
COPY agent/internal/mct-code/tests/test.sh /workspace/test.sh
COPY agent/internal/mct-code/tests/repositories/undici ./
COPY .machtiani/config.toml /workspace/.machtiani/config.toml

ENV MCT_DEFAULT_MODEL=deepseek-v4-flash
ENV MACHTIANI_CONFIG=/workspace/.machtiani/config.toml

CMD ["sh", "-c", "./test.sh"]
