package session

import (
	"os"
	"path/filepath"
	"strings"
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
