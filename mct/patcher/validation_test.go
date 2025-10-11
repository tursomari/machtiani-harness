package patcher

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidateReplaceOccurrence(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	writeFile(t, dir, "a.txt", "hello world hello")

	in := Instructions{Edits: []Edit{{
		Path: "a.txt", Mode: ModeReplace, Before: "hello", After: "hi", Occurrence: 2,
	}}}
	if err := Validate(dir, in); err != nil {
		t.Fatalf("validate failed: %v", err)
	}

	in2 := Instructions{Edits: []Edit{{
		Path: "a.txt", Mode: ModeReplace, Before: "hello", After: "hi", Occurrence: 3,
	}}}
	if err := Validate(dir, in2); err == nil {
		t.Fatalf("expected error for missing 3rd occurrence")
	}
}

func TestValidatePathTraversal(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	writeFile(t, dir, "a.txt", "x")

	in := Instructions{Edits: []Edit{{
		Path: "../a.txt", Mode: ModeDelete,
	}}}
	if err := Validate(dir, in); err == nil {
		t.Fatalf("expected traversal rejection")
	}
}
