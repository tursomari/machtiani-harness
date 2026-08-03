package modes

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/projectstore"
)

func TestSyncCanonicalRefreshesManagedAndPreservesCustom(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root, err := projectstore.ModesRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "code"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "code", "tasks.toml"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(root, "my-code", "tasks.toml")
	if err := os.MkdirAll(filepath.Dir(custom), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(custom, []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SyncCanonical(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "code", "tasks.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "changed" {
		t.Fatal("canonical mode was not refreshed")
	}
	data, err = os.ReadFile(custom)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "custom" {
		t.Fatalf("custom mode changed: %q", data)
	}
}

func TestNamesIncludesCode(t *testing.T) {
	names, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "code" {
			return
		}
	}
	t.Fatal("code mode missing")
}

func TestAgentManagedModeKeepsExecutionTopologyOutOfPlanner(t *testing.T) {
	planner, err := fs.ReadFile(canonical, "canonical/agent-managed/planner-overlay.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"shell-agent", "managed agent", "worker", "coordinator", "agent-manager", "ticket", "AGENT_MANAGER_PATH"} {
		if strings.Contains(strings.ToLower(string(planner)), strings.ToLower(forbidden)) {
			t.Fatalf("planner contains execution-topology detail %q: %s", forbidden, planner)
		}
	}
	shellPrompt, err := fs.ReadFile(canonical, "canonical/agent-managed/shell-agent-system-prompt.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AGENT_MANAGER_PATH", "DEARMACHINE_BACKEND", "ticket send", "ticket status", "ticket view", "ticket cancel"} {
		if !strings.Contains(string(shellPrompt), want) {
			t.Errorf("shell prompt missing %q", want)
		}
	}
	for _, want := range []string{"override generic shell instructions", "A direct project edit is a failed outcome", "every few seconds or minutes", "An open ticket is still running", "broader shell-agent final-answer protocol", "Do not include managed-session details"} {
		if !strings.Contains(string(shellPrompt), want) {
			t.Errorf("shell prompt missing behavior %q", want)
		}
	}
	if strings.Contains(strings.ToLower(string(shellPrompt)), "coding") {
		t.Fatalf("shell prompt contains coding-specific terminology: %s", shellPrompt)
	}
}
