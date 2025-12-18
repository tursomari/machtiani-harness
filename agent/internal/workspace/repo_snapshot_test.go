package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestShouldHydrateGitForPath_DisallowsRootWhenAllowlistPresent(t *testing.T) {
	cfg := &llm.WorkspaceConfig{
		GitHydration: []llm.GitHydrationRule{{
			Root:     "tests/repositories/undici",
			Branches: []string{"main"},
		}},
	}

	if shouldHydrateGitForPath(".", cfg) {
		t.Fatalf("expected repo root to be disallowed when git_hydration is set")
	}
	if !shouldHydrateGitForPath("tests/repositories/undici", cfg) {
		t.Fatalf("expected configured hydration root to be allowed")
	}
}

func TestShouldHydrateGitForPath_DefaultOnlyAllowsRoot(t *testing.T) {
	if !shouldHydrateGitForPath(".", nil) {
		t.Fatalf("expected repo root to be allowed by default")
	}
	if shouldHydrateGitForPath("vendor/somelib", nil) {
		t.Fatalf("expected non-root paths to be disallowed by default")
	}
}

func TestMirrorNestedRepos_CopiesFilesWhenHydrationDisabled(t *testing.T) {
	repoRoot := t.TempDir()
	snapshotRoot := t.TempDir()

	nested := filepath.Join(repoRoot, "nested")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "LICENSE"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := &llm.WorkspaceConfig{}
	if err := mirrorNestedRepos(repoRoot, snapshotRoot, cfg, false); err != nil {
		t.Fatalf("mirrorNestedRepos: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshotRoot, "nested", "LICENSE")); err != nil {
		t.Fatalf("expected nested LICENSE to be copied, got %v", err)
	}
}

func TestHydrateNestedReposGitMetadata_CopiesGitDir(t *testing.T) {
	repoRoot := t.TempDir()
	snapshotRoot := t.TempDir()

	nested := filepath.Join(repoRoot, "nested")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "LICENSE"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Simulate snapshot already has the working tree copied.
	if err := copyDirExcludingGit(nested, filepath.Join(snapshotRoot, "nested")); err != nil {
		t.Fatalf("copyDirExcludingGit: %v", err)
	}

	cfg := &llm.WorkspaceConfig{GitHydration: []llm.GitHydrationRule{{Root: "nested"}}}
	if err := hydrateNestedReposGitMetadata(repoRoot, snapshotRoot, cfg, false); err != nil {
		t.Fatalf("hydrateNestedReposGitMetadata: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshotRoot, "nested", ".git", "HEAD")); err != nil {
		t.Fatalf("expected nested .git/HEAD to be copied, got %v", err)
	}
}

func TestHydrateNestedReposGitMetadata_HydratesGitfile(t *testing.T) {
	repoRoot := t.TempDir()
	snapshotRoot := t.TempDir()

	nested := filepath.Join(repoRoot, "nested")
	referenced := filepath.Join(repoRoot, "gitdirs", "nested")
	if err := os.MkdirAll(referenced, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(referenced, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".git"), []byte("gitdir: ../gitdirs/nested\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "LICENSE"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := copyDirExcludingGit(nested, filepath.Join(snapshotRoot, "nested")); err != nil {
		t.Fatalf("copyDirExcludingGit: %v", err)
	}

	cfg := &llm.WorkspaceConfig{GitHydration: []llm.GitHydrationRule{{Root: "nested"}}}
	if err := hydrateNestedReposGitMetadata(repoRoot, snapshotRoot, cfg, false); err != nil {
		t.Fatalf("hydrateNestedReposGitMetadata: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshotRoot, "nested", ".git.hydrated", "HEAD")); err != nil {
		t.Fatalf("expected hydrated gitdir to be copied, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(snapshotRoot, "nested", ".git"))
	if err != nil {
		t.Fatalf("read gitfile: %v", err)
	}
	if !strings.Contains(string(data), "gitdir: .git.hydrated") {
		t.Fatalf("expected snapshot gitfile to point at .git.hydrated, got %q", strings.TrimSpace(string(data)))
	}
}
