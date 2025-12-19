package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestMirrorNestedRepos_RespectsIgnoreConfig(t *testing.T) {
	repoRoot := t.TempDir()
	snapshotRoot := t.TempDir()

	// Create a nested repo in an ignored directory
	ignoredDir := filepath.Join(repoRoot, "ignored_dir")
	nested := filepath.Join(ignoredDir, "nested_repo")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "LICENSE"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Create a nested repo in a non-ignored directory
	normalDir := filepath.Join(repoRoot, "normal_dir")
	nestedNormal := filepath.Join(normalDir, "nested_repo")
	if err := os.MkdirAll(filepath.Join(nestedNormal, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedNormal, "LICENSE"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ignoreCfg := &llm.IgnoreConfig{
		Paths: []string{"ignored_dir"},
	}

	if err := mirrorNestedRepos(repoRoot, snapshotRoot, ignoreCfg, false); err != nil {
		t.Fatalf("mirrorNestedRepos: %v", err)
	}

	// Verify ignored nested repo is NOT copied
	if _, err := os.Stat(filepath.Join(snapshotRoot, "ignored_dir", "nested_repo", "LICENSE")); err == nil {
		t.Fatalf("expected ignored nested repo to NOT be copied")
	}

	// Verify normal nested repo IS copied
	if _, err := os.Stat(filepath.Join(snapshotRoot, "normal_dir", "nested_repo", "LICENSE")); err != nil {
		t.Fatalf("expected normal nested repo to be copied, got %v", err)
	}
}

func TestHydrateNestedReposGitMetadata_RespectsIgnoreConfig(t *testing.T) {
	repoRoot := t.TempDir()
	snapshotRoot := t.TempDir()

	// Create a nested repo in an ignored directory
	ignoredDir := filepath.Join(repoRoot, "ignored_dir")
	nested := filepath.Join(ignoredDir, "nested_repo")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Create a nested repo in a non-ignored directory
	normalDir := filepath.Join(repoRoot, "normal_dir")
	nestedNormal := filepath.Join(normalDir, "nested_repo")
	if err := os.MkdirAll(filepath.Join(nestedNormal, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedNormal, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Simulate snapshot already has the working trees copied (excluding .git)
	if err := os.MkdirAll(filepath.Join(snapshotRoot, "normal_dir", "nested_repo"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ignoreCfg := &llm.IgnoreConfig{
		Paths: []string{"ignored_dir"},
	}
	cfg := &llm.WorkspaceConfig{
		GitHydration: []llm.GitHydrationRule{
			{Root: "ignored_dir/nested_repo"},
			{Root: "normal_dir/nested_repo"},
		},
	}

	if err := hydrateNestedReposGitMetadata(repoRoot, snapshotRoot, cfg, ignoreCfg, false); err != nil {
		t.Fatalf("hydrateNestedReposGitMetadata: %v", err)
	}

	// Verify ignored nested repo git metadata is NOT hydrated
	if _, err := os.Stat(filepath.Join(snapshotRoot, "ignored_dir", "nested_repo", ".git", "HEAD")); err == nil {
		t.Fatalf("expected ignored nested repo git metadata to NOT be hydrated")
	}

	// Verify normal nested repo git metadata IS hydrated
	if _, err := os.Stat(filepath.Join(snapshotRoot, "normal_dir", "nested_repo", ".git", "HEAD")); err != nil {
		t.Fatalf("expected normal nested repo git metadata to be hydrated, got %v", err)
	}
}
