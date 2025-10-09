# Machtiani (mct) — Local Prompting CLI

Lightweight CLI for prompting against your local repository using:
- Local file discovery (no remote services)
- Direct LLM calls to any OpenAI‑compatible endpoint
- Local filename generation and chat saving

The `prompt` command no longer relies on a remote URL or backend server. It runs file discovery locally, builds an inline context from the discovered files, streams the LLM response, and saves the chat to `.machtiani/chats/`.

## Requirements
- Go 1.22+
- OpenAI‑compatible model configuration (no implicit defaults)
  - `OPENAI_API_KEY` (required)
  - `OPENAI_BASE_URL` (required)
  - `OPENAI_MODEL` (required; can be provided via `--openai-model`/`--model`)
- Optional: `git` for repository-aware features (system messages, discovery sandboxing)

## Build and Install (recommended)
This builds the CLI and the bundled `file-discovery` helper from the submodule, then installs both into your PATH.

```
mkdir -p ~/.local/bin \
  && ./build.sh \
  && install -m 0755 ./bin/mct ~/.local/bin/mct \
  && install -m 0755 ./bin/file-discovery ~/.local/bin/file-discovery \
  && hash -r

# Ensure PATH contains local bin (if not already)
export PATH="$HOME/.local/bin:$PATH"
```

Alternative (manual) build:
```
# Build CLI
mkdir -p bin
go build -o bin/mct ./cmd/mct

# Build file-discovery from submodule
(cd submodules/file-discovery && go build -o ../../bin/file-discovery ./cmd/file-discovery)

# Install both
install -m 0755 ./bin/mct ~/.local/bin/mct
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

Cap prompt size when using models with stricter limits:
```
mct prompt "Summarize architecture" --max-input-tokens 6000
```
The flag keeps the combined prompt (history, user text, and discovered files) within the specified token estimate by truncating file content from the bottom and inserting stamps that describe the omitted line range.

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
   - Optional `--max-input-tokens` enforces an estimated token budget and replaces truncated sections with a stamped marker showing line numbers of the omission.
4. Streams tokens from your LLM endpoint and renders Markdown in the terminal.
5. Saves output to `.machtiani/chats/<generated-name>.md` and updates `.machtiani/chats/machtiani-response.md`.

### Git‑Only Discovery Sandbox
- By default, `mct` creates a temporary workspace containing only Git‑tracked files (committed or staged) and runs `file-discovery` there.
- This keeps untracked/build artifacts out of scope for privacy and reproducibility, without changing any other tools or your working tree.
- Behavior can be disabled by setting `MCT_USE_GIT_FILTER=false`.
- If not in a Git repo, discovery runs in the current directory as before.
 - Tracked files inside initialized Git submodules are included automatically. Uninitialized submodules are skipped without error.
 - Only declared submodules are considered; nested repos that are not configured as submodules are not scanned.
 - Inclusion remains strictly "tracked‑only": untracked files within submodules are not copied into the sandbox.

## Configuration
- Env vars:
  - `OPENAI_API_KEY`: API key for your LLM provider (required)
  - `OPENAI_BASE_URL`: Base URL for OpenAI‑compatible API (required)
  - `OPENAI_MODEL`: Model name (required unless supplied via `--openai-model`/`--model`)
  - `MACHTIANI_SESSION_ID`: Optional session identifier respected by `mct`.
    - If set (e.g., by the agent), `mct` loads and persists conversation history under `~/.machtiani/sessions/session-<id>.json`, enabling continuity across multiple `mct` calls within the same agent run.
    - If not set, `mct` generates a fresh unique session ID for the run, so no prior conversation is inlined and the session will not be reused unintentionally across projects.
  - `FILE_DISCOVERY_BIN`: Optional; path to a custom `file-discovery` binary
  - `MCT_USE_GIT_FILTER`: Enable Git‑only temp workspace for discovery (default: true; set to `false` to disable)

### Config resolution order

`mct` looks for `config.toml` in the following priority:
1. `MACHTIANI_CONFIG` environment override (must point to an existing file).
2. Nearest `.machtiani/config.toml` inside the current Git repository, searching from the working directory up to the repo root.
3. Global fallback at `~/.machtiani/config.toml`.

If you are outside a Git repository, step 2 is skipped and only the environment variable and global config are considered.

Binary resolution for `file-discovery`:
1) `FILE_DISCOVERY_BIN` env override
2) `file-discovery` in PATH
3) `<mct-exe-dir>/bin/file-discovery` (bundled by `build.sh`)

## Artifact storage

- **Chat transcripts**: saved under `.machtiani/chats/` at the Git repository root when running inside a repo. Outside a repo, they fall back to `~/.machtiani/chats/`.
- **Readme artifacts**: always written to `.machtiani/artifacts/readme/` at the repository root and require a Git working tree.
- `mct-agent` and helper tools use the same resolution so invocations from subdirectories share the project-scoped artifacts.

## Notes
- The `prompt` command is fully local and does not hit Machtiani server URLs.

## Troubleshooting
- “file-discovery binary not found”
  - Ensure you’ve run `./build.sh` and have `bin/file-discovery`, or install a compatible binary and set `FILE_DISCOVERY_BIN`.
- “Missing model configuration”
  - Set `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL`.
- Streaming fails or returns non‑OK
  - The CLI falls back to a non‑streaming request once; verify your base URL and key.
