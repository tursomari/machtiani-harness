package patcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCreateWorkspaceCopiesTrackedContentOnly(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.com")

	trackedPath := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(trackedPath, []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("write tracked: %v", err)
	}
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "commit", "-m", "init")

	// Add untracked cache directory that should be ignored.
	cacheDir := filepath.Join(repo, ".gomodcache", "pkg")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "data"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	// Stage a new file and modify the tracked file without staging the change.
	stagedPath := filepath.Join(repo, "staged.txt")
	if err := os.WriteFile(stagedPath, []byte("staged\n"), 0o644); err != nil {
		t.Fatalf("write staged: %v", err)
	}
	runGit(t, repo, "add", "staged.txt")
	if err := os.WriteFile(trackedPath, []byte("modified\n"), 0o644); err != nil {
		t.Fatalf("update tracked: %v", err)
	}

	ws, cleanup, err := CreateWorkspace(repo)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(ws, ".gomodcache")); !os.IsNotExist(err) {
		t.Fatalf("expected .gomodcache to be absent, got err=%v", err)
	}

	data, err := os.ReadFile(filepath.Join(ws, "tracked.txt"))
	if err != nil {
		t.Fatalf("read tracked: %v", err)
	}
	if string(data) != "modified\n" {
		t.Fatalf("expected tracked content to reflect modifications, got %q", string(data))
	}

	if _, err := os.Stat(filepath.Join(ws, "staged.txt")); err != nil {
		t.Fatalf("expected staged file, got err=%v", err)
	}

	if st, err := os.Stat(filepath.Join(ws, ".git")); err != nil || !st.IsDir() {
		t.Fatalf("expected .git directory in workspace, err=%v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}
