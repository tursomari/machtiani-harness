package session

import (
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

	recorder.conversation.AddMessage("user", "external mutation", map[string]any{"type": "user_feedback"})

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

	recorder.conversation.AddMessage("assistant", "out-of-band", map[string]any{"type": "note"})

	if err := recorder.AppendRaw("user", "Need a tighter scope", "goal_update"); err != nil {
		t.Fatalf("AppendRaw: %v", err)
	}

	content := tr.Content()
	if !strings.Contains(content, "=== GOAL UPDATE") {
		t.Fatalf("transcript missing goal-update block after fallback:\n%s", content)
	}
	if !strings.Contains(content, "Need a tighter scope") {
		t.Fatalf("transcript missing goal-update content after fallback:\n%s", content)
	}
	if !strings.Contains(recorder.JSON(), "Need a tighter scope") {
		t.Fatalf("conversation json missing appended raw content after fallback: %s", recorder.JSON())
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
