# mct Release 2 Roadmap

## Overview

Release 2 is centered on giving users explicit control over what the mct TUI
shows. The headline feature is a pair of display flags, `--focused` and
`--no-shell-steps`, which reduce on-screen noise without sacrificing the
completeness of saved transcripts or trajectories. Around that, Release 2
groups the next-release themes from `notes.md` into a coherent program: UX
simplification, easier install and distribution, public-repository readiness,
shell-agent polish, new operating modes, and commit-aware memory management.

- Add explicit TUI controls that reduce screen noise without reducing what is
  recorded on disk.
- Simplify the primary command, resume, interruption, and first-run
  experiences.
- Make installation and updates reliable for end users through a
  single-binary default and safer release mechanics.
- Prepare the repository, documentation, and public-facing material for a
  secure public release.
- Improve shell-agent ergonomics and add pi, codex, and memory workflows.

## Milestone 0: TUI display flags (highest priority)

Release 2 adds two named boolean flags to the `run` and `resume` flows:

- `--focused`: keep the full session-start banner (including its prompt,
  quote, version, and context line) and the final conclusion/final answer.
  Apart from required error notifications, those are the only visible task
  content. Hidden: planner Ask prompts, their streamed/chunked work responses,
  completed work-response lines, shell-agent `Step N of M` and `$ command`
  blocks, resume notices, and the live footer (timer, token usage, activity
  cursor). Error lines and other `NotificationEvent`s must remain visible.
- `--no-shell-steps`: show the user prompt/banner, planner Asks, and work
  responses (including the shell-agent's submitted work-response text), while
  hiding only the shell-agent `Step N of M` and `$ command` blocks. The live
  footer is kept. Work responses retain the default mode's existing rendering
  and truncation behavior; this flag does not expand them into a full stream.

Scope for now: `run` and `resume` flows only. `shell-agent`, `sync`, and other
subcommands are unchanged. The flags are CLI-only for release 2; config-file
parity (e.g., boolean keys such as `focused` and `no_shell_steps`) is noted as
future work.

`--verbose` stays orthogonal: diagnostic verbosity and conclusion detail are
unaffected by the display flags.

### Design rationale for the naming convention

Two explicit named flags are preferred over a `-q`/`-v` level scheme or a
single `--tui <full|standard|focused>` knob:

- Matches the established descriptive-boolean style of `--no-banner`,
  `--no-cursor`, and `--no-trajectory`.
- Fully backward compatible: defaults are unchanged, shell steps stay visible
  by default, and `-v` keeps its current boolean meaning.
- The modes are not purely linear: focused and intermediate hide *different*
  components, so explicit names are more self-documenting than a numeric
  level.
- Composes naturally: `--focused` implies hidden shell steps;
  `--no-shell-steps` alone is the middle mode.

### Implementation notes

- Add the flags through `configureSessionFlags()` and thread them through
  session/UI configuration.
- Gate presentation in the `internal/ui` formatter handlers
  (`handlePromptStarted`, `handleChunkReceived`, `handlePromptCompleted`,
  `handleActionExecuted`) rather than suppressing event emission. Saved
  transcripts and trajectories must remain complete even when terminal output
  is filtered.
- Preserve and process `SessionStarted`, `SessionConclusion`, and
  `SessionEnded`, plus error `NotificationEvent`s; filtering must not suppress
  upstream events. In focused mode, `SessionEnded` still performs lifecycle
  cleanup and state finalization, but its optional footer and resume-notice
  presentation remains hidden.
- In `--no-shell-steps`, keep the streamed work-response text (including the
  shell-agent's submitted answer) visible using the existing rendering and
  truncation behavior; only the Step/`$ command` blocks are hidden.
- In `--focused`, hide resume notices in addition to everything hidden by
  `--no-shell-steps`, but keep error lines.
- Add unit coverage in `internal/ui/formatter_test.go` for default output,
  `--no-shell-steps`, and `--focused`, including flag composition and error
  visibility.

### Acceptance checks

- `mct-agent run -t "<prompt>"` is visually unchanged.
- `mct-agent run -t "<prompt>" --focused` shows only the full banner and the
  final conclusion; no asks, work responses, shell steps, footer, or resume
  notices; errors still appear.
- `mct-agent run -t "<prompt>" --no-shell-steps` shows banner, asks, and work
  responses with their existing rendering/truncation behavior; no `Step N of
  M`/`$ command` blocks; footer still appears.
- The same flags work in the `resume` flow.
- Run with `--verbose` in each mode: diagnostics and conclusion detail are
  unaffected.

## Milestone 1: UX simplification

- Introduce a dedicated `resume` command to replace `--session-id`, and allow
  the primary prompt flow without the `run` prefix.
- Implement two-stage Ctrl-C handling: the first interrupt starts graceful
  shutdown and prevents trajectory-corrupting writes; the second forces an
  immediate interrupt.
- Add smoke tests for the README Quick Start and Basic Usage command snippets.
- Warn clearly when commands run in an uninitialized project, including the
  canonical initialization command.

## Milestone 2: Install and distribution

- Make the default user install a single binary; third-party submodules must
  not be mandatory.
- Pin forgecode for reproducible behavior.
- Stabilize the `mct-agent --help "<prompt>"` entry mode as a preconfigured
  path that requires no sync. It should try configured models in order with
  reasoning, then fall back to the default model without reasoning and without
  caching; its mode guidance should point to common issues, runbooks,
  documentation, and source code as a last debugging resort.
- Add `update --force`: reject updates when local `HEAD` is not on `origin`
  unless the user explicitly overrides the safeguard.
- Sign commits under the actual author name.
- Keep Guix support open as an unresolved distribution target.

## Milestone 3: Repository readiness

- Remove the temporary always-on command-supervisor diagnostic at
  `/tmp/mct-command-supervisor.jsonl` before any public/open-source release.
  Remove its runbook instructions and smoke assertions at the same time, and
  verify that no equivalent unflagged supervisor logging remains.
- Put the new history on top of the old `mct` repository, choosing and
  documenting a force-push or squash strategy and checking the plan against
  GitHub security behavior.
- Complete a security review before any public push: remove remote branches
  containing credentials or sensitive data, confirm the default branch is
  clean, inspect dependencies for typosquatting or unsafe packages, and remove
  unnecessary dependencies.
- Replace generated or provisional copy with a handwritten README.
- Produce a demo video and add it to both the README and the website.

## Milestone 4: Shell-agent polish

- Show the remaining character/token budget in the next-command prompt, using
  a conservative character estimate, and trigger the final answer when the
  limit is reached.
- Fix the stale default-model display.
- Deprecate `--tag` and derive an appropriate command tag dynamically.
- Support `--model <alias>` as the model picker rather than requiring
  provider/model syntax.
- Add a setting that makes forgecode emit only its answer, without thinking
  output.

## Milestone 5: Modes

- Add pi mode.
- Add codex mode.

## Milestone 6: Memory subsystem

- Add `mct memory list` and `mct memory prune`.
- Add `mct memory update --codebase` and `mct memory update --workflow`.
- Record the relevant commit level on sessions so memory can be derived and
  selected against repository history.
- Bind internal memory documentation to commit level and rename it away from
  "readme": use `artifacts/internal/codebase/memory.md` for codebase knowledge
  and `artifacts/internal/workflow/memory.md` for workflow patterns mined from
  sessions at relevant commits.

## Deferred / Exploratory

- Continue SGLang exploration outside the release-2 critical path.
- Defer the Neutral Resume Path work associated with commit `65360eb`, which
  applies injection-rejection framing to all resumes. Ship it when resume
  quality degrades or the Device Client requires clean resume semantics.

## Decisions log

Recorded design decisions from David's feedback on the draft roadmap:

1. **Naming/flags**: use the two named boolean flags `--focused` and
   `--no-shell-steps`; do not use `-q`/`-v` levels or a `--tui <level>` knob.
2. **Banner in focused mode**: keep the full session-start banner and the
   final conclusion. Apart from required errors, these are the only visible
   task-content blocks.
3. **Scope**: the display flags apply to `run` and `resume` flows for now;
   other subcommands are out of scope.
4. **Live footer**: hide the live footer in `--focused` mode to keep it as
   clean as possible; keep it in `--no-shell-steps`.
5. **Errors**: error lines must remain visible even in focused mode.
6. **Work-response rendering**: `--no-shell-steps` preserves the current
   default rendering and truncation behavior; it does not expand responses to
   a new full-stream view.
7. **Event lifecycle**: display filtering never suppresses upstream events.
   Focused mode still processes `SessionEnded` for cleanup and finalization,
   while keeping its footer and resume-notice presentation hidden.

Best-judgment resolutions for the remaining open items:

- `--no-shell-steps` shows the shell-agent's submitted work-response text;
  only Step/`$ command` blocks are hidden.
- `--focused` also hides resume notices, while error lines remain visible.
- The flags are CLI-only for release 2; config-file parity is future work.
