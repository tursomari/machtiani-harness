package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/transcript"
)

func initGitRepoForSessionEnvTest(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "init", "-q")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, string(output))
	}
}

func setupSessionEnvironmentTestRepo(t *testing.T, config string) string {
	t.Helper()
	repo := t.TempDir()
	initGitRepoForSessionEnvTest(t, repo)

	configPath := filepath.Join(repo, ".machtiani", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cwd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	t.Setenv("HOME", t.TempDir())
	t.Setenv("MACHTIANI_CONFIG", configPath)
	t.Setenv("MACHTIANI_TMP_ROOT", "")
	t.Setenv("MACHTIANI_SESSION_TEMP_ROOT", "")
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)

	return repo
}

func withTempSessionRecorderEnv(t *testing.T) {
	t.Helper()
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Setenv("HOME", tmp)
}

func TestConversationRecorderWriteTurnFallsBackOnDesync(t *testing.T) {
	withTempSessionRecorderEnv(t)

	tr, err := transcript.New("desync-write-turn")
	if err != nil {
		t.Fatalf("transcript.New: %v", err)
	}
	defer tr.Close()

	recorder := newConversationRecorder(tr, "desync-write-turn", formatGoalText("Keep transcript stable", ""), "", false, nil)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	recorder.conversation.AddMessage("user", "external mutation", nil)

	if err := recorder.WriteTurn(1, "Question", "", []string{"runner.go"}, "Answer", "ask"); err != nil {
		t.Fatalf("WriteTurn: %v", err)
	}

	content := tr.Content()
	if !strings.Contains(content, "Question") {
		t.Fatalf("transcript missing question after fallback:\n%s", content)
	}
	if !strings.Contains(content, "Answer") {
		t.Fatalf("transcript missing answer after fallback:\n%s", content)
	}
	if !strings.Contains(recorder.JSON(), "external mutation") {
		t.Fatalf("conversation json missing external mutation after fallback: %s", recorder.JSON())
	}
}

func TestConversationRecorderAppendRawFallsBackOnDesync(t *testing.T) {
	withTempSessionRecorderEnv(t)

	tr, err := transcript.New("desync-append-raw")
	if err != nil {
		t.Fatalf("transcript.New: %v", err)
	}
	defer tr.Close()

	recorder := newConversationRecorder(tr, "desync-append-raw", formatGoalText("Keep transcript stable", ""), "", false, nil)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Force a desync by planting a conversationRendered prefix that cannot
	// match any future ToTranscript output. This exercises the fallback
	// branch in AppendRaw without relying on an invalid message type.
	recorder.conversationRendered = "stale-prefix-that-cannot-match\n"

	if err := recorder.AppendRaw("user", "Need a tighter scope", ""); err != nil {
		t.Fatalf("AppendRaw: %v", err)
	}

	content := tr.Content()
	if !strings.Contains(content, "=== USER MESSAGE") {
		t.Fatalf("transcript missing user-message block after fallback:\n%s", content)
	}
	if !strings.Contains(content, "Need a tighter scope") {
		t.Fatalf("transcript missing user-message content after fallback:\n%s", content)
	}
	if !strings.Contains(recorder.JSON(), "Need a tighter scope") {
		t.Fatalf("conversation json missing appended raw content after fallback: %s", recorder.JSON())
	}
}

func TestConversationRecorderAppendRawUserInputRequestAndReply(t *testing.T) {
	tr, err := transcript.New("conv-user-input")
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	recorder := newConversationRecorder(tr, "conv-user-input", "Goal", "", false, nil)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := recorder.AppendRaw("assistant", "Do you want the safer fix?", "user_input_request"); err != nil {
		t.Fatalf("AppendRaw request: %v", err)
	}
	if err := recorder.AppendRaw("user", "Use the safer fix.", "user_input_response"); err != nil {
		t.Fatalf("AppendRaw reply: %v", err)
	}
	content := tr.Content()
	if !strings.Contains(content, "=== USER INPUT REQUEST") {
		t.Fatalf("expected user input request block, got %q", content)
	}
	if !strings.Contains(content, "=== USER INPUT RESPONSE") {
		t.Fatalf("expected user input reply block, got %q", content)
	}
	messages := recorder.Conversation().ToChatMessages("")
	foundRequest := false
	foundReply := false
	for _, msg := range messages {
		switch {
		case msg.Role == "assistant" && strings.Contains(msg.Content, "Do you want the safer fix?"):
			foundRequest = true
		case msg.Role == "user" && strings.Contains(msg.Content, "Use the safer fix."):
			foundReply = true
		}
	}
	if !foundRequest || !foundReply {
		t.Fatalf("expected request and reply in chat messages, got %+v", messages)
	}
}

func TestRunLifecycleStateSuspendForUserInput(t *testing.T) {
	tr, err := transcript.New("suspend-user-input")
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	recorder := newConversationRecorder(tr, "suspend-user-input", "Goal", "", false, nil)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	runState := newRunLifecycleState(context.Background(), legacyConfig{}, "suspend-user-input", "Goal", "Goal", "", "", "", nil)
	runState.recorder = recorder
	runState.tr = tr

	result, err := runState.suspendForUserInput(nil, "Do you want the safer fix?", "The safer fix preserves behavior.", "tradeoff choice", "Original mixed ask")
	if err != nil {
		t.Fatalf("suspendForUserInput: %v", err)
	}
	if result.Status != "suspended_user_input" {
		t.Fatalf("unexpected result status: %q", result.Status)
	}
	if runState.pendingState == nil || runState.pendingState.SuspendedUserInput == nil {
		t.Fatalf("expected suspended user input in pending state")
	}
	if runState.pendingState.SuspendedUserInput.Question != "Do you want the safer fix?" {
		t.Fatalf("unexpected question: %q", runState.pendingState.SuspendedUserInput.Question)
	}
	content := tr.Content()
	if !strings.Contains(content, "=== USER INPUT REQUEST") || !strings.Contains(content, "Do you want the safer fix?") {
		t.Fatalf("expected suspended question in transcript, got %q", content)
	}
}

func TestPrepareSessionEnvironmentLocalKeepsLockInSessionScratchRoot(t *testing.T) {
	repo := setupSessionEnvironmentTestRepo(t, "[environment]\ntype = \"local\"\n")

	const sessionID = "agent-123"
	bootstrap, err := prepareSessionEnvironment(sessionID, legacyConfig{})
	if err != nil {
		t.Fatalf("prepareSessionEnvironment() error = %v", err)
	}

	wantSessionRoot := filepath.Join(repo, ".machtiani", "tmp", sessionID)
	if bootstrap.useSnapshotWorkspace {
		t.Fatalf("useSnapshotWorkspace = true, want false")
	}
	if bootstrap.sessionTempRoot != wantSessionRoot {
		t.Fatalf("sessionTempRoot = %s, want %s", bootstrap.sessionTempRoot, wantSessionRoot)
	}
	if got := os.Getenv("MACHTIANI_SESSION_TEMP_ROOT"); got != wantSessionRoot {
		t.Fatalf("MACHTIANI_SESSION_TEMP_ROOT = %s, want %s", got, wantSessionRoot)
	}
	if got := os.Getenv("MACHTIANI_TMP_ROOT"); got != "" {
		t.Fatalf("MACHTIANI_TMP_ROOT = %s, want empty in local mode", got)
	}

	bootstrap.restore()

	if got := os.Getenv("MACHTIANI_SESSION_TEMP_ROOT"); got != "" {
		t.Fatalf("MACHTIANI_SESSION_TEMP_ROOT after restore = %s, want empty", got)
	}
	if got := os.Getenv("MACHTIANI_TMP_ROOT"); got != "" {
		t.Fatalf("MACHTIANI_TMP_ROOT after restore = %s, want empty", got)
	}
	if _, err := os.Stat(wantSessionRoot); !os.IsNotExist(err) {
		t.Fatalf("expected session scratch root removed after restore, stat err = %v", err)
	}
}

func TestPrepareSessionEnvironmentDockerRepurposesTmpRootToWorkspace(t *testing.T) {
	workspaceBase := filepath.Join(t.TempDir(), "docker-tmp")
	repo := setupSessionEnvironmentTestRepo(t, fmt.Sprintf("[environment]\ntype = \"docker\"\ntmp_root = \"%s\"\n", workspaceBase))

	cfg, _, err := llm.LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig() error = %v", err)
	}
	if cfg.Environment == nil {
		t.Fatalf("LoadGlobalConfig() environment = nil")
	}
	if cfg.Environment.Type != "docker" {
		t.Fatalf("environment type = %q, want docker", cfg.Environment.Type)
	}
	if cfg.Environment.TmpRoot != workspaceBase {
		t.Fatalf("environment tmp_root = %q, want %q", cfg.Environment.TmpRoot, workspaceBase)
	}

	const sessionID = "agent-456"
	bootstrap, err := prepareSessionEnvironment(sessionID, legacyConfig{})
	if err != nil {
		t.Fatalf("prepareSessionEnvironment() error = %v", err)
	}

	wantSessionRoot := filepath.Join(repo, ".machtiani", "tmp", sessionID)
	wantWorkspaceRoot := filepath.Join(workspaceBase, "workspace-"+sessionID)
	if !bootstrap.useSnapshotWorkspace {
		t.Fatalf("useSnapshotWorkspace = false, want true")
	}
	if bootstrap.sessionTempRoot != wantSessionRoot {
		t.Fatalf("sessionTempRoot = %s, want %s", bootstrap.sessionTempRoot, wantSessionRoot)
	}
	if bootstrap.workspaceRoot != wantWorkspaceRoot {
		t.Fatalf("workspaceRoot = %s, want %s", bootstrap.workspaceRoot, wantWorkspaceRoot)
	}
	if got := os.Getenv("MACHTIANI_SESSION_TEMP_ROOT"); got != wantSessionRoot {
		t.Fatalf("MACHTIANI_SESSION_TEMP_ROOT = %s, want %s", got, wantSessionRoot)
	}
	if got := os.Getenv("MACHTIANI_TMP_ROOT"); got != wantWorkspaceRoot {
		t.Fatalf("MACHTIANI_TMP_ROOT = %s, want %s", got, wantWorkspaceRoot)
	}

	if err := os.MkdirAll(wantWorkspaceRoot, 0o755); err != nil {
		t.Fatalf("mkdir workspace root: %v", err)
	}
	bootstrap.restore()

	if got := os.Getenv("MACHTIANI_SESSION_TEMP_ROOT"); got != "" {
		t.Fatalf("MACHTIANI_SESSION_TEMP_ROOT after restore = %s, want empty", got)
	}
	if got := os.Getenv("MACHTIANI_TMP_ROOT"); got != "" {
		t.Fatalf("MACHTIANI_TMP_ROOT after restore = %s, want empty", got)
	}
	if _, err := os.Stat(wantSessionRoot); !os.IsNotExist(err) {
		t.Fatalf("expected session scratch root removed after restore, stat err = %v", err)
	}
	if _, err := os.Stat(wantWorkspaceRoot); !os.IsNotExist(err) {
		t.Fatalf("expected workspace root removed after restore, stat err = %v", err)
	}
}

func TestConversationRecorderRecordsPatchValidationInConversation(t *testing.T) {
	withTempSessionRecorderEnv(t)

	tr, err := transcript.New("record-patch-validation")
	if err != nil {
		t.Fatalf("transcript.New: %v", err)
	}
	defer tr.Close()

	recorder := newConversationRecorder(tr, "record-patch-validation", "Goal", "", false, nil)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	rec := transcript.PatchValidationRecord{
		Operation: "apply",
		Status:    "failed",
		Error:     "boom",
	}
	if err := recorder.RecordPatchValidation(3, rec); err != nil {
		t.Fatalf("RecordPatchValidation: %v", err)
	}

	// Transcript must contain the rendered block.
	content := tr.Content()
	if !strings.Contains(content, "=== PATCH VALIDATION (Turn 3)") {
		t.Fatalf("transcript missing patch validation block:\n%s", content)
	}

	// Conversation must contain a patch_validation message with the canonical block.
	found := false
	for _, msg := range recorder.Conversation().Messages {
		if msg.Metadata != nil && msg.Metadata["type"] == "patch_validation" {
			found = true
			if !strings.Contains(msg.Content, "=== PATCH VALIDATION (Turn 3)") {
				t.Fatalf("conversation message missing rendered block: %q", msg.Content)
			}
		}
	}
	if !found {
		t.Fatalf("expected patch_validation message in conversation, got %+v", recorder.Conversation().Messages)
	}

	// Regenerating the transcript from the conversation must match the on-disk content.
	regen, err := recorder.Conversation().ToTranscript()
	if err != nil {
		t.Fatalf("ToTranscript: %v", err)
	}
	if !strings.Contains(regen, "=== PATCH VALIDATION (Turn 3)") {
		t.Fatalf("regenerated transcript missing patch validation block:\n%s", regen)
	}
}

func TestConversationRecorderRecordsPatchPlanInConversation(t *testing.T) {
	withTempSessionRecorderEnv(t)

	tr, err := transcript.New("record-patch-plan")
	if err != nil {
		t.Fatalf("transcript.New: %v", err)
	}
	defer tr.Close()

	recorder := newConversationRecorder(tr, "record-patch-plan", "Goal", "", false, nil)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := recorder.RecordPatchPlanCreated(1, "plan-body-v1"); err != nil {
		t.Fatalf("RecordPatchPlanCreated: %v", err)
	}
	if err := recorder.RecordPatchPlanUpdated(2, "plan-body-v2"); err != nil {
		t.Fatalf("RecordPatchPlanUpdated: %v", err)
	}

	content := tr.Content()
	if !strings.Contains(content, "PATCH PLAN CREATED") || !strings.Contains(content, "PATCH PLAN UPDATED") {
		t.Fatalf("transcript missing patch plan sections:\n%s", content)
	}

	created := 0
	updated := 0
	for _, msg := range recorder.Conversation().Messages {
		if msg.Metadata == nil {
			continue
		}
		switch msg.Metadata["type"] {
		case "patch_plan_created":
			created++
		case "patch_plan_updated":
			updated++
		}
	}
	if created != 1 || updated != 1 {
		t.Fatalf("expected one created and one updated patch plan in conversation, got created=%d updated=%d", created, updated)
	}
}
