package artifacts

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSessionChatDirectoryLocalRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	subdir := filepath.Join(repoDir, "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subdir, err)
	}

	withWorkingDir(t, subdir, func() {
		const sessionID = "session-123"
		dir, err := SessionChatDirectory(sessionID)
		if err != nil {
			t.Fatalf("SessionChatDirectory: %v", err)
		}
		expected := filepath.Join(repoDir, ".machtiani", "sessions", sessionID, "chat")
		if dir != expected {
			t.Fatalf("expected %s, got %s", expected, dir)
		}
	})
}

func TestSessionChatDirectoryGlobalFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	work := t.TempDir()
	withWorkingDir(t, work, func() {
		const sessionID = "session-abc"
		dir, err := SessionChatDirectory(sessionID)
		if err != nil {
			t.Fatalf("SessionChatDirectory: %v", err)
		}
		expected := filepath.Join(home, ".machtiani", "sessions", sessionID, "chat")
		if dir != expected {
			t.Fatalf("expected %s, got %s", expected, dir)
		}
	})
}

func TestSessionArtifactsDirectoryLocalRepo(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	withWorkingDir(t, repoDir, func() {
		const sessionID = "session-xyz"
		dir, err := SessionArtifactsDirectory(sessionID)
		if err != nil {
			t.Fatalf("SessionArtifactsDirectory: %v", err)
		}
		expected := filepath.Join(repoDir, ".machtiani", "sessions", sessionID, "artifacts")
		if dir != expected {
			t.Fatalf("expected %s, got %s", expected, dir)
		}
	})
}

func TestSessionTrajectoryDirectoryLocalRepo(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	withWorkingDir(t, repoDir, func() {
		const sessionID = "session-traj"
		dir, err := SessionTrajectoryDirectory(sessionID)
		if err != nil {
			t.Fatalf("SessionTrajectoryDirectory: %v", err)
		}
		expected := filepath.Join(repoDir, ".machtiani", "sessions", sessionID, "trajectory")
		if dir != expected {
			t.Fatalf("expected %s, got %s", expected, dir)
		}
	})
}

func TestSessionPatchesDirectoryLocalRepo(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	withWorkingDir(t, repoDir, func() {
		const sessionID = "session-patch"
		dir, err := SessionPatchesDirectory(sessionID)
		if err != nil {
			t.Fatalf("SessionPatchesDirectory: %v", err)
		}
		expected := filepath.Join(repoDir, ".machtiani", "sessions", sessionID, "artifacts", "patches")
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
		expected := filepath.Join(repoDir, ".machtiani", "artifacts", "readme")
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

func TestFileDiscoveryTrajectoryPath(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	withWorkingDir(t, repoDir, func() {
		const sessionID = "session-123"
		dir, err := FileDiscoveryTrajectoryPath(sessionID)
		if err != nil {
			t.Fatalf("FileDiscoveryTrajectoryPath: %v", err)
		}
		expected := filepath.Join(repoDir, ".machtiani", "sessions", sessionID, "artifacts", "file-discovery.jsonl")
		if dir != expected {
			t.Fatalf("expected %s, got %s", expected, dir)
		}

		file, err := SessionTrajectoryFile(sessionID, "agent")
		if err != nil {
			t.Fatalf("SessionTrajectoryFile: %v", err)
		}
		expectedFile := filepath.Join(repoDir, ".machtiani", "sessions", sessionID, "trajectory", "agent.jsonl")
		if file != expectedFile {
			t.Fatalf("expected %s, got %s", expectedFile, file)
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
