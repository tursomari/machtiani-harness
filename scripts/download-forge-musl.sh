#!/usr/bin/env bash
set -euo pipefail

usage() {
    cat <<'EOF'
Usage: download-forge-musl.sh OUTPUT_PATH [VERSION]

Download the official ForgeCode musl Linux binary to OUTPUT_PATH. VERSION may
be either "2.13.16" or "v2.13.16"; default is MCT_FORGE_VERSION or v2.13.16.

The musl artifact is used for benchmark containers because host-built GNU
Forge binaries can require newer glibc symbols than Debian bookworm provides.
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" || $# -lt 1 ]]; then
    usage
    exit 0
fi

OUTPUT_PATH="$1"
VERSION="${2:-${MCT_FORGE_VERSION:-v2.13.16}}"
if [[ "${VERSION}" != v* ]]; then
    VERSION="v${VERSION}"
fi

case "$(uname -m)" in
    x86_64|amd64)
        TARGET="x86_64-unknown-linux-musl"
        ;;
    aarch64|arm64)
        TARGET="aarch64-unknown-linux-musl"
        ;;
    *)
        echo "Error: unsupported architecture: $(uname -m)" >&2
        exit 1
        ;;
esac

URL="https://github.com/tailcallhq/forgecode/releases/download/${VERSION}/forge-${TARGET}"
TMP_PATH="${OUTPUT_PATH}.tmp"
mkdir -p "$(dirname "${OUTPUT_PATH}")"

echo "[forge] Downloading ${URL} -> ${OUTPUT_PATH}"
curl --retry 5 --retry-all-errors --retry-delay 2 --connect-timeout 20 -fL -C - -o "${TMP_PATH}" "${URL}"
chmod 0755 "${TMP_PATH}"
"${TMP_PATH}" --version >/dev/null
mv "${TMP_PATH}" "${OUTPUT_PATH}"
"${OUTPUT_PATH}" --version
