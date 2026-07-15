package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetHeadCommitHashAtUsesExplicitRepository(t *testing.T) {
	repo := t.TempDir()
	runGitUtilsTestCommand(t, repo, "init")
	runGitUtilsTestCommand(t, repo, "config", "user.name", "Test")
	runGitUtilsTestCommand(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runGitUtilsTestCommand(t, repo, "add", "tracked.txt")
	runGitUtilsTestCommand(t, repo, "commit", "-m", "initial")
	want := strings.TrimSpace(runGitUtilsTestCommand(t, repo, "rev-parse", "HEAD"))

	got, err := GetHeadCommitHashAt(repo)
	if err != nil {
		t.Fatalf("GetHeadCommitHashAt: %v", err)
	}
	if got != want {
		t.Fatalf("GetHeadCommitHashAt() = %q, want %q", got, want)
	}
}

func runGitUtilsTestCommand(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
