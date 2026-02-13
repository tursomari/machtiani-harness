package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeSubmoduleWorktreeConfigsForContainer(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, ".git", "modules", "agent", "internal", "shell-agent", "config")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "[core]\n\tworktree = /host/agent/internal/shell-agent\n"
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := SanitizeSubmoduleWorktreeConfigsForContainer(tmp, "/workspace"); err != nil {
		t.Fatalf("sanitize: %v", err)
	}

	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(updated), "worktree = /workspace/agent/internal/shell-agent") {
		t.Fatalf("expected container worktree path, got: %s", string(updated))
	}
}
