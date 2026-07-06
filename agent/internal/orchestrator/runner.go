package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
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
   - Respond with ACTION: CONTINUE and explicitly instruct the agent to:
     a) Rigorously sanity-check its implementation to ensure it meets ALL original criteria of the issue.
     b) Find and run any relevant existing tests in the repository. If no relevant tests exist, create temporary test files to validate the implementation against the original criteria. Run the tests and fix the implementation until all pass. Then delete any temporary test files you created so they are not included in the final commit.
     c) Base this check strictly on the actual code and requirements—do not rely on its past answers, investigative work, or assumptions.
     d) Update and fix the implementation as needed.

3. **Phase 2 - The Reset and Re-Execution (Second Completion Claim):**
   - When the agent returns claiming the first sanity check and subsequent fixes are complete, DO NOT accept it as done.
   - Respond with ACTION: CONTINUE and instruct the agent to:
     a) Re-read the original instruction file (/app/instruction.md) to ground the re-implementation in the original requirements.
     b) Reset the git project completely and discard all current changes.
     c) Taking everything it learned from the first implementation attempt, create a brand new plan from scratch with an actionable checklist.
     d) Execute that plan fully and commit the new code.

4. **Phase 3 - The Final Sanity Check (Third Completion Claim):**
   - When the agent reports that the second implementation is finished, DO NOT accept it as done.
   - Respond with ACTION: CONTINUE and explicitly instruct the agent to:
     a) Perform a final, rigorous sanity check on this new implementation to ensure it perfectly matches the original criteria.
     b) Find and run any relevant existing tests in the repository. If no relevant tests exist, create temporary test files to validate the implementation against the original criteria. Run the tests and fix the implementation until all pass. Then delete any temporary test files you created so they are not included in the final commit.
     c) Again, base this check strictly on the code and requirements, not assumptions.
     d) Fix any final issues and commit the changes.

5. **Phase 4 - Final Verification:**
   - Only AFTER the agent has completed the Phase 3 "Final Sanity Check" and returns with the polished implementation, evaluate it for true completion.
   - a) Confirm no stray test files remain in the working tree. Delete any temporary test files that were created during validation.
   - b) If the deliverables are fully executed and complete, respond with ACTION: DONE.

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

Third, evaluate WIDE (directional correctness):
Does the implementation address every requirement in the instruction? List each requirement and whether it is implemented, partially implemented, or missing. Is the overall architecture sound? Are there design decisions that solve the immediate problem but create problems for future extension, maintainability, or correctness? Are there requirements that were interpreted in a way that differs from what the instruction clearly asks for? Flag any misinterpretations. Are there requirements that LOOK implemented (code exists) but do not actually work end-to-end because of how pieces connect or fail to connect?

Fourth, evaluate DEEP (bug and edge-case hunting):
Trace every code path introduced or modified. For each function, walk through every branch and identify inputs that would cause incorrect behavior, panics, or silent failures. Check path handling: are paths normalized? Can they be doubled, resolve outside the intended directory, or break on symlinks, relative paths, absolute paths, or nested directories? Check error handling: does every error path produce the correct error message? Are errors swallowed? Are error messages specific enough to be useful? Check ordering and precedence: where multiple config sources or flags interact, is the precedence correct in every combination? Check edge cases: empty inputs, nil values, zero-length slices, duplicate entries, circular references, already-loaded modules, concurrent access. Check that environment variable handling (reading, parsing, precedence, fallback) matches the specification exactly, including quoted entries, whitespace, and empty values. Check that any cache or state tracking is correct: keys are normalized, lookups are consistent, state transitions are valid, and stats or reporting reflect what actually happened.

Fifth, check for regressions:
Compare each modified file against its original version. Does any change break existing behavior that the instruction did not ask to change? Are there functions whose signatures or contracts changed in a way that existing callers would break?

Output format: produce a structured review with these sections:

1. Requirements Coverage: A table with columns requirement, status (implemented, partial, missing), location (file:line or description), and notes.
IMPORTANT: Do NOT change existing behavior for features that already work correctly. Only fix clear, unambiguous bugs that are supported by concrete evidence from the code. If a finding is ambiguous or could break existing functionality, note it as an observation but do NOT recommend changing the code — introducing regressions by altering correct behavior is worse than leaving a minor issue unaddressed.
2. Directional Issues: Any design-level concerns where the approach is wrong or fragile, even if individual pieces work.
3. Bugs Found: Each bug with severity (CRITICAL, HIGH, MEDIUM, LOW), file:line, description of the bug, steps to trigger it, and suggested fix.
4. Edge Cases Not Handled: Specific input scenarios that would cause incorrect behavior, with the expected versus actual behavior and the file:line where handling is missing.
5. Regression Risks: Any changes to existing behavior that could break current functionality, with file:line.
6. Summary Verdict: One paragraph stating whether this implementation is ready or needs another round of fixes. List the top 3 to 5 issues that must be fixed, ranked by severity.

Be specific. Cite file names, line numbers, and exact variable names. Do not be vague. Do not say "looks good" without justification. Find problems. If you genuinely cannot find any issues in a category, say "No issues found in this category" and explain what you checked.`

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
- A Directional Issues section exists. If no directional issues were found, it must state "No directional issues found" AND explain what architectural-level checks were performed.
- A Bugs Found section exists with entries that cite specific file names and line numbers. Each entry describes the bug, steps to trigger it, and a suggested fix. If no bugs were found, it must state "No bugs found" AND list the code paths that were traced and why each was determined to be correct.
- An Edge Cases Not Handled section exists with specific input scenarios and the file:line where handling is missing. Each scenario must be concrete, not generic (e.g., "empty ABS_MODULE_PATH with trailing colon" not "check empty inputs").
- A Regression Risks section exists. If no regressions were identified, it must state "No regression risks identified" AND list which existing callers and behaviors were verified.
- A Summary Verdict section exists with a clear readiness assessment and at least 3 specific issues ranked by severity (CRITICAL/HIGH/MEDIUM/LOW). If fewer than 3 issues exist, explain why the implementation is exceptionally clean.
- The review references specific code from git diff output, not just the instruction. Evidence of having actually read the code changes must be present.
- All findings must cite file names and line numbers. Vague statements like "the implementation looks good" or "edge cases are handled" without specific evidence are insufficient.

When to CONTINUE (issue specific, actionable instructions):
- Any required section is missing or contains only placeholder text.
- The Requirements Coverage section is a brief yes/no list without locations or substantive notes.
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

	err := cmd.Run()
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

// invokeReviewer runs a child meta-orchestrator in review mode to evaluate
// the main agent's implementation. It returns the review content as a string.
func invokeReviewer(ctx context.Context, metaSessionID string, trajDir string, model string, shellAgentModel string, mainSessionID string) (string, error) {
	// 1. Write peer review instruction file.
	if err := os.WriteFile("/app/review-instruction.md", []byte(peerReviewInstruction), 0644); err != nil {
		return "", err
	}

	// 2. Run mct-agent sync to refresh internal state after the main agent commits.
	if model != "" {
		syncCmd := exec.CommandContext(ctx, "mct-agent", "sync", "--model", model, "--max-input-tokens", "800000")
		syncCmd.Dir = "/app"
		syncCmd.Env = append(os.Environ(), "MACHTIANI_CONFIG=/app/.machtiani/config.toml")
		if err := syncCmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "review sync warning: %v\n", err)
		}
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
	childArgs = append(childArgs, "--review-mode", "--mode", "code-forge")
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

	finalAnswerPath := filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md")
	os.Remove(finalAnswerPath)

	exitCode, err := invokeMCTAgentRun(ctx, metaSessionID, trajDir, mctSessionID, instructionFilePath, mode, model, shellAgentModel, tag, persistTmpData)

	systemPrompt := orchestratorSystemPrompt
	if reviewMode {
		systemPrompt = reviewSystemPrompt
	}

	writeTrajectoryLine(trajDir, map[string]interface{}{
		"type":    "llm_message",
		"role":    "system",
		"content": systemPrompt,
	})

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
		if !initialError {
			data, err := os.ReadFile(filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md"))
			if err != nil {
				return 1, fmt.Errorf("reading final answer: %w", err)
			}

			content := strings.TrimSpace(string(data))

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

		response, err := llm.Chat(ctx, model, nil, messages)
		if err != nil {
			return 1, fmt.Errorf("orchestrator LLM call failed: %w", err)
		}

		var actionLine string
		var messageLine string
	parseAction:
		for _, line := range strings.Split(response, "\n") {
			trimmed := strings.TrimSpace(line)
			if actionLine == "" && strings.HasPrefix(strings.ToUpper(trimmed), "ACTION:") {
				actionLine = trimmed
			}
			if messageLine == "" && strings.HasPrefix(strings.ToUpper(trimmed), "MESSAGE:") {
				messageLine = trimmed
			}
		}

		if actionLine == "" {
			return 1, fmt.Errorf("orchestrator response missing ACTION line; raw: %s", response)
		}

		action := strings.TrimSpace(strings.TrimPrefix(actionLine, "ACTION:"))

		switch action {
		case "CONTINUE":
			message := strings.TrimSpace(strings.TrimPrefix(messageLine, "MESSAGE:"))
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
			var syncArgs []string
			syncArgs = append(syncArgs, "sync")
			if model != "" {
				syncArgs = append(syncArgs, "--model", model)
			}
			syncArgs = append(syncArgs, "--max-input-tokens", "800000")
			syncCmd := exec.CommandContext(ctx, "mct-agent", syncArgs...)
			syncCmd.Dir = "/app"
			syncCmd.Env = append(os.Environ(), "MACHTIANI_CONFIG=/app/.machtiani/config.toml")
			if syncErr := syncCmd.Run(); syncErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: mct-agent sync failed: %v\n", syncErr)
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
					writeTrajectoryLine(trajDir, map[string]interface{}{
						"type":      "peer_review_phase_start",
						"timestamp": time.Now().UTC().Format(time.RFC3339),
					})

					reviewContent, reviewErr := invokeReviewer(ctx, metaSessionID, trajDir, model, shellAgentModel, mctSessionID)
					peerReviewDone = true
					if reviewErr != nil {
						fmt.Fprintf(os.Stderr, "Peer review failed: %v\n", reviewErr)
						writeTrajectoryLine(trajDir, map[string]interface{}{
							"type":   "peer_review_skipped",
							"reason": reviewErr.Error(),
						})
						// Peer review failed, continue with normal Phase 4 flow below
					} else {
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

					var syncArgs []string
					syncArgs = append(syncArgs, "sync")
					if model != "" {
						syncArgs = append(syncArgs, "--model", model)
					}
					syncArgs = append(syncArgs, "--max-input-tokens", "800000")
					syncCmd := exec.CommandContext(ctx, "mct-agent", syncArgs...)
					syncCmd.Dir = "/app"
					syncCmd.Env = append(os.Environ(), "MACHTIANI_CONFIG=/app/.machtiani/config.toml")
					if syncErr := syncCmd.Run(); syncErr != nil {
						fmt.Fprintf(os.Stderr, "Warning: mct-agent sync before review feedback failed: %v\n", syncErr)
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

					// After review feedback is successfully processed, sync git state and
					// loop back so the classifier evaluates the agents post-review output.
					syncAfterReview := exec.CommandContext(ctx, "mct-agent", "sync")
					if model != "" {
						syncAfterReview = exec.CommandContext(ctx, "mct-agent", "sync", "--model", model, "--max-input-tokens", "800000")
					}
					syncAfterReview.Dir = "/app"
					syncAfterReview.Env = append(os.Environ(), "MACHTIANI_CONFIG=/app/.machtiani/config.toml")
					if syncErr := syncAfterReview.Run(); syncErr != nil {
						fmt.Fprintf(os.Stderr, "Warning: sync after review feedback failed: %v\n", syncErr)
					}
					// Remove final answer so agent produces fresh output on next iteration
					os.Remove(filepath.Join(".machtiani", "sessions", mctSessionID, "chat", "agent-final-answer.md"))
					continue
					}
					continue
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
				actionLine = ""
				messageLine = ""
				goto parseAction
			}
			return 0, nil
		case "BLOCKED":
			message := strings.TrimSpace(strings.TrimPrefix(messageLine, "MESSAGE:"))
			return 1, fmt.Errorf("hard blocker: %s", message)
		default:
			return 1, fmt.Errorf("unexpected orchestrator ACTION %q; raw: %s", action, response)
		}
	}
}
