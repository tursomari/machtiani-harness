package workspace

import (
	"os/exec"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncSnapshotToHost_DoesNotDeleteHostGitDir(t *testing.T) {
	root := t.TempDir()
	snapshotRepoRoot := filepath.Join(root, "snapshot", "repo")
	hostRepoRoot := filepath.Join(root, "host")
	patchDir := filepath.Join(root, "patches")

	if err := os.MkdirAll(snapshotRepoRoot, 0o755); err != nil {
		t.Fatalf("mkdir snapshot repo: %v", err)
	}
	if err := os.MkdirAll(hostRepoRoot, 0o755); err != nil {
		t.Fatalf("mkdir host repo: %v", err)
	}
	if err := os.MkdirAll(patchDir, 0o755); err != nil {
		t.Fatalf("mkdir patch dir: %v", err)
	}

	gitDir := filepath.Join(hostRepoRoot, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir host .git: %v", err)
	}

	// SyncSnapshotToHost shells out to `git ls-files` to build its baseline set.
	// Create a minimal git repo so the command succeeds.
	if err := os.WriteFile(filepath.Join(hostRepoRoot, "README.md"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	cmd := execCommand(t, hostRepoRoot, "git", "init")
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	cmd = execCommand(t, hostRepoRoot, "git", "add", "README.md")
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}
	cmd = execCommand(t, hostRepoRoot, "git", "commit", "-m", "init")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// Force a manifest state that would have previously triggered host deletion.
	manifestPath := filepath.Join(filepath.Dir(snapshotRepoRoot), "manifests", "sync.json")
	m := &syncManifest{
		Version:      1,
		Baseline:     map[string]manifestEntry{},
		CreatedPaths: map[string]struct{}{filepath.ToSlash(".git"): {}},
	}
	if err := saveManifest(manifestPath, m); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if err := SyncSnapshotToHost(snapshotRepoRoot, hostRepoRoot, patchDir); err != nil {
		t.Fatalf("SyncSnapshotToHost: %v", err)
	}

	if _, err := os.Stat(gitDir); err != nil {
		t.Fatalf("expected host .git to remain, stat error: %v", err)
	}
}

func execCommand(t *testing.T, dir, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}
