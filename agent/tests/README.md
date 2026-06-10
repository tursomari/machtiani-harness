# agent/tests — Live Test Suite and A/B Testing Pipeline

This directory contains the `run-live.sh` test suite, the `ab-live.sh` result
parser, and supporting files for the Docker-based A/B testing workflow driven
by `scripts/ab-dev.sh`.

---

## A. `run-live.sh` — The Test Runner

`run-live.sh` executes the `mct-agent` binary against a series of named test
cases and emits PASS/FAIL lines on stdout. It is the primary integration-test
harness for the agent.

### What it does

1. **Preflight**: verifies the `mct-agent` binary (checks PATH, embedded commit
   metadata, dirty flag, and binary staleness) unless `MCT_AGENT_BIN` is set
   from the environment.
2. **Config generation**: creates a temporary `.machtiani/config.toml` from the
   repository template, injecting model aliases and provider credentials.
3. **Test execution**: runs each test case against the agent, collecting stdout,
   stderr, session artifacts, transcripts, and trajectories.
4. **Validation**: asserts turn counts, keyword presence in output files,
   shell-agent marker cleanup, final artifacts, and error-path behaviors.

### Test cases

Each test case has a **case identifier** (`case_id`) and, for named dispatch,
a **test name**. Case identifiers use kebab-case (e.g., `issue-a-1turn`,
`models-per-component`, `code-no-forge`). Test names use snake_case (e.g.,
`test_enforce_early_commands`, `test_code_no_forge`).

**Named test dispatch** (when run-live.sh is invoked with arguments):

| Test name | Function | `case_id` |
|---|---|---|
| `test_local_tmp_root_unset_live` | `run_local_tmp_root_unset_live_case` | `local-tmp-root-unset-live` |
| `test_code_no_forge` | `test_code_no_forge` | `code-no-forge` |
| `test_code_forge_initial` | `test_code_forge_initial` | `code-forge-initial` |
| `test_code_forge_resume_with_mode` | `test_code_forge_resume_with_mode` | `code-forge-resume` |
| `test_code_forge_resume_without_mode` | `test_code_forge_resume_without_mode` | `code-forge-resume-wo-mode` |
| `test_code_resume_without_mode_no_forge` | `test_code_resume_without_mode_no_forge` | `code-resume-wo-mode-no-forge` |
| `test_enforce_early_commands` | `run_enforce_early_commands_case` | `enforce-early-commands` |

**Default suite** (when run-live.sh is invoked with no arguments):

In dry-run mode (`LIVE_MODE` false) the suite runs stub-server-based tests plus
model-model, turn-count, error-path, and session-resume cases. In live mode
(`LIVE_MODE` true), additional cases requiring real LLM endpoints are added,
including per-component model labelling, shell-agent subcommand, file-discovery
routing, and mode-switch live cases.

### Environment variables

| Variable | Purpose |
|---|---|
| `MCT_AGENT_BIN` | Override the `mct-agent` binary path; skips `check_bin` preflight |
| `TEST_API_URL` / `TEST_BASE_URL` | LLM provider base URL for live-mode tests |
| `TEST_PROVIDER` / `TEST_API_KEY` | LLM provider API key for live-mode tests |
| `TEST_MODEL` | Model identifier for live-mode tests |
| `TEST_ORCH_MODEL` | Orchestrator model override (defaults to `TEST_MODEL`) |
| `TEST_FILE_DISCOVERY_MODEL` | File-discovery model override |
| `TEST_SHELL_AGENT_MODEL` | Shell-agent subcommand test model |
| `REPO_ROOT` | Repository root; defaults to `$(cd "$SCRIPT_DIR/../.." && pwd)` |
| `PYTHON_BIN` | Python interpreter (defaults to `python3`) |
| `KEEP_TEST_CONFIG` | If `true`, do not delete temporary config directories |
| `CHECK_SHELL_AGENT_MARKERS` | Enable/disable shell-agent marker cleanup checks |
| `MACHTIANI_SHELL_AGENT_ENFORCE_EARLY_COMMANDS` | Feature flag for `enforce-early-commands` case |

All `TEST_*` environment variables are forwarded by `scripts/ab-dev.sh` into
Docker containers automatically.

### How to add a new test case

1. **Define a function** following the pattern `test_<name>()` or
   `run_<name>_case()`. For happy-path tests, use the `run_happy_case` helper:

   ```bash
   run_happy_case "my-case-id" 2 \
     "The natural-language prompt text." \
     "regex|pattern" \
     1 \
     "${DEFAULT_MODEL_ARGS[@]}"
   ```

   For tests requiring the LLM stub server, use `start_llm_stub_server` /
   `generate_stub_config` / `stop_llm_stub_server` and invoke `mct-agent`
   directly with `MACHTIANI_CONFIG` set.

2. **Register in the `TESTS` array** (around line 3471) for named dispatch:

   ```bash
   declare -A TESTS=(
     ...
     ["test_my_case"]="test_my_case"
   )
   ```

3. **Add to the default suite** (around line 3506) if the test should run when
   no arguments are given. Condition on `LIVE_MODE` as appropriate.

### Output format

Results are printed to stderr. The following line patterns are used:

| Pattern | Meaning |
|---|---|
| `Passed: <case_id> (<N> turns)` | Test passed with N turns |
| `Passed: <case_id>` | Test passed (no turn detail) |
| `PASS: run_shell_agent_subcommand_live_case` | Shell-agent subcommand test passed |
| `PASSED: shell-command-trajectory-live case ...` | Trajectory test passed |
| `Failed (rc=<N>): <case_id>` | Test failed with a non-zero exit code |
| `Failed <reason> for <case_id>` | Test failed with a specific reason |
| `FAIL: mct-agent shell-agent ...` | Shell-agent subcommand test failed |
| `FAILED (non-fatal): <case_id>` | Test failed but does not halt the suite |
| `ERROR: <message>` | Preflight or assertion error (exits the script) |

---

## B. `ab-live.sh` — PASS/FAIL Parser

`ab-live.sh` wraps `run-live.sh` output and parses it into a structured TSV
result table. It is invoked by `scripts/ab-dev.sh` when `--parse-results` is
given.

### What it does

1. Reads `stdout.log` (and optionally `stderr.log`) from a container output
   directory produced by `ab-dev.sh`.
2. Parses each line against a set of regex rules to classify test results as
   PASS, FAIL, or SKIP.
3. Emits a TSV table with columns `case_id`, `status`, and `detail` to stdout
   (or to a file via `--tsv-out`).

### Parser rules

| Regex pattern | Extracted `case_id` | Status |
|---|---|---|
| `Passed: <id>` | `<id>` | PASS |
| `PASS: run_shell_agent_subcommand_live_case` | `run_shell_agent_subcommand_live_case` | PASS |
| `PASSED: shell-command-trajectory-live case` | `shell-command-trajectory-live` | PASS |
| `Failed ... <id>` (trailing word) | `<id>` | FAIL |
| `FAIL: mct-agent shell-agent ...` | `shell-agent-subcommand` | FAIL |
| `FAILED (non-fatal): <id>` | `<id>` | FAIL |
| `FATAL:` | `fatal-preflight` | SKIP |

When a `FATAL:` line is seen, the parser records `fatal-preflight` as SKIP.
If no results were parsed and a FATAL was seen, the empty case set is promoted
to a single SKIP entry. Duplicate case IDs keep the first status unless a
later line upgrades FAIL to PASS.

### Usage

```
# Emit TSV to stdout
agent/tests/ab-live.sh <output-dir>

# Write TSV to a file
agent/tests/ab-live.sh --tsv-out <path> <output-dir>
```

Where `<output-dir>` is a directory containing `stdout.log` (and optionally
`stderr.log`) produced by `run-live.sh` output captured via `ab-dev.sh`.

### TSV output

```
case_id status  detail
enforce-early-commands  PASS    Passed: enforce-early-commands (flag plumbed ...
models-per-component    FAIL    Missing keywords: models-per-component
```

The script always exits 0; it reports what it can parse without failing the
caller.

---

## C. `scripts/ab-dev.sh` — A/B Testing Workflow

`ab-dev.sh` builds two Docker images (control and treatment) from
`scripts/Dockerfile.build`, runs the same command in each container, and diffs
the output. It is the primary tool for verifying that a change does not
introduce regressions.

### What it does

1. **Control image** (version A): built from the current working tree with no
   patch applied.
2. **Treatment image** (version B): built from the current working tree with a
   patch applied (from a git diff or a patch file).
3. **Run**: the same command is executed in both containers; stdout/stderr are
   captured to `/tmp/mct-ab-output/{control,treatment}/`.
4. **Compare**: either a raw `diff -u` or a per-test-case PASS/FAIL comparison
   table (via `--parse-results` + `ab-live.sh`).

### Flags

| Flag | Description |
|---|---|
| `--cmd <command>` | Command to run inside each container (default: verify built binaries) |
| `--no-run` | Build both images but skip container execution |
| `--parse-results` | Parse `run-live.sh` output via `ab-live.sh` and produce a per-case PASS/FAIL comparison table instead of raw diff |
| `--env KEY=VALUE` | Forward a specific environment variable into both containers (repeatable) |
| `--control-commit <ref>` | Check out the specified commit before building the control image; restore HEAD afterward |

**Positional argument**: `<patch-file|git-ref>` — if a file path, used directly
as the treatment patch; if a git ref, `git diff <ref>..HEAD` generates the
patch.

### Default behavior

All `TEST_*` environment variables from the host are automatically forwarded
into both containers unless `--env` is used explicitly.

### Example invocations

```bash
# Compare binary metadata for the last commit
scripts/ab-dev.sh --cmd "mct-agent --version" HEAD~1

# A/B test a specific test case against an older baseline
scripts/ab-dev.sh --control-commit a4ae3bf78 --parse-results \
  --cmd "agent/tests/run-live.sh test_enforce_early_commands" HEAD~6

# A/B test the full suite with a custom API endpoint
scripts/ab-dev.sh --env TEST_API_URL=https://api.example.com --parse-results \
  --cmd "agent/tests/run-live.sh" HEAD~6

# Build-only comparison (no container run)
scripts/ab-dev.sh --no-run HEAD~3
```

### Per-case comparison table

When `--parse-results` is used, the output includes a TSV table:

```
case_id                 control_status  treatment_status    delta
enforce-early-commands  PASS            PASS                SAME
models-per-component    PASS            FAIL                REGRESSION
new-test-case                           PASS                NEW
```

Possible delta values: `SAME`, `REGRESSION`, `FIX`, `NEW`, `MISSING`.
