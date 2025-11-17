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

func TestValidateStrictPatchWithSnippet(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	writeFile(t, dir, "foo.txt", "line 1\nline 2\nline 3\n")

	hunk := Hunk{
		OldStart:      1,
		OldCount:      3,
		NewStart:      1,
		NewCount:      3,
		ContextBefore: []string{"line 1"},
		Deletions:     []string{"line 2"},
		Additions:     []string{"line two"},
		ContextAfter:  []string{"line 3"},
		SnippetSource: &SnippetSource{StartLine: 1, EndLine: 3},
	}
	instr := Instructions{Edits: []Edit{{
		Path:      "foo.txt",
		Mode:      ModePatch,
		PatchInfo: &UnifiedPatchInfo{Hunks: []Hunk{hunk}},
	}}}
	if err := Validate(dir, instr); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestValidateStrictPatchSnippetMismatch(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	writeFile(t, dir, "foo.txt", "line 1\nline x\nline 3\n")

	hunk := Hunk{
		OldStart:      1,
		OldCount:      3,
		NewStart:      1,
		NewCount:      3,
		ContextBefore: []string{"line 1"},
		Deletions:     []string{"line 2"},
		Additions:     []string{"line two"},
		ContextAfter:  []string{"line 3"},
		SnippetSource: &SnippetSource{StartLine: 1, EndLine: 3},
	}
	instr := Instructions{Edits: []Edit{{
		Path:      "foo.txt",
		Mode:      ModePatch,
		PatchInfo: &UnifiedPatchInfo{Hunks: []Hunk{hunk}},
	}}}
	err := Validate(dir, instr)
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "snippet_source content mismatch") {
		t.Fatalf("expected snippet_source content mismatch error, got %v", err)
	}
}

func TestValidateStrictPatchSnippetFallback(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	writeFile(t, dir, "foo.txt", "line 1\nline 2\nline 3\nline 4\n")

	hunk := Hunk{
		OldStart:      1,
		OldCount:      3,
		NewStart:      1,
		NewCount:      3,
		ContextBefore: []string{"line 1"},
		Deletions:     []string{"line 2"},
		Additions:     []string{"line two"},
		ContextAfter:  []string{"line 3"},
		SnippetSource: &SnippetSource{StartLine: 1, EndLine: 4},
	}
	instr := Instructions{Edits: []Edit{{
		Path:      "foo.txt",
		Mode:      ModePatch,
		PatchInfo: &UnifiedPatchInfo{Hunks: []Hunk{hunk}},
	}}}
	if err := Validate(dir, instr); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestValidateStrictPatchIgnoresOldStart(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	writeFile(t, dir, "foo.txt", "alpha\nbeta\ngamma\ndelta\n")

	hunk := Hunk{
		OldStart:      10, // intentionally incorrect
		OldCount:      3,
		NewStart:      1,
		NewCount:      3,
		ContextBefore: []string{"beta"},
		Deletions:     []string{"gamma"},
		Additions:     []string{"GAMMA"},
		ContextAfter:  []string{"delta"},
	}
	instr := Instructions{Edits: []Edit{{
		Path:      "foo.txt",
		Mode:      ModePatch,
		PatchInfo: &UnifiedPatchInfo{Hunks: []Hunk{hunk}},
	}}}
	if err := Validate(dir, instr); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestLoadSnippetLines(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "foo.txt", "a\nb\nc\n")
	lines, err := LoadSnippetLines(dir, "foo.txt", 1, 2)
	if err != nil {
		t.Fatalf("LoadSnippetLines returned error: %v", err)
	}
	if got, want := len(lines), 2; got != want {
		t.Fatalf("len(lines) = %d, want %d", got, want)
	}
	if lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("unexpected lines: %v", lines)
	}

	empty, err := LoadSnippetLines(dir, "foo.txt", 4, 3)
	if err != nil {
		t.Fatalf("LoadSnippetLines empty range error: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty slice, got %v", empty)
	}
}

func TestValidateStrictPatchSnippetSourceOutOfRange(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	writeFile(t, dir, "foo.txt", "line 1\nline 2\n")

	hunk := Hunk{
		OldStart:      1,
		OldCount:      2,
		NewStart:      1,
		NewCount:      2,
		ContextBefore: []string{"line 1"},
		Deletions:     []string{"line 2"},
		Additions:     []string{"line two"},
		ContextAfter:  nil,
		SnippetSource: &SnippetSource{StartLine: 1, EndLine: 5},
	}
	instr := Instructions{Edits: []Edit{{
		Path:      "foo.txt",
		Mode:      ModePatch,
		PatchInfo: &UnifiedPatchInfo{Hunks: []Hunk{hunk}},
	}}}

	err := Validate(dir, instr)
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "snippet_source out_of_range") {
		t.Fatalf("expected snippet_source out_of_range error, got %v", err)
	}
}

func TestValidateStrictPatchSnippetSourceContentMismatch(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	writeFile(t, dir, "foo.txt", "alpha\nbeta\ngamma\n")

	hunk := Hunk{
		OldStart:      1,
		OldCount:      3,
		NewStart:      1,
		NewCount:      3,
		ContextBefore: []string{"alpha"},
		Deletions:     []string{"beta"},
		Additions:     []string{"BETA"},
		ContextAfter:  []string{"gamma"},
		SnippetSource: &SnippetSource{StartLine: 2, EndLine: 3},
	}
	instr := Instructions{Edits: []Edit{{
		Path:      "foo.txt",
		Mode:      ModePatch,
		PatchInfo: &UnifiedPatchInfo{Hunks: []Hunk{hunk}},
	}}}

	err := Validate(dir, instr)
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "snippet_source content mismatch") {
		t.Fatalf("expected snippet_source content mismatch error, got %v", err)
	}
}
