package ui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
	"github.com/tursomari/machtiani/agent/internal/presentation"
)

func TestLoadBannerQuotesUsesNormalizedEmbeddedCorpus(t *testing.T) {
	quotes, err := loadBannerQuotes()
	if err != nil {
		t.Fatalf("loadBannerQuotes: %v", err)
	}
	if got, want := len(quotes), 47; got != want {
		t.Fatalf("quote count = %d, want %d", got, want)
	}
	for i, quote := range quotes {
		if quote.Paragraph <= 0 {
			t.Errorf("quote %d paragraph = %d, want positive integer", i, quote.Paragraph)
		}
		if quote.Line <= 0 {
			t.Errorf("quote %d line = %d, want positive integer", i, quote.Line)
		}
		if strings.TrimSpace(quote.Text) == "" {
			t.Errorf("quote %d is empty", i)
		}
		if strings.Contains(quote.Text, "*") {
			t.Errorf("quote %d contains presentation markup: %q", i, quote.Text)
		}
	}
	last := quotes[len(quotes)-1]
	if last.Paragraph != 245 || last.Text != "Quo vadis, humanitas?" {
		t.Fatalf("normalized final quote = %#v", last)
	}
}

func TestBannerQuoteForSessionIsStableAndSessionSpecific(t *testing.T) {
	first := bannerQuoteForSession("session-alpha")
	if got := bannerQuoteForSession("session-alpha"); got != first {
		t.Fatalf("same session selected different quotes: %#v then %#v", first, got)
	}

	seen := map[bannerQuote]bool{first: true}
	for _, id := range []string{"session-beta", "session-gamma", "session-delta", "session-epsilon"} {
		seen[bannerQuoteForSession(id)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("different sessions selected only one quote: %#v", seen)
	}
}

func TestRenderSessionHeaderUsesSemanticThemeAndItalicQuote(t *testing.T) {
	theme := DefaultTheme(presentation.NewForTestWithGlyphs(
		presentation.ProfileTerminal,
		presentation.GlyphASCII,
		true,
		false,
	))
	event := SessionStartedEvent{
		SessionID:     "session-alpha",
		Goal:          "Build something true, good, and beautiful.",
		BuildVersion:  "v1.2.3",
		BuildCommit:   "0123456789abcdef",
		ContextLength: 200000,
	}

	got := RenderSessionHeader(event, theme, 88)
	plain := stripBannerANSI(got)
	lines := strings.Split(plain, "\n")
	if len(lines) < 9 {
		t.Fatalf("header has too few lines: %#v", lines)
	}
	if lines[0] != "" || !strings.HasPrefix(lines[1], "machtiani (mct)") || lines[2] != "" {
		t.Errorf("header must begin with a blank line and leave a blank line after the quote: %#v", lines[:3])
	}
	if lines[6] != "" || strings.TrimSpace(lines[7]) != "PROMPT" {
		t.Errorf("header must leave a blank line before the prompt block: %#v", lines[5:8])
	}
	for _, want := range []string{
		"machtiani (mct)",
		"v1.2.3",
		"0123456",
		"200,000",
		"machtiani --help",
		"PROMPT",
		event.Goal,
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("header missing %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(got, "\x1b[3;35m") {
		t.Errorf("quote is not italic Beauty text:\n%q", got)
	}
	if !strings.Contains(got, "\x1b[1;36m") {
		t.Errorf("header has no bold Truth text:\n%q", got)
	}
	if !strings.Contains(got, "\x1b[1;32m") {
		t.Errorf("header has no bold Goodness text:\n%q", got)
	}
}

func TestRenderSessionHeaderIsCellWidthSafeAndUsesThreeDotTruncation(t *testing.T) {
	resolved, err := presentation.ResolveWithGlyphs("none", "ascii", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("resolve theme: %v", err)
	}
	theme := DefaultTheme(resolved)
	event := SessionStartedEvent{
		SessionID:     "narrow-session",
		Goal:          "A deliberately long prompt with wide glyphs 界界 that must wrap without making the terminal auto-wrap.",
		BuildVersion:  "development",
		BuildCommit:   "0123456789abcdef",
		ContextLength: 200000,
	}

	const width = 46
	got := RenderSessionHeader(event, theme, width)
	if !strings.Contains(got, "...") {
		t.Fatalf("narrow header has no ASCII three-dot truncation:\n%s", got)
	}
	for i, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if cells := runewidth.StringWidth(line); cells > width-1 {
			t.Errorf("line %d occupies %d cells, want <= %d: %q", i+1, cells, width-1, line)
		}
	}
}

func TestRenderSessionHeaderStaysCellWidthSafeAtMinimumSupportedWidth(t *testing.T) {
	resolved, err := presentation.ResolveWithGlyphs("none", "ascii", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	const width = 16
	got := RenderSessionHeader(SessionStartedEvent{
		SessionID:    "tiny",
		Goal:         "truth goodness beauty",
		BuildVersion: "development-build-with-a-long-name",
		BuildCommit:  "0123456789abcdef",
	}, DefaultTheme(resolved), width)
	for i, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if cells := runewidth.StringWidth(line); cells > width-1 {
			t.Errorf("line %d occupies %d cells, want <= %d: %q", i+1, cells, width-1, line)
		}
	}
}

var bannerANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripBannerANSI(value string) string {
	return bannerANSI.ReplaceAllString(value, "")
}
