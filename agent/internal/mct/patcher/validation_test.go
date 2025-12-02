package patcher

import (
    "os"
    "path/filepath"
    "strings"
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

func TestValidatePatchInsertion(t *testing.T) {
    dir := t.TempDir()
    os.Mkdir(filepath.Join(dir, ".git"), 0o755)
    writeFile(t, dir, "foo.txt", "a\nb\n")

    instr := Instructions{Edits: []Edit{{
        Path:      "foo.txt",
        Mode:      ModePatch,
        StartLine: 2,
        EndLine:   0,
        NewContent: "X\nY\n",
    }}}
    if err := Validate(dir, instr); err != nil {
        t.Fatalf("Validate returned error: %v", err)
    }
}

func TestValidatePatchRangeOutOfBounds(t *testing.T) {
    dir := t.TempDir()
    os.Mkdir(filepath.Join(dir, ".git"), 0o755)
    writeFile(t, dir, "foo.txt", "a\nb\n")
    instr := Instructions{Edits: []Edit{{
        Path:      "foo.txt",
        Mode:      ModePatch,
        StartLine: 1,
        EndLine:   5,
        NewContent: "x\n",
    }}}
    if err := Validate(dir, instr); err == nil {
        t.Fatalf("expected out-of-bounds error")
    }
}

func TestValidateRejectsStrictHunksPayload(t *testing.T) {
    dir := t.TempDir()
    os.Mkdir(filepath.Join(dir, ".git"), 0o755)
    writeFile(t, dir, "foo.txt", "a\nb\n")
    h := Hunk{OldStart: 1, OldCount: 1, NewStart: 1, NewCount: 1}
    instr := Instructions{Edits: []Edit{{
        Path:      "foo.txt",
        Mode:      ModePatch,
        PatchInfo: &UnifiedPatchInfo{Hunks: []Hunk{h}},
    }}}
    if err := Validate(dir, instr); err == nil || !strings.Contains(err.Error(), "strict hunk payloads no longer supported") {
        t.Fatalf("expected rejection of hunks payload, got %v", err)
    }
}

// Old hunk-tolerant behaviors removed.

// Snippet loading helpers removed with hunk model.

// SnippetSource validation removed with hunk model.

// Lenient fallback behaviors removed.

func TestValidateSingleChangeRule(t *testing.T) {
    dir := t.TempDir()
    os.Mkdir(filepath.Join(dir, ".git"), 0o755)
    writeFile(t, dir, "foo.txt", "a\nb\n")
    in := Instructions{Edits: []Edit{
        {Path: "foo.txt", Mode: ModePatch, StartLine: 1, EndLine: 1, NewContent: "a\n"},
        {Path: "foo.txt", Mode: ModePatch, StartLine: 2, EndLine: 0, NewContent: "X\n"},
    }}
    if err := Validate(dir, in); err == nil || !strings.Contains(err.Error(), "only one splice per file per patch") {
        t.Fatalf("expected single-change rule error, got %v", err)
    }
}
