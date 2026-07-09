package orchestrator

import (
	"context"
	"os"
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
	promptPath := filepath.Join(repoRoot(t), ".machtiani", "modes", "code-strong-forge", "shell-agent-system-prompt.txt")
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
		"-t",
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

func TestReadSessionOutputReturnsSuspendedUserInputSummary(t *testing.T) {
	tmp := t.TempDir()
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

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".machtiani", "modes", "code-strong-forge", "shell-agent-system-prompt.txt")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root")
		}
		dir = parent
	}
}
