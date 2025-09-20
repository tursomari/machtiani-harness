package engine

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/tursomari/machtiani/patcher/internal/instructions"
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

    instr := instructions.Instructions{Edits: []instructions.Edit{
        {Path: "bar.txt", Mode: instructions.ModeCreate, NewContent: "new"},
        {Path: "foo.txt", Mode: instructions.ModeReplace, Before: "world", After: "you", Occurrence: 1},
        {Path: "bar.txt", Mode: instructions.ModeDelete},
    }}

    after, files, err := ApplyAll(dir, instr)
    if err != nil { t.Fatalf("ApplyAll error: %v", err) }
    if len(files) != 2 { t.Fatalf("files = %v", files) }
    if string(after["foo.txt"]) != "hello you" { t.Fatalf("after foo: %q", string(after["foo.txt"])) }
    if _, ok := after["bar.txt"]; !ok || after["bar.txt"] != nil { t.Fatalf("bar should be marked deleted") }
}

func mustWrite(t *testing.T, p, s string) {
    t.Helper()
    if err := os.WriteFile(p, []byte(s), 0o644); err != nil { t.Fatal(err) }
}

