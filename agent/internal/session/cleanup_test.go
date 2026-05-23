package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

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
			t.Fatalf("restore dir: %v", err)
		}
	}()
	fn()
}

func TestCleanupRemovesOldMatchingDir(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()

	target := filepath.Join(tempDir, "mini-swe-trajectories-old")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	marker := filepath.Join(target, "marker.txt")
	if err := os.WriteFile(marker, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatalf("chtimes target: %v", err)
	}
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatalf("chtimes marker: %v", err)
	}

	if err := cleanupOrphanedTempDirsInternal(tempDir, now, 24*time.Hour, false); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected directory removed, got err=%v", err)
	}
}

func TestCleanupSkipsRecentDir(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()

	target := filepath.Join(tempDir, "shell-agent-worktree-recent")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	recent := now.Add(-2 * time.Hour)
	if err := os.Chtimes(target, recent, recent); err != nil {
		t.Fatalf("chtimes target: %v", err)
	}

	if err := cleanupOrphanedTempDirsInternal(tempDir, now, 24*time.Hour, false); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected directory to remain, got err=%v", err)
	}
}

func TestCleanupRemovesOldWorkspaceDir(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()

	target := filepath.Join(tempDir, "workspace-agent-20260313T201117-7837")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	marker := filepath.Join(target, "marker.txt")
	if err := os.WriteFile(marker, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatalf("chtimes target: %v", err)
	}
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatalf("chtimes marker: %v", err)
	}

	if err := cleanupOrphanedTempDirsInternal(tempDir, now, 24*time.Hour, false); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected directory removed, got err=%v", err)
	}
}

func TestCleanupRemovesStaleShellAgentMarker(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	markerDir := filepath.Join(root, "shell-agent", "markers")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatalf("mkdir marker dir: %v", err)
	}
	markerPath := filepath.Join(markerDir, "mct-swe-agent-finale-abc123.txt")
	if err := os.WriteFile(markerPath, []byte("done"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(markerPath, old, old); err != nil {
		t.Fatalf("chtimes marker: %v", err)
	}

	if err := cleanupStaleShellAgentMarkersAt(root, now, time.Hour, false); err != nil {
		t.Fatalf("cleanup markers: %v", err)
	}

	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("expected marker removed, got err=%v", err)
	}
}

func TestCleanupSkipsRecentShellAgentMarker(t *testing.T) {
	root := t.TempDir()
	now := time.Now()

	markerDir := filepath.Join(root, "shell-agent", "markers")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatalf("mkdir marker dir: %v", err)
	}
	markerPath := filepath.Join(markerDir, "mct-swe-agent-finale-recent.txt")
	if err := os.WriteFile(markerPath, []byte("done"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	recent := now.Add(-30 * time.Minute)
	if err := os.Chtimes(markerPath, recent, recent); err != nil {
		t.Fatalf("chtimes marker: %v", err)
	}

	if err := cleanupStaleShellAgentMarkersAt(root, now, time.Hour, false); err != nil {
		t.Fatalf("cleanup markers: %v", err)
	}

	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("expected marker to remain, got err=%v", err)
	}
}

func TestCleanupSkipsLockProtectedDir(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()

	target := filepath.Join(tempDir, "workspace-mirror-locked")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lockFile := filepath.Join(target, "index.lock")
	if err := os.WriteFile(lockFile, []byte("lock"), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatalf("chtimes target: %v", err)
	}
	if err := os.Chtimes(lockFile, old, old); err != nil {
		t.Fatalf("chtimes lock: %v", err)
	}

	if err := cleanupOrphanedTempDirsInternal(tempDir, now, 24*time.Hour, false); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected directory to remain, got err=%v", err)
	}
}

func TestCleanupIgnoresNonMatchingPrefix(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()

	target := filepath.Join(tempDir, "unrelated-prefix")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatalf("chtimes target: %v", err)
	}

	if err := cleanupOrphanedTempDirsInternal(tempDir, now, 24*time.Hour, false); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected directory to remain, got err=%v", err)
	}
}

func TestCleanupSessionDirsRemovesDirWithoutLock(t *testing.T) {
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, ".machtiani", "tmp")
	sessionPath := filepath.Join(root, "session-missing-lock")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}

	if err := cleanupOrphanedSessionDirs(root, false); err != nil {
		t.Fatalf("cleanup session dirs: %v", err)
	}

	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("expected session directory removed, got err=%v", err)
	}
}

func TestCleanupSessionDirsRemovesStaleLock(t *testing.T) {
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, ".machtiani", "tmp")
	sessionPath := filepath.Join(root, "session-stale")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	lockPath := filepath.Join(sessionPath, sessionLockFileName)
	// Create a lock file without holding a flock — this simulates an
	// orphaned session where the process has exited and released its lock.
	if err := os.WriteFile(lockPath, []byte("lock"), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	if err := cleanupOrphanedSessionDirs(root, false); err != nil {
		t.Fatalf("cleanup session dirs: %v", err)
	}

	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("expected session directory removed, got err=%v", err)
	}
}

func TestCleanupSessionDirsSkipsFreshLock(t *testing.T) {
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, ".machtiani", "tmp")
	sessionPath := filepath.Join(root, "session-fresh")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	lockPath := filepath.Join(sessionPath, sessionLockFileName)
	// Open the lock file and acquire an exclusive flock to simulate a live session.
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}
	defer func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}()

	if err := cleanupOrphanedSessionDirs(root, false); err != nil {
		t.Fatalf("cleanup session dirs: %v", err)
	}

	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("expected session directory to remain, got err=%v", err)
	}
}

func TestCleanupSessionDirsSkipsWorkspaceForActiveSession(t *testing.T) {
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, ".machtiani", "tmp")
	sessionID := "agent-20260321T000716-9363"
	sessionPath := filepath.Join(root, sessionID)
	workspacePath := filepath.Join(root, "workspace-"+sessionID)
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspacePath, "repo"), 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	lockPath := filepath.Join(sessionPath, sessionLockFileName)
	// Simulate a live session by holding an exclusive flock.
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}
	defer func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}()

	if err := cleanupOrphanedSessionDirs(root, false); err != nil {
		t.Fatalf("cleanup session dirs: %v", err)
	}

	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("expected session directory to remain, got err=%v", err)
	}
	if _, err := os.Stat(workspacePath); err != nil {
		t.Fatalf("expected active workspace directory to remain, got err=%v", err)
	}
}

// TestHelperProcess is a test helper that runs as a child process.
// When the GO_TEST_HELPER_PROCESS environment variable is set to "1",
// it opens the lock file specified by TEST_LOCK_PATH, acquires an
// exclusive flock, and blocks until stdin is closed.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
		return
	}
	lockPath := os.Getenv("TEST_LOCK_PATH")
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		os.Exit(1)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		os.Exit(1)
	}
	// Block until stdin is closed by the parent.
	buf := make([]byte, 1)
	os.Stdin.Read(buf)
	f.Close()
	os.Exit(0)
}

func TestCleanupSessionDirsSkipsLockHeldByChildProcess(t *testing.T) {
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, ".machtiani", "tmp")

	// Create a live session directory with a session.lock file.
	liveSession := filepath.Join(root, "session-held-by-child")
	if err := os.MkdirAll(liveSession, 0755); err != nil {
		t.Fatalf("mkdir live session: %v", err)
	}
	liveLock := filepath.Join(liveSession, sessionLockFileName)
	if err := os.WriteFile(liveLock, []byte("lock"), 0644); err != nil {
		t.Fatalf("write live lock: %v", err)
	}

	// Spawn a child process that holds an exclusive flock on the lock file.
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(),
		"GO_TEST_HELPER_PROCESS=1",
		"TEST_LOCK_PATH="+liveLock,
	)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() {
		stdinPipe.Close()
		cmd.Wait()
	}()

	// Wait for the child to acquire the exclusive lock by polling
	// until LOCK_EX|LOCK_NB returns EWOULDBLOCK.
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(liveLock, os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("open lock for polling: %v", err)
		}
		flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(flockErr, syscall.EWOULDBLOCK) {
			f.Close()
			break // child holds the lock
		}
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		if flockErr != nil {
			t.Fatalf("flock poll: %v", flockErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for child to acquire lock")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Create an orphan session directory with a lock file but no flock held.
	orphanSession := filepath.Join(root, "session-orphan")
	if err := os.MkdirAll(orphanSession, 0755); err != nil {
		t.Fatalf("mkdir orphan session: %v", err)
	}
	orphanLock := filepath.Join(orphanSession, sessionLockFileName)
	if err := os.WriteFile(orphanLock, []byte("lock"), 0644); err != nil {
		t.Fatalf("write orphan lock: %v", err)
	}

	// Run cleanup.
	if err := cleanupOrphanedSessionDirs(root, false); err != nil {
		t.Fatalf("cleanup session dirs: %v", err)
	}

	// Live session must be preserved.
	if _, err := os.Stat(liveSession); err != nil {
		t.Fatalf("expected live session to remain, got err=%v", err)
	}

	// Orphan session must be removed.
	if _, err := os.Stat(orphanSession); !os.IsNotExist(err) {
		t.Fatalf("expected orphan session removed, got err=%v", err)
	}
}

func TestCleanupOrphanedTempDirsPreservesNearbySessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, ".machtiani", "tmp")

	// Create a live session directory with a child process holding the lock.
	liveSession := filepath.Join(root, "session-live-nearby")
	if err := os.MkdirAll(liveSession, 0755); err != nil {
		t.Fatalf("mkdir live session: %v", err)
	}
	liveLock := filepath.Join(liveSession, sessionLockFileName)
	if err := os.WriteFile(liveLock, []byte("lock"), 0644); err != nil {
		t.Fatalf("write live lock: %v", err)
	}

	// Spawn a child holding the lock.
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(),
		"GO_TEST_HELPER_PROCESS=1",
		"TEST_LOCK_PATH="+liveLock,
	)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() {
		stdinPipe.Close()
		cmd.Wait()
	}()

	// Wait for lock acquisition.
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(liveLock, os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("open lock for polling: %v", err)
		}
		flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(flockErr, syscall.EWOULDBLOCK) {
			f.Close()
			break
		}
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		if flockErr != nil {
			t.Fatalf("flock poll: %v", flockErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for child to acquire lock")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Create an orphan session directory without a held lock.
	orphanSession := filepath.Join(root, "session-orphan-nearby")
	if err := os.MkdirAll(orphanSession, 0755); err != nil {
		t.Fatalf("mkdir orphan session: %v", err)
	}
	orphanLock := filepath.Join(orphanSession, sessionLockFileName)
	if err := os.WriteFile(orphanLock, []byte("lock"), 0644); err != nil {
		t.Fatalf("write orphan lock: %v", err)
	}

	// Change to an unrelated working directory so ScratchRoots
	// returns only the global HOME-based root.
	work := t.TempDir()
	withWorkingDir(t, work, func() {
		if err := cleanupOrphanedTempDirs(false); err != nil {
			t.Fatalf("cleanupOrphanedTempDirs: %v", err)
		}
	})

	// Live session must be preserved.
	if _, err := os.Stat(liveSession); err != nil {
		t.Fatalf("expected live session to remain, got err=%v", err)
	}

	// Orphan session must be removed.
	if _, err := os.Stat(orphanSession); !os.IsNotExist(err) {
		t.Fatalf("expected orphan session removed, got err=%v", err)
	}
}

func TestCleanupOrphanedTempDirsTargetsScratchRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	work := t.TempDir()
	root := filepath.Join(home, ".machtiani", "tmp")
	sessionPath := filepath.Join(root, "session-missing-lock")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}

	withWorkingDir(t, work, func() {
		if err := cleanupOrphanedTempDirs(false); err != nil {
			t.Fatalf("cleanupOrphanedTempDirs: %v", err)
		}
	})

	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("expected session directory removed, got err=%v", err)
	}
}
