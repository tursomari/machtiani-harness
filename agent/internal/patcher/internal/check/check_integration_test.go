package check

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestApplyCheck_Success(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := t.TempDir()
	os.Mkdir(filepath.Join(repo, ".git"), 0o755)
	// Create a simple patch that adds a file.
	patch := "diff --git a/README b/README\nnew file mode 100644\nindex 0000000..e69de29\n--- /dev/null\n+++ b/README\n"
	p := filepath.Join(repo, "test.patch")
	if err := os.WriteFile(p, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyCheck(repo, p); err != nil {
		t.Fatalf("ApplyCheck: %v", err)
	}
}
