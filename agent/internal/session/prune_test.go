package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
)

func TestPruneSessionsDryRunAndRemoval(t *testing.T) {
	root := setupPruneTest(t)
	writePruneFixture(t, root, "s1")

	dry, err := PruneSessions(PruneOptions{SessionID: "s1", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dry.SessionsScanned != 1 || dry.SessionsChanged != 1 || dry.RemovedLLMInputFiles != 1 || dry.RemovedShellAgentStateFiles != 2 {
		t.Fatalf("dry-run report = %#v", dry)
	}
	if dry.RemovedDirectories != 2 {
		t.Fatalf("dry-run directory count = %d, want 2", dry.RemovedDirectories)
	}
	assertPrunePathExists(t, filepath.Join(root, "s1", "artifacts", "llm", "inputs.jsonl"))
	assertPrunePathExists(t, filepath.Join(root, "s1", "shell-agent", "2", "state.json"))

	report, err := PruneSessions(PruneOptions{SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if report.SessionsChanged != 1 || report.RemovedDirectories != dry.RemovedDirectories {
		t.Fatalf("prune report = %#v", report)
	}
	for _, removed := range []string{
		filepath.Join(root, "s1", "artifacts", "llm"),
		filepath.Join(root, "s1", "shell-agent", "1", "state.json"),
		filepath.Join(root, "s1", "shell-agent", "2"),
	} {
		if _, err := os.Lstat(removed); !os.IsNotExist(err) {
			t.Fatalf("pruned path exists %s: %v", removed, err)
		}
	}
	for _, retained := range []string{
		filepath.Join(root, "s1", "trajectory", "agent.jsonl"),
		filepath.Join(root, "s1", "artifacts", "conversation.json"),
		filepath.Join(root, "s1", "shell-agent", "1", "trajectory.json"),
		filepath.Join(root, "s1", "unknown", "keep.txt"),
	} {
		assertPrunePathExists(t, retained)
	}
}

func TestPruneSessionsAllSkipsActiveSession(t *testing.T) {
	root := setupPruneTest(t)
	writePruneFixture(t, root, "inactive")
	writePruneFixture(t, root, "active")
	scratchDir, err := artifacts.SessionScratchDirectory("active")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := acquireSessionLock("active", scratchDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	report, err := PruneSessions(PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.SkippedActiveSessions) != 1 || report.SkippedActiveSessions[0] != "active" {
		t.Fatalf("active skips = %#v", report.SkippedActiveSessions)
	}
	if _, err := os.Stat(filepath.Join(root, "inactive", "artifacts", "llm", "inputs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("inactive log was not pruned: %v", err)
	}
	assertPrunePathExists(t, filepath.Join(root, "active", "artifacts", "llm", "inputs.jsonl"))

	if _, err := PruneSessions(PruneOptions{SessionID: "active"}); err == nil {
		t.Fatal("expected specifically requested active session to fail")
	}
}

func TestPruneSessionsRejectsInvalidSessionID(t *testing.T) {
	setupPruneTest(t)
	if _, err := PruneSessions(PruneOptions{SessionID: "../outside", DryRun: true}); err == nil {
		t.Fatal("expected invalid session ID error")
	}
}

func setupPruneTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MACHTIANI_CONFIG", "")
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	return filepath.Join(home, ".machtiani", "sessions")
}

func writePruneFixture(t *testing.T, root, sessionID string) {
	t.Helper()
	for path, contents := range map[string]string{
		filepath.Join(root, sessionID, "artifacts", "conversation.json"):      "conversation\n",
		filepath.Join(root, sessionID, "artifacts", "llm", "inputs.jsonl"):    "full input\n",
		filepath.Join(root, sessionID, "trajectory", "agent.jsonl"):           "agent\n",
		filepath.Join(root, sessionID, "shell-agent", "1", "state.json"):      "state\n",
		filepath.Join(root, sessionID, "shell-agent", "1", "trajectory.json"): "trajectory\n",
		filepath.Join(root, sessionID, "shell-agent", "2", "state.json"):      "state\n",
		filepath.Join(root, sessionID, "unknown", "keep.txt"):                 "keep\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertPrunePathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected path %s: %v", path, err)
	}
}
