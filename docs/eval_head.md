HEAD-Based Evaluation Script Documentation

This document describes scripts/run_eval_head.sh, a standalone evaluation pipeline for scenarios without a ground truth commit. It is derived from run_eval.sh but modified to work from the current HEAD commit.

Usage: scripts/run_eval_head.sh --repo <path> --prompt <path> [--model <alias>] [--mode <read-only|write>] [--judge-model <alias>] [--api-key <provider:key>] [--config <path>] [--output-dir <path>] [--keep] [--sequential]. The default mode is write.

Flags and their purposes:

--repo: path to the git repository.

--prompt: path to the task prompt file.

--model: model alias for mct-agent and forge evaluation agents (default glm-5-high-deepinfra).

--judge-model: separate model alias for the judge forge agent (optional).

--mode: write or read-only. In write mode the judge produces an unscored implementation benchmark alongside the evaluation. In read-only mode only the evaluation is performed.

--api-key: API key in provider:key format.

--config: path to the config.toml file.

--output-dir: directory for all output artifacts.

--keep: preserves worktrees on exit for debugging.

--sequential: runs the agents sequentially instead of in parallel (default is parallel).

Output artifacts produced in the output directory:

Worktree directories for each agent: worktree_mct, worktree_forge, and worktree_judge.

Plan files: mct_plan.md and forge_plan.md, containing the implementation plans produced by each agent during the planning phase.

Implementation answer files: mct_answer.md and forge_answer.md, containing each agent's final implementation answer.

Code diff patches: mct_changes.patch and forge_changes.patch, capturing the actual code changes made by each agent as unified diffs.

Judge evaluation: judgment.md containing the judge's comparative evaluation with scores across plan quality and implementation quality dimensions.

Judge implementation files (write mode only): judge_impl_prompt.md (the prompt given to the judge for its own implementation), judge_answer.md (the judge's implementation answer), and judge_changes.patch (the code diff produced by the judge's implementation).

Report: report.md containing the final comparative results in a structured format with score tables, winner determinations, and artifact path references.

Session lock workaround: When running run_eval_head.sh inside an active machtiani session, unset the environment variables MACHTIANI_SESSION_ID and MACHTIANI_SESSION_TEMP_ROOT, and set MACHTIANI_SESSION_TEMP_ROOT to an isolated directory outside the session scratch root. This avoids flock conflicts between the outer machtiani session and the evaluation script's own subprocesses.

Validation checklist after a completed run:

1. Verify that all three worktree directories (worktree_mct, worktree_forge, worktree_judge) exist and are checked out to HEAD by running git rev-parse HEAD inside each one.

2. Check that the plan files mct_plan.md and forge_plan.md are non-empty.

3. Check that the implementation answer files mct_answer.md and forge_answer.md exist.

4. Check that the judge produced judgment.md containing evaluation scores for both agents.

5. For write mode, verify that judge_changes.patch exists and is non-empty.

6. Examine report.md to confirm it reflects the correct mode designation and contains the expected comparative results.
