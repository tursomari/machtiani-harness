package presentation

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/muesli/termenv"
	"golang.org/x/term"
)

type ProfileName string

const (
	ProfileTerminal       ProfileName = "terminal"
	ProfileMachtianiDark  ProfileName = "machtiani-dark"
	ProfileMachtianiLight ProfileName = "machtiani-light"
	ProfileNone           ProfileName = "none"
)

var ValidProfiles = []ProfileName{
	ProfileTerminal,
	ProfileMachtianiDark,
	ProfileMachtianiLight,
	ProfileNone,
}

type Role int

const (
	RoleNormal Role = iota
	RoleTruth
	RoleGoodness
	RoleBeauty
	RoleProvenance
	RoleRupture
)

type Theme struct {
	Profile      ProfileName
	glyphMode    GlyphMode
	ansiEnabled  bool
	colorEnabled bool
	trueColor    bool
}

type fdWriter interface {
	Fd() uintptr
}

func NormalizeProfile(value string) (ProfileName, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ProfileTerminal, nil
	}
	profile := ProfileName(value)
	for _, candidate := range ValidProfiles {
		if profile == candidate {
			return profile, nil
		}
	}
	return "", fmt.Errorf("unknown UI theme %q (want terminal, machtiani-dark, machtiani-light, or none)", value)
}

func ResolveProfile(configured string) (ProfileName, error) {
	if override := strings.TrimSpace(os.Getenv("MACHTIANI_THEME")); override != "" {
		return NormalizeProfile(override)
	}
	return NormalizeProfile(configured)
}

func Resolve(configured string, out io.Writer) (Theme, error) {
	return ResolveWithGlyphs(configured, "", out)
}

func ResolveWithGlyphs(configuredTheme, configuredGlyphs string, out io.Writer) (Theme, error) {
	profile, err := ResolveProfile(configuredTheme)
	if err != nil {
		return Theme{}, err
	}
	glyphMode, err := ResolveGlyphMode(configuredGlyphs)
	if err != nil {
		return Theme{}, err
	}
	theme := Theme{Profile: profile, glyphMode: glyphMode}
	if profile == ProfileNone || strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return theme, nil
	}
	fdw, ok := out.(fdWriter)
	if !ok || !term.IsTerminal(int(fdw.Fd())) {
		return theme, nil
	}
	theme.ansiEnabled = true
	output := termenv.NewOutput(out, termenv.WithTTY(true))
	colorProfile := output.ColorProfile()
	theme.colorEnabled = colorProfile != termenv.Ascii && strings.TrimSpace(os.Getenv("NO_COLOR")) == ""
	theme.trueColor = colorProfile == termenv.TrueColor
	return theme, nil
}

func NewForTest(profile ProfileName, colorEnabled, trueColor bool) Theme {
	return NewForTestWithGlyphs(profile, GlyphUnicode, colorEnabled, trueColor)
}

func NewForTestWithGlyphs(profile ProfileName, glyphMode GlyphMode, colorEnabled, trueColor bool) Theme {
	if glyphMode == "" {
		glyphMode = GlyphUnicode
	}
	return Theme{Profile: profile, glyphMode: glyphMode, ansiEnabled: true, colorEnabled: colorEnabled, trueColor: trueColor}
}

func (t Theme) ANSIEnabled() bool  { return t.ansiEnabled }
func (t Theme) ColorEnabled() bool { return t.colorEnabled }
func (t Theme) GlyphMode() GlyphMode {
	if t.glyphMode == "" {
		return GlyphUnicode
	}
	return t.glyphMode
}
func (t Theme) Glyphs() GlyphSet { return glyphSet(t.GlyphMode()) }

// Color returns a Glamour-compatible foreground color for a semantic role.
// It intentionally returns no value when hue has been disabled.
func (t Theme) Color(role Role) string {
	if !t.colorEnabled || role == RoleNormal {
		return ""
	}
	if t.Profile == ProfileMachtianiDark && t.trueColor {
		return darkHexPalette[role]
	}
	if t.Profile == ProfileMachtianiLight && t.trueColor {
		return lightHexPalette[role]
	}
	return ansiIndexPalette[role]
}

func (t Theme) RenderSpan(span StyledSpan) string {
	if span.Text == "" || !t.ansiEnabled {
		return span.Text
	}
	var codes []string
	if span.Bold {
		codes = append(codes, "1")
	}
	if span.Italic {
		codes = append(codes, "3")
	}
	if span.Underline {
		codes = append(codes, "4")
	}
	if t.colorEnabled && span.Role != RoleNormal {
		codes = append(codes, t.roleColor(span.Role))
	}
	if len(codes) == 0 {
		return span.Text
	}
	return "\033[" + strings.Join(codes, ";") + "m" + span.Text + "\033[0m"
}

func (t Theme) RenderLine(line StyledLine) string {
	var b strings.Builder
	for _, span := range line {
		b.WriteString(t.RenderSpan(span))
	}
	return b.String()
}

func (t Theme) roleColor(role Role) string {
	if t.Profile == ProfileMachtianiDark && t.trueColor {
		return darkPalette[role]
	}
	if t.Profile == ProfileMachtianiLight && t.trueColor {
		return lightPalette[role]
	}
	return ansiPalette[role]
}

var ansiPalette = map[Role]string{
	RoleTruth:      "36",
	RoleGoodness:   "32",
	RoleBeauty:     "35",
	RoleProvenance: "33",
	RoleRupture:    "31",
}

var ansiIndexPalette = map[Role]string{
	RoleTruth:      "6",
	RoleGoodness:   "2",
	RoleBeauty:     "5",
	RoleProvenance: "3",
	RoleRupture:    "1",
}

var darkHexPalette = map[Role]string{
	RoleTruth:      "#58C7D9",
	RoleGoodness:   "#74C991",
	RoleBeauty:     "#C39BE8",
	RoleProvenance: "#D7B45A",
	RoleRupture:    "#E06C75",
}

var lightHexPalette = map[Role]string{
	RoleTruth:      "#006B78",
	RoleGoodness:   "#226B3A",
	RoleBeauty:     "#70428F",
	RoleProvenance: "#795A00",
	RoleRupture:    "#A72E3F",
}

var darkPalette = map[Role]string{
	RoleTruth:      "38;2;88;199;217",
	RoleGoodness:   "38;2;116;201;145",
	RoleBeauty:     "38;2;195;155;232",
	RoleProvenance: "38;2;215;180;90",
	RoleRupture:    "38;2;224;108;117",
}

var lightPalette = map[Role]string{
	RoleTruth:      "38;2;0;107;120",
	RoleGoodness:   "38;2;34;107;58",
	RoleBeauty:     "38;2;112;66;143",
	RoleProvenance: "38;2;121;90;0",
	RoleRupture:    "38;2;167;46;63",
}
