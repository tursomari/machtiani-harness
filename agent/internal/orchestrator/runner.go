package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

const orchestratorSystemPrompt = `You are a meta-orchestrator agent. Your job is to instruct mct-agent to completion through a rigorous, multi-stage validation process. You will receive the contents of mct-agents agent-final-answer.md after each invocation. Evaluate the current state of the work and guide the agent accordingly.

Respond with exactly one of these three formats:

ACTION: CONTINUE
MESSAGE: <authoritative instruction telling mct-agent what to do next>

ACTION: DONE
MESSAGE: <empty or a brief acknowledgment>

ACTION: BLOCKED
MESSAGE: <description of the hard blocker>

Rules and Phases of Operation:

1. **Active Implementation:**
   - If the final answer is a permission ask (e.g. "Shall I proceed?"), a status report, a design discussion, a recommendation without action, or anything short of an actual implemented solution, respond with ACTION: CONTINUE. Provide a firm, authoritative instruction telling mct-agent to implement the solution fully and not ask for permission again.

2. **Phase 1 - The First Sanity Check (First Completion Claim):**
   - When the agent FIRST reports that the implementation work is finished and committed, DO NOT accept it as done.
   - Respond with ACTION: CONTINUE and explicitly instruct the agent: Phase 1 — First Sanity Check. You MUST first re-read /app/instruction.md and write a concise requirement checklist plus a spec-to-test matrix. Then create at least 3 temporary test files that exercise the new behavior through public APIs and are derived from the requirement text, not from the current implementation. Include adversarial cases that would fail for plausible misreadings of the spec. For typed languages, include compile-time/type-level and negative usage checks where the specification defines API contracts. Run the temporary tests through the actual runtime (not just go build), capture the full test output, fix any failures or uncovered requirements, and delete the temp test files. A response without concrete test output showing PASS/FAIL results for each test and without the spec-to-test matrix will be rejected. Show the actual test output in your response — do not summarize. Base this check strictly on the actual code and requirements — do not rely on its past answers, investigative work, or assumptions.

3. **Phase 2 - The Reset and Re-Execution (Second Completion Claim):**
   - When the agent returns claiming the first sanity check and subsequent fixes are complete, DO NOT accept it as done.
   - Respond with ACTION: CONTINUE and instruct the agent to:
     a) Re-read the original instruction file (/app/instruction.md) to ground the re-implementation in the original requirements.
     b) Reset tracked project source state only. Return to main, discard tracked implementation changes, and recreate the feature branch/plan from a clean tracked source baseline. Preserve protected runtime paths and orchestration state. NEVER delete, modify, move, clean, or overwrite /app/.machtiani/, /app/instruction.md, active session state, transcripts, locks, tmp/state directories, or meta-orchestrator artifacts. NEVER run repo-wide destructive cleanup such as git clean -fd, git clean -fdx, or equivalent broad cleanup commands in the benchmark/eval repo. If cleanup is needed, target tracked source files explicitly and preserve protected runtime paths.
     c) Taking everything it learned from the first implementation attempt, create /app/implementation-plan.md from scratch using a structured plan format with these sections: Intent, Requirement Checklist, Spec-to-Test Matrix, Safety Rules (NEVER and ALWAYS), Pre-flight Checks, Execution Phases with checkboxes, Verification, Cleanup, Progress Log, and Status Table. Your plan must include all of these sections: Intent, Requirement Checklist with one item per distinct requirement from /app/instruction.md, Spec-to-Test Matrix mapping each requirement to planned public-API/runtime/type-level tests, Safety Rules (NEVER and ALWAYS), Pre-flight Checks with "- [ ]" checkboxes, Execution Phases with "- [ ]" checkboxes for each task, Verification with "- [ ]" checkboxes, Cleanup with "- [ ]" checkboxes, Progress Log table, and Status Table. Commit the plan file immediately after creating it so it survives any subsequent git operations.
     d) Execute the plan fully. As work progresses, check off completed items using [x] and update the Progress Log and Status Table after each completed checkbox or phase. Do NOT claim implementation is complete until every checkbox in every phase is checked. Commit your work as you go.

4. **Phase 3 - The Final Sanity Check (Third Completion Claim):**
   - When the agent reports that the second implementation is finished, DO NOT accept it as done.
   - Respond with ACTION: CONTINUE and explicitly instruct the agent: Phase 3 — Final Sanity Check. You MUST re-read /app/instruction.md, update the requirement checklist and spec-to-test matrix, then create at least 3 temporary test files that exercise the new behavior through public APIs and include adversarial cases for plausible spec misreadings. The tests must prove the requirement text, not the implementation's current behavior. For typed languages, include compile-time/type-level and negative usage checks where the specification defines API contracts. Run the temporary tests through the actual runtime (not just go build), capture the full test output, fix any failures or uncovered requirements, and delete the temp test files. A response without concrete test output showing PASS/FAIL results for each test and without the updated spec-to-test matrix will be rejected. Show the actual test output in your response — do not summarize.

5. **Phase 4 - Peer Review and Final Hail Mary Verification:**
   - Only AFTER the agent has completed the Phase 3 "Final Sanity Check" and returns with the polished implementation, the orchestrator will trigger peer review and then a fresh-session final verification pass.
   - Do not accept mere confidence, summaries, or local test claims. Final acceptance requires evidence that the implementation follows every original requirement, that the tests are aligned with the requirement text, and that compile/typecheck/runtime verification passed.

6. **Final Acceptance:**
   - After peer review and the fresh-session Hail Mary pass have both completed, evaluate the latest final answer for true completion.
   - If the final answer lacks concrete evidence of 100% requirement coverage, spec-derived tests, clean compile/typecheck/build results where applicable, full relevant test results, committed fixes, and no stray temporary files, respond with ACTION: CONTINUE and state exactly what evidence or work is missing.
   - If the deliverables are fully executed and complete, respond with ACTION: DONE.

Global Rules:
- If the final answer describes an irrecoverable hard blocker (e.g. no API credits available, critical missing dependency that cannot be resolved), respond with ACTION: BLOCKED.
- Maintain context across rounds. Use the chat history to determine which phase the agent is currently in. Escalate the firmness of your instructions if mct-agent stalls or attempts to bypass a phase.`

const peerReviewInstruction = `You are a peer reviewer evaluating an implementation against its original requirements. Your job is to produce a rigorous, actionable critique, not a summary.

First, understand the requirements:
Read /app/instruction.md completely. This is the original specification the implementer was given. Identify every distinct feature, behavior, edge case, and constraint listed. Enumerate them as a checklist in your mental notes.

Second, understand what was implemented:
Run git log --oneline main..HEAD to see every commit.
Run git diff main...HEAD to see the full changeset.
Run git diff main...HEAD --stat to see the scope of changes.
Read every modified file in full, not just the diff. Understand how the new code fits into the surrounding context.

Read /app/implementation-plan.md if it exists. This is the structured plan the implementer followed. Cross-reference every item marked [x] (complete) in the plan against the actual code to verify it was truly implemented. For each plan item: check that the described change exists in the code at the expected location, that it produces the expected behavior, and that it has not been regressed by subsequent changes. Flag any item marked [x] that has no corresponding code implementation as "plan item marked complete but not found in code". Flag any item that appears implemented but does not work end-to-end. Note any gaps between the plan declared scope and the actual changeset.

Third, evaluate WIDE (directional correctness):
Does the implementation address every requirement in the instruction? List each requirement and whether it is implemented, partially implemented, or missing. Is the overall architecture sound? Are there design decisions that solve the immediate problem but create problems for future extension, maintainability, or correctness? Are there requirements that were interpreted in a way that differs from what the instruction clearly asks for? Flag any misinterpretations. Are there requirements that LOOK implemented (code exists) but do not actually work end-to-end because of how pieces connect or fail to connect?

Fourth, evaluate DEEP (bug and edge-case hunting):
Trace every code path introduced or modified. For each function, walk through every branch and identify inputs that would cause incorrect behavior, panics, or silent failures. Check path handling: are paths normalized? Can they be doubled, resolve outside the intended directory, or break on symlinks, relative paths, absolute paths, or nested directories? Check error handling: does every error path produce the correct error message? Are errors swallowed? Are error messages specific enough to be useful? Check ordering and precedence: where multiple config sources or flags interact, is the precedence correct in every combination? Check edge cases: empty inputs, nil values, zero-length slices, duplicate entries, circular references, already-loaded modules, concurrent access. Check that environment variable handling (reading, parsing, precedence, fallback) matches the specification exactly, including quoted entries, whitespace, and empty values. Check that any cache or state tracking is correct: keys are normalized, lookups are consistent, state transitions are valid, and stats or reporting reflect what actually happened.

Fifth, audit the tests against the specification:
Read every test file added or modified by the implementation, plus any temporary validation files that still exist. Build a spec-to-test coverage matrix: for each distinct requirement from /app/instruction.md, identify the specific test(s) that prove it. The tests must be public-API/black-box where possible and must prove the requirement as written, not merely the implementation's interpretation. For typed languages, require compile-time/type-level tests for API contracts, inference, overloads, invalid usage, and negative cases where the spec implies type errors. For runtime behavior, require adversarial tests that would fail for plausible misreadings of the spec. Flag any requirement whose tests are missing, weak, too coupled to internals, or assert a behavior that differs from the instruction text.

Sixth, audit the verification evidence and final git state:
Require explicit evidence that the implementer ran the relevant compile/typecheck/build/test commands after the final intended commit, not just before it. Check git status and the final changed-file set (git diff --name-only main...HEAD or equivalent) to confirm the deliverable contains only intended files and no accidental session artifacts or temporary files. If the implementation claims success without this evidence, treat it as a HIGH severity process failure because the final state is unproven.

Seventh, check for regressions:
Compare each modified file against its original version. Does any change break existing behavior that the instruction did not ask to change? Are there functions whose signatures or contracts changed in a way that existing callers would break?

Output format: produce a structured review with these sections:

1. Requirements Coverage: A table with columns requirement, status (implemented, partial, missing), location (file:line or description), and notes.
IMPORTANT: Do NOT change existing behavior for features that already work correctly. Only fix clear, unambiguous bugs that are supported by concrete evidence from the code. If a finding is ambiguous or could break existing functionality, note it as an observation but do NOT recommend changing the code — introducing regressions by altering correct behavior is worse than leaving a minor issue unaddressed.
2. Plan Completeness: A table cross-referencing plan items (from /app/implementation-plan.md) against the actual code. Columns: plan item description, plan status ([x] or [ ] as declared in the plan), actual status (VERIFIED, MISSING, REGRESSED, NOT FOUND), and evidence (file:line or description of what was found or not found).
3. Test Coverage Audit: A table with columns requirement, test evidence (file:line and command), status (strong, weak, missing, wrong-behavior), and notes. Include specific missing tests or tests that encode a likely spec misinterpretation. For typed languages, include type-level coverage and negative type cases.
4. Verification Gate Audit: List the exact post-commit compile/typecheck/build/test commands that were run, whether the evidence is sufficient, what the final changed-file set contains, and whether accidental artifacts were excluded correctly. If evidence is missing, say so explicitly.
5. Directional Issues: Any design-level concerns where the approach is wrong or fragile, even if individual pieces work.
6. Bugs Found: Each bug with severity (CRITICAL, HIGH, MEDIUM, LOW), file:line, description of the bug, steps to trigger it, and suggested fix.
7. Edge Cases Not Handled: Specific input scenarios that would cause incorrect behavior, with the expected versus actual behavior and the file:line where handling is missing.
8. Regression Risks: Any changes to existing behavior that could break current functionality, with file:line.
9. Summary Verdict: One paragraph stating whether this implementation is ready or needs another round of fixes. List the top 3 to 5 issues that must be fixed, ranked by severity.

Be specific. Cite file names, line numbers, and exact variable names. Do not be vague. Do not say "looks good" without justification. Find problems. If you genuinely cannot find any issues in a category, say "No issues found in this category" and explain what you checked.`

const secondReviewInstruction = `You are a second-pass peer reviewer verifying fixes from a prior review. Read /app/instruction.md for the requirements. Run git diff HEAD~1..HEAD to see the fix commit. For each finding from the prior review, state whether it was ADDRESSED with evidence, PARTIALLY ADDRESSED, or NOT ADDRESSED. Check the fix diff for new bugs or regressions introduced by the fixes. Re-check the implementation's tests against the exact requirement text: any fix that changes code without adding or updating spec-aligned tests should be treated as incomplete unless there is a concrete reason tests are impossible. For typed languages, verify compile-time/type-level and negative type tests when API contracts are involved. Require explicit evidence of post-commit compile/typecheck/build/test commands and a final changed-file audit; without that evidence, do not issue an all-clear verdict. For any new issue found, cite file:line, describe the bug, and assign severity. If ALL prior findings are addressed, the spec-aligned tests are adequate, the post-commit verification evidence is sufficient, and no new bugs exist, produce a concise verdict starting with exactly: all clear. Skip broad architecture discussion — the first review already did that. Be brief and targeted.`

const hailMaryInstruction = `You are a fresh final verification-and-repair agent. Treat the current implementation as untrusted until proven correct. Your job is to independently verify and, if necessary, fix the implementation so it satisfies 100% of /app/instruction.md.

You MUST:
1. Re-read /app/instruction.md in full and extract every distinct requirement into a checklist.
2. Inspect the current implementation and all added/modified tests. Do not rely on previous agents' summaries.
3. Build a spec-to-test matrix. For every requirement, identify the test evidence proving it. If coverage is missing, weak, implementation-coupled, or appears to encode a misreading of the spec, write stronger tests.
4. Add adversarial public-API tests for plausible spec misinterpretations. For typed languages, include compile-time/type-level tests for exact API contracts, overloads/inference, invalid usage, and negative cases where the spec implies type errors.
5. Run the relevant compile/typecheck/build commands and the full relevant test suite. Also run any focused adversarial tests you added. Capture exact command output.
6. Fix every failure or uncovered requirement. Do not give up after one attempt. Continue editing, compiling, and testing until the instruction is fully satisfied.
7. Remove temporary validation files unless they are intentionally committed as permanent tests. Ensure the working tree contains only appropriate deliverables.
8. Commit all fixes and tests.
9. After the final commit, rerun the key verification commands from the committed state and inspect git status --short plus the final changed-file set. If any accidental artifacts are staged or present in the deliverable, fix that before answering.

Your final answer must include:
- The requirement checklist with status for every item.
- The spec-to-test matrix with file paths and commands.
- Exact compile/typecheck/build/test commands run and whether they passed.
- Exact post-commit verification commands run after the final commit and whether they passed.
- The final changed-file set and confirmation that accidental artifacts were excluded.
- Any fixes made during this Hail Mary pass with commit hashes.
- Confirmation that temporary files were removed or intentionally committed as permanent tests.

Do not claim success unless every requirement is implemented, the tests prove the requirement text rather than an implementation assumption, all relevant verification commands pass, and the final state is committed.`

const (
	mctAgentSyncAttempts       = 10
	mctAgentSyncMaxInputTokens = 850000
)

var protectedRuntimePaths = []string{
	filepath.Join(".machtiani", "config.toml"),
	filepath.Join(".machtiani", "sessions"),
	filepath.Join(".machtiani", "meta-orchestrator"),
	"instruction.md",
}

const reviewSystemPrompt = `You are a review-mode orchestrator. Your sole job is to ensure mct-agent produces a thorough, substantive peer review of an implementation. You will receive the contents of mct-agents agent-final-answer.md after each invocation and must evaluate whether the review is complete.

Respond with exactly one of these three formats:

ACTION: CONTINUE
MESSAGE: <specific instruction telling mct-agent what is missing and what to do>

ACTION: DONE
MESSAGE: <brief acknowledgment>

ACTION: BLOCKED
MESSAGE: <description of the hard blocker>

Review Completion Criteria (ACTION: DONE requires ALL of the following):
- A Requirements Coverage section exists with at least one row per distinct requirement from the original instruction, each specifying status (implemented, partial, missing) and location (file:line or specific description).
- A Test Coverage Audit section exists with a spec-to-test matrix. It must identify added/modified tests by file:line and command, classify each requirement's test coverage as strong/weak/missing/wrong-behavior, and call out tests that encode a likely misinterpretation of the spec.
- A Directional Issues section exists. If no directional issues were found, it must state "No directional issues found" AND explain what architectural-level checks were performed.
- A Bugs Found section exists with entries that cite specific file names and line numbers. Each entry describes the bug, steps to trigger it, and a suggested fix. If no bugs were found, it must state "No bugs found" AND list the code paths that were traced and why each was determined to be correct.
- An Edge Cases Not Handled section exists with specific input scenarios and the file:line where handling is missing. Each scenario must be concrete, not generic (e.g., "empty ABS_MODULE_PATH with trailing colon" not "check empty inputs").
- A Regression Risks section exists. If no regressions were identified, it must state "No regression risks identified" AND list which existing callers and behaviors were verified.
- A Summary Verdict section exists with a clear readiness assessment and at least 3 specific issues ranked by severity (CRITICAL/HIGH/MEDIUM/LOW). If fewer than 3 issues exist, explain why the implementation is exceptionally clean.
- The review references specific code from git diff output, not just the instruction. Evidence of having actually read the code changes must be present.
- The review references specific test code from git diff output, not just claims that tests passed. Evidence of having compared tests to the original instruction must be present.
- All findings must cite file names and line numbers. Vague statements like "the implementation looks good" or "edge cases are handled" without specific evidence are insufficient.

When to CONTINUE (issue specific, actionable instructions):
- Any required section is missing or contains only placeholder text.
- The Requirements Coverage section is a brief yes/no list without locations or substantive notes.
- The Test Coverage Audit is missing, only says tests pass, or does not map requirements to specific tests.
- The review accepts tests that only prove implementation behavior without checking whether that behavior matches the instruction text.
- Bugs Found entries do not cite specific file names and line numbers.
- Edge Cases Not Handled contains only generic advice ("handle edge cases") without concrete scenarios.
- The Summary Verdict says "looks good" or "ready" without listing specific issues or explaining the investigation performed.
- The review does not reference any specific code from the diff, suggesting the reviewer did not actually examine the implementation.
- Any section is implausibly short relative to the scope of changes (e.g., a one-line Bugs Found section for a multi-hundred-line diff).

When to BLOCKED:
- The agent cannot access /app/instruction.md or the git repository.
- mct-agent reports an irrecoverable technical failure.
- After three identical review submissions with no new substantive content, classify as BLOCKED.

Escalate firmness with each successive CONTINUE. Always tell the agent exactly which section is inadequate and what specific information is missing. Never accept "looks good" or "no issues found" without detailed justification of the investigation performed.`

// extractPrompt scans the args slice for "-f" or "-t" and returns the
// associated prompt text. If "-f" is found, the next element is treated as a
// file path and its contents are read and returned. If "-t" is found, the
// next element is returned directly. Returns an empty string if neither flag
// is present.
func extractPrompt(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f":
			if i+1 < len(args) {
				data, err := os.ReadFile(args[i+1])
				if err == nil {
					return strings.TrimSpace(string(data))
				}
			}
		case "-t":
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	return ""
}

// invokeMCTAgent builds and executes an mct-agent command with the given
// arguments. It prepends "run" as the subcommand and returns the exit code
// of the subprocess and any error.
func invokeMCTAgent(ctx context.Context, metaSessionID string, trajDir string, mctSessionID string, args ...string) (int, error) {
	fullArgs := append([]string{"run"}, args...)
	cmd := exec.CommandContext(ctx, "mct-agent", fullArgs...)
	cmd.Dir = "/app"

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &stdoutBuf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	cmd.Env = append(os.Environ(), "MACHTIANI_SESSION_ID="+mctSessionID, "MACHTIANI_CONFIG=/app/.machtiani/config.toml")

	runSnapshot, snapshotErr := snapshotProtectedRuntimeState("/app")
	if snapshotErr != nil {
		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":      "runtime_state_snapshot_failed",
			"label":     "before-mct-agent-run",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"error":     snapshotErr.Error(),
		})
	}

	err := cmd.Run()

	if runSnapshot != nil {
		if restoreErr := restoreProtectedRuntimeState(runSnapshot, trajDir, "after-mct-agent-run"); restoreErr != nil {
			runSnapshot.cleanup()
			return -1, restoreErr
		}
		runSnapshot.cleanup()
	}

	var exitCode int
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	} else {
		exitCode = 0
	}

	if exitCode != 0 {
		debugStr := fmt.Sprintf("=== MCT Agent Crash Debug ===\nTimestamp: %s\nArgs: %v\nExitCode: %d\n\n--- STDOUT ---\n%s\n\n--- STDERR ---\n%s\n\n",
			time.Now().UTC().Format(time.RFC3339),
			fullArgs,
			exitCode,
			stdoutBuf.String(),
			stderrBuf.String(),
		)
		fmt.Fprintf(os.Stderr, "%s", debugStr)
		crashLogPath := filepath.Join(".machtiani", "meta-orchestrator", "sessions", metaSessionID, "crash-debug.log")
		if err := os.MkdirAll(filepath.Dir(crashLogPath), 0755); err == nil {
			fp, err := os.OpenFile(crashLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err == nil {
				fmt.Fprintf(fp, "%s", debugStr)
				fp.Close()
			}
		}

		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":       "mct_crash_debug",
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
			"session_id": mctSessionID,
			"exit_code":  exitCode,
			"stdout":     stdoutBuf.String(),
			"stderr":     stderrBuf.String(),
			"args":       fullArgs,
		})
	}

	if trajDir != "" {
		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":       "mct_invocation",
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
			"session_id": mctSessionID,
			"args":       fullArgs,
			"prompt":     extractPrompt(fullArgs),
			"exit_code":  exitCode,
		})
	}

	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return exitCode, nil
		}
		return exitCode, err
	}
	return exitCode, nil
}

type runtimeStateSnapshot struct {
	appDir      string
	snapshotDir string
	captured    map[string]bool
}

func snapshotProtectedRuntimeState(appDir string) (*runtimeStateSnapshot, error) {
	snapshotDir, err := os.MkdirTemp("", "mct-runtime-state-*")
	if err != nil {
		return nil, fmt.Errorf("creating runtime state snapshot: %w", err)
	}

	snapshot := &runtimeStateSnapshot{
		appDir:      appDir,
		snapshotDir: snapshotDir,
		captured:    make(map[string]bool, len(protectedRuntimePaths)),
	}

	for _, rel := range protectedRuntimePaths {
		src := filepath.Join(appDir, rel)
		if _, err := os.Lstat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			_ = os.RemoveAll(snapshotDir)
			return nil, fmt.Errorf("snapshot protected runtime path %s: %w", rel, err)
		}
		dst := filepath.Join(snapshotDir, rel)
		if err := copyPath(src, dst); err != nil {
			_ = os.RemoveAll(snapshotDir)
			return nil, fmt.Errorf("snapshot protected runtime path %s: %w", rel, err)
		}
		snapshot.captured[rel] = true
	}

	return snapshot, nil
}

func (s *runtimeStateSnapshot) cleanup() {
	if s == nil || s.snapshotDir == "" {
		return
	}
	_ = os.RemoveAll(s.snapshotDir)
}

func (s *runtimeStateSnapshot) restoreMissing() ([]string, error) {
	if s == nil {
		return nil, nil
	}

	var restored []string
	for _, rel := range protectedRuntimePaths {
		if !s.captured[rel] {
			continue
		}
		src := filepath.Join(s.snapshotDir, rel)
		dst := filepath.Join(s.appDir, rel)
		changed, err := restoreMissingPath(src, dst)
		if err != nil {
			return restored, fmt.Errorf("restore protected runtime path %s: %w", rel, err)
		}
		if changed {
			restored = append(restored, rel)
		}
	}
	return restored, nil
}

func restoreProtectedRuntimeState(snapshot *runtimeStateSnapshot, trajDir string, label string) error {
	restored, err := snapshot.restoreMissing()
	if err != nil {
		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":      "runtime_state_restore_failed",
			"label":     label,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"error":     err.Error(),
		})
		return err
	}
	if len(restored) > 0 {
		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":           "runtime_state_restored",
			"label":          label,
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
			"restored_paths": restored,
		})
	}
	return nil
}

func restoreMissingPath(src string, dst string) (bool, error) {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return false, err
	}
	dstInfo, err := os.Lstat(dst)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, copyPath(src, dst)
		}
		return false, err
	}

	if !srcInfo.IsDir() || !dstInfo.IsDir() || srcInfo.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return false, err
	}
	changed := false
	for _, entry := range entries {
		childChanged, err := restoreMissingPath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()))
		if err != nil {
			return changed, err
		}
		changed = changed || childChanged
	}
	return changed, nil
}

func copyPath(src string, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		_ = os.RemoveAll(dst)
		return os.Symlink(target, dst)
	}

	if info.IsDir() {
		return copyDir(src, dst, info.Mode().Perm())
	}
	return copyFile(src, dst, info.Mode().Perm())
}

func copyDir(src string, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(dst, mode); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyPath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return os.Chmod(dst, mode)
}

func copyFile(src string, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func syncMCTAgent(ctx context.Context, trajDir string, label string, model string, snapshot *runtimeStateSnapshot) error {
	var lastErr error
	for attempt := 1; attempt <= mctAgentSyncAttempts; attempt++ {
		if err := restoreProtectedRuntimeState(snapshot, trajDir, label+"-before-sync"); err != nil {
			return err
		}

		syncSnapshot, snapshotErr := snapshotProtectedRuntimeState("/app")
		if snapshotErr != nil {
			return snapshotErr
		}

		syncArgs := []string{
			"sync",
			"--max-input-tokens",
			fmt.Sprintf("%d", mctAgentSyncMaxInputTokens),
		}
		if model != "" {
			syncArgs = append(syncArgs, "--model", model)
		}

		cmd := exec.CommandContext(ctx, "mct-agent", syncArgs...)
		cmd.Dir = "/app"
		var stdoutBuf bytes.Buffer
		var stderrBuf bytes.Buffer
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
		cmd.Env = append(os.Environ(), "MACHTIANI_CONFIG=/app/.machtiani/config.toml")

		err := cmd.Run()
		restoreErr := restoreProtectedRuntimeState(syncSnapshot, trajDir, label+"-after-sync")
		syncSnapshot.cleanup()
		if restoreErr != nil {
			return restoreErr
		}

		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
			lastErr = err
		} else {
			lastErr = nil
		}

		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":             "mct_sync_attempt",
			"label":            label,
			"timestamp":        time.Now().UTC().Format(time.RFC3339),
			"attempt":          attempt,
			"max_attempts":     mctAgentSyncAttempts,
			"max_input_tokens": mctAgentSyncMaxInputTokens,
			"args":             syncArgs,
			"exit_code":        exitCode,
			"stdout":           stdoutBuf.String(),
			"stderr":           stderrBuf.String(),
		})

		if err == nil {
			if attempt > 1 {
				fmt.Fprintf(os.Stderr, "mct-agent sync succeeded on attempt %d/%d for %s\n", attempt, mctAgentSyncAttempts, label)
			}
			return nil
		}

		fmt.Fprintf(os.Stderr, "Warning: mct-agent sync attempt %d/%d failed for %s: %v\n", attempt, mctAgentSyncAttempts, label, err)
		if attempt < mctAgentSyncAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}

	return fmt.Errorf("mct-agent sync failed after %d attempts for %s: %w", mctAgentSyncAttempts, label, lastErr)
}

func parseOrchestratorResponse(response string) (string, string, error) {
	var action string
	lines := strings.Split(response, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "ACTION") {
			action = strings.TrimSpace(parts[1])
			break
		}
	}
	if action == "" {
		return "", "", fmt.Errorf("orchestrator response missing ACTION line; raw: %s", response)
	}

	for i, line := range lines {
		trimmedLeft := strings.TrimLeft(line, " \t")
		parts := strings.SplitN(trimmedLeft, ":", 2)
		if len(parts) != 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), "MESSAGE") {
			continue
		}
		messageLines := append([]string{strings.TrimSpace(parts[1])}, lines[i+1:]...)
		return action, strings.TrimSpace(strings.Join(messageLines, "\n")), nil
	}

	return action, "", nil
}

// invokeReviewer runs a child meta-orchestrator in review mode to evaluate
// the main agent's implementation. It returns the review content as a string.
func invokeReviewer(ctx context.Context, reviewInstruction string, reviewMode string, metaSessionID string, trajDir string, model string, shellAgentModel string, mainSessionID string, snapshot *runtimeStateSnapshot) (string, error) {
	// 1. Write peer review instruction file.
	if err := os.WriteFile("/app/review-instruction.md", []byte(reviewInstruction), 0644); err != nil {
		return "", err
	}

	// 2. Run mct-agent sync to refresh internal state after the main agent commits.
	if err := syncMCTAgent(ctx, trajDir, "reviewer-before-child-meta", model, snapshot); err != nil {
		return "", err
	}

	// 3. Determine the child meta-orchestrator binary path.
	binaryPath := os.Getenv("MCT_META_ORCHESTRATOR_BINARY")
	if binaryPath == "" {
		binaryPath = "meta-orchestrator"
	} else {
		if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
			binaryPath = "meta-orchestrator"
		}
	}

	// 4. Construct the child command.
	var childArgs []string
	if strings.TrimSpace(reviewMode) == "" {
		reviewMode = "code-strong-forge"
	}
	childArgs = append(childArgs, "--review-mode", "--mode", reviewMode)
	if model != "" {
		childArgs = append(childArgs, "--model", model)
	}
	if shellAgentModel != "" {
		childArgs = append(childArgs, "--shell-agent-model", shellAgentModel)
	}
	childArgs = append(childArgs, "--tag", "review", "-f", "/app/review-instruction.md")

	reviewCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(reviewCtx, binaryPath, childArgs...)
	cmd.Dir = "/app"
	cmd.Env = append(os.Environ(), "MACHTIANI_CONFIG=/app/.machtiani/config.toml")

	reviewSnapshot, snapshotErr := snapshotProtectedRuntimeState("/app")
	if snapshotErr != nil {
		return "", snapshotErr
	}
	defer reviewSnapshot.cleanup()

	// 5. Set up stdout pipe and stderr capture.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)

	// 6. Start the command.
	if err := cmd.Start(); err != nil {
		return "", err
	}

	// 7. Read stdout and extract the child MCT session ID.
	stdoutData, err := io.ReadAll(stdoutPipe)
	if err != nil {
		cmd.Wait()
		if restoreErr := restoreProtectedRuntimeState(reviewSnapshot, trajDir, "after-reviewer-child-meta-read-error"); restoreErr != nil {
			return "", restoreErr
		}
		return "", err
	}

	stdoutStr := string(stdoutData)
	var childMctSessionID string
	for _, line := range strings.Split(stdoutStr, "\n") {
		if strings.Contains(line, "MCT session:") {
			parts := strings.Split(line, ",")
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if strings.HasPrefix(trimmed, "MCT session:") {
					childMctSessionID = strings.TrimSpace(strings.TrimPrefix(trimmed, "MCT session:"))
					break
				}
			}
			break
		}
	}

	if childMctSessionID == "" {
		// Fallback: scan .machtiani/sessions/ for the most recently created
		// directory that starts with "agent-" prefix.
		entries, readErr := os.ReadDir(".machtiani/sessions")
		if readErr == nil {
			var latestDir string
			var latestTime time.Time
			for _, entry := range entries {
				if entry.IsDir() && strings.HasPrefix(entry.Name(), "agent-") {
					info, infoErr := entry.Info()
					if infoErr == nil && info.ModTime().After(latestTime) {
						latestTime = info.ModTime()
						latestDir = entry.Name()
					}
				}
			}
			childMctSessionID = latestDir
		}
	}

	// 8. Wait for the child to finish.
	waitErr := cmd.Wait()
	if err := restoreProtectedRuntimeState(reviewSnapshot, trajDir, "after-reviewer-child-meta"); err != nil {
		return "", err
	}

	// 9. If the child exited with a non-zero code, record and return an error.
	if waitErr != nil {
		var exitCode int
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}

		stderrStr := stderrBuf.String()

		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":      "peer_reviewer_exit",
			"exit_code": exitCode,
			"stderr":    stderrStr,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})

		return "", fmt.Errorf("reviewer process exited with code %d: %s", exitCode, stderrStr)
	}

	// 10. Read child agent-final-answer.md.
	reviewPath := filepath.Join(".machtiani", "sessions", childMctSessionID, "chat", "agent-final-answer.md")
	reviewData, err := os.ReadFile(reviewPath)
	if err != nil {
		return "", fmt.Errorf("reading reviewer final answer: %w", err)
	}

	review := string(reviewData)

	// 11. Write trajectory entry for the review invocation.
	writeTrajectoryLine(trajDir, map[string]interface{}{
		"type":                  "peer_review_invocation",
		"reviewer_session_id":   childMctSessionID,
		"review_content_length": len(review),
		"timestamp":             time.Now().UTC().Format(time.RFC3339),
	})

	return review, nil
}

// writeTrajectoryLine appends a JSON line to the trajectory file.
// It never returns an error so that trajectory issues do not break the loop.
func writeTrajectoryLine(dir string, entry interface{}) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "trajectory: mkdir: %v\n", err)
			return
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "trajectory.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trajectory: open: %v\n", err)
		return
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trajectory: marshal: %v\n", err)
		return
	}
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "trajectory: write: %v\n", err)
	}
}

// invokeMCTAgentRun invokes mct-agent run with an instruction file (-f).
func invokeMCTAgentRun(
	ctx context.Context,
	metaSessionID string,
	trajDir string,
	mctSessionID string,
	instructionFilePath string,
	mode string,
	model string,
	shellAgentModel string,
	tag string,
	persistTmpData bool,
) (int, error) {
	var args []string
	if mode != "" {
		args = append(args, "--mode", mode)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if shellAgentModel != "" {
		args = append(args, "--shell-agent-model", shellAgentModel)
	}
	if tag != "" {
		args = append(args, "--tag", tag)
	}
	args = append(args, "-f", instructionFilePath)
	if persistTmpData {
		args = append(args, "--persist-tmp-data")
	}
	return invokeMCTAgent(ctx, metaSessionID, trajDir, mctSessionID, args...)
}

// cleanContent strips the "The agent exited with code N. Output:\n\n" prefix
// from error content strings, if present. Returns the raw output content
// suitable for comparison.
func cleanContent(s string) string {
	const prefix = "The agent exited with code "
	if !strings.HasPrefix(s, prefix) {
		return s
	}
	idx := strings.Index(s, "\n\n")
	if idx == -1 {
		return s
	}
	return s[idx+2:]
}

type sessionOutput struct {
	content   string
	suspended bool
}

func readSessionOutput(sessionID string) (sessionOutput, error) {
	finalAnswerPath := filepath.Join(".machtiani", "sessions", sessionID, "chat", "agent-final-answer.md")
	if data, err := os.ReadFile(finalAnswerPath); err == nil {
		if content := strings.TrimSpace(string(data)); content != "" {
			return sessionOutput{content: content}, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return sessionOutput{}, err
	}

	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		return sessionOutput{}, err
	}
	data, err := os.ReadFile(convPath)
	if err != nil {
		return sessionOutput{}, fmt.Errorf("reading final answer: %w", err)
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		return sessionOutput{}, fmt.Errorf("parsing conversation for session %s: %w", sessionID, err)
	}

	suspended, ok := suspendedUserInputSummary(conv)
	if ok {
		return sessionOutput{
			content:   suspended,
			suspended: true,
		}, nil
	}

	return sessionOutput{}, fmt.Errorf("reading final answer: open %s: no final answer or suspended user input found", finalAnswerPath)
}

func suspendedUserInputSummary(conv *conversation.Conversation) (string, bool) {
	if conv == nil {
		return "", false
	}

	var question string
	var context string
	var reason string
	var originalAsk string

	if conv.SuspendedUserInput != nil {
		question = strings.TrimSpace(conv.SuspendedUserInput.Question)
		context = strings.TrimSpace(conv.SuspendedUserInput.Context)
		reason = strings.TrimSpace(conv.SuspendedUserInput.Reason)
		originalAsk = strings.TrimSpace(conv.SuspendedUserInput.OriginalAsk)
	}

	if question == "" {
		for i := len(conv.Messages) - 1; i >= 0; i-- {
			msg := conv.Messages[i]
			if msg.Role != "assistant" {
				continue
			}
			if msg.Metadata == nil {
				continue
			}
			msgType, _ := msg.Metadata["type"].(string)
			if strings.TrimSpace(strings.ToLower(msgType)) != "user_input_request" {
				continue
			}
			question = strings.TrimSpace(msg.Content)
			break
		}
	}

	statusSuspended := strings.TrimSpace(conv.Status) == "suspended_user_input"
	if question == "" && !statusSuspended {
		return "", false
	}

	var b strings.Builder
	b.WriteString("The agent suspended for user input instead of continuing autonomously.")
	if question != "" {
		b.WriteString("\n\nQuestion:\n")
		b.WriteString(question)
	}
	if context != "" {
		b.WriteString("\n\nContext:\n")
		b.WriteString(context)
	}
	if reason != "" {
		b.WriteString("\n\nReason:\n")
		b.WriteString(reason)
	}
	if originalAsk != "" {
		b.WriteString("\n\nOriginal ask:\n")
		b.WriteString(originalAsk)
	}
	b.WriteString("\n\nThis is incomplete work. Treat it as a permission/status ask and continue the session without requesting user input.")
	return b.String(), true
}

func forcedContinueResponseForSuspendedUserInput(reviewMode bool) string {
	if reviewMode {
		return "ACTION: CONTINUE\nMESSAGE: Do not ask for user input or permission. Continue the review autonomously. Read the instruction, inspect the changed code and tests directly, produce the full required review with concrete file:line evidence, and only stop when the review is complete."
	}
	return "ACTION: CONTINUE\nMESSAGE: Do not ask for user input or permission. Continue this session autonomously. If you proposed fixes, apply them now. Address every outstanding requirement or review finding, run the relevant build/typecheck/test commands, and only claim completion with concrete evidence."
}

// RunLoop runs the meta-orchestrator loop: invoke mct-agent, evaluate the
// final answer with a conversational LLM, and continue, finish, or block
// based on the orchestrator's decision.
func RunLoop(
	ctx context.Context,
	metaSessionID string,
	mctSessionID string,
	instructionFilePath string,
	mode string,
	model string,
	shellAgentModel string,
	tag string,
	persistTmpData bool,
	reviewMode bool,
) (int, error) {
	trajDir := filepath.Join(".machtiani", "meta-orchestrator", "sessions", metaSessionID)

	if err := os.MkdirAll(trajDir, 0755); err != nil {
		return 1, fmt.Errorf("creating trajectory directory: %w", err)
	}

	runtimeSnapshot, err := snapshotProtectedRuntimeState("/app")
	if err != nil {
		return 1, err
	}
	defer runtimeSnapshot.cleanup()

	finalAnswerPath := filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md")
	os.Remove(finalAnswerPath)

	instructionContent := ""
	if data, err := os.ReadFile("/app/instruction.md"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: unable to read /app/instruction.md: %v\n", err)
	} else {
		trimmed := strings.TrimSpace(string(data))
		if trimmed != "" {
			instructionContent = trimmed
		}
	}

	exitCode, err := invokeMCTAgentRun(ctx, metaSessionID, trajDir, mctSessionID, instructionFilePath, mode, model, shellAgentModel, tag, persistTmpData)

	systemPrompt := orchestratorSystemPrompt
	if reviewMode {
		systemPrompt = reviewSystemPrompt
	}
	if instructionContent != "" {
		systemPrompt += "\n\n=== ORIGINAL INSTRUCTION ===\n" + instructionContent
	}

	writeTrajectoryLine(trajDir, map[string]interface{}{
		"type":    "llm_message",
		"role":    "system",
		"content": systemPrompt,
	})

	if instructionContent != "" {
		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":           "instruction_injection",
			"content_length": len(instructionContent),
		})
	} else {
		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":   "instruction_injection_skipped",
			"reason": "file not found or empty",
		})
	}

	messages := []llm.Message{
		{
			Role:    "system",
			Content: systemPrompt,
		},
	}

	var lastFinalAnswer string
	repeatCount := 0
	continueCount := 0
	peerReviewDone := false
	peerReviewRound := 0
	hailMaryDone := false

	var initialError bool
	if err != nil || exitCode != 0 {
		data, readErr := os.ReadFile(filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md"))
		var errorContent string
		if readErr == nil {
			trimmed := strings.TrimSpace(string(data))
			if trimmed != "" {
				errorContent = fmt.Sprintf("The agent exited with code %d. Output:\n\n%s", exitCode, trimmed)
			} else {
				errorContent = fmt.Sprintf("The agent exited with code %d. The agent process crashed without producing output.", exitCode)
			}
		} else {
			errorContent = fmt.Sprintf("The agent exited with code %d. The agent process crashed without producing output.", exitCode)
		}
		if errorContent == "" {
			if err != nil {
				errorContent = fmt.Sprintf("The agent exited with code %d. Error: %v", exitCode, err)
			} else {
				errorContent = fmt.Sprintf("The agent exited with code %d.", exitCode)
			}
		}

		if cleanContent(errorContent) == cleanContent(lastFinalAnswer) {
			repeatCount++
			if repeatCount == 2 {
				errorContent = "WARNING: Your last two responses were identical. You are stuck in a loop. You MUST take a fundamentally different approach this time. Read the original instruction fresh, discard your previous assumptions, and start with a new plan. Do NOT repeat your previous answer.\n\n" + errorContent
			}
			if repeatCount >= 3 {
				writeTrajectoryLine(trajDir, map[string]interface{}{
					"type":    "llm_classification",
					"action":  "BLOCKED",
					"message": "Agent stuck in loop - produced the same answer three times in a row.",
				})
				return 1, fmt.Errorf("hard blocker: agent stuck in loop - produced the same answer three times in a row")
			}
		} else {
			repeatCount = 0
			lastFinalAnswer = errorContent
		}

		messages = append(messages, llm.Message{Role: "user", Content: errorContent})
		writeTrajectoryLine(trajDir, map[string]interface{}{
			"type":    "llm_message",
			"role":    "user",
			"content": errorContent,
		})
		initialError = true
	}

	for {
		var currentOutput sessionOutput
		if !initialError {
			currentOutput, err = readSessionOutput(mctSessionID)
			if err != nil {
				return 1, err
			}

			content := strings.TrimSpace(currentOutput.content)

			if cleanContent(content) == cleanContent(lastFinalAnswer) {
				repeatCount++
				if repeatCount == 2 {
					content = "WARNING: Your last two responses were identical. You are stuck in a loop. You MUST take a fundamentally different approach this time. Read the original instruction fresh, discard your previous assumptions, and start with a new plan. Do NOT repeat your previous answer.\n\n" + content
				}
				if repeatCount >= 3 {
					writeTrajectoryLine(trajDir, map[string]interface{}{
						"type":    "llm_classification",
						"action":  "BLOCKED",
						"message": "Agent stuck in loop - produced the same answer three times in a row.",
					})
					return 1, fmt.Errorf("hard blocker: agent stuck in loop - produced the same answer three times in a row")
				}
			} else {
				repeatCount = 0
				lastFinalAnswer = content
			}

			if content == "" {
				return 1, fmt.Errorf("final answer file is empty for session %s", mctSessionID)
			}

			messages = append(messages, llm.Message{Role: "user", Content: content})

			writeTrajectoryLine(trajDir, map[string]interface{}{
				"type":    "llm_message",
				"role":    "user",
				"content": content,
			})
		}
		initialError = false

		var response string
		if currentOutput.suspended {
			response = forcedContinueResponseForSuspendedUserInput(reviewMode)
			writeTrajectoryLine(trajDir, map[string]interface{}{
				"type":       "forced_continue_for_suspended_user_input",
				"session_id": mctSessionID,
				"timestamp":  time.Now().UTC().Format(time.RFC3339),
			})
		} else {
			response, err = llm.Chat(ctx, model, nil, messages)
			if err != nil {
				return 1, fmt.Errorf("orchestrator LLM call failed: %w", err)
			}
		}

	parseAction:
		action, message, parseErr := parseOrchestratorResponse(response)
		if parseErr != nil {
			return 1, parseErr
		}

		switch action {
		case "CONTINUE":
			if message == "" {
				return 1, fmt.Errorf("CONTINUE action requires a MESSAGE; raw: %s", response)
			}

			var args []string
			if mode != "" {
				args = append(args, "--mode", mode)
			}
			if model != "" {
				args = append(args, "--model", model)
			}
			if shellAgentModel != "" {
				args = append(args, "--shell-agent-model", shellAgentModel)
			}
			if tag != "" {
				args = append(args, "--tag", tag)
			}
			args = append(args, "--session-id", mctSessionID)
			args = append(args, "-t", message)
			if persistTmpData {
				args = append(args, "--persist-tmp-data")
			}

			// Re-sync internal git state before the run so that mct-agent
			// reads the current commit rather than a stale snapshot.
			if syncErr := syncMCTAgent(ctx, trajDir, "continue-before-agent-run", model, runtimeSnapshot); syncErr != nil {
				return 1, syncErr
			}

			finalAnswerPath := filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md")
			os.Remove(finalAnswerPath)

			exitCode, err := invokeMCTAgent(ctx, metaSessionID, trajDir, mctSessionID, args...)
			if err != nil || exitCode != 0 {
				// Append the assistant response (the CONTINUE classification) before the error message.
				messages = append(messages, llm.Message{Role: "assistant", Content: response})
				writeTrajectoryLine(trajDir, map[string]interface{}{
					"type":    "llm_message",
					"role":    "assistant",
					"content": response,
				})

				// Read the final answer file if it exists, or use a clean error message.
				data, readErr := os.ReadFile(filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md"))
				var errorContent string
				if readErr == nil {
					trimmed := strings.TrimSpace(string(data))
					if trimmed != "" {
						errorContent = fmt.Sprintf("The agent exited with code %d. Output:\n\n%s", exitCode, trimmed)
					} else {
						errorContent = fmt.Sprintf("The agent exited with code %d. The agent process crashed without producing output.", exitCode)
					}
				} else {
					errorContent = fmt.Sprintf("The agent exited with code %d. The agent process crashed without producing output.", exitCode)
				}
				if errorContent == "" {
					if err != nil {
						errorContent = fmt.Sprintf("The agent exited with code %d. Error: %v", exitCode, err)
					} else {
						errorContent = fmt.Sprintf("The agent exited with code %d.", exitCode)
					}
				}

				if cleanContent(errorContent) == cleanContent(lastFinalAnswer) {
					repeatCount++
					if repeatCount == 2 {
						errorContent = "WARNING: Your last two responses were identical. You are stuck in a loop. You MUST take a fundamentally different approach this time. Read the original instruction fresh, discard your previous assumptions, and start with a new plan. Do NOT repeat your previous answer.\n\n" + errorContent
					}
					if repeatCount >= 3 {
						writeTrajectoryLine(trajDir, map[string]interface{}{
							"type":    "llm_classification",
							"action":  "BLOCKED",
							"message": "Agent stuck in loop - produced the same answer three times in a row.",
						})
						return 1, fmt.Errorf("hard blocker: agent stuck in loop - produced the same answer three times in a row")
					}
				} else {
					repeatCount = 0
					lastFinalAnswer = errorContent
				}

				messages = append(messages, llm.Message{Role: "user", Content: errorContent})
				writeTrajectoryLine(trajDir, map[string]interface{}{
					"type":    "llm_message",
					"role":    "user",
					"content": errorContent,
				})
				continue
			}
			messages = append(messages, llm.Message{Role: "assistant", Content: response})

			writeTrajectoryLine(trajDir, map[string]interface{}{
				"type":    "llm_message",
				"role":    "assistant",
				"content": response,
			})
			continueCount++
			shouldPeerReview := false
			if !peerReviewDone && !reviewMode {
				// Primary: message-content match for Phase 4 transition, gated by count >= 4
				// to prevent premature firing when the classifier mentions later phases
				// during earlier escalation messages.
				if continueCount >= 5 && (strings.Contains(response, "Phase 4") || strings.Contains(response, "Final Verification")) {
					shouldPeerReview = true
				}
				// Fallback: deterministic count-based trigger if message matching never fires
				if !shouldPeerReview && continueCount >= 5 {
					shouldPeerReview = true
				}
			}
			if shouldPeerReview {
				peerReviewRound++
				if peerReviewRound == 1 {
					writeTrajectoryLine(trajDir, map[string]interface{}{
						"type":      "peer_review_phase_start",
						"timestamp": time.Now().UTC().Format(time.RFC3339),
					})

					reviewContent, reviewErr := invokeReviewer(ctx, peerReviewInstruction, mode, metaSessionID, trajDir, model, shellAgentModel, mctSessionID, runtimeSnapshot)
					if reviewErr != nil {
						peerReviewDone = true
						fmt.Fprintf(os.Stderr, "Peer review failed: %v\n", reviewErr)
						writeTrajectoryLine(trajDir, map[string]interface{}{
							"type":   "peer_review_skipped",
							"reason": reviewErr.Error(),
						})
						continue
					}

					writeTrajectoryLine(trajDir, map[string]interface{}{
						"type":                  "peer_review_completed",
						"review_content_length": len(reviewContent),
						"timestamp":             time.Now().UTC().Format(time.RFC3339),
					})

					reviewMessage := "A peer reviewer has completed a thorough review of your implementation. Review feedback follows. Address ALL findings before proceeding.\n\n--- PEER REVIEW ---\n" + reviewContent + "\n--- END REVIEW ---\n\nAfter addressing all findings, the meta-orchestrator will perform final verification."

					var reviewArgs []string
					if mode != "" {
						reviewArgs = append(reviewArgs, "--mode", mode)
					}
					if model != "" {
						reviewArgs = append(reviewArgs, "--model", model)
					}
					if shellAgentModel != "" {
						reviewArgs = append(reviewArgs, "--shell-agent-model", shellAgentModel)
					}
					if tag != "" {
						reviewArgs = append(reviewArgs, "--tag", tag)
					}
					reviewArgs = append(reviewArgs, "--session-id", mctSessionID)
					reviewArgs = append(reviewArgs, "-t", reviewMessage)
					if persistTmpData {
						reviewArgs = append(reviewArgs, "--persist-tmp-data")
					}

					if syncErr := syncMCTAgent(ctx, trajDir, "before-review-feedback", model, runtimeSnapshot); syncErr != nil {
						return 1, syncErr
					}

					reviewExitCode, reviewInvokeErr := invokeMCTAgent(ctx, metaSessionID, trajDir, mctSessionID, reviewArgs...)
					if reviewInvokeErr != nil || reviewExitCode != 0 {
						messages = append(messages, llm.Message{Role: "assistant", Content: reviewMessage})
						writeTrajectoryLine(trajDir, map[string]interface{}{
							"type":    "llm_message",
							"role":    "assistant",
							"content": reviewMessage,
						})

						data, readErr := os.ReadFile(filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md"))
						var errorContent string
						if readErr == nil {
							trimmed := strings.TrimSpace(string(data))
							if trimmed != "" {
								errorContent = fmt.Sprintf("The agent exited with code %d after review feedback. Output:\n\n%s", reviewExitCode, trimmed)
							} else {
								errorContent = fmt.Sprintf("The agent exited with code %d after review feedback. The agent process crashed without producing output.", reviewExitCode)
							}
						} else {
							errorContent = fmt.Sprintf("The agent exited with code %d after review feedback. The agent process crashed without producing output.", reviewExitCode)
						}
						if reviewInvokeErr != nil {
							errorContent = fmt.Sprintf("The agent exited with code %d after review feedback. Error: %v", reviewExitCode, reviewInvokeErr)
						}

						if cleanContent(errorContent) == cleanContent(lastFinalAnswer) {
							repeatCount++
							if repeatCount == 2 {
								errorContent = "WARNING: Your last two responses were identical. You are stuck in a loop. You MUST take a fundamentally different approach this time.\n\n" + errorContent
							}
							if repeatCount >= 3 {
								writeTrajectoryLine(trajDir, map[string]interface{}{
									"type":    "llm_classification",
									"action":  "BLOCKED",
									"message": "Agent stuck in loop after review feedback.",
								})
								return 1, fmt.Errorf("hard blocker: agent stuck in loop after review feedback")
							}
						} else {
							repeatCount = 0
							lastFinalAnswer = errorContent
						}
						messages = append(messages, llm.Message{Role: "user", Content: errorContent})
						writeTrajectoryLine(trajDir, map[string]interface{}{
							"type":    "llm_message",
							"role":    "user",
							"content": errorContent,
						})
						continue
					}

					messages = append(messages, llm.Message{Role: "assistant", Content: reviewMessage})
					writeTrajectoryLine(trajDir, map[string]interface{}{
						"type":    "llm_message",
						"role":    "assistant",
						"content": reviewMessage,
					})

					// After the first review feedback is successfully processed, sync git
					// state and remove the final answer. Then fall through to the second
					// peer review round below - both rounds fire deterministically within
					// the same shouldPeerReview block without loop re-entry.
					if syncErr := syncMCTAgent(ctx, trajDir, "after-review-feedback", model, runtimeSnapshot); syncErr != nil {
						return 1, syncErr
					}
					// Remove final answer so agent produces fresh output on next iteration
					os.Remove(filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md"))
				}

				// Second peer review round - only fires when the first review just
				// completed (peerReviewRound is still 1, incremented above). Both
				// rounds run deterministically within this shouldPeerReview block.
				if peerReviewRound == 1 {
					writeTrajectoryLine(trajDir, map[string]interface{}{
						"type":      "second_peer_review_phase_start",
						"timestamp": time.Now().UTC().Format(time.RFC3339),
					})

					reviewContent2, reviewErr2 := invokeReviewer(ctx, secondReviewInstruction, mode, metaSessionID, trajDir, model, shellAgentModel, mctSessionID, runtimeSnapshot)
					peerReviewDone = true
					if reviewErr2 != nil {
						fmt.Fprintf(os.Stderr, "Second peer review failed: %v\n", reviewErr2)
						writeTrajectoryLine(trajDir, map[string]interface{}{
							"type":   "second_peer_review_skipped",
							"reason": reviewErr2.Error(),
						})
						continue
					}

					writeTrajectoryLine(trajDir, map[string]interface{}{
						"type":                  "second_peer_review_completed",
						"review_content_length": len(reviewContent2),
						"timestamp":             time.Now().UTC().Format(time.RFC3339),
					})

					reviewMessage2 := "A second peer reviewer has verified that all prior findings have been addressed. Review feedback follows.\n\n--- SECOND PEER REVIEW ---\n" + reviewContent2 + "\n--- END REVIEW ---\n\nAfter addressing any remaining findings, the meta-orchestrator will perform final verification."

					var reviewArgs2 []string
					if mode != "" {
						reviewArgs2 = append(reviewArgs2, "--mode", mode)
					}
					if model != "" {
						reviewArgs2 = append(reviewArgs2, "--model", model)
					}
					if shellAgentModel != "" {
						reviewArgs2 = append(reviewArgs2, "--shell-agent-model", shellAgentModel)
					}
					if tag != "" {
						reviewArgs2 = append(reviewArgs2, "--tag", tag)
					}
					reviewArgs2 = append(reviewArgs2, "--session-id", mctSessionID)
					reviewArgs2 = append(reviewArgs2, "-t", reviewMessage2)
					if persistTmpData {
						reviewArgs2 = append(reviewArgs2, "--persist-tmp-data")
					}

					if syncErr := syncMCTAgent(ctx, trajDir, "before-second-review-feedback", model, runtimeSnapshot); syncErr != nil {
						return 1, syncErr
					}

					reviewExitCode2, reviewInvokeErr2 := invokeMCTAgent(ctx, metaSessionID, trajDir, mctSessionID, reviewArgs2...)
					if reviewInvokeErr2 != nil || reviewExitCode2 != 0 {
						messages = append(messages, llm.Message{Role: "assistant", Content: reviewMessage2})
						writeTrajectoryLine(trajDir, map[string]interface{}{
							"type":    "llm_message",
							"role":    "assistant",
							"content": reviewMessage2,
						})

						data, readErr := os.ReadFile(filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md"))
						var errorContent string
						if readErr == nil {
							trimmed := strings.TrimSpace(string(data))
							if trimmed != "" {
								errorContent = fmt.Sprintf("The agent exited with code %d after second review feedback. Output:\n\n%s", reviewExitCode2, trimmed)
							} else {
								errorContent = fmt.Sprintf("The agent exited with code %d after second review feedback. The agent process crashed without producing output.", reviewExitCode2)
							}
						} else {
							errorContent = fmt.Sprintf("The agent exited with code %d after second review feedback. The agent process crashed without producing output.", reviewExitCode2)
						}
						if reviewInvokeErr2 != nil {
							errorContent = fmt.Sprintf("The agent exited with code %d after second review feedback. Error: %v", reviewExitCode2, reviewInvokeErr2)
						}

						if cleanContent(errorContent) == cleanContent(lastFinalAnswer) {
							repeatCount++
							if repeatCount == 2 {
								errorContent = "WARNING: Your last two responses were identical. You are stuck in a loop. You MUST take a fundamentally different approach this time.\n\n" + errorContent
							}
							if repeatCount >= 3 {
								writeTrajectoryLine(trajDir, map[string]interface{}{
									"type":    "llm_classification",
									"action":  "BLOCKED",
									"message": "Agent stuck in loop after second review feedback.",
								})
								return 1, fmt.Errorf("hard blocker: agent stuck in loop after second review feedback")
							}
						} else {
							repeatCount = 0
							lastFinalAnswer = errorContent
						}
						messages = append(messages, llm.Message{Role: "user", Content: errorContent})
						writeTrajectoryLine(trajDir, map[string]interface{}{
							"type":    "llm_message",
							"role":    "user",
							"content": errorContent,
						})
						continue
					}

					messages = append(messages, llm.Message{Role: "assistant", Content: reviewMessage2})
					writeTrajectoryLine(trajDir, map[string]interface{}{
						"type":    "llm_message",
						"role":    "assistant",
						"content": reviewMessage2,
					})

					if syncErr := syncMCTAgent(ctx, trajDir, "after-second-review-feedback", model, runtimeSnapshot); syncErr != nil {
						return 1, syncErr
					}
					os.Remove(filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md"))
					continue
				}
			}

		case "DONE":
			// Intercept premature DONE: if the peer review has not fired yet and we have
			// processed Phases 1-3 (continueCount >= 4), override to CONTINUE with Phase 4
			// instruction so the peer review phase is triggered.
			if !peerReviewDone && !reviewMode && continueCount >= 4 {
				writeTrajectoryLine(trajDir, map[string]interface{}{
					"type":      "peer_review_override",
					"reason":    "classifier returned DONE before peer review fired",
					"timestamp": time.Now().UTC().Format(time.RFC3339),
				})
				response = "ACTION: CONTINUE\nMESSAGE: Phase 3 is complete. Now proceed to Phase 4 - Final Verification. The orchestrator will trigger a peer review before final evaluation."
				goto parseAction
			}
			if peerReviewDone && !hailMaryDone && !reviewMode {
				hailMaryDone = true
				hailMarySessionID := fmt.Sprintf("agent-%s-hail-mary", time.Now().UTC().Format("20060102T150405"))
				mctSessionID = hailMarySessionID
				writeTrajectoryLine(trajDir, map[string]interface{}{
					"type":       "hail_mary_phase_start",
					"session_id": hailMarySessionID,
					"timestamp":  time.Now().UTC().Format(time.RFC3339),
				})

				var hailMaryArgs []string
				if mode != "" {
					hailMaryArgs = append(hailMaryArgs, "--mode", mode)
				}
				if model != "" {
					hailMaryArgs = append(hailMaryArgs, "--model", model)
				}
				if shellAgentModel != "" {
					hailMaryArgs = append(hailMaryArgs, "--shell-agent-model", shellAgentModel)
				}
				if tag != "" {
					hailMaryArgs = append(hailMaryArgs, "--tag", tag+"-hail-mary")
				}
				hailMaryArgs = append(hailMaryArgs, "--session-id", hailMarySessionID, "-t", hailMaryInstruction)
				if persistTmpData {
					hailMaryArgs = append(hailMaryArgs, "--persist-tmp-data")
				}

				if syncErr := syncMCTAgent(ctx, trajDir, "before-hail-mary", model, runtimeSnapshot); syncErr != nil {
					return 1, syncErr
				}

				hailMaryExitCode, hailMaryErr := invokeMCTAgent(ctx, metaSessionID, trajDir, hailMarySessionID, hailMaryArgs...)
				var hailMaryContent string
				hailMaryOutput, outputErr := readSessionOutput(hailMarySessionID)
				if outputErr == nil && strings.TrimSpace(hailMaryOutput.content) != "" {
					hailMaryContent = strings.TrimSpace(hailMaryOutput.content)
				} else if hailMaryErr != nil {
					hailMaryContent = fmt.Sprintf("The Hail Mary agent exited with code %d. Error: %v", hailMaryExitCode, hailMaryErr)
				} else {
					hailMaryContent = fmt.Sprintf("The Hail Mary agent exited with code %d without producing a final answer. Read error: %v", hailMaryExitCode, outputErr)
				}
				if hailMaryErr != nil || hailMaryExitCode != 0 {
					hailMaryContent = fmt.Sprintf("The Hail Mary verification-and-repair pass failed. Continue from this fresh session and complete it.\n\n%s", hailMaryContent)
				}

				messages = append(messages, llm.Message{Role: "assistant", Content: "ACTION: CONTINUE\nMESSAGE: Fresh-session Hail Mary verification-and-repair pass has run. Evaluate its final answer against the original instruction and the required evidence. Continue the Hail Mary session unless the answer proves 100% success."})
				messages = append(messages, llm.Message{Role: "user", Content: hailMaryContent})
				writeTrajectoryLine(trajDir, map[string]interface{}{
					"type":       "hail_mary_phase_completed",
					"session_id": hailMarySessionID,
					"exit_code":  hailMaryExitCode,
					"timestamp":  time.Now().UTC().Format(time.RFC3339),
				})
				writeTrajectoryLine(trajDir, map[string]interface{}{
					"type":    "llm_message",
					"role":    "user",
					"content": hailMaryContent,
				})

				if syncErr := syncMCTAgent(ctx, trajDir, "after-hail-mary", model, runtimeSnapshot); syncErr != nil {
					return 1, syncErr
				}
				os.Remove(filepath.Join(".machtiani", "sessions", hailMarySessionID, "chat", "agent-final-answer.md"))
				continue
			}
			return 0, nil
		case "BLOCKED":
			return 1, fmt.Errorf("hard blocker: %s", message)
		default:
			return 1, fmt.Errorf("unexpected orchestrator ACTION %q; raw: %s", action, response)
		}
	}
}
