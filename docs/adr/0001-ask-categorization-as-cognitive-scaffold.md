# ADR: Ask Categorization as Cognitive Scaffold

## Status

Accepted

## Context

The planner's ask system presents two modes — `no-shell` (repository understanding, no commands) and `shell` (requires command execution). A mixed-ask monitor can detect asks that conflate both and split them into `No-shell:`/`Shell:` prefixed lines. A preflight routing system (`PreflightShellRouting`) classifies asks as "content" or "shell" using heuristics and LLM calls.

Historically, these categories routed asks to specialized execution paths: `no-shell`/`content` to file-discovery (in-memory file indexing), `shell` to shell-agent (subprocess command execution). The system had three distinct execution backends.

Over time, testing showed shell-agent performed as well as or better than file-discovery on understanding-oriented asks. A well-phrased `no-shell` ask, when given to a capable general executor, produced better answers than a poorly-phrased command ask. The categorization improved the *question*, not the *answer mechanism*.

## Decision

Route all asks through shell-agent in `mct-agent run`. Retain ask categorization (`no-shell`/`shell`) as a reasoning discipline for the planner, not as a routing signal.

The enforcement is explicit in code:

1. **`applySingleAskRoutingPolicy()`** — For any single ask, forces `useShellAgent=true` regardless of preflight classification. The preflight heuristic and LLM call are computed but their "content" result is always overridden.

2. **Split-ask collapse** — When `splitAskLines()` detects a mixed ask with both `No-shell:` and `Shell:` prefixes, the code immediately resets `hasSplitAsk=false` and sets `collapsedLegacyBothAsk=true`. The split-ask dual-execution branch (which would route the no-shell portion to file-discovery) is dead code. The ask is merged back into a single question and routed through shell-agent.

3. **`discoveryrunner.Run()` is unreachable in `mct-agent run`** — The guard `if !isAnswerOnly && !opts.ShellAgent` in `promptsvc.Run()` always evaluates to false because `ShellAgent` is always `true` after routing policy application.

The only path where file-discovery executes is `mct-agent sync`, which calls `promptsvc.Run()` with `ShellAgent` unset (defaults to `false`) and no routing policy override.

## Historical Evidence

The current architecture emerged from repeated attempts to make literal routing work correctly. Each failure mode is documented in the commit history:

### Phase 1: Manual selection (Oct 2025)

`6ab6b8b` introduced `--shell-agent` as an opt-in alternative to file-discovery. File-discovery remained the default; the flag was too coarse for per-ask routing but established shell-agent as a viable execution path.

### Phase 2: Autonomous per-ask routing (Oct 2025–Mar 2026)

`95efe9c` introduced `PreflightShellRouting()` with LLM classification to route each ask to file-discovery ("content") or shell-agent ("shell"). This replaced the manual flag with per-ask autonomous routing.

**Problem: Mixed asks produced poor results.** Asks combining explanation with commands — e.g., "Explain the auth flow and run `git diff --stat`" or "Show the full file src/app.js and run `grep -n TODO -r .`" — produced bad outcomes when routed to either path alone. File-discovery couldn't run the command; shell-agent would skip the explanation. `c383995` (Feb 2026) introduced the mixed-ask monitor to split these into `No-shell:`/`Shell:` prefixed lines for dual execution.

**Problem: File-discovery gave incomplete answers.** `e3c9cd1` (Mar 2026) introduced `showFileHandled` routing: when file-discovery couldn't fully answer an ask (`showFileHandled=false`), the ask was re-routed to shell-agent for completion. The test `TestApplySingleAskRoutingPolicy_NonShowSingleAskForcesShell` captures this pattern — file-discovery tries first, fails, then shell-agent finishes. This was a stepping stone toward just sending asks to shell-agent directly.

**Problem: The dual-execution path was unreliable.** Running file-discovery for the no-shell portion and shell-agent for the shell portion of a split ask didn't work well enough. `0d226b7` (Apr 2026) collapsed split asks entirely, removing the `both` ask mode from the template and replacing it with: "If a request would previously have needed both repository understanding and shell work, choose shell and restate it as one combined ask." The telemetry annotation `"routing policy: legacy both ask -> shell agent"` marks this as a deliberate retirement of the split-ask path.

**Problem: The preflight LLM mis-routed explanation asks.** `0aa7bd3` and `065111d` (Apr 2026) added `shouldPreferContentRouting()` to bypass the LLM for common explanation patterns. Test cases reveal specific mis-routings:
- `"Summarize how shell-agent falls back to the orchestrator model when unspecified"` → sent to shell-agent instead of file-discovery
- `"Explain the planner ask monitor guardrail flow in the agent/ directory and when it retries"` → the word "directory" triggered shell routing, treating it as a directory listing request

These fixes were attempts to make literal routing work correctly. But by the time they were committed, `applySingleAskRoutingPolicy` was already overriding all single asks to shell-agent — so the fixes only affected telemetry annotations, not actual execution.

### Phase 3: Shell-agent as default execution (Apr 2026 onward)

The collapse of split asks and forcing of single asks to shell-agent made shell-agent the exclusive execution path for `mct-agent run`. The cognitive scaffold (ask categorization as no-shell/shell) remains as a reasoning discipline for the planner but has no routing effect — all asks execute through shell-agent. File-discovery is only reachable in `mct-agent sync`.

## Consequences

**Positive:**
- Simpler execution model: one capable generalist instead of specialized paths
- Better asks: the planner reasons about *what kind of information it needs* before formulating the question, producing more focused and well-structured asks
- Reduced maintenance surface: no need to keep file-discovery's prompt templates and execution logic in sync with shell-agent's capabilities
- A simple computation path outperformed clever routing

**Negative:**
- Dead code in the `run` path: `PreflightShellRouting()`, `shouldPreferContentRouting()`, `discoveryrunner.Run()`, and the split-ask execution branch are all unreachable during `mct-agent run`. Future contributors may attempt to "fix" the override or wire up file-discovery, not realizing the override *is* the design
- The preflight LLM classification call is computed but discarded for single asks — a small cost in latency and tokens per turn
- `mct-agent sync` still uses file-discovery, creating an inconsistency between the two execution paths

**Neutral:**
- The ask categorization templates (`ask_prompt.tpl`, `ask_mixed_monitor.tpl`) remain valuable as reasoning scaffolds even without routing effect
- The planner's instruction to prefer `shell` for combined asks ("If a request would previously have needed both repository understanding and shell work, choose shell and restate it as one combined ask") aligns with the routing reality, reducing cognitive dissonance between what the planner sees and what happens at execution time

## References

- `agent/internal/core/prompt/util.go` — `applySingleAskRoutingPolicy()`
- `agent/internal/session/runner_turns.go` — split-ask collapse (`hasSplitAsk=false`, `collapsedLegacyBothAsk=true`)
- `agent/internal/core/prompt/preflight.go` — `PreflightShellRouting()`, `shouldPreferContentRouting()`
- `agent/internal/core/prompt/run.go` — `promptsvc.Run()` guard on `opts.ShellAgent`
- `agent/internal/core/discoveryrunner/discoveryrunner.go` — file-discovery execution (unreachable in `run`)
- `agent/internal/core/readmesync/sync.go` — sync path (only reachable file-discovery path)

| Commit | Date | Significance |
|---|---|---|
| `6ab6b8b` | 2025-10-25 | `--shell-agent` flag introduced as opt-in alternative to file-discovery |
| `95efe9c` | 2025-10-30 | `PreflightShellRouting()` with LLM classification for per-ask routing |
| `c383995` | 2026-02-12 | Mixed-ask guardrails, ask categorization (`no-shell`/`shell`/`both`), `splitAskLines()`, `applySingleAskRoutingPolicy()` |
| `e3c9cd1` | 2026-03-02 | `showFileHandled` routing — file-discovery answers re-routed to shell-agent when incomplete |
| `0d226b7` | 2026-04-01 | Collapse split asks into shell-agent routing; `both` mode removed; file-discovery unreachable in `run` |
| `065111d` | 2026-04-15 | `shouldPreferContentRouting()` heuristic to fix explanation-ask mis-routing |
| `0aa7bd3` | 2026-04-15 | Refined heuristic; removed "directory" from shell-routing triggers; added code-signal terms |
