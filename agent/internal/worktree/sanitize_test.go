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

func TestSanitizeSubmoduleWorktreeConfigsForContainer_ResolvedGitDirAndCommonDir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".git"), []byte("gitdir: .git.hydrated\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, ".git.hydrated"), 0o755); err != nil {
		t.Fatalf("mkdir hydrated gitdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".git.hydrated", "commondir"), []byte("common\n"), 0o644); err != nil {
		t.Fatalf("write commondir: %v", err)
	}

	paths := []string{
		filepath.Join(tmp, ".git.hydrated", "modules", "agent", "internal", "shell-agent", "config"),
		filepath.Join(tmp, ".git.hydrated", "common", "modules", "agent", "internal", "shell-agent", "config"),
	}
	for _, configPath := range paths {
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", configPath, err)
		}
		content := "[core]\n\tworktree = ../../../../../agent/internal/shell-agent\n"
		if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", configPath, err)
		}
	}

	if err := SanitizeSubmoduleWorktreeConfigsForContainer(tmp, "/workspace"); err != nil {
		t.Fatalf("sanitize: %v", err)
	}

	for _, configPath := range paths {
		updated, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read %s: %v", configPath, err)
		}
		if !strings.Contains(string(updated), "worktree = /workspace/agent/internal/shell-agent") {
			t.Fatalf("expected container worktree path in %s, got: %s", configPath, string(updated))
		}
	}
}
