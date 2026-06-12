package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestAcquireSessionLockExclusive(t *testing.T) {
	tempDir := t.TempDir()
	sessionRoot := filepath.Join(tempDir, "session")
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatalf("mkdir session root: %v", err)
	}

	lock, err := acquireSessionLock("test-session", sessionRoot)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer lock.Close()

	if _, err := acquireSessionLock("test-session-2", sessionRoot); err == nil {
		t.Fatalf("expected second lock acquisition to fail")
	} else if !strings.Contains(err.Error(), "session already active") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAcquireSessionLockWithStalePid(t *testing.T) {
	tempDir := t.TempDir()
	sessionRoot := filepath.Join(tempDir, "session")

	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatalf("mkdir session root: %v", err)
	}

	lockPath := filepath.Join(sessionRoot, sessionLockFileName)
	// Write a lock file with a non-existent PID to simulate a stale lock.
	if err := os.WriteFile(lockPath, []byte("session_id=stale\npid=99999\n"), 0o644); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	lock, err := acquireSessionLock("test-session", sessionRoot)
	if err != nil {
		t.Fatalf("acquire lock with stale PID should succeed: %v", err)
	}
	lock.Close()
}

func TestAcquireSessionLockWithLivePidRejects(t *testing.T) {
	tempDir := t.TempDir()
	sessionRoot := filepath.Join(tempDir, "session")

	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatalf("mkdir session root: %v", err)
	}

	lockPath := filepath.Join(sessionRoot, sessionLockFileName)
	// Write pid=0 which resolves to our own on some systems, but use our actual PID.
	info := fmt.Sprintf("session_id=live\npid=%d\n", os.Getpid())
	if err := os.WriteFile(lockPath, []byte(info), 0o644); err != nil {
		t.Fatalf("write live lock: %v", err)
	}

	// Acquiring should succeed because the PID is our own process (validateLockPID returns nil for our own PID).
	lock, err := acquireSessionLock("test-session-self", sessionRoot)
	if err != nil {
		t.Fatalf("acquire lock with own PID should succeed: %v", err)
	}
	defer lock.Close()

	// Now try to acquire another lock on the same file with a different session ID.
	// The flock is still held exclusively by the first lock, so it should fail with "session already active".
	if _, err2 := acquireSessionLock("test-session-other", sessionRoot); err2 == nil {
		t.Fatalf("expected second lock acquisition to fail")
	} else if !strings.Contains(err2.Error(), "session already active") {
		t.Fatalf("unexpected error: %v", err2)
	}
}

func TestCleanupSkipsActiveSessionLock(t *testing.T) {
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, "tmp")
	sessionID := "session-test-active-cleanup"
	sessionPath := filepath.Join(root, sessionID)

	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}

	lockPath := filepath.Join(sessionPath, sessionLockFileName)
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		t.Fatalf("flock: %v", err)
	}
	defer func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}()

	// Write PID info to the lock file so it has valid content.
	info := fmt.Sprintf("session_id=%s\npid=%d\n", sessionID, os.Getpid())
	if err := os.WriteFile(lockPath, []byte(info), 0o644); err != nil {
		t.Fatalf("write lock content: %v", err)
	}

	// Run cleanup on the parent directory. The active session must be preserved.
	if err := cleanupOrphanedSessionDirs(root, false); err != nil {
		t.Fatalf("cleanup session dirs: %v", err)
	}

	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("expected active session directory to remain, got err=%v", err)
	}
}

func TestCleanupRemovesStaleSessionLock(t *testing.T) {
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, "tmp")
	sessionID := "session-test-stale-cleanup"
	sessionPath := filepath.Join(root, sessionID)

	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}

	lockPath := filepath.Join(sessionPath, sessionLockFileName)
	// Write a lock file without holding a flock — simulates a crashed session.
	if err := os.WriteFile(lockPath, []byte("session_id=stale\npid=99999\n"), 0o644); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	// Run cleanup. The stale session must be removed.
	if err := cleanupOrphanedSessionDirs(root, false); err != nil {
		t.Fatalf("cleanup session dirs: %v", err)
	}

	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale session directory removed, got err=%v", err)
	}
}
