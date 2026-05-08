# Harness Comparison Runbook: mct-agent vs Forge (`muse`)

**Generated** {{GENERATION_DATE}}
**Project** `{{PROJECT_NAME}}` at `{{PROJECT_ROOT}}`
**Prompt** `{{PROMPT_FILE}}`

---

## Purpose

Evaluate **mct-agent** (`code` mode) against **forge** (`muse` agent) on their
ability to analyze the `{{PROJECT_NAME}}` codebase and produce a high-quality
analytical document. **You, the agent reading this runbook, will perform the
judgment yourself** by verifying claims against the actual source code.

Both harnesses use **DeepSeek V4 Pro** as the underlying model.

---

## Prerequisites

| Requirement | Detail |
|---|
| **API key** | `DEEPSEEK_API_KEY` exported in the shell |
| **mct-agent** | Binary on `PATH`; repo root is the working directory |
| **Forge** | Binary at `|/.local/bin/forge`, logged into OpenRouter |
| **jq** | Installed (for extracting forge's answer) |
| **Project** | `{{PROJECT_ROOT}}` exists and is readable |
| **Prompt** | `{{PROMPT_FILE}}` exists |

**Pre-flight checks:**

    # Verify prerequisites
    command -v mct-agent >/dev/null 2>&1 || { echo "mct-agent not on PATH"; exit 1; }
    command -v forge >/dev/null 2>&1      || { echo "forge not found"; exit 1; }
    command -v jq >/dev/null 2>&1          || { echo "jq not installed"; exit 1; }
    test -z "${DEEPSEEK_API_KEY:-}" && test -z "${TEST_API_KEY:-}" && { echo "Neither DEEPSEEK_API_KEY nor TEST_API_KEY set"; exit 1; }
    test -d "{{PROJECT_ROOT}}"             || { echo "Project root not found: {{PROJECT_ROOT}}"; exit 1; }
    test -f "{{PROMPT_FILE}}"              || { echo "Prompt file not found: {{PROMPT_FILE}}"; exit 1; }
    echo "All checks passed"

---

## Step 1: Run mct-agent

### Cleanup Stale Locks

### Invocation

    cd $(pwd)   # mct-agent repository root

    export DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-$TEST_API_KEY}" && mct-agent run \
      --mode code \
      --final-file /tmp/mct_answer.md \
      --timeout-per-turn 0 \
      --model deepseek-v4-pro \
      --file "{{PROMPT_FILE}}"

- `--mode code` — coding-oriented orchestrator preset.
- `--final-file /tmp/mct_answer.md` — writes the final answer directly to this path.
- `--timeout-per-turn 0` — disables the per-turn timeout.
- Model resolves from `.machtiani/config.toml` → `deepseek-v4-pro`.

### Verify Answer

    test -s /tmp/mct_answer.md || { echo "mct-agent did not produce an answer"; exit 1; }
    wc -l /tmp/mct_answer.md
    echo "mct-agent finished"

---

## Step 2: Run Forge (`muse` Agent)

### Invocation

    forge --conversation-id handoff-forge --agent muse --prompt "$(cat "{{PROMPT_FILE}}")"

- `--agent muse` → OpenRouter → `deepseek/deepseek-v4-pro`.
- `--conversation-id handoff-forge` — fixed conversation ID for easy retrieval.

### Extract the Answer

    forge conversation dump handoff-forge | jq -r '.messages[-1].content' > /tmp/forge_answer.md

### Verify Answer

    test -s /tmp/forge_answer.md || { echo "forge did not produce an answer"; exit 1; }

---

## Step 3: Judge the Answers (You Do This)

**This step is yours.** Do not delegate the judgment.

1. **Read both answers:**
   - `/tmp/mct_answer.md` (mct-agent)
   - `/tmp/forge_answer.md` (forge)

2. **Explore the `{{PROJECT_NAME}}` source** at `{{PROJECT_ROOT}}`. Verify every claim against actual code.

3. **Score each answer** on accuracy, completeness, clarity, actionability.

4. **Write judgment** to `/tmp/judgment.md` with file:line evidence.

---

## Step 4: Clean Up

    rm /tmp/forge_answer.md /tmp/mct_answer.md /tmp/judgment.md

---

## Model Configuration Reference

| Harness | Provider | Model |
|---|
| mct-agent | DeepSeek direct (`api.deepseek.com`) | `deepseek-v4-pro` |
| Forge `muse` | OpenRouter | `deepseek/deepseek-v4-pro` |

Both resolve to DeepSeek V4 Pro. To eliminate the provider difference, pass
`--model deepseek-v4-pro-openrouter` to mct-agent and set `OPENROUTER_API_KEY`. [ ]

---

## Notes

- Session ID (`handoff-mct-<epoch>-<pid>`) is generated fresh on each run — no collisions.
- All intermediate artifacts go to `/tmp/` — nothing written into the project under analysis.
- Both harnesses receive exactly the same prompt from `{{PROMPT_FILE}}`.