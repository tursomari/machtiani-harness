package artifacts

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestChatDirectoryLocalRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	subdir := filepath.Join(repoDir, "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subdir, err)
	}

	withWorkingDir(t, subdir, func() {
		dir, err := ChatDirectory()
		if err != nil {
			t.Fatalf("ChatDirectory: %v", err)
		}
		expected := filepath.Join(repoDir, ".machtiani", "chats")
		if dir != expected {
			t.Fatalf("expected %s, got %s", expected, dir)
		}
	})
}

func TestChatDirectoryGlobalFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	work := t.TempDir()
	withWorkingDir(t, work, func() {
		dir, err := ChatDirectory()
		if err != nil {
			t.Fatalf("ChatDirectory: %v", err)
		}
		expected := filepath.Join(home, ".machtiani", "chats")
		if dir != expected {
			t.Fatalf("expected %s, got %s", expected, dir)
		}
	})
}

func TestReadmeDirectoryLocalRepo(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	withWorkingDir(t, repoDir, func() {
		dir, err := ReadmeDirectory()
		if err != nil {
			t.Fatalf("ReadmeDirectory: %v", err)
		}
		expected := filepath.Join(repoDir, ".machtiani", "readme")
		if dir != expected {
			t.Fatalf("expected %s, got %s", expected, dir)
		}
	})
}

func TestReadmeDirectoryRequiresGit(t *testing.T) {
	work := t.TempDir()
	withWorkingDir(t, work, func() {
		if _, err := ReadmeDirectory(); err == nil {
			t.Fatal("expected error outside git repo")
		}
	})
}

func TestIsLocalContext(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	withWorkingDir(t, repoDir, func() {
		local, err := IsLocalContext()
		if err != nil {
			t.Fatalf("IsLocalContext: %v", err)
		}
		if !local {
			t.Fatal("expected local context inside git repo")
		}
	})

	work := t.TempDir()
	withWorkingDir(t, work, func() {
		local, err := IsLocalContext()
		if err != nil {
			t.Fatalf("IsLocalContext: %v", err)
		}
		if local {
			t.Fatal("expected global context outside git repo")
		}
	})
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()
	fn()
}
