# Machtiani (mct) — Local Prompting CLI

Lightweight CLI for prompting against your local repository using:
- Local file discovery (no remote services)
- Direct LLM calls to any OpenAI‑compatible endpoint
- Local filename generation and chat saving

The `prompt` command no longer relies on a remote URL or backend server. It runs file discovery locally, builds an inline context from the discovered files, streams the LLM response, and saves the chat to `.machtiani/chat/`.

## Requirements
- Go 1.22+
- OpenAI‑compatible model configuration (no implicit defaults)
  - `OPENAI_API_KEY` (required)
  - `OPENAI_BASE_URL` (required)
  - `OPENAI_MODEL` (required; can be provided via `--openai-model`/`--model`)
- Optional: `git` for other subcommands (`status`, `sync`, `remove`)

## Build and Install (recommended)
This builds the CLI and the bundled `file-discovery` helper from the submodule, then installs both into your PATH.

```
mkdir -p ~/.local/bin \
  && ./build.sh \
  && install -m 0755 ./machtiani-cli ~/.local/bin/mct \
  && install -m 0755 ./bin/file-discovery ~/.local/bin/file-discovery \
  && hash -r

# Ensure PATH contains local bin (if not already)
export PATH="$HOME/.local/bin:$PATH"
```

Alternative (manual) build:
```
# Build CLI
go build -o mct ./cmd/mct

# Build file-discovery from submodule
(cd submodules/file-discovery && go build -o ../../bin/file-discovery ./cmd/file-discovery)

# Install both
install -m 0755 ./mct ~/.local/bin/mct
install -m 0755 ./bin/file-discovery ~/.local/bin/file-discovery
```

## Quick Start
Set your model configuration:
```
export OPENAI_API_KEY=sk_...
export OPENAI_BASE_URL=https://api.openai.com/v1  # or your provider
export OPENAI_MODEL=gpt-4o-mini
```

Run a prompt against the current repo:
```
mct prompt "Summarize the architecture and main APIs."
```

Use a markdown prompt file:
```
mct prompt --file prompt.md
```

Answer‑only mode (no discovery, no saving):
```
mct prompt --mode=answer-only -f prompt.md
```

## How It Works (prompt)
1. Runs `file-discovery` in the repo to select relevant files.
2. Filters file paths via `.machtiani.ignore` rules:
   - Exact match (e.g., `path/to/file.go`)
   - Directory prefix when rule ends with `/` (e.g., `internal/cli/`)
   - Globs using `*` and `?` (e.g., `**/*.md` is not supported; use simple globs)
3. Builds a combined prompt that inlines file contents:
   - Default caps: ~100 KB per file; ~2 MB total
   - Appends `[TRUNCATED]` when clipping
4. Streams tokens from your LLM endpoint and renders Markdown in the terminal.
5. Saves output to `.machtiani/chat/<generated-name>.md` and updates `.machtiani/chat/machtiani-response.md`.

## Configuration
- Env vars:
  - `OPENAI_API_KEY`: API key for your LLM provider (required)
  - `OPENAI_BASE_URL`: Base URL for OpenAI‑compatible API (required)
  - `OPENAI_MODEL`: Model name (required unless supplied via `--openai-model`/`--model`)
  - `MACHTIANI_SESSION_ID`: Optional; forwarded to `file-discovery`
  - `FILE_DISCOVERY_BIN`: Optional; path to a custom `file-discovery` binary
  - Legacy `MCT_MODEL_*` accepted as fallback with a deprecation warning

Binary resolution for `file-discovery`:
1) `FILE_DISCOVERY_BIN` env override
2) `file-discovery` in PATH
3) `<mct-exe-dir>/bin/file-discovery` (bundled by `build.sh`)

## Notes
- The `prompt` command is fully local and does not hit Machtiani server URLs.
- Other commands (`status`, `sync`, `remove`) still interact with server APIs and may require additional env such as `MACHTIANI_URL` and `MACHTIANI_REPO_MANAGER_URL`.

## Troubleshooting
- “file-discovery binary not found”
  - Ensure you’ve run `./build.sh` and have `bin/file-discovery`, or install a compatible binary and set `FILE_DISCOVERY_BIN`.
- “Missing model configuration”
  - Set `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL`.
- Streaming fails or returns non‑OK
  - The CLI falls back to a non‑streaming request once; verify your base URL and key.
