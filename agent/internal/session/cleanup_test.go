package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func TestCleanupSkipsLockProtectedDir(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()

	target := filepath.Join(tempDir, "patcher-mirror-locked")
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
	root := filepath.Join(tempDir, "mct")
	sessionPath := filepath.Join(root, "session-missing-lock")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}

	now := time.Now()
	if err := cleanupOrphanedSessionDirs(root, now, sessionLockStaleDuration, false); err != nil {
		t.Fatalf("cleanup session dirs: %v", err)
	}

	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("expected session directory removed, got err=%v", err)
	}
}

func TestCleanupSessionDirsRemovesStaleLock(t *testing.T) {
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, "mct")
	sessionPath := filepath.Join(root, "session-stale")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	lockPath := filepath.Join(sessionPath, sessionLockFileName)
	if err := os.WriteFile(lockPath, []byte("lock"), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	stale := time.Now().Add(-5 * time.Second)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatalf("chtimes lock: %v", err)
	}

	if err := cleanupOrphanedSessionDirs(root, time.Now(), sessionLockStaleDuration, false); err != nil {
		t.Fatalf("cleanup session dirs: %v", err)
	}

	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("expected session directory removed, got err=%v", err)
	}
}

func TestCleanupSessionDirsSkipsFreshLock(t *testing.T) {
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, "mct")
	sessionPath := filepath.Join(root, "session-fresh")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	lockPath := filepath.Join(sessionPath, sessionLockFileName)
	if err := os.WriteFile(lockPath, []byte("lock"), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	if err := cleanupOrphanedSessionDirs(root, time.Now(), sessionLockStaleDuration, false); err != nil {
		t.Fatalf("cleanup session dirs: %v", err)
	}

	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("expected session directory to remain, got err=%v", err)
	}
}
