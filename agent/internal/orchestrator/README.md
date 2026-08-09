# Meta-Orchestrator

## Overview

The meta-orchestrator is a wrapper binary around `mct-agent` that prevents premature finalization. When `mct-agent` runs in autonomous mode, it has a tendency to halt and ask for permission (e.g., "Want me to implement it?") instead of completing the task. The meta-orchestrator intercepts this by classifying the `agent-final-answer.md` produced by each run and re-invoking `mct-agent` with an authoritative continuation instruction when it detects a permission ask instead of a real solution.

## Usage

```sh
meta-orchestrator --mode code --model gpt-4 -f instruction.md
```

### Flags

| Flag | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `--mode` | string | yes | | Operating mode (e.g., `code`, `code-forge-auto`) |
| `--model` | string | no | | Model alias for the primary LLM |
| `--shell-agent-model` | string | no | | Model alias for shell-agent subprocesses |
| `--instruction`, `-f` | string | yes | | Path to the instruction file |
| `--tag` | string | no | `now` | Tag suffix for answer and command tags |
| `--persist-tmp-data` | bool | no | `false` | Keep temporary data after execution |

## How It Works

The meta-orchestrator runs a loop with the following steps:

1. **Invoke** `mct-agent run` with the given mode, model, and instruction file. On the first invocation, a fresh session ID is generated and passed to `mct-agent` via the `MACHTIANI_SESSION_ID` environment variable. Internal recovery still uses the deprecated `--session-id` flag during the compatibility window; user-facing flows use `mct-agent resume <session-id>`.

2. **Classify** the resulting `agent-final-answer.md` using a separate LLM call with a strict classification prompt. The classifier assigns one of three labels:
   - `SOLUTION` — a genuinely completed solution with concrete code changes or implemented work.
   - `PERMISSION_ASK` — a permission ask, status report, design discussion, proposal, or anything short of actual implementation.
   - `HARD_BLOCKER` — an unrecoverable condition such as missing API credits or a critical environment failure.

3. **Re-invoke** if the classification is `PERMISSION_ASK`. During the deprecation window, the meta-orchestrator internally calls `mct-agent run --session-id <id> -t "<your follow-up prompt>"` to start a fresh turn with an authoritative instruction to resume work and implement the solution without asking for permission. This internal transport is intentionally retained until the dedicated resume command is battle-tested.

4. **Repeat** steps 2–3 until the classifier returns `SOLUTION` (the task is complete) or `HARD_BLOCKER` (further continuation is impossible).

## Architecture

| Component | Location |
|-----------|----------|
| Binary entrypoint | `agent/cmd/meta-orchestrator/` |
| Library (loop and classifier) | `agent/internal/orchestrator/` |

The binary at `agent/cmd/meta-orchestrator/main.go` parses CLI flags, generates a session ID, and delegates to `orchestrator.RunLoop`. The library at `agent/internal/orchestrator/` contains the run loop logic (`runner.go`) and the LLM-based final-answer classifier (`classifier.go`).
