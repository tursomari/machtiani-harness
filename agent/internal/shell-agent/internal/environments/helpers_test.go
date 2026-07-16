package environments

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
	cmd = exec.Command("git", "-C", dir, "config", "user.email", "test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config user.email: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-C", dir, "config", "user.name", "Test User")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config user.name: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("write tracked.txt in %s: %v", dir, err)
	}
	cmd = exec.Command("git", "-C", dir, "add", "tracked.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add tracked.txt in %s: %v\n%s", dir, err, out)
	}
	cmd = exec.Command("git", "-C", dir, "commit", "-m", "initial commit")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit in %s: %v\n%s", dir, err, out)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return string(out)
}
