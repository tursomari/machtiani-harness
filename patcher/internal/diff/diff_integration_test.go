package diff

import (
    "os"
    "os/exec"
    "path/filepath"
    "testing"
)

func TestGenerateAndStats_WithGit(t *testing.T) {
    if _, err := exec.LookPath("git"); err != nil {
        t.Skip("git not found")
    }
    repo := t.TempDir()
    os.Mkdir(filepath.Join(repo, ".git"), 0o755)
    mustWrite(t, filepath.Join(repo, "a.txt"), "hello")

    mirror := t.TempDir()
    mustWrite(t, filepath.Join(mirror, "a.txt"), "hello world")

    patch, err := Generate(repo, mirror, []string{"a.txt"})
    if err != nil { t.Fatalf("generate: %v", err) }
    st := ExtractStats(patch)
    if st.Insertions <= 0 { t.Fatalf("expected insertions > 0, got %d", st.Insertions) }
    if len(st.FilesModified) != 1 || filepath.Base(st.FilesModified[0]) != "a.txt" {
        t.Fatalf("files modified: %v", st.FilesModified)
    }
}

func mustWrite(t *testing.T, p, s string) {
    t.Helper()
    if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { t.Fatal(err) }
    if err := os.WriteFile(p, []byte(s), 0o644); err != nil { t.Fatal(err) }
}
