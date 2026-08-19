package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/transcript"
)

func TestPrepareTranscriptBootstrapPersistsEffectiveTags(t *testing.T) {
	dir := t.TempDir()
	cfg := legacyConfig{
		transcriptFile: filepath.Join(dir, "agent-transcript.adoc"),
		answerTag:      "answer-review",
		commandTag:     "command-review",
	}
	sessionID := "persist-effective-tags"
	conversationPath := filepath.Join(dir, "conversation.json")
	runState := newRunLifecycleState(context.Background(), cfg, sessionID, "Goal", "Goal", "", "", "", 0, "", nil)

	setup, err := prepareTranscriptBootstrap(cfg, sessionID, "Goal", conversationPath, false, nil, false, nil, dir, runState, os.Stderr)
	if err != nil {
		t.Fatalf("prepareTranscriptBootstrap: %v", err)
	}
	defer setup.transcript.Close()

	if setup.conversation.AnswerTag != "answer-review" || setup.conversation.CommandTag != "command-review" {
		t.Fatalf("conversation tags = (%q, %q), want (answer-review, command-review)", setup.conversation.AnswerTag, setup.conversation.CommandTag)
	}
	data, err := os.ReadFile(conversationPath)
	if err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if !strings.Contains(string(data), `"answer_tag": "answer-review"`) || !strings.Contains(string(data), `"command_tag": "command-review"`) {
		t.Fatalf("persisted conversation missing effective tags: %s", data)
	}
}

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

	recorder := newConversationRecorder(tr, "desync-write-turn", formatGoalText("Keep transcript stable", ""), "", false, nil, false)
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

	recorder := newConversationRecorder(tr, "desync-append-raw", formatGoalText("Keep transcript stable", ""), "", false, nil, false)
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

	recorder := newConversationRecorder(tr, "conv-user-input", "Goal", "", false, nil, false)
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

func TestConversationRecorderEnsureRecoveryIsIdempotent(t *testing.T) {
	const sessionID = "conv-recovery-idempotent"
	dir := t.TempDir()
	conversationPath := filepath.Join(dir, "conversation.json")
	transcriptPath := filepath.Join(dir, "agent-transcript.adoc")

	tr, err := transcript.NewWithPath(transcriptPath, sessionID)
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}

	recorder := newConversationRecorder(tr, sessionID, "Goal", conversationPath, false, nil, false)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	const (
		feedback   = "Continue the interrupted request."
		systemNote = "The previous completion was rejected."
	)
	key := recoveryMessageKey(sessionID, 4, feedback)
	added, err := recorder.EnsureRecovery(key, 4, systemNote, feedback)
	if err != nil {
		t.Fatalf("EnsureRecovery first call: %v", err)
	}
	if !added {
		t.Fatal("expected first recovery call to append messages")
	}
	added, err = recorder.EnsureRecovery(key, 4, systemNote, feedback)
	if err != nil {
		t.Fatalf("EnsureRecovery retry: %v", err)
	}
	if added {
		t.Fatal("expected retry with the same key to reuse recovery messages")
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("close first transcript: %v", err)
	}

	// Recreate both objects from the durable conversation, matching the
	// process boundary exercised by Dear Machine's systemd restart loop.
	resumedTranscript, err := transcript.NewWithPath(transcriptPath, sessionID)
	if err != nil {
		t.Fatalf("resumed transcript init: %v", err)
	}
	t.Cleanup(func() { _ = resumedTranscript.Close() })
	resumedRecorder := newConversationRecorder(resumedTranscript, sessionID, "Goal", conversationPath, true, nil, false)
	if err := resumedRecorder.Load(); err != nil {
		t.Fatalf("resumed recorder Load: %v", err)
	}
	added, err = resumedRecorder.EnsureRecovery(key, 4, systemNote, feedback)
	if err != nil {
		t.Fatalf("EnsureRecovery after restart: %v", err)
	}
	if added {
		t.Fatal("expected retry after restart to reuse durable recovery messages")
	}
	recorder = resumedRecorder

	var recoveryMessages []conversation.Message
	for _, msg := range recorder.Conversation().Messages {
		if msgMetaType(msg.Metadata) == "recovery" {
			recoveryMessages = append(recoveryMessages, msg)
		}
	}
	if len(recoveryMessages) != 2 {
		t.Fatalf("recovery message count = %d, want 2", len(recoveryMessages))
	}
	for _, msg := range recoveryMessages {
		if got, _ := msg.Metadata["recovery_key"].(string); got != key {
			t.Fatalf("recovery key = %q, want %q", got, key)
		}
		if msg.Turn == nil || *msg.Turn != 4 {
			t.Fatalf("recovery turn = %v, want 4", msg.Turn)
		}
	}
	persisted, err := os.ReadFile(conversationPath)
	if err != nil {
		t.Fatalf("read persisted conversation: %v", err)
	}
	if got := strings.Count(string(persisted), systemNote); got != 1 {
		t.Fatalf("persisted system recovery note count = %d, want 1", got)
	}
	if got := strings.Count(string(persisted), feedback); got != 1 {
		t.Fatalf("persisted recovery feedback count = %d, want 1", got)
	}

	chatMessages := recorder.Conversation().ToChatMessages("")
	foundSystem := false
	foundFeedback := false
	for _, msg := range chatMessages {
		switch {
		case msg.Role == "system" && msg.Content == systemNote:
			foundSystem = true
		case msg.Role == "user" && msg.Content == feedback:
			foundFeedback = true
		}
	}
	if !foundSystem || !foundFeedback {
		t.Fatalf("typed recovery messages missing from chat context: %+v", chatMessages)
	}

	nextKey := recoveryMessageKey(sessionID, 5, feedback)
	if nextKey == key {
		t.Fatal("expected a later turn to have a distinct recovery key")
	}
	added, err = recorder.EnsureRecovery(nextKey, 5, systemNote, feedback)
	if err != nil {
		t.Fatalf("EnsureRecovery later turn: %v", err)
	}
	if !added {
		t.Fatal("expected a later turn to append a new recovery marker")
	}
}

func TestRunLifecycleStateSuspendForUserInput(t *testing.T) {
	tr, err := transcript.New("suspend-user-input")
	if err != nil {
		t.Fatalf("transcript init: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	recorder := newConversationRecorder(tr, "suspend-user-input", "Goal", "", false, nil, false)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	runState := newRunLifecycleState(context.Background(), legacyConfig{}, "suspend-user-input", "Goal", "Goal", "", "", "", 0, "", nil)
	runState.recorder = recorder
	runState.tr = tr

	result, err := runState.suspendForUserInput(nil, os.Stderr, "Do you want the safer fix?", "The safer fix preserves behavior.", "tradeoff choice", "Original mixed ask")
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
	bootstrap, err := prepareSessionEnvironment(sessionID, legacyConfig{}, os.Stderr)
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

func TestPrepareSessionEnvironment_ImplicitSessionID_IgnoresInheritedTempRoot(t *testing.T) {
	repo := setupSessionEnvironmentTestRepo(t, "[environment]\ntype = \"local\"\n")

	// Simulate a child process that inherited both MACHTIANI_SESSION_ID
	// and MACHTIANI_SESSION_TEMP_ROOT from a parent process.
	// When no explicit resume/session selector is given (cfg.sessionID == ""),
	// prepareRunBootstrap unsets the env var before
	// prepareSessionEnvironment runs so that the child session never
	// reuses the parent's lock directory.
	t.Setenv("MACHTIANI_SESSION_ID", "parent-session-123")
	t.Setenv("MACHTIANI_SESSION_TEMP_ROOT", "/tmp/fake-parent-session")

	cfg := legacyConfig{} // sessionID defaults to ""
	// Simulate the fix from prepareRunBootstrap: ignore inherited
	// temp root for a fresh session.
	_ = os.Unsetenv("MACHTIANI_SESSION_TEMP_ROOT")

	const sessionID = "agent-implicit"
	bootstrap, err := prepareSessionEnvironment(sessionID, cfg, os.Stderr)
	if err != nil {
		t.Fatalf("prepareSessionEnvironment() error = %v", err)
	}

	// Must never use the inherited parent session path.
	if bootstrap.sessionTempRoot == "/tmp/fake-parent-session" {
		t.Fatalf("sessionTempRoot = %s, should not equal inherited parent path", bootstrap.sessionTempRoot)
	}

	wantSessionRoot := filepath.Join(repo, ".machtiani", "tmp", sessionID)
	if bootstrap.sessionTempRoot != wantSessionRoot {
		t.Fatalf("sessionTempRoot = %s, want %s", bootstrap.sessionTempRoot, wantSessionRoot)
	}
	if got := os.Getenv("MACHTIANI_SESSION_TEMP_ROOT"); got != bootstrap.sessionTempRoot {
		t.Fatalf("MACHTIANI_SESSION_TEMP_ROOT = %s, want %s", got, bootstrap.sessionTempRoot)
	}
}

func TestPrepareSessionEnvironment_ExplicitTempRootHonoredWhenNotChildProcess(t *testing.T) {
	setupSessionEnvironmentTestRepo(t, "[environment]\ntype = \"local\"\n")

	// Simulate an explicit temp root set by the caller (e.g. via
	// --session-temp-root).  Since MACHTIANI_SESSION_ID is NOT set,
	// this is not a child-process inherit scenario and the explicit
	// temp root should be honored as-is.
	t.Setenv("MACHTIANI_SESSION_TEMP_ROOT", "/tmp/explicit-temp-root")

	const sessionID = "agent-explicit"
	bootstrap, err := prepareSessionEnvironment(sessionID, legacyConfig{}, os.Stderr)
	if err != nil {
		t.Fatalf("prepareSessionEnvironment() error = %v", err)
	}

	if bootstrap.sessionTempRoot != "/tmp/explicit-temp-root" {
		t.Fatalf("sessionTempRoot = %s, want /tmp/explicit-temp-root", bootstrap.sessionTempRoot)
	}
	if got := os.Getenv("MACHTIANI_SESSION_TEMP_ROOT"); got != "/tmp/explicit-temp-root" {
		t.Fatalf("MACHTIANI_SESSION_TEMP_ROOT = %s, want /tmp/explicit-temp-root", got)
	}
}
