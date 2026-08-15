# agent/tests/ - Test Infrastructure

This directory contains `run-live.sh` (the live integration test runner for
`machtiani`), `ab-live.sh` (a bash parser that translates test output into
structured TSV), and supporting files for the Docker-based A/B testing workflow
driven by `scripts/ab-dev.sh` and `scripts/Dockerfile.build`.

---

## run-live.sh

`run-live.sh` is the live integration test runner for `machtiani`.  It executes
the agent binary against a series of named test cases and emits PASS/FAIL lines
on stdout/stderr.  It can be run as a full suite (no arguments) or with one or
more test names for targeted execution.

### Named test case dispatch system

When invoked with arguments (e.g., `agent/tests/run-live.sh test_enforce_early_commands`),
the script performs a **case-statement-style lookup** through a `TESTS`
associative array defined near the bottom of the file.  Each entry maps a
human-friendly test name to the function that implements it:

```bash
declare -A TESTS=(
  ["test_local_tmp_root_unset_live"]="run_local_tmp_root_unset_live_case"
  ["test_code_no_forge"]="test_code_no_forge"
  ["test_code_forge_initial"]="test_code_forge_initial"
  ["test_code_forge_resume_with_mode"]="test_code_forge_resume_with_mode"
  ["test_code_forge_resume_without_mode"]="test_code_forge_resume_without_mode"
  ["test_code_resume_without_mode_no_forge"]="test_code_resume_without_mode_no_forge"
  ["test_enforce_early_commands"]="run_enforce_early_commands_case"
)
```

If the requested test name is not found in the array, the script prints the
list of available names and exits with an error.  When no arguments are given,
the full default suite runs instead.

### REPO_ROOT environment variable with fallback

The script resolves the repository root at the top of the file:

```bash
REPO_ROOT="${REPO_ROOT:-$(cd "$SCRIPT_DIR/../.." && pwd)}"
```

This lets an environment variable (`REPO_ROOT`) override the filesystem-based
computation.  In Docker, the runtime image sets `ENV REPO_ROOT=/workspace`, so
the script automatically works inside containers; on a host, the `cd`/`pwd`
fallback computes the correct path from the script's location.

### MACHTIANI_BIN env-var-skip pattern

The preflight block uses an **env-var-skip** pattern to bypass the `check_bin`
function entirely when `MACHTIANI_BIN` is set:

```bash
if [ -n "${MACHTIANI_BIN:-}" ]; then
  echo "Using MACHTIANI_BIN from environment: $MACHTIANI_BIN" >&2
else
  check_bin MACHTIANI_BIN machtiani "$REPO_ROOT/agent" "--version"
fi
```

`check_bin` performs commit-metadata validation, dirty-flag comparison, and
binary-staleness checks that all require a live `.git` directory.  In Docker
the runtime image omits `.git/` (only the builder stage has it), so setting
`MACHTIANI_BIN` in the container is the expected way to make `run-live.sh`
work there.

### TEST_* environment variables

The harness reads these variables to determine whether to run in live mode
(with real LLM endpoints) or dry-run mode (stubbed):

| Variable | Purpose |
|---|---|
| `TEST_API_KEY` | API key for the LLM provider (fallback: `OPENAI_API_KEY`) |
| `TEST_BASE_URL` | Base URL for the LLM provider (fallback: `OPENAI_BASE_URL`) |
| `TEST_MODEL` | Model identifier for live-mode tests (fallback: `OPENAI_MODEL`) |
| `TEST_ORCH_MODEL` | Orchestrator model override (defaults to `TEST_MODEL`) |
| `TEST_FILE_DISCOVERY_MODEL` | File-discovery model override |
| `TEST_SHELL_AGENT_MODEL` | Model for the shell-agent subcommand test case |

When all three of `TEST_API_KEY`, `TEST_BASE_URL`, and `TEST_MODEL` (or their
`OPENAI_*` fallbacks) are set, the harness enters live mode; otherwise it
forces `--dry-run`.  These variables are forwarded from the host into Docker
containers automatically by `scripts/ab-dev.sh`.

### pushd "$REPO_ROOT/agent" navigation

Many test cases navigate the agent source tree with `pushd "$REPO_ROOT/agent"`
before building Go binaries or running the agent.  This ensures that Go module
resolution and any `go build` invocations operate from the correct module root.

### How to add a new test case

1. **Define a function** — for happy-path tests, use the `run_happy_case`
   helper; for stub-server tests, use `start_llm_stub_server` /
   `generate_stub_config` / `stop_llm_stub_server` and invoke `machtiani`
   directly with `MACHTIANI_CONFIG` set.

2. **Register it in the `TESTS` array** — add a `["test_<name>"]="<function>"` entry
   to the associative array near the end of the script.

3. **Add to the default suite** (optional) — if the test should also run when
   the script is invoked with no arguments, call the function in the no-args
   block (guarded by `if [[ $# -eq 0 ]];`), conditioned on `LIVE_MODE` as
   appropriate.

---

## ab-live.sh

`ab-live.sh` is a bash parser that wraps `run-live.sh` output.  It reads
`stdout.log` and `stderr.log` from a **run-output-dir** (produced by
`scripts/ab-dev.sh` for a single container run) and emits a structured TSV
result table.

### Usage

```
agent/tests/ab-live.sh <run-output-dir>
agent/tests/ab-live.sh --tsv-out <path> <run-output-dir>
```

The script always exits 0; it reports whatever it can parse without failing
the caller.

### Parsing rules

Each line from `stdout.log` and `stderr.log` is checked against these regex
rules (evaluated in order):

| Regex pattern | Extracted `case_id` | Status |
|---|---|---|
| `Passed: <case_id>` | `<case_id>` | PASS |
| `PASS: run_shell_agent_subcommand_live_case` | `run_shell_agent_subcommand_live_case` | PASS |
| `PASSED: shell-command-trajectory-live case` | `shell-command-trajectory-live` | PASS |
| `Failed ... for <case_id>` (trailing word) | `<case_id>` | FAIL |
| `Failed (rc=N): <case_id>` | `<case_id>` | FAIL |
| `FAIL: machtiani shell-agent ...` | `shell-agent-subcommand` | FAIL |
| `FAILED (non-fatal): <case_id>` | `<case_id>` | FAIL |
| `FATAL:` (no `case_id`) | `fatal-preflight` | SKIP |

When a `FATAL:` line is seen the parser records `fatal-preflight` as SKIP.  If
no other results were parsed and a FATAL was seen, an explicit
`fatal-preflight` SKIP entry is emitted so the output is never silently empty.

Duplicate case IDs keep the first recorded status unless a later line upgrades
FAIL to PASS.

### TSV output format

The parser emits a TSV table with this header:

```
case_id	status	detail
```

Each row contains the case identifier, one of `PASS`/`FAIL`/`SKIP`, and the
sanitised source line (truncated to 200 characters) as detail.  Example:

```
case_id	status	detail
enforce-early-commands	PASS	Passed: enforce-early-commands (flag plumbed through end-to-end without regression)
models-per-component	FAIL	Missing keywords: models-per-component
fatal-preflight	SKIP	FATAL: machtiani binary not found
```

### Known gap: shell-agent subcommand case_id mismatch

The shell-agent subcommand test (`run_shell_agent_subcommand_live_case`) emits
two different `case_id` strings depending on whether the run passed or failed:

- **Pass**: emits `PASS: run_shell_agent_subcommand_live_case` — the parser
  maps this to `case_id = run_shell_agent_subcommand_live_case`.
- **Fail**: emits `FAIL: machtiani shell-agent ...` — the parser maps this to
  `case_id = shell-agent-subcommand`.

This means the control and treatment containers may produce different
`case_id` values for the same logical test case, which can appear as
NEW/MISSING deltas in the comparison table even when both sides agree on
PASS/FAIL status.  This is a known discrepancy in the current output
formatting.

---

## A/B testing workflow

The A/B workflow is driven by `scripts/ab-dev.sh`.  It builds two Docker images
(control and treatment) from `scripts/Dockerfile.build`, optionally runs a
command inside each container, and compares the output.

### scripts/ab-dev.sh usage

```
scripts/ab-dev.sh [--cmd <command>] [--no-run] [--parse-results]
                  [--env KEY=VALUE] [--control-commit <ref>]
                  <patch-file|git-ref>
```

**Positional argument** (`<patch-file|git-ref>`):
- If a file path, it is used directly as the treatment patch.
- If a git ref (e.g., `HEAD~1`, `origin/main`), `git diff <ref>..HEAD` generates
  the treatment patch.

**Control baseline**: the control image is built from the current working tree
with no patch applied, unless `--control-commit` is given.

#### Flags

| Flag | Description |
|---|---|
| `--cmd <command>` | Command to run inside each container via `sh -c`.  Default: verify all built binaries report version/help output. |
| `--no-run` | Build both images but skip container execution. |
| `--parse-results` | Parse `run-live.sh` output via `ab-live.sh` and produce a per-case PASS/FAIL comparison table instead of a raw `diff -u`. |
| `--env KEY=VALUE` | Forward a specific environment variable into both containers (repeatable).  Once any `--env` is used, automatic `TEST_*` forwarding is **disabled**; you must forward every needed variable explicitly. |
| `--control-commit <ref>` | Check out the specified commit before building the control image, then restore HEAD afterward.  When not given, the control is the current working tree. |

#### Automatic TEST_* forwarding

When `--env` is **not** used, `ab-dev.sh` automatically discovers every
`TEST_*` variable from the host environment and forwards them into both
containers.  This means running `agent/tests/run-live.sh` inside the container
naturally picks up the same live-mode credentials and model configuration used
on the host.

#### Per-case comparison table

When `--parse-results` is given, `ab-dev.sh` invokes `ab-live.sh` on both the
control and treatment output directories, then produces a side-by-side
comparison:

```
case_id                 control_status  treatment_status    delta
enforce-early-commands  PASS            PASS                SAME
models-per-component    PASS            FAIL                REGRESSION
new-test-case                           PASS                NEW
```

Delta values: `SAME`, `REGRESSION`, `FIX`, `NEW`, `MISSING`.

### Example invocations

```bash
# A/B test a specific test case against the parent commit
scripts/ab-dev.sh --parse-results \
  --cmd "agent/tests/run-live.sh test_enforce_early_commands" \
  HEAD~1

# A/B test all test cases against a known-good baseline commit
scripts/ab-dev.sh --control-commit a4ae3bf78 --parse-results \
  --cmd "agent/tests/run-live.sh" \
  HEAD

# A/B test with a custom API endpoint
scripts/ab-dev.sh --env TEST_BASE_URL=https://api.example.com/v1 --parse-results \
  --cmd "agent/tests/run-live.sh" \
  HEAD~3

# Build-only comparison (no container execution)
scripts/ab-dev.sh --no-run HEAD~3
```

---

## Docker A/B pipeline internals

The pipeline is implemented in `scripts/Dockerfile.build`, a multi-stage Docker
image.

### Multi-stage Dockerfile.build

**Stage 1: builder** (base: `golang:1.23-bookworm`)

1. Installs build dependencies: ripgrep, rsync, git, gcc.
2. Copies `agent/go.mod` and `agent/go.sum` first for layer caching.
3. Copies `agent/`, `scripts/`, `.machtiani/`, and `.git/` into `/build`.
4. If a `CHANGE_PATCH_B64` build-arg is present (treatment build only):
   - Decodes the base64 patch.
   - Removes submodule gitlinks (their `.git/modules/` entries are excluded
     from the build context) to avoid broken path resolution.
   - Reverts patched files to `HEAD~1` so the patch applies cleanly.
   - Applies the patch with `git apply`.
   - Creates a git commit with message `"treatment patch"` so that embedded
     commit metadata (from `-buildvcs=auto`) matches the patched state.
5. Builds the required benchmark binaries directly into `/build/bin`:
   `machtiani`, `meta-orchestrator`, `file-discovery`, `snippet-discovery`, and
   `shell-agent`.

**Stage 2: runtime** (base: `debian:bookworm-slim`)

1. Installs runtime dependencies: ripgrep, rsync, bash, python3, git.
2. Copies built binaries from the builder: `/build/bin/` → `/usr/local/bin/`.
3. Copies `peripherals/machtiani-forge` → `/usr/local/bin/machtiani-forge`.
4. Copies `.machtiani/` from the builder → `/workspace/.machtiani/`.
5. Copies `agent/` from the builder → `/workspace/agent/`.
6. Sets environment variables:
   ```
   ENV MACHTIANI_CONFIG=/workspace/.machtiani/config.toml
   ENV REPO_ROOT=/workspace
   ```
7. Working directory: `/workspace`.  Entrypoint: `/bin/sh`.

### Key design decisions

- **`.git/` is NOT in the runtime image.**  Only the builder stage has it.
  This means runtime git operations (like those in `check_bin`) will not find
  a repository.  To work around this, `run-live.sh` uses the `MACHTIANI_BIN`
  env-var-skip pattern to bypass `check_bin` entirely when a binary path is
  provided from the environment.

- **`REPO_ROOT=/workspace`** is set in the runtime image so that
  `run-live.sh`'s `REPO_ROOT` fallback never activates — the image-provided
  value always takes precedence.

- **`MACHTIANI_CONFIG`** points to the config file copied into the image,
  ensuring the agent always resolves the correct provider and model
  configuration.

- **Treatment commit metadata**: because the builder applies the treatment
  patch and commits it as `"treatment patch"`, the Go build embeds commit
  metadata that reflects the patched state.  This is important for `check_bin`
  commit-mismatch detection and for `machtiani --version` output.

### .dockerignore

The `.dockerignore` at the repository root drastically reduces the Docker build
context from approximately **14 GB to roughly 3.4 MB** by excluding:

- `.machtiani/sessions/`, `tmp/`, `artifacts/`, `issues/` — large session and
  runtime state directories.
- `.git/modules/`, `lost-found/`, `worktrees/` — multi-GB submodule and
  worktree data (only the top-level `.git/` objects are retained for the
  builder stage).
- `third_party/` — vendored submodule contents.
- Plan markdown files at the repo root (`docker-ab-testing-action-plan*.md`,
  `*.md`).
- Stray test output logs (`stdout.log`, `stderr.log`).
