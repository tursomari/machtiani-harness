package parser

import "testing"

func TestExtractRetrievedFilePaths(t *testing.T) {
    md := `# Some Heading

## Retrieved File Paths
- internal/foo/bar.go
- README.md  (reference)

## Other
text`
    got := ExtractRetrievedFilePaths(md)
    want := []string{"internal/foo/bar.go", "README.md"}
    if len(got) != len(want) {
        t.Fatalf("expected %d paths, got %d: %#v", len(want), len(got), got)
    }
    for i := range want {
        if got[i] != want[i] {
            t.Fatalf("mismatch at %d: want %q got %q", i, want[i], got[i])
        }
    }
}

