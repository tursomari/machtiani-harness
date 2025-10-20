package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSyncCommandUsesHeadCommit(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://example.com/v1")
	t.Setenv("OPENAI_MODEL", "test-model")
	t.Setenv("MCT_README_TEST_STUB", "basic")

	repoDir := initTestRepo(t)
	origWD := mustChdir(t, repoDir)
	defer mustChdir(t, origWD)

	exitCode := handleSyncCommand(nil)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	readmePath := filepath.Join(repoDir, ".machtiani", "artifacts", "readme", "internal-readme.md")
	if _, err := os.Stat(readmePath); err != nil {
		t.Fatalf("expected readme file at %s: %v", readmePath, err)
	}
}

func TestSyncCommandInvalidCommit(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://example.com/v1")
	t.Setenv("OPENAI_MODEL", "test-model")
	repoDir := initTestRepo(t)
	origWD := mustChdir(t, repoDir)
	defer mustChdir(t, origWD)

	exitCode := handleSyncCommand([]string{"--commit", "deadbeef"})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code for invalid commit")
	}
}

func TestSyncCommandRequiresGitRepository(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://example.com/v1")
	t.Setenv("OPENAI_MODEL", "test-model")
	t.Setenv("MCT_README_TEST_STUB", "basic")

	tempDir := t.TempDir()
	origWD := mustChdir(t, tempDir)
	defer mustChdir(t, origWD)

	exitCode := handleSyncCommand(nil)
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code outside git repo")
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test User")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "initial commit")
	return repoDir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
}

func mustChdir(t *testing.T, dir string) string {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	return orig
}
