# Harness Comparison Runbook: mct-agent vs Forge (`muse`)

**Generated** {{GENERATION_DATE}}
**Project** `{{PROJECT_NAME}}` at `{{PROJECT_ROOT}}`
**Prompt** `{{PROMPT_FILE}}`
<!-- IF_EVAL -->
**Eval commit** `{{EVAL_COMMIT}}` (pre-fix state)
**Ground truth** `{{GROUND_TRUTH_COMMIT}}` (actual fix)
<!-- END_IF_EVAL -->

---

## Purpose

Evaluate **mct-agent** (`code` mode) against **forge** (`muse` agent) on their
ability to analyze the `{{PROJECT_NAME}}` codebase and produce a high-quality
analytical document. **You, the agent reading this runbook, will perform the
judgment yourself** by verifying claims against the actual source code.

mct-agent uses the **{{MCT_MODEL}}** model alias; forge uses **DeepSeek V4 Pro** via OpenRouter.
<!-- IF_EVAL -->

This is a **retrospective eval case**: an isolated git worktree will be created at commit
`{{EVAL_COMMIT}}` (pre-fix state), both agents will run against the pre-fix code in the worktree,
and their outputs will be compared against the ground-truth fix at commit `{{GROUND_TRUTH_COMMIT}}`.
<!-- END_IF_EVAL -->

---

## Prerequisites

| Requirement | Detail |
|---|---|
| **API key** | API key configured via config file or `--api-key` flag (see Model Configuration Reference) |
| **mct-agent** | Binary on `PATH`; repo root is the working directory |
| **Forge** | Binary at `~/.local/bin/forge`, logged into OpenRouter |
| **jq** | Installed (for extracting forge answer) |
| **Project** | `{{PROJECT_ROOT}}` exists and is readable |
| **Prompt** | `{{PROMPT_FILE}}` exists |
<!-- IF_EVAL -->
| **Git** | `{{PROJECT_ROOT}}` is a git repo; git worktree support available |
<!-- END_IF_EVAL -->

**Pre-flight checks:**

    # Verify prerequisites
    command -v mct-agent >/dev/null 2>&1 || { echo "mct-agent not on PATH"; exit 1; }
    command -v forge >/dev/null 2>&1      || { echo "forge not found"; exit 1; }
    command -v jq >/dev/null 2>&1          || { echo "jq not installed"; exit 1; }
    mct-agent config check >/dev/null 2>&1 || { echo "mct-agent config check failed (verify config.toml and API key)"; exit 1; }
    test -d "{{PROJECT_ROOT}}"             || { echo "Project root not found: {{PROJECT_ROOT}}"; exit 1; }
    test -f "{{PROMPT_FILE}}"              || { echo "Prompt file not found: {{PROMPT_FILE}}"; exit 1; }
    if [ -n "{{MCT_API_KEY_ARG}}" ]; then
      echo "API key will be passed via --api-key flag"
    fi
<!-- IF_EVAL -->
    cd "{{PROJECT_ROOT}}" && git rev-parse {{EVAL_COMMIT}} >/dev/null 2>&1 || { echo "Eval commit not found: {{EVAL_COMMIT}}"; exit 1; }
    git rev-parse {{GROUND_TRUTH_COMMIT}} >/dev/null 2>&1 || { echo "Ground truth commit not found: {{GROUND_TRUTH_COMMIT}}"; exit 1; }
<!-- END_IF_EVAL -->
    echo "All checks passed"

---
<!-- IF_EVAL -->

## Step 0: Create Isolated Worktree

Checkout the eval commit in an isolated worktree so the codebase reflects the unresolved issue
without modifying the main repository.

    if [ -d "{{WORKTREE_DIR}}" ]; then
      echo "Removing existing worktree at {{WORKTREE_DIR}}"
      rm -rf "{{WORKTREE_DIR}}"
      git -C "{{PROJECT_ROOT}}" worktree prune
    fi
    git -C "{{PROJECT_ROOT}}" worktree add {{WORKTREE_DIR}} {{EVAL_COMMIT}}
    cd "{{WORKTREE_DIR}}"
    # Use the config path resolved by the runbook generator.
      MACHTIANI_CONFIG="{{MCT_CONFIG_PATH}}" mct-agent sync --model {{MCT_MODEL}} --timeout-per-turn 0 {{MCT_API_KEY_ARG}}

- The worktree is an isolated checkout at the pre-fix state. The main repo is untouched.
- mct-agent sync generates the internal README for this commit (required before mct-agent run).
- MACHTIANI_CONFIG is set to the config path resolved by the runbook generator.

---
<!-- END_IF_EVAL -->

## Step 1: Run mct-agent

### Invocation

<!-- IF_EVAL -->
    cd {{WORKTREE_DIR}}   # isolated worktree at eval commit
<!-- END_IF_EVAL -->
<!-- IF_NOT_EVAL -->
    cd {{PROJECT_ROOT}}   # project repository root
<!-- END_IF_NOT_EVAL -->

    MACHTIANI_CONFIG="{{MCT_CONFIG_PATH}}" mct-agent run \
      --mode code \
      --final-file /tmp/mct_answer{{ARTIFACT_SUFFIX}}.md \
      --timeout-per-turn 0 \
      --model {{MCT_MODEL}} {{MCT_API_KEY_ARG}} \
      --file "{{PROMPT_FILE}}"

- `--mode code` — coding-oriented orchestrator preset.
- `--final-file /tmp/mct_answer{{ARTIFACT_SUFFIX}}.md` — writes the final answer directly to this path.
- `--timeout-per-turn 0` — disables the per-turn timeout.
- `--model {{MCT_MODEL}}` resolves from config.toml `[models]` section.
- API key resolution order: CLI `--api-key` override then config file then environment variables.
- If mct-agent exits after 0 turns, re-run with `--step-limit` increased (e.g., `--step-limit 30`).

> **Note on duration:** mct-agent runs can take **10-30 minutes** for complex tasks.
> The `--timeout-per-turn 0` flag disables per-turn timeouts, but the overall process may
> still be killed by shell timeouts (e.g., SSH idle disconnect or job control limits).
> For long evals, consider running with `nohup` or in a `tmux` session:
>
>     nohup mct-agent run ... > /tmp/mct_agent{{ARTIFACT_SUFFIX}}.log 2>&1 &
>     # or
>     tmux new-session -d -s mct-eval 'mct-agent run ...'

### Verify Answer

    test -s /tmp/mct_answer{{ARTIFACT_SUFFIX}}.md || { echo "mct-agent did not produce an answer"; exit 1; }
    wc -l /tmp/mct_answer{{ARTIFACT_SUFFIX}}.md
    echo "mct-agent finished"

---

## Step 2: Run Forge (`muse` Agent)

### Invocation

    forge --agent muse --prompt "$(cat "{{PROMPT_FILE}}")"

- `--agent muse` → OpenRouter → `deepseek/deepseek-v4-pro`.
- Forge auto-generates a UUID conversation ID internally (no `--conversation-id` flag needed).
- Forge muse runs can take 10+ minutes; be patient.

### Extract the Answer

    && cd /tmp \
    && CONV_ID=$(forge conversation list 2>/dev/null | grep -oE "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}" | tail -1) \
    && forge conversation dump "$CONV_ID" 2>&1 \
    && DUMP_FILE=$(ls -t /tmp/conversation_*-dump.json 2>/dev/null | head -1) \
    && if [ -z "$DUMP_FILE" ] || [ ! -f "$DUMP_FILE" ]; then \
        DUMP_FILE=$(ls -t /tmp/*-dump.json 2>/dev/null | head -1); \
    fi \
    && jq -r "[.conversation.context.messages[] | select(.text.role == \"Assistant\") | .text.content | select(length > 0)] | last" "$DUMP_FILE" > /tmp/forge_answer{{ARTIFACT_SUFFIX}}.md \
    && rm -f "$DUMP_FILE" \
    &&
<!-- IF_EVAL -->
    cd "{{WORKTREE_DIR}}"
<!-- END_IF_EVAL -->
<!-- IF_NOT_EVAL -->
    cd "{{PROJECT_ROOT}}"
<!-- END_IF_NOT_EVAL -->

### Verify Answer

    test -s /tmp/forge_answer{{ARTIFACT_SUFFIX}}.md || { echo "forge did not produce an answer"; exit 1; }

---
<!-- IF_EVAL -->

## Step 2.5: Capture Ground Truth

Now that both agents have run against the pre-fix state, capture the actual fix as a diff. The main repo is used for the diff since it has both commits available.

    git -C "{{PROJECT_ROOT}}" diff {{EVAL_COMMIT}}..{{GROUND_TRUTH_COMMIT}} > /tmp/ground_truth{{ARTIFACT_SUFFIX}}.patch

### Verify Ground Truth

    test -s /tmp/ground_truth{{ARTIFACT_SUFFIX}}.patch || { echo "Ground truth diff is empty"; exit 1; }
    echo "Ground truth captured (patch lines: $(wc -l < /tmp/ground_truth{{ARTIFACT_SUFFIX}}.patch))"

---
<!-- END_IF_EVAL -->

## Step 3: Judge the Answers (You Do This)

**This step is yours.** Do not delegate the judgment.

1. **Read both answers:**
   - `/tmp/mct_answer{{ARTIFACT_SUFFIX}}.md` (mct-agent)
   - `/tmp/forge_answer{{ARTIFACT_SUFFIX}}.md` (forge)
<!-- IF_EVAL -->
2. **Read the ground truth:**
   - `/tmp/ground_truth{{ARTIFACT_SUFFIX}}.patch` (the actual fix diff from `{{EVAL_COMMIT}}` to `{{GROUND_TRUTH_COMMIT}}`)

3. **Explore the `{{PROJECT_NAME}}` source** at `{{PROJECT_ROOT}}` (main checkout, untouched by the eval). Verify every claim against actual code.

4. **Compare each answer against the ground truth:**
   - Did the agent identify the same files that the fix touched?
   - Did the agent propose the same approach as the actual fix?
   - Did the agent pinpoint the same lines or functions?

5. **Score each answer** on accuracy, completeness, clarity, actionability — weighted by how closely they match the ground truth.
<!-- END_IF_EVAL -->
<!-- IF_NOT_EVAL -->
2. **Explore the `{{PROJECT_NAME}}` source** at `{{PROJECT_ROOT}}`. Verify every claim against actual code.

3. **Score each answer** on accuracy, completeness, clarity, actionability.
<!-- END_IF_NOT_EVAL -->

<!-- IF_EVAL -->
6. **Write judgment** to `/tmp/judgment{{ARTIFACT_SUFFIX}}.md` with file:line evidence and ground-truth comparison.
<!-- END_IF_EVAL -->
<!-- IF_NOT_EVAL -->
4. **Write judgment** to `/tmp/judgment{{ARTIFACT_SUFFIX}}.md` with file:line evidence.
<!-- END_IF_NOT_EVAL -->

---

## Step 4: Clean Up

<!-- IF_EVAL -->
    rm -rf {{WORKTREE_DIR}}
    git -C "{{PROJECT_ROOT}}" worktree prune
    rm -f /tmp/forge_answer{{ARTIFACT_SUFFIX}}.md /tmp/mct_answer{{ARTIFACT_SUFFIX}}.md /tmp/judgment{{ARTIFACT_SUFFIX}}.md /tmp/ground_truth{{ARTIFACT_SUFFIX}}.patch
<!-- END_IF_EVAL -->
<!-- IF_NOT_EVAL -->
    rm -f /tmp/forge_answer{{ARTIFACT_SUFFIX}}.md /tmp/mct_answer{{ARTIFACT_SUFFIX}}.md /tmp/judgment{{ARTIFACT_SUFFIX}}.md
<!-- END_IF_NOT_EVAL -->

---

## Model Configuration Reference

| Harness | Provider | Model |
|---|---|---|
| mct-agent | Per `{{MCT_MODEL}}` alias in `.machtiani/config.toml` | `{{MCT_MODEL}}` |
| Forge `muse` | OpenRouter | `deepseek/deepseek-v4-pro` |

To route mct-agent through OpenRouter, use `--model` with an alias mapped to the openrouter provider and pass `--api-key openrouter:key`.

> **Note:** If using a model alias that routes through OpenRouter (e.g.,
> `deepseek-v4-pro-openrouter`), the API key is already stored in the config file
> (`[models]` section) and **no `--api-key` flag is needed**.
<!-- IF_EVAL -->

### Eval Case Reference

| Field | Value |
|---|---|
| Eval commit (pre-fix) | `{{EVAL_COMMIT}}` |
| Ground truth (fix) | `{{GROUND_TRUTH_COMMIT}}` |
| Artifact suffix | `{{ARTIFACT_SUFFIX}}` |
<!-- END_IF_EVAL -->

---

## Notes

- Session ID (`handoff-mct-<epoch>-<pid>`) is generated fresh on each run — no collisions.
- All intermediate artifacts go to `/tmp/` — nothing written into the project under analysis.
- Both harnesses receive exactly the same prompt from `{{PROMPT_FILE}}`.
<!-- IF_EVAL -->
- When running multiple eval cases, each gets a unique `ARTIFACT_SUFFIX` so artifacts do not collide.
- The main repo is never modified — all eval work happens in an isolated worktree under /tmp/.
<!-- END_IF_EVAL -->
