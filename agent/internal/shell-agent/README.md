# Shell Agent (Go)

A minimal implementation of the mini SWE shell agent written in Go 1.26.8+. It mirrors the reference Python agent by running a tight observe–think–act loop over shell commands while persisting a JSON trajectory for every run.

## Features

- Default agent loop with budget enforcement, templated prompts, and structured error handling
- Pluggable model/environment interfaces (LiteLLM-compatible model + local shell environment included)
- Text/template cascade for system/instance/feedback messaging
- JSON trajectory writer for post-run inspection

## Format-error recovery

Rejected responses receive the specific validation reason, the consecutive
rejection count, and the required command/answer tags. No command from a
rejected response executes. The built-in feedback template and fallback use
the same wording; custom feedback templates can still override it.

Three consecutive format errors stop the current attempt with
`FormatErrorLoop` and a non-nil loop error. The library permits one fresh
conversation for the same work request, under the original context/deadline.
Each attempt keeps the configured step limit. The redo resets conversation
history and format-error state, preserves the workspace, and receives bounded
observations of previously executed commands. It must inspect current state
before repeating actions.

The first failed trajectory is retained as `format-error-attempt-1.json`
beside the current `trajectory.json`. A persisted recovery marker prevents
interruption/resume from granting another redo. If recovery fails, the planner
receives a system-generated JSON failure with a status, code, attempt count,
diagnostic, and trajectory path. The work result is marked failed; it is never
an empty success or a fabricated confidence-scored final answer.

## Model-host failure recovery

Model-host generations retry transient transport/provider failures, rate limits,
and empty responses up to three total attempts within the original turn deadline.
Only the failed model query is repeated; earlier shell commands and conversation
history are retained. Authentication, quota, invalid-request, and protocol errors
do not retry. A streaming caller that has already received text does not replay
the generation. Retry events record the delay and provider explanation.

If a model-host request still fails, the shell-agent saves its trajectory and
returns a structured failed work result to the planner, including the provider
code, diagnostic, and model-call attempt count. Cancellation and the turn deadline
retain their existing interruption behavior. This recovery is separate from the
fresh-conversation redo for format errors.

## Getting Started

```bash
# install dependencies (Go modules)
GOCACHE=$(pwd)/.gocache go mod tidy

# build the entire workspace
GOCACHE=$(pwd)/.gocache go build ./...

# run the shell agent with a task prompt
go run ./cmd/shell-agent -- "Fix the failing unit tests"
```

Pass `--api-key provider:key` (repeatable) to override provider credentials for a single run without editing `.machtiani/config.toml`. Flag values take precedence over both the config file and environment variables.

Use `--context-length <n>` to enforce the total input-plus-output window for the session. The derived input budget trims older transcript context, collapses long observations, and annotates truncated messages with `[TRUNCATED: …]` markers before querying the model. If prompt caching is enabled, the truncation logic keeps the active cache anchor in place and re-anchors when pre-anchor content must be dropped so the cached prefix stays stable.

> **Note**: The `GOCACHE` override keeps build artifacts inside the repo when the default Go build cache is not writable. Feel free to drop it if your environment permits using the global cache.

## Configuration

Runtime behaviour is driven by the shared `.machtiani/config.toml` used across the Machtiani toolchain. The shell agent consumes prompt templates from the `[prompts.*]` hierarchy and behavioural settings from `[planner]`, `[shell-agent]`, `[model]`, and `[environment]`:

```toml
[prompts.planner]
system_template = """
You are the planning layer for the Machtiani shell agent.
Use the task description, prior observations, and machine state to choose the next single shell action.
Before each step, explicitly assess what the task is asking for, what facts are still missing, what evidence has already been gathered, and whether one more command is likely to materially improve the answer.
Respond with exactly one <command>...</command> block containing the Bash command that executes the next action, unless you are concluding.
The command must be a single line and runnable as-is.
Never use background execution (&).
Each command runs from the project root directory by default. If you must run in a different directory, chain it explicitly (for example: cd path/to/dir && <command>).
State any required context explicitly by encoding it in the command (paths, filters, flags) so nothing is left implicit.
Conclude as soon as the task is sufficiently answerable from the evidence already collected.
Completion criteria include: the explicit user asks have been addressed; the requested files, code paths, or facts have been found and can be explained; additional searching is unlikely to change the answer in a meaningful way; or the task cannot be completed but the limitations and findings can now be stated clearly.
Do not wait for forced finalization if the answer is already sufficient.
When you are ready to conclude, output exactly one <answer>...</answer> block and no <command> block. Put the entire final answer inside the <answer> tags.
Present the answer as a short list of substantive claims.
Prefix each substantive claim with a confidence label formatted exactly as "Confidence: <0-100>% - ".
Do not provide a single overall confidence score; instead, every material factual claim or inference in the answer must carry its own confidence score, lowered when evidence is indirect, incomplete, or uncertain.
Assume read-only intent unless the task clearly authorises a write, and keep writes minimal.
Do not include any commentary outside the <command> block unless you are concluding.
"""
instance_template = """
Task: {{.Task}}

Machine: {{.Machine}}
Available steps: {{.StepLimit}}

Before choosing the next step, decide which of these modes applies:
1. Conclude now if the task is already answerable from the evidence gathered so far.
2. Run one concrete command if that command is likely to materially reduce a specific uncertainty.
3. Conclude with partial findings if further commands are unlikely to add meaningful new evidence.

Return exactly one <command>...</command> block containing the Bash command that the worker can execute without additional interpretation, unless you are concluding.
The command must be a single line.
If you need to run in a subdirectory, chain it explicitly (cd path/to/dir && <command>) because each step starts in the project root.
If a safe single command is impossible, emit a <command> block with an echo/printf that explains the limitation instead of inventing extra steps.
Avoid exploratory commands with no clear hypothesis, repeating similar searches without new information, or continuing after the task has already been sufficiently answered.
When you intend to conclude, output exactly one <answer>...</answer> block and no <command> block. Put the entire final answer inside the <answer> tags, and present it as claim-by-claim findings where each substantive claim begins with "Confidence: <0-100>% - "; do not use a single overall confidence score.
"""

[prompts.shell-agent]
timeout_template = { file = "templates/shell-agent/timeout_template.tpl" }
format_error_template = { file = "templates/shell-agent/format_error_template.tpl" }
action_observation_template = { file = "templates/shell-agent/action_observation_template.txt" }

[planner]
step_limit = 5
cost_limit = 3.0

[model]
model_name = "anthropic/claude-haiku-4.5"
api_key = "" # leave empty to prefer OPENROUTER_API_KEY/OPENAI_API_KEY

[model.model_kwargs]
temperature = 0
base_url = "https://openrouter.ai/api/v1"

[environment]
type = "local"
timeout = 30

```

Templates receive variables merged from configuration, the active environment, the model state, and per-step extras (`Task`, `Action`, `Output`, etc.). Planner prompts provide defaults for the shell-agent loop—override or extend them under `[prompts.shell-agent]` only when you need behaviour that differs from the orchestrator. Missing variables raise template errors to preserve deterministic behaviour.

Shell commands start in the directory where `shell-agent` is launched. The
absolute path is available to templates as both `CWD` and `cwd`.

### Prompt Caching

Prompt caching is configured under `[models.<alias>]` (shared with the rest of the Machtiani toolchain). When `cache_key_name`, `cache_control`, and `cache_trigger_threshold` are set, the shell agent inserts a persistent `cache_anchor` message once the prompt grows large enough and rotates that anchor using `cache_reanchor_tokens` and `cache_reanchor_messages`. Context-derived trimming keeps the active anchor in the prompt and re-anchors if earlier content must be removed so cached prefixes remain stable.

### Command Response Format

Model responses must consist of exactly one `<command>...</command>` block containing the Bash command,
except when finalizing. When finalizing, output exactly one `<answer>...</answer>` block and no `<command>` block, put the entire final answer inside the answer tags, give each substantive claim its own "Confidence: <0-100>% - " prefix, and do not emit a command. The shell agent strips the answer tags before returning the submitted result upstream.
Place a single command line inside the tags. The parser locates the first `<command>` opening tag and scans for the matching `</command>` closing tag, respecting shell quoting (single quotes, double quotes, backslash escapes) to avoid false matches inside quoted strings.
The shell agent rejects commands that introduce newlines,
chaining operators (`&&`, `||`, `;`), pipes (`|`), or background execution unless the natural
language instruction explicitly states that the construct is required. Command substitution (both `$(...)` and backtick forms) is now permitted.
When no safe single-command solution exists, respond with a command that prints an explanation, for example:

<command>
printf "This request needs multiple commands; please rephrase.\n"
</command>

The agent treats everything inside the `<command>` tags as literal Bash source and executes it directly.

### Targeting OpenRouter (or any OpenAI-compatible API)

Update the `model` section to point at your provider and credentials. The agent falls back to the `OPENROUTER_API_KEY` (preferred) or `OPENAI_API_KEY` environment variables when `api_key` is omitted.

```toml
[model]
model_name = "openrouter/anthropic/claude-3.5-sonnet"
api_key = ""            # leave blank to read from OPENROUTER_API_KEY

[model.model_kwargs]
temperature = 0.2
base_url = "https://openrouter.ai/api/v1"  # provider endpoint
```

To switch back to the OpenAI API, supply the appropriate `model_name` (e.g. `gpt-4o-mini`), set `base_url` to `https://api.openai.com/v1`, and either place the key directly in `api_key` or export an `OPENAI_API_KEY`.

## Project Layout

```
cmd/                # CLI entry points (shell-agent implemented; others stubbed)
internal/agents/    # Default agent implementation
internal/environments/
internal/models/
internal/run/       # Template + trajectory helpers
pkg/minisweagent/   # Public interfaces and shared types
```

## Development

- `GOCACHE=$(pwd)/.gocache go test ./...` – run the full test suite, including integration and e2e shells
- `go test ./...` – run unit tests (add alongside new packages)
- `gofmt -w` – format changes before committing
- For new model/environment integrations, implement the `Model` or `Environment` interface from `pkg/minisweagent/minitrain.go` and wire them through the agent constructors.

## Trajectories

Every `shell-agent` run produces a timestamped `trajectory-*.json` file containing the full message history, exit status, and result. The CLI prints the exit status and full result followed by a condensed, plain-text transcript by default; pass `--output-format markdown` for the formatted transcript or `--output-format json` to stream the raw trajectory payload to stdout. The JSON files remain available for deep debugging (`cmd/inspector` to be implemented).

## Roadmap

- Implement remaining CLIs (`github-runner`, `inspector`, `hello-world`)
- Add deterministic test harnesses mirroring the Python reference suite
- Expand environment support (Docker, Singularity) and add global cost tracking utilities
