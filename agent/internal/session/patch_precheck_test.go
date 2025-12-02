package session

import (
	"os"
	"path/filepath"
	"testing"

	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
)

func TestPreprocessAdjustsCreateToRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	instr := mctpatcher.Instructions{
		Edits: []mctpatcher.Edit{{
			Path:       "file.txt",
			Mode:       mctpatcher.ModeCreate,
			NewContent: "new",
		}},
	}
	prepared, adjustments, err := preprocessPatchInstructions(dir, instr)
	if err != nil {
		t.Fatalf("unexpected precheck error: %v", err)
	}
	if prepared.Edits[0].Mode != mctpatcher.ModeRewrite {
		t.Fatalf("expected rewrite mode, got %s", prepared.Edits[0].Mode)
	}
	if len(adjustments) != 1 {
		t.Fatalf("expected 1 adjustment, got %d", len(adjustments))
	}
}

func TestPreprocessAdjustsRewriteToCreate(t *testing.T) {
	dir := t.TempDir()
	instr := mctpatcher.Instructions{
		Edits: []mctpatcher.Edit{{
			Path:       "missing.txt",
			Mode:       mctpatcher.ModeRewrite,
			NewContent: "content",
		}},
	}
	prepared, adjustments, err := preprocessPatchInstructions(dir, instr)
	if err != nil {
		t.Fatalf("unexpected precheck error: %v", err)
	}
	if prepared.Edits[0].Mode != mctpatcher.ModeCreate {
		t.Fatalf("expected create mode, got %s", prepared.Edits[0].Mode)
	}
	if len(adjustments) != 1 {
		t.Fatalf("expected 1 adjustment, got %d", len(adjustments))
	}
}

func TestPreprocessDetectsReplaceOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	instr := mctpatcher.Instructions{
		Edits: []mctpatcher.Edit{{
			Path:   "new.go",
			Mode:   mctpatcher.ModeReplace,
			Before: "package main",
			After:  "package main",
		}},
	}
	_, adjustments, err := preprocessPatchInstructions(dir, instr)
	if err == nil {
		t.Fatalf("expected error for replace on missing file")
	}
	if err.Count() == 0 {
		t.Fatalf("expected conflict count > 0")
	}
	if len(adjustments) != 0 {
		t.Fatalf("expected no adjustments, got %d", len(adjustments))
	}
}

func TestPreprocessDetectsDeleteOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	instr := mctpatcher.Instructions{
		Edits: []mctpatcher.Edit{{
			Path: "absent.txt",
			Mode: mctpatcher.ModeDelete,
		}},
	}
	_, _, err := preprocessPatchInstructions(dir, instr)
	if err == nil {
		t.Fatalf("expected error for delete on missing file")
	}
	if err.Count() != 1 {
		t.Fatalf("expected one conflict, got %d", err.Count())
	}
}

func TestPreprocessConvertsPatchToCreateWhenFileMissing(t *testing.T) {
	dir := t.TempDir()
	instr := mctpatcher.Instructions{
		Edits: []mctpatcher.Edit{{
			Path:       "LICENSE",
			Mode:       mctpatcher.ModePatch,
			StartLine:  1,
			EndLine:    0,
			NewContent: "line one\nline two\n",
		}},
	}
	prepared, adjustments, err := preprocessPatchInstructions(dir, instr)
	if err != nil {
		t.Fatalf("unexpected precheck error: %v", err)
	}
	if got := prepared.Edits[0].Mode; got != mctpatcher.ModeCreate {
		t.Fatalf("expected mode create, got %s", got)
	}
	if len(adjustments) != 1 {
		t.Fatalf("expected one adjustment, got %d", len(adjustments))
	}
}

func TestPreprocessRejectsPatchOnMissingFileWithoutContent(t *testing.T) {
	dir := t.TempDir()
	instr := mctpatcher.Instructions{
		Edits: []mctpatcher.Edit{{
			Path:      "LICENSE",
			Mode:      mctpatcher.ModePatch,
			StartLine: 1,
			EndLine:   0,
		}},
	}
	_, _, err := preprocessPatchInstructions(dir, instr)
	if err == nil {
		t.Fatalf("expected precheck error for empty patch on missing file")
	}
	if err.Count() != 1 {
		t.Fatalf("expected single conflict, got %d", err.Count())
	}
}

// Conversion of patch hunks is no longer supported; range splice requires existing file unless synthesized from new_content.
