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
