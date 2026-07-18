package presentation

import "testing"

func TestNormalizeGlyphMode(t *testing.T) {
	for input, want := range map[string]GlyphMode{
		"":          GlyphUnicode,
		" UNICODE ": GlyphUnicode,
		"ASCII":     GlyphASCII,
	} {
		got, err := NormalizeGlyphMode(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeGlyphMode(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := NormalizeGlyphMode("heavy"); err == nil {
		t.Fatal("expected unknown glyph mode error")
	}
}

func TestResolveGlyphModeEnvironmentOverride(t *testing.T) {
	t.Setenv("MACHTIANI_GLYPHS", "ascii")
	got, err := ResolveGlyphMode("unicode")
	if err != nil || got != GlyphASCII {
		t.Fatalf("ResolveGlyphMode = %q, %v", got, err)
	}
}

func TestGlyphSetsPreserveBalancedHierarchy(t *testing.T) {
	unicode := NewForTestWithGlyphs(ProfileTerminal, GlyphUnicode, true, false).Glyphs()
	if unicode.OuterRule != "━" || unicode.CommandRule != "─" {
		t.Fatalf("unicode rules = %q/%q", unicode.OuterRule, unicode.CommandRule)
	}
	ascii := NewForTestWithGlyphs(ProfileMachtianiDark, GlyphASCII, true, true).Glyphs()
	if ascii.OuterRule != "=" || ascii.CommandRule != "-" {
		t.Fatalf("ascii rules = %q/%q", ascii.OuterRule, ascii.CommandRule)
	}
	if ascii.Bullet != "* " || ascii.BlockQuote != "| " || ascii.Arrow != "->" || ascii.Ellipsis != "..." {
		t.Fatalf("unexpected ASCII glyph set: %+v", ascii)
	}
}
