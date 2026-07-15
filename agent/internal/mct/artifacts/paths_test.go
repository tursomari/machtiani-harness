package artifacts

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
)

func TestInitializedProjectUsesUUIDHomeStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	id := uuid.New()
	if err := projectstore.WriteProjectUUID(repoDir, id); err != nil {
		t.Fatal(err)
	}

	withWorkingDir(t, repoDir, func() {
		sessions, err := SessionsRoot()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(home, ".machtiani", id.String(), "sessions"); sessions != want {
			t.Fatalf("sessions root = %s, want %s", sessions, want)
		}
		readme, err := ReadmeDirectory()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(home, ".machtiani", id.String(), "artifacts", "readme"); readme != want {
			t.Fatalf("README root = %s, want %s", readme, want)
		}
	})
}

func TestCleanGitProjectRequiresInitialization(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	if err := os.RemoveAll(filepath.Join(repoDir, ".machtiani")); err != nil {
		t.Fatal(err)
	}

	withWorkingDir(t, repoDir, func() {
		for name, resolve := range map[string]func() (string, error){
			"sessions": SessionsRoot,
			"scratch":  ScratchRoot,
			"readme":   ReadmeDirectory,
		} {
			if _, err := resolve(); err == nil {
				t.Fatalf("%s root unexpectedly resolved", name)
			}
		}
	})
}

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

func TestSessionScratchDirectoryLocalRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	withWorkingDir(t, repoDir, func() {
		const sessionID = "session-scratch"
		dir, err := SessionScratchDirectory(sessionID)
		if err != nil {
			t.Fatalf("SessionScratchDirectory: %v", err)
		}
		expected := filepath.Join(repoDir, ".machtiani", "tmp", sessionID)
		if dir != expected {
			t.Fatalf("expected %s, got %s", expected, dir)
		}
	})
}

func TestSessionScratchDirectoryGlobalFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	work := t.TempDir()
	withWorkingDir(t, work, func() {
		const sessionID = "session-global"
		dir, err := SessionScratchDirectory(sessionID)
		if err != nil {
			t.Fatalf("SessionScratchDirectory: %v", err)
		}
		expected := filepath.Join(home, ".machtiani", "tmp", sessionID)
		if dir != expected {
			t.Fatalf("expected %s, got %s", expected, dir)
		}
	})
}

func TestScratchRootsLocalRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	withWorkingDir(t, repoDir, func() {
		roots, err := ScratchRoots()
		if err != nil {
			t.Fatalf("ScratchRoots: %v", err)
		}
		expected := map[string]struct{}{
			filepath.Join(repoDir, ".machtiani", "tmp"): {},
			filepath.Join(home, ".machtiani", "tmp"):    {},
		}
		if len(roots) != len(expected) {
			t.Fatalf("expected %d roots, got %d", len(expected), len(roots))
		}
		for _, root := range roots {
			if _, ok := expected[root]; !ok {
				t.Fatalf("unexpected root: %s", root)
			}
			delete(expected, root)
		}
		if len(expected) != 0 {
			t.Fatalf("missing expected roots: %v", expected)
		}
	})
}

func TestScratchRootsGlobalFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	work := t.TempDir()
	withWorkingDir(t, work, func() {
		roots, err := ScratchRoots()
		if err != nil {
			t.Fatalf("ScratchRoots: %v", err)
		}
		expected := []string{filepath.Join(home, ".machtiani", "tmp")}
		if len(roots) != len(expected) {
			t.Fatalf("expected %d roots, got %d", len(expected), len(roots))
		}
		for i, root := range roots {
			if root != expected[i] {
				t.Fatalf("expected %s, got %s", expected[i], root)
			}
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
func TestSessionConversationFileLocalRepo(t *testing.T) {
	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	withWorkingDir(t, repoDir, func() {
		const sessionID = "session-conv"
		path, err := SessionConversationFile(sessionID)
		if err != nil {
			t.Fatalf("SessionConversationFile: %v", err)
		}
		expected := filepath.Join(repoDir, ".machtiani", "sessions", sessionID, "artifacts", "conversation.json")
		if path != expected {
			t.Fatalf("expected %s, got %s", expected, path)
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

func TestShellAgentTrajectoryPathLocalRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	subdir := filepath.Join(repoDir, "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subdir, err)
	}

	withWorkingDir(t, subdir, func() {
		const sessionID = "session-abc"
		const turn = 3
		path, err := ShellAgentTrajectoryPath(sessionID, turn)
		if err != nil {
			t.Fatalf("ShellAgentTrajectoryPath: %v", err)
		}
		expected := filepath.Join(repoDir, ".machtiani", "sessions", sessionID, "shell-agent", "3", "trajectory.json")
		if path != expected {
			t.Fatalf("expected %s, got %s", expected, path)
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
	if err := os.MkdirAll(filepath.Join(dir, ".machtiani", "sessions"), 0o755); err != nil {
		t.Fatalf("create legacy project state: %v", err)
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
