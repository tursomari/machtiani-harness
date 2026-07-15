# Repo-local `mct-agent` runbook

Use this runbook when operating `mct-agent` from inside this repository.

## Start here

- Run from the repo root and initialize or migrate it before the first run. Use `mct-agent project show` to inspect the active UUID store.
- This repository historically used repo-local state. Review `mct-agent migrate --dry-run`, then run `mct-agent migrate --no-interactive --yes` when ready to adopt the home store.
- Set `PROJECT_STORE="$HOME/.machtiani/$(cat .machtiani/project.uuid)"` when using the artifact-path examples below.
- In this repo, prefer the `glm-5-high` model alias from the selected global or UUID-project config.
- See [`examples/config.minimal.toml`](examples/config.minimal.toml) for a minimal
  starting point, or
  [`examples/config.comprehensive.toml`](examples/config.comprehensive.toml) for
  a reference covering every section and field.
- For OpenRouter-backed runs here, export `OPENROUTER_API_KEY` from the existing `TEST_API_KEY` environment variable.

Preferred live invocation:

```bash
OPENROUTER_API_KEY="$TEST_API_KEY" \
mct-agent run --mode code --model glm-5-high \
  --max-steps 100 --timeout-per-turn 0 --verbose \
  -t "<goal>"
```

- Use `--max-steps 100` as the practical default ceiling in this repo.
- Use `--timeout-per-turn 0` to disable per-turn timeouts; runs may take from a minute to an hour or more depending on the prompt.

## Why `--mode code`

- In this repo, the practical mode to use is `code`.
- That creates a parent session first, then spawns child task sessions under `$PROJECT_STORE/sessions/`.
- Installed modes live under `~/.machtiani/modes/`. Copy a canonical mode to a new name before customizing it.

## Planner system prompt shape

- For `--mode code`, the mode task definition lives in `~/.machtiani/modes/code/tasks.toml`.
- That file points `system_prompt` at `~/.machtiani/modes/code/code.txt`.
- The contents of `code.txt` are loaded as repo/mode planner guidance and injected into the planner system template inside `<REPO_MODE_GUIDANCE> ... </REPO_MODE_GUIDANCE>`.
- `description` is metadata only, and `instruction` is the task-local objective that flows through the planner user-message path.
- So the planner system prompt is not just `code.txt`; it is the base planner system template plus the full text of `code.txt` in the third system layer.

Mental model:

```text
System message
= <CORE_SAFETY_RULES>
+ <PLANNER_OPERATING_RULES>
+ <REPO_MODE_GUIDANCE> (optional)

User message(s)
= goal / task objective / latest step instruction
```

Approximate rendered shape:

```text
You are the planner for mct, orchestrating repository understanding and modification.

<PLANNER_SYSTEM_PROMPT>
<CORE_SAFETY_RULES>
...
</CORE_SAFETY_RULES>

<PLANNER_OPERATING_RULES>

<YOUR_ROLE>
...
</YOUR_ROLE>

<ASK_GUIDELINES>
...
</ASK_GUIDELINES>

<SHELL_AGENT>
...
</SHELL_AGENT>

<CONTEXT_HANDLING>
...
</CONTEXT_HANDLING>

<OUTPUT_FORMAT>
...
</OUTPUT_FORMAT>
</PLANNER_OPERATING_RULES>

<REPO_MODE_GUIDANCE>

## Goal Adherence
...

## Evidence Grounding
...

## Factual Accuracy
...

## Conclusions
...

## Gap Identification
...

## Verification
...
</REPO_MODE_GUIDANCE>
</PLANNER_SYSTEM_PROMPT>
```

- `system_prompt` is the only task-config field that contributes repo/mode planner guidance.
- `instruction` is the explicit task objective for the child-session user-message path.
- `title` still matters for session/task labeling and remains the final fallback when no explicit `instruction` is authored.

## Session behavior

- A `--mode code` run typically creates a parent session plus one or more child sessions.
- The parent session records the high-level orchestration and prompts for the next action after a child finishes.
- In practice, entering `c` at the parent prompt marks the task complete and lets the parent emit its summary/final artifacts.

Useful artifacts under `$PROJECT_STORE/sessions/<session-id>/`:

- `chat/agent-final-answer.md` — strongest signal that a session finished successfully.
- `chat/agent-transcript.adoc` — readable transcript of the session.
- `session-state.json` — machine-readable session metadata.
- `trajectory/agent.jsonl` — lower-level step/event log.
- `mode-plan.json` — parent-session plan state for mode-system runs.

## Completion signal

- The best completion check is the presence and contents of `chat/agent-final-answer.md` for the session you care about.
- For parent sessions, that summary is the best indication the orchestrated run actually wrapped up.
- For child sessions, this file is also the most reliable summary artifact.

## Sync after new commits

- After landing new commits in this repo, run `mct-agent sync` so the internal README state is updated to the current project `HEAD`.
- If more than one commit has landed since the last sync, that's fine; `sync` compares the current `HEAD` against the last processed commit and catches up in one run.
- If `mct-agent run` or `bash agent/tests/run-live.sh` fails with `mct is not synced at current git state ... Run mct-agent sync before proceeding.`, this is the missing prerequisite. Re-run the sync command below, then retry the harness or agent run.
- In this repo, developers and coding agents should use `TEST_*` for test and harness flows. For the repo-local sync command below, pass `TEST_API_KEY` through the same OpenRouter credential source as the normal `run` workflow via an `openrouter:` override on `--api-key`.

Repo-local sync command:

```bash
mct-agent sync \
  --api-key "openrouter:$TEST_API_KEY" \
  --model glm-5-high \
  --context-length 128000
```

- A successful sync prints `Readme synced for commit <hash>`.
- If the new commits do not materially change the internal README, `sync` may report that there were no significant changes and simply move the sync marker forward to the current `HEAD`.
- Each synced project commit has an immutable `oid-<project-commit>` tag in the internal README repository. New sessions inject the README blob directly from that tag, so a stale or concurrently materialized `artifacts/readme/internal-readme.md` worktree file cannot change the injected background.
- The materialized `artifacts/readme/internal-readme.md` is a compatibility view shared by worktrees with the same project UUID. Treat the tagged object—and the copy persisted in a session's `conversation.json`—as authoritative when worktrees are on different commits.

## Follow-up workflow

To continue a child session directly:

```bash
OPENROUTER_API_KEY="$TEST_API_KEY" \
mct-agent run --model glm-5-high \
  --max-steps 100 --timeout-per-turn 0 \
  --session-id <child-session-id> -t "<follow-up>"
```

To resume or continue the parent session:

```bash
OPENROUTER_API_KEY="$TEST_API_KEY" \
mct-agent run --model glm-5-high \
  --max-steps 100 --timeout-per-turn 0 \
  --session-id <parent-session-id> -t "<next instruction or original goal>"
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

## Operator tips

- `mct-agent` often delegates repo inspection to `shell-agent`, so expect synthesized answers rather than raw shell output.
- Tight prompt contracts help: explicitly say what to return, what not to do, and whether file edits are allowed.
- Trust on-disk artifacts more than intermediate console chatter; `chat/agent-final-answer.md` is the strongest completion signal.
- Follow-ups on an existing child session rewrite that child's `chat/agent-final-answer.md`, so copy it elsewhere first if you want to preserve an earlier summary.

## Example prompts

These are adapted from actual local sessions under `$PROJECT_STORE/sessions/`.

### Starter: draft a commit subject from staged changes

Adapted from `agent-20260409T170609-4898`.

```bash
OPENROUTER_API_KEY="$TEST_API_KEY" \
mct-agent run --model glm-5-high \
  --max-steps 100 --timeout-per-turn 0 --verbose \
  -t 'Inspect the recent git commit subject style in this repository and inspect the currently staged changes. Then draft exactly one conventional commit subject line that matches the existing style. Return only the commit subject line, with no quotes, no bullets, and no explanation. Do not modify files.'
```

### Medium: explain behavior and report repo state

Adapted from `agent-20260302T212840-6500`.

```bash
OPENROUTER_API_KEY="$TEST_API_KEY" \
mct-agent run --mode code --model glm-5-high \
  --max-steps 100 --timeout-per-turn 0 --verbose \
  -t 'Explain the planner menu flow and what happens after an ask is selected. Also run `git diff --stat` and report the output. Do not modify files.'
```

### Complex: investigate a behavior and draft an engineering issue

Adapted from `agent-20260212T184335-8455`.

```bash
PROMPT=$(cat <<'EOF'
Create an issue for the engineering team.

Problem:
`file-discovery` is too lax when determining relevant files. In `$PROJECT_STORE/sessions/agent-20260212T125514-5835/chat/agent-transcript.adoc`, planner messages focused exclusively on `README.md`, but `file-discovery` still returned clearly unrelated files.

Investigate at minimum:
- the `file-discovery` system prompt and instructions
- any tpl overrides that could confound `file-discovery` LLM calls
- missing guards or filters that should prevent unrelated files from being returned when the planner message is narrowly scoped

Deliverable:
- Draft an issue for the engineering team
- Recommend a live test similar to `agent/tests/run-live.sh` that demonstrates a `README.md`-only prompt returns only that file, while broader prompts can still return multiple related files appropriately
- Do not create hardcoded semantic checks; rely on LLM inference
- Do not modify files unless I explicitly ask in a follow-up
EOF
)

OPENROUTER_API_KEY="$TEST_API_KEY" \
mct-agent run --mode code --model glm-5-high \
  --max-steps 100 --timeout-per-turn 0 --verbose \
  -t "$PROMPT"
```
