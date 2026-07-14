package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
)

func TestNewTrajectoryWriterReportsProjectRootForUUIDStore(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	t.Setenv("HOME", home)
	cmd := exec.Command("git", "init", "--quiet", repo)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	id := uuid.New()
	if err := projectstore.WriteProjectUUID(repo, id); err != nil {
		t.Fatal(err)
	}
	if err := projectstore.EnsureLayout(filepath.Join(home, ".machtiani", id.String())); err != nil {
		t.Fatal(err)
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	writer, root, err := newTrajectoryWriter(legacyConfig{}, "trajectory-root-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if root != repo {
		t.Fatalf("repo root = %q, want %q", root, repo)
	}
}
