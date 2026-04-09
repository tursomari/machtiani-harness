# Repo-local `mct-agent` runbook

Use this runbook when operating `mct-agent` from inside this repository.

## Start here

- Run from the repo root so the agent picks up `.machtiani/config.toml` and writes artifacts under this repo's `.machtiani/sessions/` tree.
- In this repo, prefer the `glm-5-high` model alias from `.machtiani/config.toml`.
- For OpenRouter-backed runs here, export `OPENROUTER_API_KEY` from the existing `TEST_API_KEY` environment variable.

Preferred live invocation:

```bash
OPENROUTER_API_KEY="$TEST_API_KEY" \
MACHTIANI_CONFIG=.machtiani/config.toml \
mct-agent run --mode code --model glm-5-high --verbose -t "<goal>"
```

## Why `--mode code`

- In this repo, the practical meta-orchestrator mode to use is `code`.
- That creates a parent session first, then spawns child task sessions under `.machtiani/sessions/`.
- The repo-local mode files live under `.machtiani/meta-orchestrator/custom-instructions/code/`.

## Session behavior

- A `--mode code` run typically creates a parent session plus one or more child sessions.
- The parent session records the high-level orchestration and prompts for the next action after a child finishes.
- In practice, entering `c` at the parent prompt marks the task complete and lets the parent emit its summary/final artifacts.

Useful artifacts under `.machtiani/sessions/<session-id>/`:

- `chat/agent-final-answer.md` — strongest signal that a session finished successfully.
- `chat/agent-transcript.adoc` — readable transcript of the session.
- `session-state.json` — machine-readable session metadata.
- `trajectory/agent.jsonl` — lower-level step/event log.
- `meta-plan.json` — parent-session plan state for meta-orchestrated runs.

## Completion signal

- The best completion check is the presence and contents of `chat/agent-final-answer.md` for the session you care about.
- For parent sessions, that summary is the best indication the orchestrated run actually wrapped up.
- For child sessions, this file is also the most reliable summary artifact.

## Follow-up workflow

To continue a child session directly:

```bash
OPENROUTER_API_KEY="$TEST_API_KEY" \
MACHTIANI_CONFIG=.machtiani/config.toml \
mct-agent run --model glm-5-high --session-id <child-session-id> -t "<follow-up>"
```

To resume or continue the parent session:

```bash
OPENROUTER_API_KEY="$TEST_API_KEY" \
MACHTIANI_CONFIG=.machtiani/config.toml \
mct-agent run --model glm-5-high --session-id <parent-session-id> -t "<next instruction or original goal>"
```

- A follow-up run with `--session-id <child-session-id>` rewrites that child session's `chat/agent-final-answer.md`.
- Check artifacts on disk after follow-ups rather than assuming every printed path or summary is perfectly current.

## Practical expectations

- In this repo, treat `mct-agent` primarily as an informational / research assistant.
- It can inspect the codebase, use file-discovery, invoke shell steps, and run existing scripts when useful.
- Do not rely on it as a dependable direct file-editing agent in the current setup.
- Verify behavior from artifacts on disk instead of assuming a requested write happened.

## Notes from local usage

- The local workflow relies on `--mode` to create the parent orchestration session.
- A good workflow checks or updates `chat/agent-final-answer.md` in the relevant session directory to confirm the run finished.
- Follow-ups are typically done by passing a new prompt together with `--session-id`.
