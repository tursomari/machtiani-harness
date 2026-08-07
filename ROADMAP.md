# Release-2 goals overview

Release 2 focuses first on the TUI focus and verbosity controls requested by David. It also groups the next-release themes from `notes.md` into a coherent program of UX simplification, easier distribution, public-repository readiness, shell-agent polish, new operating modes, and commit-aware memory management.

- Add explicit TUI controls that reduce on-screen noise without reducing the completeness of saved transcripts or trajectories.
- Simplify the primary command, resume, interruption, and first-run experiences.
- Make installation and updates reliable for end users through a single-binary default and safer release mechanics.
- Prepare the repository, documentation, and public-facing material for a secure public release.
- Improve shell-agent ergonomics and add pi, codex, and memory workflows.

# The two requested TUI flags (top priority)

Release 2 should add two named boolean flags:

- `--focused`: show only the user prompt and the final conclusion/final answer. Hide planner Asks, streamed work responses, completed work-response lines, and shell-agent `Step N of M` and `$ command` blocks.
- `--no-shell-steps`: show the user prompt, planner Asks, and work responses, while hiding only shell-agent `Step N of M` and `$ command` blocks.

The recommended convention is to ship both explicit flags. This design:

- Matches the established descriptive-boolean style of `--no-banner`, `--no-cursor`, and `--no-trajectory`.
- Is fully backward compatible: defaults remain unchanged, shell steps remain visible by default, and `-v` keeps its current boolean meaning.
- Reflects that the modes are not purely linear. Focused and intermediate presentation hide different components, so explicit names are more self-documenting than a numeric level.
- Composes naturally: `--focused` implies hidden shell steps, while `--no-shell-steps` alone provides the middle mode.

Two alternatives are possible but are not recommended for release 2. A single `--tui <full|standard|focused>` option centralizes selection but makes the intermediate behavior less discoverable and complicates composition with existing boolean UI flags. Recasting `-q` and `-v` as levels would require re-leveling shell-step output behind `-v` and changing the meaning of today's `--verbose`. The two named flags preserve current semantics and state intent directly.

Implementation should follow these rules:

- Add the flags through `configureSessionFlags()` and carry their values in session/UI configuration. Optional config-file parity can use boolean keys such as `focused` and `no_shell_steps`; a normalized `tui_level` is another option if one stored setting is preferred.
- Gate presentation in the `internal/ui` formatter handlers—`handlePromptStarted`, `handleChunkReceived`, `handlePromptCompleted`, and `handleActionExecuted`—rather than suppressing event emission. Saved transcripts and trajectories must remain complete when terminal output is filtered.
- Keep `--verbose` orthogonal. Diagnostic verbosity and the amount of task-progress UI shown are separate concerns.
- Always retain `SessionStarted`, `SessionConclusion`, and `SessionEnded`, along with error `NotificationEvents`. The exact focused-mode rendering of the start event remains an open decision because the prompt currently lives inside the banner.
- Add unit coverage in `internal/ui/formatter_test.go` for default/full output, `--no-shell-steps`, and `--focused`, including flag composition and error visibility.

# Other release-2 milestones from notes.md (grouped)

## UX simplification

- Introduce a dedicated `resume` command to replace `--session-id`, and allow the primary prompt flow without the `run` prefix.
- Implement two-stage Ctrl-C handling: the first interrupt starts graceful shutdown and prevents trajectory-corrupting writes; the second forces an immediate interrupt.
- Add smoke tests for the README Quick Start and Basic Usage command snippets.
- Warn clearly when commands run in an uninitialized project, including the canonical initialization command.

## Install and distribution

- Make the default user install a single binary; third-party submodules must not be mandatory.
- Pin forgecode for reproducible behavior.
- Stabilize the `mct-agent --help "<prompt>"` entry mode as a preconfigured path that requires no sync. It should try configured models in order with reasoning, then fall back to the default model without reasoning and without caching; its mode guidance should point to common issues, runbooks, documentation, and source code as a last debugging resort.
- Add `update --force`: reject updates when local `HEAD` is not on `origin` unless the user explicitly overrides the safeguard.
- Sign commits under the actual author name.
- Keep Guix support open as an unresolved distribution target.

## Repository readiness

- Remove the temporary always-on command-supervisor diagnostic at
  `/tmp/mct-command-supervisor.jsonl` before any public/open-source release.
  Remove its runbook instructions and smoke assertions at the same time, and
  verify that no equivalent unflagged supervisor logging remains.
- Put the new history on top of the old `mct` repository, choosing and documenting a force-push or squash strategy and checking the plan against GitHub security behavior.
- Complete a security review before any public push: remove remote branches containing credentials or sensitive data, confirm the default branch is clean, inspect dependencies for typosquatting or unsafe packages, and remove unnecessary dependencies.
- Replace generated or provisional copy with a handwritten README.
- Produce a demo video and add it to both the README and the website.

## Shell-agent polish

- Show the remaining character/token budget in the next-command prompt, using a conservative character estimate, and trigger the final answer when the limit is reached.
- Fix the stale default-model display.
- Deprecate `--tag` and derive an appropriate command tag dynamically.
- Support `--model <alias>` as the model picker rather than requiring provider/model syntax.
- Add a setting that makes forgecode emit only its answer, without thinking output.

## Modes

- Add pi mode.
- Add codex mode.

## Memory subsystem

- Add `mct memory list` and `mct memory prune`.
- Add `mct memory update --codebase` and `mct memory update --workflow`.
- Record the relevant commit level on sessions so memory can be derived and selected against repository history.
- Bind internal memory documentation to commit level and rename it away from “readme”: use `artifacts/internal/codebase/memory.md` for codebase knowledge and `artifacts/internal/workflow/memory.md` for workflow patterns mined from sessions at relevant commits.

## Deferred

- Continue SGLang exploration outside the release-2 critical path.
- Defer the Neutral Resume Path work associated with commit `65360eb`, which applies injection-rejection framing to all resumes. Ship it when resume quality degrades or the Device Client requires clean resume semantics.

# Open decisions

1. Focused mode: show only the bare prompt, or the full banner? Today the prompt lives inside the banner and `--no-banner` hides it entirely—focused mode needs to decide whether it overrides/implies banner behavior.
2. Do the flags apply only to `run`, or also to `shell-agent`, `sync`, and resume flows?
3. In the middle mode, is the work response the full streamed answer, or keep today's last-N-lines truncation?
4. In shell-agent routing, the work response is literally the shell-agent's submitted answer—should intermediate mode show that (it is a response), while only Step/$-command blocks are hidden?
5. Should the live footer (timer, token usage, activity cursor) be kept in focused mode?
6. Do error lines and resume notices stay visible in focused mode?
7. Naming: `--focused`/`--no-shell-steps` as recommended, or `--tui full|standard|focused`, or `-q`/`-v` levels (accepting re-leveling)?
8. CLI-only, or also config-file settings (global/project) like `verbose`?
