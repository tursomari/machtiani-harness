package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
)

func TestParseOrchestratorResponse_MultilineMessage(t *testing.T) {
	response := "ACTION: CONTINUE\nMESSAGE: Phase 2 — The Reset and Re-Execution. You MUST now:\n\n" +
		"a) Re-read /app/instruction.md.\n" +
		"b) Reset the git project completely.\n" +
		"c) Create /app/implementation-plan.md and commit it."

	action, message, err := parseOrchestratorResponse(response)
	if err != nil {
		t.Fatalf("parseOrchestratorResponse returned error: %v", err)
	}
	if action != "CONTINUE" {
		t.Fatalf("action = %q, want CONTINUE", action)
	}
	for _, want := range []string{
		"Phase 2 — The Reset and Re-Execution. You MUST now:",
		"a) Re-read /app/instruction.md.",
		"b) Reset the git project completely.",
		"c) Create /app/implementation-plan.md and commit it.",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
}

func TestPhase2PromptProtectsRuntimeState(t *testing.T) {
	for _, want := range []string{
		"Reset tracked project source state only",
		"/app/.machtiani/",
		"/app/instruction.md",
		"active session state",
		"meta-orchestrator artifacts",
		"git clean -fd",
		"git clean -fdx",
		"target tracked source files explicitly",
	} {
		if !strings.Contains(orchestratorSystemPrompt, want) {
			t.Fatalf("orchestratorSystemPrompt missing %q", want)
		}
	}
}

func TestSanityCheckPromptsDoNotRequireRawShellOutput(t *testing.T) {
	for _, unwanted := range []string{
		"capture the full test output",
		"Show the actual test output",
		"do not summarize",
	} {
		if strings.Contains(orchestratorSystemPrompt, unwanted) {
			t.Fatalf("orchestratorSystemPrompt should not require raw shell output, found %q", unwanted)
		}
	}
	want := "***Do not require or expect verbatim/raw shell output in the final answer.***"
	if count := strings.Count(orchestratorSystemPrompt, want); count < 2 {
		t.Fatalf("orchestratorSystemPrompt should include raw-output disclaimer for Phase 1 and Phase 3, count = %d", count)
	}
}

func TestOrchestratorSystemPromptGatesOnRunnerWorktreeAudit(t *testing.T) {
	for _, want := range []string{
		"RUNNER-OBSERVED WORKTREE AUDIT",
		"authoritative for phase-gating",
		"gate_result is BLOCK_NEXT_PHASE",
		"commit intentional deliverables and remove accidental deliverables",
		"gate_result is AUDIT_ERROR",
		"Do not proceed to the next phase or final DONE while the runner-observed audit blocks",
	} {
		if !strings.Contains(orchestratorSystemPrompt, want) {
			t.Fatalf("orchestratorSystemPrompt missing %q", want)
		}
	}
}

func TestPeerReviewPromptsInspectFullWorktree(t *testing.T) {
	for name, prompt := range map[string]string{
		"peerReviewInstruction":   peerReviewInstruction,
		"secondReviewInstruction": secondReviewInstruction,
	} {
		for _, want := range []string{
			"git status --short",
			"git diff --cached",
			"git diff",
			"git ls-files --others --exclude-standard",
			"staged",
			"unstaged",
			"untracked",
			"HIGH severity process failure",
		} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("%s missing %q", name, want)
			}
		}
	}

	for _, want := range []string{
		"committed, staged, unstaged, and relevant untracked deliverable files",
		"still review them and flag that state as a HIGH severity process failure",
	} {
		if !strings.Contains(reviewSystemPrompt, want) {
			t.Fatalf("reviewSystemPrompt missing %q", want)
		}
	}
}

func TestHailMaryPromptAuditsFullDeliverableState(t *testing.T) {
	for _, want := range []string{
		"Audit the full deliverable state, not only committed history",
		"git status --short",
		"git diff main...HEAD",
		"git diff --cached",
		"git diff",
		"git ls-files --others --exclude-standard",
		"committed changes, staged changes, unstaged tracked changes, and relevant untracked deliverable files",
		"source files, tests, fixtures, configuration, documentation, generated type declarations, lockfiles, and plan files",
		"runtime/session artifacts, logs, caches, temporary scratch directories, and orchestrator-owned state",
		"commit it as an intentional deliverable or remove it if accidental",
		"The final deliverable-state audit",
		"fully committed with no unintended deliverable files",
	} {
		if !strings.Contains(hailMaryInstruction, want) {
			t.Fatalf("hailMaryInstruction missing %q", want)
		}
	}
}

func TestRunnerWorktreeAuditClassifiesDeliverablesAndRuntimePaths(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.com")

	writeFile(t, filepath.Join(repo, "src", "app.txt"), "base\n")
	runGit(t, repo, "add", "src/app.txt")
	runGit(t, repo, "commit", "-m", "initial")

	writeFile(t, filepath.Join(repo, "src", "app.txt"), "changed\n")
	writeFile(t, filepath.Join(repo, "tests", "app_test.txt"), "test\n")
	runGit(t, repo, "add", "tests/app_test.txt")
	writeFile(t, filepath.Join(repo, "docs", "new.md"), "docs\n")
	writeFile(t, filepath.Join(repo, ".machtiani", "sessions", "agent-1", "state"), "runtime\n")
	writeFile(t, filepath.Join(repo, "logs", "runner.log"), "log\n")
	writeFile(t, filepath.Join(repo, "__pycache__", "x.pyc"), "cache\n")

	audit := collectRunnerWorktreeAudit(context.Background(), repo)
	if audit.gateResult() != "BLOCK_NEXT_PHASE" {
		t.Fatalf("gateResult = %q, want BLOCK_NEXT_PHASE: %#v", audit.gateResult(), audit)
	}
	for _, want := range []string{"tests/app_test.txt"} {
		if !containsString(audit.StagedDeliverables, want) {
			t.Fatalf("staged deliverables missing %q: %#v", want, audit.StagedDeliverables)
		}
	}
	for _, want := range []string{"src/app.txt"} {
		if !containsString(audit.UnstagedDeliverables, want) {
			t.Fatalf("unstaged deliverables missing %q: %#v", want, audit.UnstagedDeliverables)
		}
	}
	for _, want := range []string{"docs/new.md"} {
		if !containsString(audit.UntrackedDeliverables, want) {
			t.Fatalf("untracked deliverables missing %q: %#v", want, audit.UntrackedDeliverables)
		}
	}
	for _, want := range []string{
		".machtiani/sessions/agent-1/state",
		"logs/runner.log",
		"__pycache__/x.pyc",
	} {
		if !containsString(audit.ProtectedOrRuntimeDirtyPaths, want) {
			t.Fatalf("runtime paths missing %q: %#v", want, audit.ProtectedOrRuntimeDirtyPaths)
		}
	}

	formatted := formatRunnerWorktreeAudit(audit)
	for _, want := range []string{
		"gate_result: BLOCK_NEXT_PHASE",
		"staged_deliverables:",
		"unstaged_deliverables:",
		"untracked_deliverables:",
		"protected_or_runtime_dirty_paths:",
		"Gate rule: if gate_result is BLOCK_NEXT_PHASE or AUDIT_ERROR",
	} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted audit missing %q:\n%s", want, formatted)
		}
	}
}

func TestRunnerWorktreeAuditPassesWithOnlyRuntimePaths(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.com")

	writeFile(t, filepath.Join(repo, "README.md"), "base\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")

	writeFile(t, filepath.Join(repo, ".machtiani", "sessions", "agent-1", "state"), "runtime\n")
	writeFile(t, filepath.Join(repo, "logs", "runner.log"), "log\n")
	writeFile(t, filepath.Join(repo, ".pytest_cache", "state"), "cache\n")

	audit := collectRunnerWorktreeAudit(context.Background(), repo)
	if audit.gateResult() != "PASS" {
		t.Fatalf("gateResult = %q, want PASS: %#v", audit.gateResult(), audit)
	}
	if len(audit.StagedDeliverables) != 0 || len(audit.UnstagedDeliverables) != 0 || len(audit.UntrackedDeliverables) != 0 {
		t.Fatalf("runtime-only audit should not report deliverables: %#v", audit)
	}
	for _, want := range []string{
		".machtiani/sessions/agent-1/state",
		"logs/runner.log",
		".pytest_cache/state",
	} {
		if !containsString(audit.ProtectedOrRuntimeDirtyPaths, want) {
			t.Fatalf("runtime paths missing %q: %#v", want, audit.ProtectedOrRuntimeDirtyPaths)
		}
	}
}

func TestWriteTrajectoryLineRecreatesRemovedDirectory(t *testing.T) {
	trajDir := filepath.Join(t.TempDir(), ".machtiani", "meta-orchestrator", "sessions", "meta-test")
	if err := os.MkdirAll(trajDir, 0755); err != nil {
		t.Fatalf("mkdir trajectory dir: %v", err)
	}
	if err := os.RemoveAll(trajDir); err != nil {
		t.Fatalf("remove trajectory dir: %v", err)
	}

	writeTrajectoryLine(trajDir, map[string]interface{}{
		"type": "test_entry",
	})

	data, err := os.ReadFile(filepath.Join(trajDir, "trajectory.jsonl"))
	if err != nil {
		t.Fatalf("read trajectory: %v", err)
	}
	if !strings.Contains(string(data), `"type":"test_entry"`) {
		t.Fatalf("trajectory missing entry: %s", data)
	}
}

func TestRuntimeStateSnapshotRestoresMissingProtectedPaths(t *testing.T) {
	appDir := t.TempDir()
	writeFile(t, filepath.Join(appDir, ".machtiani", "config.toml"), "config = true\n")
	writeFile(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-1", "chat", "agent-final-answer.md"), "done\n")
	writeFile(t, filepath.Join(appDir, ".machtiani", "meta-orchestrator", "sessions", "meta-1", "trajectory.jsonl"), "{}\n")
	writeFile(t, filepath.Join(appDir, "instruction.md"), "requirements\n")

	snapshot, err := snapshotProtectedRuntimeState(appDir)
	if err != nil {
		t.Fatalf("snapshotProtectedRuntimeState: %v", err)
	}
	defer snapshot.cleanup()

	if err := os.RemoveAll(filepath.Join(appDir, ".machtiani")); err != nil {
		t.Fatalf("remove .machtiani: %v", err)
	}
	if err := os.Remove(filepath.Join(appDir, "instruction.md")); err != nil {
		t.Fatalf("remove instruction: %v", err)
	}

	restored, err := snapshot.restoreMissing()
	if err != nil {
		t.Fatalf("restoreMissing: %v", err)
	}
	for _, want := range protectedRuntimePaths {
		if !containsString(restored, want) {
			t.Fatalf("restored paths missing %q: %#v", want, restored)
		}
	}

	assertFileContent(t, filepath.Join(appDir, ".machtiani", "config.toml"), "config = true\n")
	assertFileContent(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-1", "chat", "agent-final-answer.md"), "done\n")
	assertFileContent(t, filepath.Join(appDir, ".machtiani", "meta-orchestrator", "sessions", "meta-1", "trajectory.jsonl"), "{}\n")
	assertFileContent(t, filepath.Join(appDir, "instruction.md"), "requirements\n")

	restoredAgain, err := snapshot.restoreMissing()
	if err != nil {
		t.Fatalf("second restoreMissing: %v", err)
	}
	if len(restoredAgain) != 0 {
		t.Fatalf("second restore should be idempotent, restored %#v", restoredAgain)
	}
}

func TestRuntimeStateRestoreDoesNotOverwriteExistingProtectedPaths(t *testing.T) {
	appDir := t.TempDir()
	writeFile(t, filepath.Join(appDir, "instruction.md"), "original\n")

	snapshot, err := snapshotProtectedRuntimeState(appDir)
	if err != nil {
		t.Fatalf("snapshotProtectedRuntimeState: %v", err)
	}
	defer snapshot.cleanup()

	writeFile(t, filepath.Join(appDir, "instruction.md"), "updated\n")
	restored, err := snapshot.restoreMissing()
	if err != nil {
		t.Fatalf("restoreMissing: %v", err)
	}
	if len(restored) != 0 {
		t.Fatalf("restore should not overwrite existing paths, restored %#v", restored)
	}
	assertFileContent(t, filepath.Join(appDir, "instruction.md"), "updated\n")
}

func TestRuntimeStateRestoreMergesMissingChildrenWithoutOverwriting(t *testing.T) {
	appDir := t.TempDir()
	writeFile(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-1", "state"), "session\n")
	writeFile(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-2", "state"), "original\n")

	snapshot, err := snapshotProtectedRuntimeState(appDir)
	if err != nil {
		t.Fatalf("snapshotProtectedRuntimeState: %v", err)
	}
	defer snapshot.cleanup()

	if err := os.RemoveAll(filepath.Join(appDir, ".machtiani", "sessions", "agent-1")); err != nil {
		t.Fatalf("remove session child: %v", err)
	}
	writeFile(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-2", "state"), "updated\n")

	restored, err := snapshot.restoreMissing()
	if err != nil {
		t.Fatalf("restoreMissing: %v", err)
	}
	if !containsString(restored, filepath.Join(".machtiani", "sessions")) {
		t.Fatalf("restored paths missing sessions directory: %#v", restored)
	}
	assertFileContent(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-1", "state"), "session\n")
	assertFileContent(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-2", "state"), "updated\n")
}

func TestRestoreProtectedRuntimeStateBeforeSyncRestoresAndLogs(t *testing.T) {
	appDir := t.TempDir()
	writeFile(t, filepath.Join(appDir, ".machtiani", "config.toml"), "config = true\n")
	writeFile(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-1", "state"), "session\n")
	writeFile(t, filepath.Join(appDir, ".machtiani", "meta-orchestrator", "sessions", "meta-1", "state"), "meta\n")
	writeFile(t, filepath.Join(appDir, "instruction.md"), "requirements\n")

	snapshot, err := snapshotProtectedRuntimeState(appDir)
	if err != nil {
		t.Fatalf("snapshotProtectedRuntimeState: %v", err)
	}
	defer snapshot.cleanup()

	if err := os.RemoveAll(filepath.Join(appDir, ".machtiani", "sessions")); err != nil {
		t.Fatalf("remove sessions: %v", err)
	}
	if err := os.Remove(filepath.Join(appDir, ".machtiani", "config.toml")); err != nil {
		t.Fatalf("remove config: %v", err)
	}

	trajDir := filepath.Join(appDir, ".machtiani", "meta-orchestrator", "sessions", "meta-1")
	if err := restoreProtectedRuntimeState(snapshot, trajDir, "continue-before-agent-run-before-sync"); err != nil {
		t.Fatalf("restoreProtectedRuntimeState: %v", err)
	}

	assertFileContent(t, filepath.Join(appDir, ".machtiani", "config.toml"), "config = true\n")
	assertFileContent(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-1", "state"), "session\n")
	trajectory, err := os.ReadFile(filepath.Join(trajDir, "trajectory.jsonl"))
	if err != nil {
		t.Fatalf("read trajectory: %v", err)
	}
	for _, want := range []string{
		`"type":"runtime_state_restored"`,
		`"label":"continue-before-agent-run-before-sync"`,
		`.machtiani/config.toml`,
		`.machtiani/sessions`,
	} {
		if !strings.Contains(string(trajectory), want) {
			t.Fatalf("trajectory missing %q:\n%s", want, trajectory)
		}
	}
}

func TestCodeStrongForgePromptProtectsRepoLocalRuntimeArtifacts(t *testing.T) {
	promptPath := filepath.Join(repoRoot(t), "agent", "internal", "modes", "canonical", "code-strong-forge", "shell-agent-system-prompt.txt")
	data, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	prompt := string(data)
	for _, want := range []string{
		"treat `.machtiani/` and `instruction.md` as protected runtime state",
		"Do NOT run broad destructive cleanup",
		"`git clean -fd`",
		"`git clean -fdx`",
		"`git clean -fd*`",
		"Add `.machtiani/` to `.git/info/exclude` early",
		"do not use broad staging commands or broad cleanup commands",
		"mct-forge may take several minutes",
		"Do not kill, interrupt, background-kill, or replace a running mct-forge command merely because it is slow or quiet",
		"Do not switch to direct file writes merely because forge is slow",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("code-strong-forge prompt missing %q", want)
		}
	}
}

func TestBuildHailMaryRunArgsStartsFreshSessionWithoutSessionIDFlag(t *testing.T) {
	args := buildHailMaryRunArgs("code-strong-forge", "planner-model", "shell-model", "task-tag", true)

	if containsString(args, "--session-id") {
		t.Fatalf("fresh Hail Mary run args must not include --session-id: %#v", args)
	}
	for _, want := range []string{
		"--mode",
		"code-strong-forge",
		"--model",
		"planner-model",
		"--shell-agent-model",
		"shell-model",
		"--tag",
		"task-tag-hail-mary",
		"-p",
		hailMaryInstruction,
		"--persist-tmp-data",
	} {
		if !containsString(args, want) {
			t.Fatalf("hail mary args missing %q: %#v", want, args)
		}
	}
}

// TestRunLoop_StatefulSignature is a compile-time type check that verifies the
// RunLoop function signature compiles and can be called with the expected
// argument types. We skip actual execution because RunLoop spawns real
// subprocesses and makes LLM calls.
func TestRunLoop_StatefulSignature(t *testing.T) {
	t.Skip("compile-time type check only — RunLoop calls exec and LLM, not suitable for unit testing")

	ctx := context.Background()
	_, _ = RunLoop(ctx, "test-meta-session", "test-mct-session", "/tmp/instruction.md", "code", "gpt-4", "", "", false, false)
}

// TestInvokeMCTAgent_Signature is a compile-time type check that verifies the
// invokeMCTAgent function signature compiles and can be called with the new
// metaSessionID parameter. We skip actual execution because invokeMCTAgent
// spawns a real subprocess.
func TestInvokeMCTAgent_Signature(t *testing.T) {
	t.Skip("compile-time type check only — invokeMCTAgent calls exec, not suitable for unit testing")

	ctx := context.Background()
	_, _ = invokeMCTAgent(ctx, "test-meta-session", "/tmp/traj", "test-mct-session", "--mode", "code")
}

func TestEnsureSessionIDArgAddsMissingSessionID(t *testing.T) {
	args := ensureSessionIDArg([]string{"--mode", "code", "-p", "continue"}, "agent-1")
	if !containsString(args, "--session-id") || !containsString(args, "agent-1") {
		t.Fatalf("args missing session id: %#v", args)
	}
	if countString(args, "--session-id") != 1 {
		t.Fatalf("args should contain one --session-id: %#v", args)
	}
}

func TestEnsureSessionIDArgDoesNotDuplicateSessionID(t *testing.T) {
	args := ensureSessionIDArg([]string{"--mode", "code", "--session-id", "agent-existing", "-p", "continue"}, "agent-1")
	if countString(args, "--session-id") != 1 {
		t.Fatalf("args should contain one --session-id: %#v", args)
	}
	if !containsString(args, "agent-existing") || containsString(args, "agent-1") {
		t.Fatalf("existing session id should be preserved: %#v", args)
	}

	args = ensureSessionIDArg([]string{"--mode", "code", "--session-id=agent-inline", "-p", "continue"}, "agent-1")
	if !containsString(args, "--session-id=agent-inline") || containsString(args, "agent-1") {
		t.Fatalf("inline session id should be preserved: %#v", args)
	}
}

func TestInvokeMCTAgentWithRecoveryRetriesFailedRunWithSameSession(t *testing.T) {
	appDir := t.TempDir()
	writeFile(t, filepath.Join(appDir, ".machtiani", "config.toml"), "config = true\n")
	writeFile(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-1", "chat", "agent-final-answer.md"), "stale\n")
	writeFile(t, filepath.Join(appDir, ".machtiani", "meta-orchestrator", "sessions", "meta-1", "trajectory.jsonl"), "")
	writeFile(t, filepath.Join(appDir, "instruction.md"), "requirements\n")

	fakeAgent := filepath.Join(t.TempDir(), "machtiani")
	writeFakeMCTAgent(t, fakeAgent, appDir, true)

	restore := overrideMCTAgentTestConfig(t, fakeAgent, appDir)
	defer restore()

	snapshot, err := snapshotProtectedRuntimeState(appDir)
	if err != nil {
		t.Fatalf("snapshotProtectedRuntimeState: %v", err)
	}
	defer snapshot.cleanup()

	trajDir := filepath.Join(appDir, ".machtiani", "meta-orchestrator", "sessions", "meta-1")
	exitCode, err := invokeMCTAgentWithRecovery(context.Background(), "meta-1", trajDir, "agent-1", "test-run", snapshot, "--mode", "code", "-p", "continue")
	if err != nil {
		t.Fatalf("invokeMCTAgentWithRecovery returned error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}

	calls, err := os.ReadFile(filepath.Join(appDir, "calls.txt"))
	if err != nil {
		t.Fatalf("read calls: %v", err)
	}
	callLines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(callLines) != 2 {
		t.Fatalf("expected two machtiani calls, got %d:\n%s", len(callLines), calls)
	}
	if !strings.Contains(callLines[1], "--session-id agent-1") {
		t.Fatalf("retry did not use same session id:\n%s", calls)
	}

	trajectory, err := os.ReadFile(filepath.Join(trajDir, "trajectory.jsonl"))
	if err != nil {
		t.Fatalf("read trajectory: %v", err)
	}
	for _, want := range []string{
		`"type":"mct_invocation_recovery_attempt"`,
		`"label":"test-run"`,
		`"prior_exit_code":42`,
		`"type":"mct_invocation"`,
	} {
		if !strings.Contains(string(trajectory), want) {
			t.Fatalf("trajectory missing %q:\n%s", want, trajectory)
		}
	}
}

func TestInvokeMCTAgentWithRecoveryRestoresRuntimeStateBeforeRetry(t *testing.T) {
	appDir := t.TempDir()
	writeFile(t, filepath.Join(appDir, ".machtiani", "config.toml"), "config = true\n")
	writeFile(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-1", "state"), "session\n")
	writeFile(t, filepath.Join(appDir, ".machtiani", "meta-orchestrator", "sessions", "meta-1", "trajectory.jsonl"), "")
	writeFile(t, filepath.Join(appDir, "instruction.md"), "requirements\n")

	fakeAgent := filepath.Join(t.TempDir(), "machtiani")
	writeFakeMCTAgent(t, fakeAgent, appDir, true)

	restore := overrideMCTAgentTestConfig(t, fakeAgent, appDir)
	defer restore()

	snapshot, err := snapshotProtectedRuntimeState(appDir)
	if err != nil {
		t.Fatalf("snapshotProtectedRuntimeState: %v", err)
	}
	defer snapshot.cleanup()

	trajDir := filepath.Join(appDir, ".machtiani", "meta-orchestrator", "sessions", "meta-1")
	exitCode, err := invokeMCTAgentWithRecovery(context.Background(), "meta-1", trajDir, "agent-1", "test-run", snapshot, "--mode", "code", "-p", "continue")
	if err != nil {
		t.Fatalf("invokeMCTAgentWithRecovery returned error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}

	assertFileContent(t, filepath.Join(appDir, ".machtiani", "config.toml"), "config = true\n")
	assertFileContent(t, filepath.Join(appDir, ".machtiani", "sessions", "agent-1", "state"), "session\n")
	assertFileContent(t, filepath.Join(appDir, "instruction.md"), "requirements\n")
}

func TestReadSessionOutputReturnsSuspendedUserInputSummary(t *testing.T) {
	tmp := t.TempDir()
	restore := overrideMCTAgentTestConfig(t, mctAgentBinary, tmp)
	defer restore()

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(prevWD); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	prevHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", tmp); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer func() {
		if err := os.Setenv("HOME", prevHome); err != nil {
			t.Fatalf("restore HOME: %v", err)
		}
	}()

	sessionID := "agent-test-suspended"
	conv := conversation.New(sessionID, "test goal")
	conv.Status = "suspended_user_input"
	conv.SuspendedUserInput = &conversation.SuspendedUserInputState{
		Question: "Should I apply the fixes now?",
		Context:  "The peer review found three concrete defects.",
		Reason:   "user-directed-ask",
	}
	conv.AddMessage("assistant", "Should I apply the fixes now?", map[string]any{"type": "user_input_request"})

	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal conversation: %v", err)
	}

	convPath := filepath.Join(tmp, ".machtiani", "sessions", sessionID, "artifacts", "conversation.json")
	if err := os.MkdirAll(filepath.Dir(convPath), 0755); err != nil {
		t.Fatalf("mkdir conversation dir: %v", err)
	}
	if err := os.WriteFile(convPath, data, 0644); err != nil {
		t.Fatalf("write conversation: %v", err)
	}

	output, err := readSessionOutput(sessionID)
	if err != nil {
		t.Fatalf("readSessionOutput: %v", err)
	}
	if !output.suspended {
		t.Fatal("expected suspended output")
	}
	for _, want := range []string{
		"The agent suspended for user input instead of continuing autonomously.",
		"Should I apply the fixes now?",
		"The peer review found three concrete defects.",
	} {
		if !strings.Contains(output.content, want) {
			t.Fatalf("output missing %q:\n%s", want, output.content)
		}
	}
}

func TestReadSessionOutputPrefersFinalAnswer(t *testing.T) {
	tmp := t.TempDir()
	restore := overrideMCTAgentTestConfig(t, mctAgentBinary, tmp)
	defer restore()

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(prevWD); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}

	sessionID := "agent-test-final-answer"
	finalAnswerPath := filepath.Join(".machtiani", "sessions", sessionID, "chat", "agent-final-answer.md")
	if err := os.MkdirAll(filepath.Dir(finalAnswerPath), 0755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	if err := os.WriteFile(finalAnswerPath, []byte("Implementation complete."), 0644); err != nil {
		t.Fatalf("write final answer: %v", err)
	}

	output, err := readSessionOutput(sessionID)
	if err != nil {
		t.Fatalf("readSessionOutput: %v", err)
	}
	if output.suspended {
		t.Fatal("did not expect suspended output")
	}
	if output.content != "Implementation complete." {
		t.Fatalf("content = %q, want %q", output.content, "Implementation complete.")
	}
}

func TestClearFinalAnswerBeforeRunRemovesAndLogs(t *testing.T) {
	tmp := t.TempDir()
	restore := overrideMCTAgentTestConfig(t, mctAgentBinary, tmp)
	defer restore()

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(prevWD); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}

	sessionID := "agent-test-cleanup"
	finalAnswerPath := filepath.Join(".machtiani", "sessions", sessionID, "chat", "agent-final-answer.md")
	writeFile(t, finalAnswerPath, "stale answer\n")

	trajDir := filepath.Join(".machtiani", "meta-orchestrator", "sessions", "meta-test")
	if err := clearFinalAnswerBeforeRun(sessionID, trajDir, "before-test-run"); err != nil {
		t.Fatalf("clearFinalAnswerBeforeRun: %v", err)
	}
	if _, err := os.Stat(finalAnswerPath); !os.IsNotExist(err) {
		t.Fatalf("final answer should be removed, stat err = %v", err)
	}

	trajectory, err := os.ReadFile(filepath.Join(trajDir, "trajectory.jsonl"))
	if err != nil {
		t.Fatalf("read trajectory: %v", err)
	}
	for _, want := range []string{
		`"type":"final_answer_cleared_before_run"`,
		`"label":"before-test-run"`,
		`"removed":true`,
		`agent-final-answer.md`,
	} {
		if !strings.Contains(string(trajectory), want) {
			t.Fatalf("trajectory missing %q:\n%s", want, trajectory)
		}
	}

	if err := clearFinalAnswerBeforeRun(sessionID, trajDir, "before-test-run-again"); err != nil {
		t.Fatalf("second clearFinalAnswerBeforeRun: %v", err)
	}
	trajectory, err = os.ReadFile(filepath.Join(trajDir, "trajectory.jsonl"))
	if err != nil {
		t.Fatalf("read trajectory after second clear: %v", err)
	}
	for _, want := range []string{
		`"label":"before-test-run-again"`,
		`"removed":false`,
	} {
		if !strings.Contains(string(trajectory), want) {
			t.Fatalf("trajectory missing %q after second clear:\n%s", want, trajectory)
		}
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func countString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func overrideMCTAgentTestConfig(t *testing.T, binary string, appDir string) func() {
	t.Helper()
	oldBinary := mctAgentBinary
	oldAppDir := mctAgentAppDir
	oldRecoveryAttempts := mctAgentRecoveryAttempts

	mctAgentBinary = binary
	mctAgentAppDir = appDir
	mctAgentRecoveryAttempts = 1

	return func() {
		mctAgentBinary = oldBinary
		mctAgentAppDir = oldAppDir
		mctAgentRecoveryAttempts = oldRecoveryAttempts
	}
}

func writeFakeMCTAgent(t *testing.T, path string, appDir string, deleteRuntimeOnFirstRun bool) {
	t.Helper()
	deleteRuntime := "false"
	if deleteRuntimeOnFirstRun {
		deleteRuntime = "true"
	}
	script := fmt.Sprintf(`#!/bin/sh
set -eu
app_dir=%q
count_file="$app_dir/count.txt"
calls_file="$app_dir/calls.txt"
count=0
if [ -f "$count_file" ]; then
  count=$(cat "$count_file")
fi
count=$((count + 1))
printf '%%s' "$count" > "$count_file"
printf '%%s\n' "$*" >> "$calls_file"
if [ "$count" -eq 1 ]; then
  if [ %q = "true" ]; then
    rm -rf "$app_dir/.machtiani/sessions" "$app_dir/.machtiani/meta-orchestrator"
  fi
  exit 42
fi
exit 0
`, appDir, deleteRuntime)
	writeFile(t, path, script)
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatalf("chmod fake machtiani: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "agent", "internal", "modes", "canonical", "code-strong-forge", "shell-agent-system-prompt.txt")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root")
		}
		dir = parent
	}
}
