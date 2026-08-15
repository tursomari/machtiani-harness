# ADR: Go as Implementation Language

## Status

Accepted

## Context

Machtiani is a terminal-native AI coding agent. Its core loop: prompt → stream LLM tokens → execute shell command → capture stdout/stderr → return result. It avoids AST parsers, MCP, LSP hooks, and IDE extensions by design. The project needs a language that excels at concurrent process orchestration, text processing, and single-binary distribution.

## Decision

Machtiani is implemented in Go.

### Go strengths demonstrated by Machtiani

1. **Process orchestration with `os/exec` and `context`.** Commands spawn via `exec.CommandContext`; timeouts and cancellation propagate through the context tree. Go's stdin/stdout/stderr piping eliminates subprocess bugs common elsewhere.

2. **Concurrency without an async runtime.** Goroutines and channels coordinate the orchestrator, shell-agent, and LLM streaming. A buffered channel feeds tokens to the renderer while a separate goroutine monitors cancellation — no `async`/`await`, no executor.

3. **Frictionless text processing.** Prompts, shell output, ANSI stripping, and terminal rendering use `strings`, `bytes`, `regexp`, and `fmt`. External dependencies are `goldmark` and `glamour`.

4. **Single static binary, cross-compiled trivially.** A ~24 MB statically linked binary (`CGO_ENABLED=0`). Cross-compilation for linux/amd64, darwin/amd64, and arm64 requires only `GOOS` and `GOARCH`.

5. **Simplified tool calling.** A `<command>...</command>` XML-tag triggers shell execution — no MCP server, no JSON Schema generation. A `Command string` field is the entire tool surface.

## Considered Alternatives

### TypeScript

**Why it dominates AI coding agents:** TypeScript/Node.js runs natively inside VS Code, enabling IDE extensions via LSP hooks and webviews at zero overhead. Zod powers dynamic JSON schema generation for MCP tool calling. This is why Cursor and Copilot use TypeScript.

**Why not for Machtiani:** Machtiani is terminal-only — no IDE, no MCP, no JSON schemas. A TS CLI requires bundling Node.js. `exec.Command` needs no Zod.

### Rust

**Why it dominates AI coding agents:** Rust powers code-analysis tools: tree-sitter and SWC for AST parsing, oxc for linting, rust-analyzer for LSP. MCP servers favor Rust for performance and memory safety.

**Why not for Machtiani:** Machtiani avoids AST parsing, MCP, and LSP. Its I/O-bound workload gains nothing from Rust's ownership model; goroutines with synchronous I/O are simpler.

### Why Go wins by default

Machtiani sidesteps every domain where TS and Rust excel: no AST parsing, no MCP, no IDE, no LSP. Their advantages are irrelevant. What remains — streaming tokens, running commands, processing text — is Go's sweet spot: concurrent process management, single static binary, lightweight text handling.

### Technological Subsidiarity

Machtiani does not replace IDE-native coding agents. It complements them. Through its mode system, users define custom modes that invoke external tools headlessly, delegating structured code analysis and complex refactoring to the Rust and TypeScript agents that excel at those tasks.

Machtiani stays focused on raw-intelligence-tapping, concurrent subprocess orchestration, and the main interaction loop. Go handles the orchestration layer — streaming tokens, spawning commands, managing sessions. Rust and TypeScript handle deep code intelligence, accessed headlessly via CLI. Each tool operates at its proper level; no capability is sacrificed.

The ecosystem is balanced: no single language overreaches. The only requirement: a headless interface. Every tool should be usable in composition, not in isolation.

## Consequences

**Positive:**
- Single static binary, no runtime dependencies
- Concurrent process orchestration with minimal ceremony
- Standard library covers text, HTTP, and subprocess management
- Fast compile times, cross-compilation to all major platforms

**Negative:**
- ~24 MB binary larger than equivalent Rust, acceptable for a dev tool
- GC adds non-deterministic latency, negligible for I/O workloads
- No algebraic data types; invariants enforced by convention

**Neutral:**
- Simplicity means fewer compile-time guarantees; test suite provides coverage

## References

- `go.mod` — Go 1.26.5
- `agent/internal/shell-agent/` — `os/exec.CommandContext`
- `agent/internal/orchestrator/` — goroutine/channel concurrency
- `agent/internal/session/runner_turns.go` — routing policy
- `flake.nix` — `CGO_ENABLED=0`, cross-compilation
