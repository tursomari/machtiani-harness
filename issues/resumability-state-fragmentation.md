Title: Resumability State Fragmentation: Duplicated State Across Three Files

Problem: The shell-agent resumability feature persists resume state across three separate files:
- session-state.json (resume flag, trajectory path, turns completed, goal)
- conversation.json (same two flags duplicated as metadata on work_request messages)
- shell-agent/<n>/state.json (step counter, exit status, last output nearly identical to trajectory.json resume_state)

The same fields (shell_agent_resumable, shell_agent_trajectory_path) are stored in both session-state.json and conversation.json, creating a drift risk. The shell-agent state.json is almost entirely redundant with the resume_state block already embedded in trajectory.json. This fragmentation makes the architecture harder to reason about, increases the surface area for bugs, and forces developers to understand three overlapping state models.

Background: Shell-agent resumability was added incrementally on top of an existing session persistence layer. The session runner already used session-state.json for general checkpointing (TurnsCompleted, Goal, OriginalPrompt). When resumability was added, the path of least resistance was to add ShellAgentResumable and ShellAgentTrajectoryPath fields to the existing struct. Separately, conversation.json needed the work request question text for the planner short-circuit, so the same metadata was duplicated there. The shell-agent itself, being a standalone library with its own CLI mode, already had its own state.json and trajectory.json, so those persisted independently. No consolidation pass was ever done.

The session-state.json fields are all derivable from conversation.json: TurnsCompleted equals count of distinct turns in messages, Goal equals original_goal in conversation, OriginalPrompt equals first user message. The shell-agent state.json fields are almost entirely covered by trajectory.json resume_state block (which already has step_counter, commands_executed, exit_status). Only consecutive_format_errors and last_non_empty_output are extra in state.json, and those could be added to trajectory.json resume_state.

Solution: Collapse session-state.json into conversation.json by making conversation.json the single authoritative source for all session state (resume flag, trajectory path, turns completed, goal, original prompt, work request question). Eliminate shell-agent/state.json by merging its fields into trajectory.json resume_state block. This reduces the persistent state footprint from 4 files to 2: conversation.json (state plus transcript) and shell-agent/<n>/trajectory.json (execution history plus resume checkpoint).

The migration plan:
1. Add resume_state.consecutive_format_errors and resume_state.last_non_empty_output to trajectory.json schema (write both when resumable is true at checkpoint time).
2. Remove all reads of shell-agent/state.json from the shell-agent library; use trajectory.json resume_state exclusively.
3. Remove all writes of session-state.json from the session runner; use conversation.json exclusively. Store ShellAgentResumable, ShellAgentTrajectoryPath, TurnsCompleted, Goal, and OriginalPrompt as top-level fields in conversation.json (alongside the existing session_id, original_goal, messages, etc.).
4. Update ExtractResumableWorkRequestQuestion to read from conversation.json top-level fields rather than scanning metadata.
5. Delete session-state.json and state.json file I/O.

This is a pure refactoring issue no functional behavior changes. It simplifies the architecture, eliminates drift-prone duplication, and reduces the total persistent state surface from 4 files to 2. The existing tests for resumability should continue to pass after migration.

Tag: enhancement/architecture
