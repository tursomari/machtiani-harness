package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/mct/artifacts"
)

func TestResolveFileDiscoveryTrajectoryConflictingFlags(t *testing.T) {
	cfg := config{
		fileDiscoveryTrajectory: "one",
		fileDiscoveryOutputDir:  "two",
	}
	if _, err := resolveFileDiscoveryTrajectory(cfg, "session"); err == nil {
		t.Fatalf("expected error when both trajectory and output dir flags set")
	}
}

func TestResolveFileDiscoveryTrajectoryOverrideCreatesDir(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nested", "traj.jsonl")
	cfg := config{fileDiscoveryTrajectory: path}
	got, err := resolveFileDiscoveryTrajectory(cfg, "session")
	if err != nil {
		t.Fatalf("resolveFileDiscoveryTrajectory returned error: %v", err)
	}
	if got != path {
		t.Fatalf("expected path %q, got %q", path, got)
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || !st.IsDir() {
		t.Fatalf("expected directory %q to exist: %v", filepath.Dir(path), err)
	}
}

func TestResolveFileDiscoveryTrajectoryDefaultDir(t *testing.T) {
	cfg := config{dryRun: true}
	sessionID := "agent-20240101"
	got, err := resolveFileDiscoveryTrajectory(cfg, sessionID)
	if err != nil {
		t.Fatalf("resolveFileDiscoveryTrajectory returned error: %v", err)
	}
	chatDir, err := artifacts.ChatDirectory()
	if err != nil {
		t.Fatalf("failed to resolve chat directory: %v", err)
	}
	expected := filepath.Join(chatDir, fmt.Sprintf("file-discovery-%s.jsonl", sessionID))
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestResolveFileDiscoveryTrajectoryCustomDir(t *testing.T) {
	cfg := config{fileDiscoveryOutputDir: "custom-dir", dryRun: true}
	sessionID := "agent-20240202"
	got, err := resolveFileDiscoveryTrajectory(cfg, sessionID)
	if err != nil {
		t.Fatalf("resolveFileDiscoveryTrajectory returned error: %v", err)
	}
	absDir, err := filepath.Abs(cfg.fileDiscoveryOutputDir)
	if err != nil {
		t.Fatalf("failed to compute absolute dir: %v", err)
	}
	expected := filepath.Join(absDir, fmt.Sprintf("file-discovery-%s.jsonl", sessionID))
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}
