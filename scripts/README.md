# scripts/ — Build, Install, A/B Testing, and Evaluation Infrastructure

---

## Overview

The `scripts/` directory contains the build, install, A/B testing harness, and
evaluation infrastructure for the mct-agent monorepo. It provides the Docker
tooling for reproducible A/B comparison of code changes (`ab-dev.sh` plus
`Dockerfile.build`), a local binary installer (`install.sh`), the evaluation
pipeline for comparing mct-agent against Forge (`run_eval.sh`,
`run_eval_head.sh`), and supporting files for Skyvern web interaction and judge
prompting.  Agent-side test runner documentation lives in
`agent/tests/README.md` — this file focuses on the scripts that sit above the
agent layer.

---

## ab-dev.sh — Docker A/B testing harness

`scripts/ab-dev.sh` builds control and treatment Docker images from
`scripts/Dockerfile.build`, optionally runs a command inside each container,
diffs the output, and supports per-case PASS/FAIL comparison via
`agent/tests/ab-live.sh`.  The script lives at `scripts/ab-dev.sh:1`.

The required positional argument `<patch-file|git-ref>` can be either a file
path (used directly as the treatment patch) or a git ref (e.g., `HEAD~1`,
`origin/main`).  When a git ref is given, the script generates a patch via
`git diff <ref>..HEAD` and applies it as the treatment patch.  The control
image is always built from the current working tree with no patch applied,
unless `--control-commit` is given.

Key flags:

| Flag | Description |
|---|---|
| `--cmd <command>` | Command to run inside each container via `sh -c`.  Default: verify all built binaries report version/help output. |
| `--no-run` | Build both images but skip container execution. |
| `--parse-results` | Parse `run-live.sh` output through `agent/tests/ab-live.sh` and produce a side-by-side per-case PASS/FAIL comparison table. |
| `--control-commit <ref>` | Check out the specified commit before building the control image, then restore HEAD. |
| `--env KEY=VALUE` | Forward a specific environment variable into both containers (repeatable).  Once any `--env` is used, automatic `TEST_*` forwarding is disabled. |
| `--help` | Print usage and exit. |

When `--env` is not used, the script automatically discovers every `TEST_*`
variable from the host environment and forwards them into both containers.  The
script also always forwards `MCT_AGENT_BIN=/usr/local/bin/mct-agent` so that
`run-live.sh` bypasses its `check_bin` validation inside the container (where
`.git/` is absent).  Output is collected under `/tmp/mct-ab-output/control/`
and `/tmp/mct-ab-output/treatment/`.

For the per-case parser and full A/B workflow details (including example
invocations, delta values, and the `ab-live.sh` parser rules), see
`agent/tests/README.md`.

---

## Dockerfile.build — Multi-stage build

`scripts/Dockerfile.build` is a two-stage Docker image that powers the A/B
testing harness.

**Stage 1 (builder)** is based on `golang:1.23-bookworm`.  It installs build
dependencies (ripgrep, rsync, git, gcc), copies `agent/go.mod` and
`agent/go.sum` first for layer caching, then copies the full `agent/` source
tree, `scripts/`, `.machtiani/`, and `.git/`.  When the `CHANGE_PATCH_B64`
build-arg is present (treatment build only), the stage decodes the base64
patch, removes broken submodule gitlinks to avoid path resolution errors,
reverts patched files to `HEAD~1` so the patch applies cleanly, applies the
patch with `git apply`, and creates a git commit with message `"treatment
patch"`.  This last commit step is non-obvious but required: `install.sh`
embeds the short git commit hash and dirty flag into the binary via
`-ldflags`, and `check_bin` at runtime compares the embedded commit against
HEAD.  Without a post-patch commit, the binary metadata would mismatch the
working tree state, causing a hard exit.  All binaries (`mct-agent`, `mct`,
`file-discovery`, `snippet-discovery`, `shell-agent`) are built into
`/build/bin` via `PREFIX=/build ./scripts/install.sh --install-peripherals`.

**Stage 2 (runtime)** is based on `debian:bookworm-slim`.  It installs runtime
dependencies (ripgrep, rsync, bash, python3, git), copies the built binaries
from the builder (`/build/bin/` → `/usr/local/bin/`), copies `mct-forge` from
the host's `peripherals/` directory, copies `.machtiani/` and `agent/` from
the builder into `/workspace/`, and copies the full Go toolchain from the
builder (`/usr/local/go`) so that tests which shell out to `go build` work in
the runtime container.  It then runs a `git init` + `git add -A` + commit step
to create a valid repository at `/workspace`, and tags an artifact readme
sub-repository if present.  The stage sets `REPO_ROOT=/workspace` and
`MACHTIANI_CONFIG=/workspace/.machtiani/config.toml`.  The resulting runtime
image is approximately 1.4 GB.

The `.dockerignore` at the repository root reduces the build context from
~14 GB to ~3.4 MB by excluding `.machtiani/sessions/`, `tmp/`, `.git/modules/`,
`third_party/`, and other large directories.  Only top-level `.git/` objects
reach the builder stage.

---

## Dockerfile.base

`scripts/Dockerfile.base` is a simpler, single-stage base image for non-A/B
Docker workflows such as the LLM evaluator.  It starts from
`golang:1.23-bookworm`, installs ripgrep, rsync, git, and gcc, sets
`WORKDIR /build`, copies `agent/go.mod`, and runs `go mod download` for layer
caching.  It does not embed the A/B infrastructure (no patch application
logic, no multi-stage separation, no runtime stage), making it suitable as a
lightweight base for evaluation containers that only need the Go toolchain and
module dependencies rather than a full mct-agent runtime.

---

## install.sh — Local binary installer

`scripts/install.sh` builds and installs `mct-agent` (and optionally `mct`,
`file-discovery`, `snippet-discovery`, and `shell-agent`) into `PREFIX/bin`,
which defaults to `~/.local/bin`.  It embeds the short git commit hash and
dirty flag into each binary via `-ldflags`, producing version strings like
`dev-<12-char-commit>` (or `dev-<12-char-commit>-dirty` for uncommitted
changes).  The script uses `-buildvcs=false` to prevent the Go toolchain from
embedding its own VCS metadata, relying solely on the script's own commit and
dirty-flag detection.  The `PREFIX` environment variable controls the install
destination; `GOCACHE` defaults to `$REPO_ROOT/.gocache` when unset.  Pass
`--install-peripherals` (or the older alias `--all-binaries`) to build and
install the four non-mct-agent binaries: `mct`, `file-discovery`,
`snippet-discovery`, and `shell-agent`.  The script lives at
`scripts/install.sh:1`.

---

## generate_comparison.sh

`scripts/generate_comparison.sh` generates a structured comparison runbook
between two `mct-agent` run outputs (e.g., control vs treatment).  It takes
`--root <path>` (the project root), `--prompt <path>` (the task prompt file),
and optional `--model`, `--api-key`, `--api-key-file`, `--config`,
`--ground-truth <oid>`, and `--eval-commit <oid>` flags.  The script produces a
Markdown runbook (default: `/tmp/comparison_runbook.md`) that the evaluation
pipeline uses as input.  It is invoked by `run_eval.sh` to produce the per-task
comparison document that feeds into the judge LLM.  The script lives at
`scripts/generate_comparison.sh:1`.

---

## run_eval_head.sh

`scripts/run_eval_head.sh` runs the head evaluation: tasks without a
ground-truth commit, used for measuring mct-agent quality against the current
repository HEAD.  It is a two-phase pipeline that creates git worktrees for
mct-agent and Forge, runs both agents on the same prompt, invokes the judge LLM
with `scripts/judge_prompt_head_template.md`, and produces scored evaluation
output.  It supports `--mode read-only` (judge evaluates agent outputs only)
and `--mode write` (judge also produces its own implementation as an unscored
benchmark).  Required flags: `--prompt <path>` and `--api-key <provider:key>`.
Optional flags include `--repo`, `--judge-model`, `--model`, `--sync-model`,
`--sync-api-key`, `--config`, `--output-dir`, `--keep`, and `--sequential`.
The script lives at `scripts/run_eval_head.sh:1`.  See `docs/eval_head.md` for
full usage and output artifact descriptions.

---

## run_eval.sh

`scripts/run_eval.sh` runs the standard evaluation pipeline: a two-phase
comparison of mct-agent against Forge on resolved issues with known
ground-truth commits.  It creates git worktrees for the mct-agent run, the
Forge run, and the judge, invokes both agents against the pre-fix commit,
generates a comparison runbook via `scripts/generate_comparison.sh`, and runs
the judge LLM with `scripts/judge_prompt_template.md` to score plan quality and
implementation quality.  Required flags: `--repo <path>`, `--prompt <path>`,
`--eval-commit <oid>`, and `--ground-truth <oid>`.  Optional flags include
`--model`, `--api-key-file`, `--api-key`, `--config`, `--output-dir`, and
`--keep`.  The script lives at `scripts/run_eval.sh:1`.

---

## git-full-diff.go

`scripts/git-full-diff.go` is a Go program that generates a complete diff of
a single file between its HEAD version and its current working-tree version.
It takes a file path as its sole argument, validates that the file is tracked
by git, retrieves the HEAD content via `git show HEAD:<path>`, writes it to a
temp file, and runs `git diff --no-index --unified=99999999` between the temp
file and the on-disk file.  The `--unified=99999999` flag forces the diff to
emit the entire file context, producing a "full diff" rather than a hunks-only
patch.  This is used by some A/B evaluation workflows that need a complete file
diff for the judge rather than a contextual patch.  The program lives at
`scripts/git-full-diff.go:1`.

---

## judge_prompt_*.md

`scripts/judge_prompt_template.md` and
`scripts/judge_prompt_head_template.md` are prompt templates used by the judge
LLM in the evaluation pipeline.  Each template defines a two-axis rubric (Plan
Quality and Implementation Quality) and provides the context, scoring
instructions, and inline placeholders (`{{PROJECT_NAME}}`, `{{EVAL_COMMIT}}`,
`{{GROUND_TRUTH_COMMIT}}`, `{{HEAD_SHA}}`) that get populated by the evaluation
scripts.  The standard template (`judge_prompt_template.md`) is used by
`run_eval.sh` for cases with a known ground-truth fix commit.  The head
template (`judge_prompt_head_template.md`) is used by `run_eval_head.sh` for
cases without a ground-truth commit and supports both read-only and write
evaluation modes.

---

## runbook_template.md

`scripts/runbook_template.md` is a template for human-written runbooks that
guide mct-agent through complex tasks.  It includes placeholders for project
name, project root, prompt file, eval commit, and ground truth commit, along
with sections for purpose, prerequisites, setup instructions, agent invocation
commands, and evaluation criteria.  The evaluation pipeline populates this
template via `scripts/generate_comparison.sh` and feeds the resulting runbook
to the judge LLM.  The template lives at `scripts/runbook_template.md:1`.

---

## skyvern-start.sh

`scripts/skyvern-start.sh` starts the local Skyvern API server for web
interaction tasks.  It checks whether Skyvern is already running on port 8000
and, if so, extracts and prints the API key.  Otherwise it kills any existing
process on port 8000, starts the Skyvern server from
`third_party/skyvern/.env`, and outputs the generated API key.  The script is
used by mct-agent modes that require browser automation (e.g.,
`code-forge-skyvern`).  It lives at `scripts/skyvern-start.sh:1`.

---

## skyvern-examples/

`scripts/skyvern-examples/` contains SkyvernScript example payloads and a
README for common web automation workflows.  It includes `machtiani-extract.json`
(data extraction from the Machtiani site), `osan3-navigate.json` (navigation
example), and `obstacle-detection.md` (guide to handling captchas and
authentication walls).  The directory's own `README.md` documents the Skyvern
local server API (endpoints, request fields, polling pattern) and links to
additional resources in `third_party/skyvern/docs/`.

---

## Quick reference: A/B testing workflow

The most common A/B testing command:

```
scripts/ab-dev.sh --parse-results \
  --cmd "agent/tests/run-live.sh test_enforce_early_commands" \
  HEAD~1
```

This builds control and treatment images, runs a single test case in both,
parses the PASS/FAIL output, and prints a side-by-side comparison table.  For
the full A/B workflow documentation including all flags, example invocations,
parser rules, and Docker internals, see `agent/tests/README.md`.
