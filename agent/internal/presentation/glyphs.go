package presentation

import (
	"fmt"
	"os"
	"strings"
)

// GlyphMode selects program-owned terminal decoration independently from the
// configured color palette.
type GlyphMode string

const (
	GlyphUnicode GlyphMode = "unicode"
	GlyphASCII   GlyphMode = "ascii"
)

// GlyphSet is the centralized vocabulary for terminal-owned visual marks.
// Content supplied by users or models must never be rewritten through it.
type GlyphSet struct {
	OuterRule   string
	CommandRule string
	BlockQuote  string
	Bullet      string
	TaskTick    string
	Arrow       string
	Ellipsis    string
	Separator   string
	Success     string
	Failure     string
}

var unicodeGlyphs = GlyphSet{
	OuterRule:   "━",
	CommandRule: "─",
	BlockQuote:  "│ ",
	Bullet:      "• ",
	TaskTick:    "✓",
	Arrow:       "→",
	Ellipsis:    "…",
	Separator:   "—",
	Success:     "✓",
	Failure:     "✗",
}

var asciiGlyphs = GlyphSet{
	OuterRule:   "=",
	CommandRule: "-",
	BlockQuote:  "| ",
	Bullet:      "* ",
	TaskTick:    "x",
	Arrow:       "->",
	Ellipsis:    "...",
	Separator:   "-",
	Success:     "[OK]",
	Failure:     "[ERROR]",
}

func NormalizeGlyphMode(value string) (GlyphMode, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return GlyphUnicode, nil
	}
	switch GlyphMode(value) {
	case GlyphUnicode, GlyphASCII:
		return GlyphMode(value), nil
	default:
		return "", fmt.Errorf("unknown UI glyph mode %q (want unicode or ascii)", value)
	}
}

func ResolveGlyphMode(configured string) (GlyphMode, error) {
	if override := strings.TrimSpace(os.Getenv("MACHTIANI_GLYPHS")); override != "" {
		return NormalizeGlyphMode(override)
	}
	return NormalizeGlyphMode(configured)
}

func glyphSet(mode GlyphMode) GlyphSet {
	if mode == GlyphASCII {
		return asciiGlyphs
	}
	return unicodeGlyphs
}

// GlyphsForMode returns the terminal-owned glyph vocabulary for a normalized
// mode. Unknown and empty modes safely use the Unicode default.
func GlyphsForMode(mode GlyphMode) GlyphSet {
	return glyphSet(mode)
}
