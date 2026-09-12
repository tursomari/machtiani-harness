package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCoordinatedOwnerPreventsStandaloneMutation(t *testing.T) {
	home := t.TempDir()
	m := NewManager(Options{Home: home})
	if got, err := m.CoordinatedLauncher(); err != nil || got != "" {
		t.Fatalf("fresh home: %s %v", got, err)
	}
	current := filepath.Join(home, "custom data", "dearmachine", "current")
	native := filepath.Join(current, "bin", "dearmachine")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "release.json"), []byte(`{"version":1,"method":"nix"}`), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(home, ".local", "bin", "dearmachine")
	if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(native, launcher); err != nil {
		t.Fatal(err)
	}
	if got, err := m.CoordinatedLauncher(); err != nil || got != launcher {
		t.Fatalf("owner: %s %v", got, err)
	}
	if _, err := m.Install(context.Background(), "missing source", home); err == nil {
		t.Fatal("standalone install accepted")
	}
	if _, err := m.Update(context.Background(), Result{Status: StatusAvailable, CandidateCommit: "candidate"}); err == nil {
		t.Fatal("standalone update accepted")
	}
	if _, err := os.Stat(m.Paths().Root); !os.IsNotExist(err) {
		t.Fatal("standalone state created")
	}
	if err := os.Remove(filepath.Join(current, "release.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CoordinatedLauncher(); err == nil {
		t.Fatal("broken coordinated ownership ignored")
	}
}
