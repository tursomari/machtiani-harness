package presentation

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestNormalizeProfile(t *testing.T) {
	for input, want := range map[string]ProfileName{
		"":                ProfileTerminal,
		" TERMINAL ":      ProfileTerminal,
		"machtiani-dark":  ProfileMachtianiDark,
		"MACHTIANI-LIGHT": ProfileMachtianiLight,
		"none":            ProfileNone,
	} {
		got, err := NormalizeProfile(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeProfile(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := NormalizeProfile("auto"); err == nil {
		t.Fatal("expected an unknown-profile error")
	}
}

func TestResolveProfileEnvironmentOverride(t *testing.T) {
	t.Setenv("MACHTIANI_THEME", "machtiani-light")
	got, err := ResolveProfile("machtiani-dark")
	if err != nil || got != ProfileMachtianiLight {
		t.Fatalf("ResolveProfile = %q, %v", got, err)
	}
}

func TestResolveDisablesANSIForNonTerminalOutput(t *testing.T) {
	t.Setenv("MACHTIANI_THEME", "machtiani-dark")
	theme, err := Resolve("terminal", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if theme.ANSIEnabled() || theme.ColorEnabled() {
		t.Fatalf("non-terminal output unexpectedly enabled ANSI: %+v", theme)
	}
}

func TestResolveDisablesMotionForDumbTerminal(t *testing.T) {
	t.Setenv("TERM", "dumb")
	theme, err := ResolveWithGlyphsAndMotion("terminal", "unicode", "full", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if theme.MotionMode() != MotionNone {
		t.Fatalf("dumb-terminal motion = %q, want none", theme.MotionMode())
	}
}

func TestResolveMotionEnvironmentOverride(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("MACHTIANI_MOTION", "reduced")
	theme, err := ResolveWithGlyphsAndMotion("none", "unicode", "full", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if theme.MotionMode() != MotionReduced {
		t.Fatalf("motion = %q, want reduced", theme.MotionMode())
	}
}

func TestRenderSpanSemanticPalettes(t *testing.T) {
	tests := []struct {
		name  string
		theme Theme
		want  string
	}{
		{"terminal", NewForTest(ProfileTerminal, true, true), "\x1b[1;36mtruth\x1b[0m"},
		{"dark", NewForTest(ProfileMachtianiDark, true, true), "\x1b[1;38;2;88;199;217mtruth\x1b[0m"},
		{"light", NewForTest(ProfileMachtianiLight, true, true), "\x1b[1;38;2;0;107;120mtruth\x1b[0m"},
		{"no hue", NewForTest(ProfileTerminal, false, true), "\x1b[1mtruth\x1b[0m"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.theme.RenderSpan(Bold(RoleTruth, "truth")); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestExactMachtianiPaletteValues(t *testing.T) {
	roles := []Role{RoleTruth, RoleGoodness, RoleBeauty, RoleProvenance, RoleRupture}
	tests := []struct {
		name string
		got  Theme
		want []string
	}{
		{"dark", NewForTest(ProfileMachtianiDark, true, true), []string{"#58C7D9", "#74C991", "#C39BE8", "#D7B45A", "#E06C75"}},
		{"light", NewForTest(ProfileMachtianiLight, true, true), []string{"#006B78", "#226B3A", "#70428F", "#795A00", "#A72E3F"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for i, role := range roles {
				if got := test.got.Color(role); got != test.want[i] {
					t.Errorf("role %v color = %q, want %q", role, got, test.want[i])
				}
			}
		})
	}
}

func TestMarkdownRendererUsesSemanticThemeWithoutQueriesOrBackgrounds(t *testing.T) {
	theme := NewForTest(ProfileMachtianiDark, true, true)
	renderer, err := NewMarkdownRenderer(theme, false)
	if err != nil {
		t.Fatal(err)
	}
	output, err := renderer.Render("# Truth\n\n[Beauty](https://example.com) and `source`.\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"\x1b]11;?", "\x1b[6n", "\x1b[48;"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("output contains forbidden terminal sequence %q: %q", forbidden, output)
		}
	}
	for _, color := range []string{"38;2;88;199;217", "38;2;195;155;232", "38;2;215;179;89"} {
		if !strings.Contains(output, color) {
			t.Errorf("output missing semantic color %q: %q", color, output)
		}
	}
	plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(output, "")
	for _, text := range []string{"# Truth", "Beauty", "https://example.com", "source"} {
		if !strings.Contains(plain, text) {
			t.Errorf("plain output missing %q: %q", text, plain)
		}
	}
}

func TestMarkdownRendererNoColorRetainsEmphasis(t *testing.T) {
	theme := NewForTest(ProfileTerminal, false, false)
	renderer, err := NewMarkdownRenderer(theme, false)
	if err != nil {
		t.Fatal(err)
	}
	output, err := renderer.Render("**strong** and *emphasis* and [link](https://example.com)")
	if err != nil {
		t.Fatal(err)
	}
	huePattern := regexp.MustCompile(`\x1b\[(?:3[0-9]|38;)`)
	if huePattern.MatchString(output) {
		t.Fatalf("NO_COLOR output contains hue: %q", output)
	}
	for _, emphasis := range []string{"\x1b[1m", "\x1b[3m", "\x1b[4m"} {
		if !strings.Contains(output, emphasis) {
			t.Errorf("output missing emphasis %q: %q", emphasis, output)
		}
	}
}

func TestMarkdownRendererNoneEmitsNoANSI(t *testing.T) {
	renderer, err := NewMarkdownRenderer(Theme{Profile: ProfileNone}, false)
	if err != nil {
		t.Fatal(err)
	}
	output, err := renderer.Render("# Heading\n\n**strong** and `code`")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "\x1b") {
		t.Fatalf("none profile emitted ANSI: %q", output)
	}
	for _, text := range []string{"# Heading", "**strong**", "code"} {
		if !strings.Contains(output, text) {
			t.Errorf("plain Markdown output missing %q: %q", text, output)
		}
	}
}

func TestNormalizeGlowWordWrapWidth(t *testing.T) {
	tests := []struct {
		name  string
		width int
		want  int
	}{
		{name: "unavailable", width: 0, want: 80},
		{name: "error", width: -1, want: 80},
		{name: "terminal width", width: 100, want: 100},
		{name: "maximum", width: 120, want: 120},
		{name: "capped", width: 200, want: 120},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeGlowWordWrapWidth(test.width); got != test.want {
				t.Fatalf("normalizeGlowWordWrapWidth(%d) = %d, want %d", test.width, got, test.want)
			}
		})
	}
}
