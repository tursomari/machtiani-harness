package engine

import (
	"testing"

	patcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
)

func TestApplyStrictPatchSnippetMismatch(t *testing.T) {
	repoRoot := t.TempDir()
	content := "line 1\nline x\nline 3\n"
	info := &patcher.UnifiedPatchInfo{
		Hunks: []patcher.Hunk{
			{
				OldStart:      1,
				OldCount:      3,
				NewStart:      1,
				NewCount:      3,
				ContextBefore: []string{"line 1"},
				Deletions:     []string{"line 2"},
				Additions:     []string{"line two"},
				ContextAfter:  []string{"line 3"},
				SnippetSource: &patcher.SnippetSource{StartLine: 1, EndLine: 3},
			},
		},
	}
	_, diags, err := applyStrictPatch(repoRoot, "foo.txt", content, info)
	if err == nil {
		t.Fatalf("expected error")
	}
	if len(diags) == 0 {
		t.Fatalf("expected diagnostics")
	}
	if diags[0].Reason != "match_not_found" {
		t.Fatalf("expected match_not_found, got %q", diags[0].Reason)
	}
}

func TestApplyStrictPatchSnippetSuccess(t *testing.T) {
	repoRoot := t.TempDir()
	content := "line 1\nline 2\nline 3\n"
	info := &patcher.UnifiedPatchInfo{
		Hunks: []patcher.Hunk{
			{
				OldStart:      1,
				OldCount:      3,
				NewStart:      1,
				NewCount:      3,
				ContextBefore: []string{"line 1"},
				Deletions:     []string{"line 2"},
				Additions:     []string{"line two"},
				ContextAfter:  []string{"line 3"},
				SnippetSource: &patcher.SnippetSource{StartLine: 1, EndLine: 3},
			},
		},
	}
	out, diags, err := applyStrictPatch(repoRoot, "foo.txt", content, info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics, got %#v", diags)
	}
	expected := "line 1\nline two\nline 3\n"
	if out != expected {
		t.Fatalf("out = %q, want %q", out, expected)
	}
}

func TestApplyStrictPatchSnippetFallback(t *testing.T) {
	repoRoot := t.TempDir()
	content := "line 1\nline 2\nline 3\nline 4\n"
	info := &patcher.UnifiedPatchInfo{
		Hunks: []patcher.Hunk{
			{
				OldStart:      1,
				OldCount:      3,
				NewStart:      1,
				NewCount:      3,
				ContextBefore: []string{"line 1"},
				Deletions:     []string{"line 2"},
				Additions:     []string{"line two"},
				ContextAfter:  []string{"line 3"},
				SnippetSource: &patcher.SnippetSource{StartLine: 1, EndLine: 4},
			},
		},
	}
	out, diags, err := applyStrictPatch(repoRoot, "foo.txt", content, info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics, got %#v", diags)
	}
	expected := "line 1\nline two\nline 3\nline 4\n"
	if out != expected {
		t.Fatalf("out = %q, want %q", out, expected)
	}
}

func TestApplyStrictPatchIgnoresOldStart(t *testing.T) {
	repoRoot := t.TempDir()
	content := "alpha\nbeta\ngamma\ndelta\n"
	info := &patcher.UnifiedPatchInfo{
		Hunks: []patcher.Hunk{
			{
				OldStart:      1,
				OldCount:      3,
				NewStart:      1,
				NewCount:      3,
				ContextBefore: []string{"beta"},
				Deletions:     []string{"gamma"},
				Additions:     []string{"GAMMA"},
				ContextAfter:  []string{"delta"},
			},
		},
	}
	out, diags, err := applyStrictPatch(repoRoot, "foo.txt", content, info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics, got %#v", diags)
	}
	expected := "alpha\nbeta\nGAMMA\ndelta\n"
	if out != expected {
		t.Fatalf("out = %q, want %q", out, expected)
	}
}

func TestApplyStrictPatchAmbiguousMatch(t *testing.T) {
	repoRoot := t.TempDir()
	content := "keep\nanchor\nvalue\nanchor\nvalue\n"
	baseHunk := patcher.Hunk{
		OldStart:      2,
		OldCount:      2,
		NewStart:      2,
		NewCount:      2,
		ContextBefore: []string{"anchor"},
		Deletions:     []string{"value"},
		Additions:     []string{"VALUE"},
		ContextAfter:  []string{},
	}

	t.Run("with positional hint", func(t *testing.T) {
		info := &patcher.UnifiedPatchInfo{Hunks: []patcher.Hunk{baseHunk}}
		out, diags, err := applyStrictPatch(repoRoot, "foo.txt", content, info)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(diags) != 0 {
			t.Fatalf("expected no diagnostics, got %#v", diags)
		}
		want := "keep\nanchor\nVALUE\nanchor\nvalue\n"
		if out != want {
			t.Fatalf("out = %q, want %q", out, want)
		}
	})

	t.Run("without hint remains ambiguous", func(t *testing.T) {
		h := baseHunk
		// Clear positional hint so the matcher cannot disambiguate duplicates.
		h.OldStart = 0
		info := &patcher.UnifiedPatchInfo{Hunks: []patcher.Hunk{h}}
		_, diags, err := applyStrictPatch(repoRoot, "foo.txt", content, info)
		if err == nil {
			t.Fatalf("expected error")
		}
		if len(diags) == 0 {
			t.Fatalf("expected diagnostics")
		}
		if diags[0].Reason != "match_ambiguous" {
			t.Fatalf("expected match_ambiguous, got %q", diags[0].Reason)
		}
	})
}
