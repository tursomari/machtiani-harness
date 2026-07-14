package modes

import (
	"os"
	"path/filepath"
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
