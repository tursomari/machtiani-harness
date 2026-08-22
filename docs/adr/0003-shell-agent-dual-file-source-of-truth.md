# ADR: Dual-File Source of Truth for Shell-Agent Activity

## Status

Accepted

## Context

`machtiani run --attach --resume <id>` is a read-only, lock-free observer. It renders the persisted `conversation.json` snapshot and polls that file for new conversation messages while the owning process continues to run. It currently renders no shell steps. That makes the default attach display visually indistinguishable from `run --no-shell-steps`, even though the attached shell-agent may be announcing and executing commands.

Shell-agent state and shell-action display currently travel through two mechanisms with different purposes:

- Each planner turn has a checkpoint directory containing `trajectory.json`. The file contains the shell-agent messages, `resume_state`, exit status, and result. `checkpointStep` writes it after a completed step through `run.SaveTrajectoryToPath`; termination and resume paths also save it. The implementation uses a whole-file `os.WriteFile` rewrite. This is suitable as a resumable snapshot, but not as a live-tail protocol: a reader can observe an incomplete replacement, completed steps appear only at checkpoint time, and each save replaces bytes instead of extending a stable stream.
- Immediately before command execution, `emitShellAction` announces the description, resolved command, and step counters on the shell-agent stdout bridge. `agent/internal/core/prompt/shell_action_intercept.go` intercepts the announcement and emits a `shell-agent.action` event only through the optional unified trajectory writer. `startShellActionStreamer` tails that writer in the normal TUI, but `agent/internal/session/runner.go` starts the streamer only when the writer is non-nil.

Consequently, `--no-trajectory` does more than disable telemetry. It makes the unified writer nil, prevents `startShellActionStreamer` from starting, and removes live shell-action blocks from the TUI. User-visible action reporting is therefore accidentally controlled by a telemetry flag.

The unified writer's default `trajectory/agent.jsonl` is append-only, but it is optional, session-wide telemetry containing many kinds of events. It is not an appropriate display authority for an always-available, per-turn attach view.

The existing `agent/internal/trajectory/listener/subscriber.go` establishes a safe tailing pattern for newline-framed append logs: retain a partial trailing record until its newline arrives, advance only across complete records, and reset the offset and pending bytes if truncation is detected. A dedicated shell-action journal can reuse that behavior without treating a rewritten JSON snapshot as a stream.

`conversation.json` already carries the discovery data attach needs. Work-request metadata includes `shell_agent_session_id`; the conversation's resumability metadata includes `shell_agent_trajectory_path` and `command_tag`. Together these fields identify the active turn and its shell-agent directory without attach guessing from the current working directory.

## Decision

Use two per-turn files as the authoritative persisted representation of shell-agent activity, with each file authoritative for a different concern.

### `trajectory.json`: resumable state

Keep `trajectory.json` as the checkpointed source of truth for resumption. It continues to hold the message history, `resume_state`, exit status, result, and related checkpoint metadata. Existing resume loading, validation, tag restoration, and replay behavior continue to read this snapshot.

The whole-file save should be hardened to use an atomic same-directory replacement if that can be done without breaking current resume behavior: marshal first, write and sync a temporary file, close it, then rename it over `trajectory.json`. This is a design consideration, not a claim that atomic trajectory writes are already implemented and not a prerequisite for introducing the action journal. The checkpoint remains a snapshot rather than a tail-able event stream.

### `actions.jsonl`: command-announcement journal

Add `actions.jsonl` beside `trajectory.json` in every shell-agent turn directory. It is an always-on, append-only journal of command-start announcements.

The existing announcement occurs in `agent/internal/shell-agent/internal/agents/loop.go` after command parsing and validation and before `executeCommand`. The stdout interceptor persists each announcement as it receives it. A record contains:

- schema version, shell-agent session identifier, planner turn, and a monotonically increasing per-turn sequence;
- the natural-language description and resolved command; and
- the display step, step limit, remaining steps, and commands-executed counters.

Open the journal with `O_APPEND`, serialize one record, and append that record plus its newline as one framed write under a single-writer discipline. A partial trailing record is not visible to consumers until the terminating newline is present. On writer recovery, discard an incomplete trailing record before resuming the sequence. The journal is not rewritten during an active turn.

Journal creation and writes are independent of `--no-trajectory` and independent of whether the optional unified trajectory writer exists. The journal belongs to operational session state, not opt-in telemetry. Append failures are reported and do not abort execution. Under this two-file decision, the journal does not replace resumable state and is not a transactional execution ledger; making persistence failure prevent execution requires the durability policy described in the single-log alternative below.

The read-only attach path reads complete existing records to render the shell-step snapshot, then polls for newly completed lines during its existing conversation poll cycle. It retains only complete newline-framed records and tracks the last rendered sequence per turn. Shell steps are visible by default and may still be suppressed by the explicit `--no-shell-steps` display option.

The normal TUI may migrate to the same per-turn journal in later work. That migration would remove the current coupling in which `--no-trajectory` suppresses live action blocks. It is not part of this decision's initial attach implementation.

### Discovery and telemetry

Attach locates the per-turn directory from persisted `conversation.json` fields (`shell_agent_session_id` and `shell_agent_trajectory_path`) and canonical artifact path helpers. It does not derive storage from the current working directory. This keeps attach read-only and allows it to observe an active session without acquiring the session lock.

Keep the session-wide `trajectory/agent.jsonl` writer as optional, derived telemetry. When enabled, the action announcement may also be mirrored there as `shell-agent.action` for diagnostics and existing telemetry consumers. The mirror may be absent or delayed and is never the source used to render attach.

## Alternatives

### One append-only per-turn JSONL as the only source of truth

A single log containing events, small state snapshots, and paired `command.started`/`command.finished` records would give every consumer one reducer and let attach tail one file. A start without a matching finish would elegantly encode interruption.

This is the preferred direction to research, but it is not the implementation selected here. Today `checkpointStep` treats `trajectory.json` persistence as best-effort: it logs save failures and continues. Once one JSONL file becomes resumable and display state, its writes become load-bearing. Failure to persist `command.started` must prevent execution, and the design must define append atomicity, fsync boundaries, crash recovery, corrupt or partial-record handling, and the policy for an unmatched start. It must also fold `loadTrajectoryForResume`, resumable validation and tag restoration, `replayShellAgentActions`, atomic `conversation.json` persistence, and attach live tailing onto one reducer.

That larger redesign is non-blocking future work tracked by PM issue `4732923b9e67c9285661ed82aa412d68b5567ed9` (`4732923`), “Explore single-JSONL shell-agent source of truth; drop best-effort trajectory writes.” The two-file design is the accepted path until that work defines and proves the stronger durability contract.

### Live-tail `trajectory.json`

Rejected. `trajectory.json` is a whole-file snapshot rewritten after completed steps. It has no stable append offset, can expose a transient partial JSON document without atomic replacement, and cannot report an announced or in-flight command before the next checkpoint. Polling and reparsing it would still not provide correct command-start timing.

### Use only `trajectory/agent.jsonl`

Rejected. The unified stream is explicitly optional telemetry and is disabled by `--no-trajectory`. Making attach depend on it would preserve the current bug in which a telemetry preference controls user-visible shell actions. It is also session-wide rather than the canonical per-turn state location.

### Derive commands from `conversation.json` or process output

Rejected. `conversation.json` records work requests and results, not each command announcement, so it cannot show live per-command progress. Attaching directly to process stdout or adding an IPC channel would couple a read-only observer to the lifetime and synchronization of the owning process, while a per-turn append journal supports snapshot plus tail without a session lock.

## Consequences

**Positive:**

- Attach shows shell steps by default from command announcements made before execution.
- The action journal is available even when unified trajectory telemetry is disabled.
- Resume behavior remains grounded in the existing `trajectory.json` schema and loaders, limiting the risk of the attach change.
- `actions.jsonl` has stable append offsets and newline framing, so readers can ignore an incomplete trailing record safely.
- `conversation.json` and canonical artifact helpers supply discovery information, keeping attach lock-free.
- The same journal can later become the normal TUI's action source, while the global unified stream remains useful as optional derived telemetry.

**Negative:**

- Two files describe different aspects of one shell-agent turn and must share stable turn/session identifiers and ordering conventions.
- The action journal records command starts, not completed results. Consumers needing execution outcomes must use the checkpointed messages or wait for the future paired-event design.
- The implementation adds append/error handling, schema/versioning, retention, and tests for partial lines, duplicate-free snapshot-to-tail handoff, and interrupted runs.
- A journal append can fail while resumable checkpointing remains available. That failure is visible, but this ADR deliberately does not introduce the execution-blocking and fsync contract required of a single durable ledger.
- Atomic replacement of `trajectory.json` remains separate hardening work and must be validated against resume and crash behavior.

**Neutral:**

- `trajectory.json` and `actions.jsonl` are both sources of truth, but for non-overlapping responsibilities: resumable state and command announcements respectively.
- `trajectory/agent.jsonl` may duplicate action records when telemetry is enabled. This duplication is intentional; the global file is a derived mirror and may not be used as attach's display authority.
- A future single-log migration will require reducers and compatibility handling for existing two-file turn directories.
