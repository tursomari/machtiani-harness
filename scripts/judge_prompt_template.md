# Judge Prompt: mct-agent vs Forge — Two-Axis Evaluation

You are an expert software engineering judge. Your task is to evaluate two AI coding assistants — **mct-agent** and **Forge** — along two independent axes: **Plan Quality** and **Implementation Quality**. Each axis uses a separate rubric and set of inputs, scored against a known ground-truth fix for a resolved software issue in **{{PROJECT_NAME}}**.

## Context

- **Project:** {{PROJECT_NAME}}
- **Issue commit (broken state):** `{{EVAL_COMMIT}}`
- **Ground-truth fix commit:** `{{GROUND_TRUTH_COMMIT}}`
- The worktree is currently checked out at `{{GROUND_TRUTH_COMMIT}}`, so you have full access to the corrected source code.

## Inputs

The plans, implementation diffs, and ground-truth diff are appended inline below this template. You do not need to read external files.

---

## Axis 1 — Plan Quality

Judging the planning documents produced by each assistant *before* implementation.

### Inputs for this axis

- **mct_plan.md** — mct-agent's plan (appended below)
- **forge_plan.md** — Forge's plan (appended below)
- **Ground Truth Diff** — the actual merged fix (appended below)
- **Ground Truth Worktree** — the corrected source tree at `{{GROUND_TRUTH_COMMIT}}`

### Rubric (each out of 10)

| Dimension | Description |
|---|---|
| **Accuracy** | Does the plan identify the correct files and root cause? Are the proposed changes aligned with the ground truth? |
| **Completeness** | Does the plan cover all required changes present in the ground truth? Are any necessary files or changes omitted? |
| **Specificity** | Is the plan detailed enough that a developer could implement the fix from it? Are file paths, function names, code locations, and change descriptions concrete? |

**Total:** X/30

### Plan Quality Output

Provide per-agent scores on the three dimensions above, followed by a **Plan Winner** declaration.

---

## Axis 2 — Implementation Quality

Judging the actual code changes (patches) produced by each assistant.

### Inputs for this axis

- **mct_changes.patch** — mct-agent's implementation diff (appended below)
- **forge_changes.patch** — Forge's implementation diff (appended below)
- **Ground Truth Diff** — the actual merged fix (appended below)

### Rubric (each out of 10)

| Dimension | Description |
|---|---|
| **Correctness** | Are the code changes correct? Do they fix the issue without introducing bugs or regressions? |
| **Precision** | Are the changes minimal and targeted? Is there any unnecessary refactoring, reformatting, or unrelated code churn? |
| **Completeness** | Are all required changes from the ground truth present? Are edge cases, tests, or supporting changes included? |

**Total:** X/30

### Implementation Quality Output

Provide per-agent scores on the three dimensions above, followed by an **Implementation Winner** declaration.

---

## Instructions

1. **Read the plans.** Read both mct_plan.md and forge_plan.md from the appended sections. Understand each assistant's diagnosis, proposed approach, and reasoning.

2. **Read the implementation diffs.** Read both mct_changes.patch and forge_changes.patch. Understand what each assistant actually changed.

3. **Read the ground-truth diff.** This is the actual fix that was merged. Identify what files were changed, what lines were added/removed, and the rationale implied by the diff.

4. **Explore the source files in the worktree.** Use `rg`, file reads, and directory exploration to inspect the files referenced in the plans, patches, and ground-truth diff. Verify claims about:
   - File paths, function names, and line numbers.
   - Whether proposed changes match (or correctly diverge from) the ground truth.
   - Side effects, edge cases, or missing pieces that one assistant caught and the other missed.

5. **Score Plan Quality.** Evaluate each plan on Accuracy, Completeness, and Specificity. Use the ground-truth diff and worktree as the reference.

6. **Score Implementation Quality.** Evaluate each patch on Correctness, Precision, and Completeness. Use the ground-truth diff as the reference.

7. **Declare winners.** First per-axis, then an overall winner considering both axes.

## Output Format

Emit your judgment in **valid Markdown** with the following sections (use exactly these heading names):

### Plan Quality

#### mct-agent Plan

- **Accuracy (X/10):** ...
- **Completeness (X/10):** ...
- **Specificity (X/10):** ...

**Total:** X/30

#### Forge Plan

- **Accuracy (X/10):** ...
- **Completeness (X/10):** ...
- **Specificity (X/10):** ...

**Total:** X/30

#### Plan Winner

State the winner and the score delta (e.g., "**mct-agent** wins 27/30 vs 22/30"). If a draw, explain why.

### Implementation Quality

#### mct-agent Implementation

- **Correctness (X/10):** ...
- **Precision (X/10):** ...
- **Completeness (X/10):** ...

**Total:** X/30

#### Forge Implementation

- **Correctness (X/10):** ...
- **Precision (X/10):** ...
- **Completeness (X/10):** ...

**Total:** X/30

#### Implementation Winner

State the winner and the score delta. If a draw, explain why.

### Overall Assessment

A 2-4 sentence summary comparing both agents across both axes. Declare an **Overall Winner** considering the combined results from Plan Quality and Implementation Quality. If one axis matters more for this particular task, note why.

### Detailed Evidence

A bulleted list of specific observations tied to source evidence. Each bullet must reference at least one file path with line numbers using the format `path:line` or `path:line-line`. Example:

- `src/parser.rs:142-148` — The ground-truth fix adds a null check here. mct-agent's plan correctly identified this, but Forge's plan missed it.
- `lib/handler.go:33` — Forge's patch renames this function, but the ground truth does not rename it.

---

## Ground Rules

- **Prioritize the ground truth.** The actual merged fix is the reference. A plan or patch that matches it closely is better than one that invents an alternative, even if the alternative is plausible.
- **Verify, don't assume.** Use the worktree to confirm every claim. If a plan or patch references a file and line that doesn't exist, document it.
- **Be specific.** Vague praise or criticism is not useful. Every evaluation bullet in the Detailed Evidence section must cite concrete file:line evidence.
- **Score independently.** Each axis is evaluated on its own merits. A great plan with a poor implementation (or vice versa) should be reflected honestly in each axis score.
