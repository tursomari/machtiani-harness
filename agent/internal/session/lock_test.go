package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestSessionLockKeepAliveUpdatesModTime(t *testing.T) {
	tempDir := t.TempDir()
	sessionRoot := filepath.Join(tempDir, "session")
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatalf("mkdir session root: %v", err)
	}

	lock, err := acquireSessionLock("test-session", sessionRoot)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	t.Cleanup(func() {
		if lock != nil {
			_ = lock.Close()
		}
	})

	lockPath := filepath.Join(sessionRoot, sessionLockFileName)
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat lock: %v", err)
	}
	initialMod := info.ModTime()

	time.Sleep(sessionLockUpdateInterval + 500*time.Millisecond)

	info, err = os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat lock: %v", err)
	}
	if !info.ModTime().After(initialMod) {
		t.Fatalf("expected lock mod time to advance, got initial=%v, current=%v", initialMod, info.ModTime())
	}

	if err := lock.Close(); err != nil {
		t.Fatalf("close lock: %v", err)
	}
	lock = nil
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected lock file removed, got err=%v", err)
	}
}
