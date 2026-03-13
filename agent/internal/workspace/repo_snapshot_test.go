package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestEnsureRepoSnapshotHydratesAllowlistedSubmodule(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "main")
	subRepo := filepath.Join(parent, "submodule-src")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("create main repo dir: %v", err)
	}
	if err := os.MkdirAll(subRepo, 0o755); err != nil {
		t.Fatalf("create submodule repo dir: %v", err)
	}
	initGitRepo(t, repo)
	initGitRepo(t, subRepo)

	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	relSubPath, err := filepath.Rel(repo, subRepo)
	if err != nil {
		t.Fatalf("compute relative submodule path: %v", err)
	}
	modulePath := filepath.Join("modules", "example")
	runGit(t, repo, "submodule", "add", relSubPath, modulePath)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "add submodule")

	configPath := writeWorkspaceConfig(t, repo, `[workspace]
git_hydration = [
  { root = ".", branches = ["master"] },
  { root = "modules/example/", branches = ["master"] }
]
`)
	t.Setenv("MACHTIANI_CONFIG", configPath)
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)

	snapshotRoot, cleanup, err := EnsureRepoSnapshot(repo, filepath.Join(parent, "snapshot"))
	if err != nil {
		t.Fatalf("EnsureRepoSnapshot() error = %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(snapshotRoot, modulePath, ".git")); err != nil {
		t.Fatalf("submodule .git metadata missing: %v", err)
	}
}

func TestEnsureRepoSnapshotSkipsNonAllowlistedCommonGitMetadata(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "main")
	subRepo := filepath.Join(parent, "submodule-src")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("create main repo dir: %v", err)
	}
	if err := os.MkdirAll(subRepo, 0o755); err != nil {
		t.Fatalf("create submodule repo dir: %v", err)
	}
	initGitRepo(t, repo)
	initGitRepo(t, subRepo)

	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	relSubPath, err := filepath.Rel(repo, subRepo)
	if err != nil {
		t.Fatalf("compute relative submodule path: %v", err)
	}
	modulePath := filepath.Join("modules", "example")
	runGit(t, repo, "submodule", "add", relSubPath, modulePath)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "add submodule")

	configPath := writeWorkspaceConfig(t, repo, `[workspace]
git_hydration = [
  { root = ".", branches = ["master"] }
]
`)
	t.Setenv("MACHTIANI_CONFIG", configPath)
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)

	snapshotRoot, cleanup, err := EnsureRepoSnapshot(repo, filepath.Join(parent, "snapshot"))
	if err != nil {
		t.Fatalf("EnsureRepoSnapshot() error = %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(snapshotRoot, ".git.hydrated", "common", "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("expected common worktrees metadata to be skipped, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshotRoot, ".git.hydrated", "common", "modules", modulePath)); !os.IsNotExist(err) {
		t.Fatalf("expected non-allowlisted submodule metadata to be skipped, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshotRoot, ".git.hydrated", "modules", modulePath)); !os.IsNotExist(err) {
		t.Fatalf("expected non-allowlisted snapshot submodule metadata to be absent, got err=%v", err)
	}

	cmd := exec.Command("git", "-C", snapshotRoot, "status", "-sb")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status failed: %v\noutput: %s", err, string(output))
	}
	if !strings.Contains(string(output), "##") {
		t.Fatalf("unexpected git status output: %s", string(output))
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial commit")
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\noutput: %s", strings.Join(args, " "), err, string(output))
	}
	return string(output)
}

func writeWorkspaceConfig(t *testing.T, repoRoot, content string) string {
	t.Helper()
	dir := filepath.Join(repoRoot, ".machtiani")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
