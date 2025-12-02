package engine

import (
	"os"
	"path/filepath"
	"testing"

	patcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
)

func TestReplaceNth(t *testing.T) {
	out, ok := replaceNth("a a a", "a", "b", 2)
	if !ok || out != "a b a" {
		t.Fatalf("unexpected: %v %q", ok, out)
	}
}

func TestApplyAll_CreateReplaceDelete(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	// Start with foo.txt
	mustWrite(t, filepath.Join(dir, "foo.txt"), "hello world")

	instr := patcher.Instructions{Edits: []patcher.Edit{
		{Path: "bar.txt", Mode: patcher.ModeCreate, NewContent: "new"},
		{Path: "foo.txt", Mode: patcher.ModeReplace, Before: "world", After: "you", Occurrence: 1},
		{Path: "bar.txt", Mode: patcher.ModeDelete},
	}}

	after, files, err := ApplyAll(dir, instr)
	if err != nil {
		t.Fatalf("ApplyAll error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}
	if string(after["foo.txt"]) != "hello you" {
		t.Fatalf("after foo: %q", string(after["foo.txt"]))
	}
	if _, ok := after["bar.txt"]; !ok || after["bar.txt"] != nil {
		t.Fatalf("bar should be marked deleted")
	}
}

func TestApplyAll_PatchInsertReplaceDelete(t *testing.T) {
    dir := t.TempDir()
    os.Mkdir(filepath.Join(dir, ".git"), 0o755)
    mustWrite(t, filepath.Join(dir, "a.txt"), "1\n2\n3\n")

    instr := patcher.Instructions{Edits: []patcher.Edit{
        // Insert before line 2
        {Path: "a.txt", Mode: patcher.ModePatch, StartLine: 2, EndLine: 0, NewContent: "X\n"},
    }}
    after, _, err := ApplyAll(dir, instr)
    if err != nil {
        t.Fatalf("apply insert: %v", err)
    }
    if got := string(after["a.txt"]); got != "1\nX\n2\n3\n" {
        t.Fatalf("unexpected after insert: %q", got)
    }

    instr2 := patcher.Instructions{Edits: []patcher.Edit{
        // Replace lines 2..3
        {Path: "a.txt", Mode: patcher.ModePatch, StartLine: 2, EndLine: 3, NewContent: "B\nC\n"},
    }}
    after2, _, err := ApplyAll(dir, instr2)
    if err != nil {
        t.Fatalf("apply replace: %v", err)
    }
    if got := string(after2["a.txt"]); got != "1\nB\nC\n" {
        t.Fatalf("unexpected after replace: %q", got)
    }

    instr3 := patcher.Instructions{Edits: []patcher.Edit{
        // Delete line 2
        {Path: "a.txt", Mode: patcher.ModePatch, StartLine: 2, EndLine: 2, NewContent: ""},
    }}
    after3, _, err := ApplyAll(dir, instr3)
    if err != nil {
        t.Fatalf("apply delete: %v", err)
    }
    if got := string(after3["a.txt"]); got != "1\n3\n" {
        t.Fatalf("unexpected after delete: %q", got)
    }
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
