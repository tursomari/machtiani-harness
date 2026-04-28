# Runtime Prerequisites

> **Stability:** `[CONTRACT]` — Changes to the dependencies listed here require a
> version-controlled update to this file.

## Dependency Table

| # | Dependency | Min Version | Used By | Failure Mode | Install Check |
|---|---|---|---|---|---|
| 1 | Go | 1.23 | All binaries (build only) | Build fails. No runtime concern for pre-built binaries. | ✅ |
| 2 | git | 2.x | `mct-agent run` (repo hydration), `mct-agent sync` (commit resolution) | Repo hydration fails; agent runs without repo context producing degraded results. `sync` fails entirely. | ✅ |
| 3 | ripgrep (`rg`) | 13+ | `file-discovery` (regex file search backend) | File discovery errors out; agent operates without file-awareness, producing significantly degraded planning. No graceful fallback. | ✅ |
| 4 | bash | 3.2+ | `shell-agent` (command execution), `scripts/install.sh` | Shell-agent commands fail immediately. Install script won't execute. | ❌ |
| 5 | sed | POSIX sed | `mct-agent` (transcript processing, patch helpers) | Text transformations fail silently or produce malformed output. BSD sed `-i` requires an argument, causing silent failures on macOS. | ❌ |

## Install Script Gaps

`scripts/install.sh` checks for Go, git, and ripgrep. It does **not** check for bash or sed. On minimal systems (Alpine, distroless), the user discovers these failures only at runtime.

**Phase 1 recommendation:** Add `bash --version` and `sed --version` (or a functional test) to the install script. Fail with a clear message if either is missing.

## Platform Notes

- **macOS:** Ships bash 3.2 and BSD sed. The BSD sed `-i` flag requires an explicit empty-string argument (`-i ''`), while GNU sed does not. Any sed invocation using `-i` without the BSD-compatible form will silently produce wrong output on macOS.
- **Minimal containers (Alpine, distroless):** May lack bash and sed entirely. Shell-agent is non-functional without bash. Phase 3 (production Docker image) must explicitly include all five dependencies.
