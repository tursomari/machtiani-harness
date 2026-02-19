package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestSyncSnapshotToHost_RespectsIgnoreConfig(t *testing.T) {
	root := t.TempDir()
	snapshotRepoRoot := filepath.Join(root, "snapshot", "repo")
	hostRepoRoot := filepath.Join(root, "host")
	patchDir := filepath.Join(root, "patches")

	if err := os.MkdirAll(snapshotRepoRoot, 0o755); err != nil {
		t.Fatalf("mkdir snapshot repo: %v", err)
	}
	if err := os.MkdirAll(hostRepoRoot, 0o755); err != nil {
		t.Fatalf("mkdir host repo: %v", err)
	}
	if err := os.MkdirAll(patchDir, 0o755); err != nil {
		t.Fatalf("mkdir patch dir: %v", err)
	}

	// Create a minimal git repo on host
	if err := os.WriteFile(filepath.Join(hostRepoRoot, "README.md"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = hostRepoRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Create an ignored file in snapshot
	ignoredRel := "ignored_dir/secret.txt"
	ignoredSnap := filepath.Join(snapshotRepoRoot, ignoredRel)
	if err := os.MkdirAll(filepath.Dir(ignoredSnap), 0o755); err != nil {
		t.Fatalf("mkdir ignored snap: %v", err)
	}
	if err := os.WriteFile(ignoredSnap, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write ignored snap: %v", err)
	}

	// Create a normal file in snapshot
	normalRel := "normal.txt"
	normalSnap := filepath.Join(snapshotRepoRoot, normalRel)
	if err := os.WriteFile(normalSnap, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write normal snap: %v", err)
	}

	// Setup manifest with ignore config
	manifestPath := filepath.Join(filepath.Dir(snapshotRepoRoot), "manifests", "sync.json")
	m := &syncManifest{
		Version: 1,
		IgnoreConfig: &llm.IgnoreConfig{
			Paths: []string{"ignored_dir"},
		},
		CreatedPaths: map[string]struct{}{},
	}
	if err := saveManifest(manifestPath, m); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if err := SyncSnapshotToHost(snapshotRepoRoot, hostRepoRoot, patchDir); err != nil {
		t.Fatalf("SyncSnapshotToHost: %v", err)
	}

	// Verify ignored file is NOT on host
	if _, err := os.Stat(filepath.Join(hostRepoRoot, ignoredRel)); err == nil {
		t.Fatalf("expected ignored file to NOT be on host")
	}

	// Verify normal file IS on host
	if _, err := os.Stat(filepath.Join(hostRepoRoot, normalRel)); err != nil {
		t.Fatalf("expected normal file to be on host, got %v", err)
	}
}

func TestSyncSnapshotToHost_RespectsHardExcluded(t *testing.T) {
	root := t.TempDir()
	snapshotRepoRoot := filepath.Join(root, "snapshot", "repo")
	hostRepoRoot := filepath.Join(root, "host")
	patchDir := filepath.Join(root, "patches")

	if err := os.MkdirAll(snapshotRepoRoot, 0o755); err != nil {
		t.Fatalf("mkdir snapshot repo: %v", err)
	}
	if err := os.MkdirAll(hostRepoRoot, 0o755); err != nil {
		t.Fatalf("mkdir host repo: %v", err)
	}

	// Create a minimal git repo on host
	if err := os.WriteFile(filepath.Join(hostRepoRoot, "README.md"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = hostRepoRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Create .git.hydrated in snapshot
	hydratedRel := "nested/.git.hydrated/HEAD"
	hydratedSnap := filepath.Join(snapshotRepoRoot, hydratedRel)
	if err := os.MkdirAll(filepath.Dir(hydratedSnap), 0o755); err != nil {
		t.Fatalf("mkdir hydrated snap: %v", err)
	}
	if err := os.WriteFile(hydratedSnap, []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write hydrated snap: %v", err)
	}

	// Setup manifest
	manifestPath := filepath.Join(filepath.Dir(snapshotRepoRoot), "manifests", "sync.json")
	m := &syncManifest{
		Version:      1,
		CreatedPaths: map[string]struct{}{},
	}
	if err := saveManifest(manifestPath, m); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if err := SyncSnapshotToHost(snapshotRepoRoot, hostRepoRoot, patchDir); err != nil {
		t.Fatalf("SyncSnapshotToHost: %v", err)
	}

	// Verify .git.hydrated is NOT on host
	if _, err := os.Stat(filepath.Join(hostRepoRoot, "nested", ".git.hydrated")); err == nil {
		t.Fatalf("expected .git.hydrated to NOT be on host")
	}
}

func TestSyncSnapshotToHost_ExcludesTrajectoryArtifacts(t *testing.T) {
	root := t.TempDir()
	snapshotRepoRoot := filepath.Join(root, "snapshot", "repo")
	hostRepoRoot := filepath.Join(root, "host")
	patchDir := filepath.Join(root, "patches")

	if err := os.MkdirAll(snapshotRepoRoot, 0o755); err != nil {
		t.Fatalf("mkdir snapshot repo: %v", err)
	}
	if err := os.MkdirAll(hostRepoRoot, 0o755); err != nil {
		t.Fatalf("mkdir host repo: %v", err)
	}
	if err := os.MkdirAll(patchDir, 0o755); err != nil {
		t.Fatalf("mkdir patch dir: %v", err)
	}

	// Create a minimal git repo on host
	if err := os.WriteFile(filepath.Join(hostRepoRoot, "README.md"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = hostRepoRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Create trajectory artifacts in snapshot
	partialRel := "partial-trajectory-1.json"
	partialSnap := filepath.Join(snapshotRepoRoot, partialRel)
	if err := os.WriteFile(partialSnap, []byte(`{"step":1}`+"\n"), 0o644); err != nil {
		t.Fatalf("write partial trajectory snap: %v", err)
	}

	trajRel := "trajectory-20260101-000000.json"
	trajSnap := filepath.Join(snapshotRepoRoot, trajRel)
	if err := os.WriteFile(trajSnap, []byte(`{"exit_status":"Ongoing"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write trajectory snap: %v", err)
	}

	// Create a normal file in snapshot to ensure sync still works.
	normalRel := "normal.txt"
	normalSnap := filepath.Join(snapshotRepoRoot, normalRel)
	if err := os.WriteFile(normalSnap, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write normal snap: %v", err)
	}

	manifestPath := filepath.Join(filepath.Dir(snapshotRepoRoot), "manifests", "sync.json")
	m := &syncManifest{
		Version:      1,
		CreatedPaths: map[string]struct{}{},
	}
	if err := saveManifest(manifestPath, m); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if err := SyncSnapshotToHost(snapshotRepoRoot, hostRepoRoot, patchDir); err != nil {
		t.Fatalf("SyncSnapshotToHost: %v", err)
	}

	// Verify trajectory artifacts are NOT on host
	if _, err := os.Stat(filepath.Join(hostRepoRoot, partialRel)); err == nil {
		t.Fatalf("expected partial trajectory to NOT be on host")
	}
	if _, err := os.Stat(filepath.Join(hostRepoRoot, trajRel)); err == nil {
		t.Fatalf("expected trajectory to NOT be on host")
	}

	// Verify normal file IS on host
	if _, err := os.Stat(filepath.Join(hostRepoRoot, normalRel)); err != nil {
		t.Fatalf("expected normal file to be on host, got %v", err)
	}

	updated, err := loadOrInitManifest(manifestPath)
	if err != nil {
		t.Fatalf("reload manifest: %v", err)
	}
	if _, ok := updated.CreatedPaths[partialRel]; ok {
		t.Fatalf("expected partial trajectory to be excluded from created paths")
	}
	if _, ok := updated.CreatedPaths[trajRel]; ok {
		t.Fatalf("expected trajectory to be excluded from created paths")
	}
}
