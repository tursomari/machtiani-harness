package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/session"
)

func TestSessionPruneCommandDryRunAndConfirmedRemoval(t *testing.T) {
	_, project := setupMigrationTest(t)
	logPath := filepath.Join(project, ".machtiani", "sessions", "s1", "artifacts", "llm", "inputs.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("full input"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := captureOutput(func() {
		if code := handleSessionPruneCommand([]string{"s1", "--dry-run", "--json"}); code != 0 {
			t.Fatalf("dry-run exit = %d", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	var preview session.PruneReport
	if err := json.Unmarshal([]byte(stdout), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.RemovedLLMInputFiles != 1 || preview.RemovedDirectories != 1 {
		t.Fatalf("preview = %#v", preview)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("dry-run changed log: %v", err)
	}

	_, stderr = captureOutput(func() {
		if code := handleSessionPruneCommand([]string{"s1", "--no-interactive", "--yes", "--json"}); code != 0 {
			t.Fatalf("prune exit = %d", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if _, err := os.Stat(filepath.Dir(logPath)); !os.IsNotExist(err) {
		t.Fatalf("LLM directory remains after prune: %v", err)
	}
}

func TestSessionPruneCommandRequiresNonInteractiveConfirmation(t *testing.T) {
	setupMigrationTest(t)
	_, stderr := captureOutput(func() {
		if code := handleSessionPruneCommand([]string{"--no-interactive"}); code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
	})
	if stderr == "" {
		t.Fatal("expected confirmation guidance")
	}
}
