package llm

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLocateConfigPrefersLocalWithinGitRoot(t *testing.T) {
	t.Setenv("MACHTIANI_CONFIG", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWriteFile(t, filepath.Join(home, ".machtiani", "config.toml"), "global = true\n")

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)
	localConfig := filepath.Join(repoDir, ".machtiani", "config.toml")
	mustWriteFile(t, localConfig, "local = true\n")

	subDir := filepath.Join(repoDir, "nested", "child")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subDir, err)
	}

	withWorkingDir(t, subDir, func() {
		path, err := locateConfig()
		if err != nil {
			t.Fatalf("locateConfig: %v", err)
		}
		if path != localConfig {
			t.Fatalf("expected local config %s, got %s", localConfig, path)
		}
	})
}

func TestLocateConfigFallsBackToGlobalConfig(t *testing.T) {
	t.Setenv("MACHTIANI_CONFIG", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalConfig := filepath.Join(home, ".machtiani", "config.toml")
	mustWriteFile(t, globalConfig, "global = true\n")

	repoDir := t.TempDir()
	initGitRepo(t, repoDir)

	subDir := filepath.Join(repoDir, "deep")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subDir, err)
	}

	withWorkingDir(t, subDir, func() {
		path, err := locateConfig()
		if err != nil {
			t.Fatalf("locateConfig: %v", err)
		}
		if path != globalConfig {
			t.Fatalf("expected global config %s, got %s", globalConfig, path)
		}
	})
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
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
