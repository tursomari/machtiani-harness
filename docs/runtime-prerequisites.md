# Runtime Prerequisites

> **Stability:** `[CONTRACT]` — Changes to the dependencies listed here require a
> version-controlled update to this file.

## Dependency Table

| # | Dependency | Min Version | Used By | Failure Mode | Install Check |
|---|---|---|---|---|---|
| 1 | Nix | 2.24+ | Installation, updates, development shells | Install/update cannot realize the locked flake. | ✅ |
| 2 | git | Pinned by flake | Repository operations and sync | Commands cannot inspect or hydrate repositories. | ✅ |
| 3 | ripgrep (`rg`) | Pinned by flake | File discovery | File discovery cannot search source. | ✅ |
| 4 | bash | Pinned by flake | Shell-agent command execution | Shell commands fail immediately. | ✅ |
| 5 | GNU sed | Pinned by flake | Transcript and helper transformations | Text transformations fail. | ✅ |

Go is a locked build dependency and is not retained in the installed runtime
closure. Nix itself is a host prerequisite and is not bundled into machtiani.

## Platform Notes

- **macOS:** Agent-launched commands see the flake's GNU Bash, coreutils, and
  GNU sed before the platform tools. Scripts that intentionally need the Apple
  tools should use absolute paths.
