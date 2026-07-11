package presentation

import (
	"regexp"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/muesli/termenv"
)

type MarkdownRenderer struct {
	renderer  *glamour.TermRenderer
	stripANSI bool
}

var markdownANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func (r *MarkdownRenderer) Render(content string) (string, error) {
	rendered, err := r.renderer.Render(content)
	if r.stripANSI {
		rendered = markdownANSI.ReplaceAllString(rendered, "")
	}
	return rendered, err
}

// NewMarkdownRenderer builds a renderer from the already-resolved terminal
// theme. It never probes the terminal background, so Markdown and the TUI use
// the same explicit profile and never emit background-query control sequences.
func NewMarkdownRenderer(theme Theme, preserveNewLines bool) (*MarkdownRenderer, error) {
	options := []glamour.TermRendererOption{}
	if theme.ansiEnabled {
		profile := termenv.ANSI
		if theme.trueColor {
			profile = termenv.TrueColor
		}
		options = append(options, glamour.WithStyles(markdownStyle(theme)), glamour.WithColorProfile(profile))
	} else {
		options = append(options, glamour.WithStyles(styles.NoTTYStyleConfig), glamour.WithColorProfile(termenv.Ascii))
	}
	if preserveNewLines {
		options = append(options, glamour.WithPreservedNewLines())
	}
	renderer, err := glamour.NewTermRenderer(options...)
	if err != nil {
		return nil, err
	}
	return &MarkdownRenderer{renderer: renderer, stripANSI: !theme.ansiEnabled}, nil
}

func markdownStyle(theme Theme) ansi.StyleConfig {
	margin := uint(2)
	indent := uint(1)
	indentToken := "│ "
	style := ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{BlockPrefix: "\n", BlockSuffix: "\n"},
			Margin:         &margin,
		},
		BlockQuote:            ansi.StyleBlock{Indent: &indent, IndentToken: &indentToken},
		List:                  ansi.StyleList{LevelIndent: 2},
		Heading:               ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{BlockSuffix: "\n"}},
		H1:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "# "}},
		H2:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "## "}},
		H3:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "### "}},
		H4:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "#### "}},
		H5:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "##### "}},
		H6:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "###### "}},
		HorizontalRule:        ansi.StylePrimitive{Format: "\n--------\n"},
		Item:                  ansi.StylePrimitive{BlockPrefix: "• "},
		Enumeration:           ansi.StylePrimitive{BlockPrefix: ". "},
		Task:                  ansi.StyleTask{Ticked: "[✓] ", Unticked: "[ ] "},
		ImageText:             ansi.StylePrimitive{Format: "Image: {{.text}} →"},
		CodeBlock:             ansi.StyleCodeBlock{StyleBlock: ansi.StyleBlock{Margin: &margin}},
		DefinitionDescription: ansi.StylePrimitive{BlockPrefix: "\n• "},
	}
	style.Heading.Bold = boolPointer(true)
	style.Heading.Color = colorPointer(theme.Color(RoleTruth))
	style.H1.Bold = boolPointer(true)
	style.Strikethrough.BlockPrefix = ""
	style.Strikethrough.BlockSuffix = ""
	style.Strikethrough.CrossedOut = boolPointer(true)
	style.Emph.BlockPrefix = ""
	style.Emph.BlockSuffix = ""
	style.Emph.Italic = boolPointer(true)
	style.Strong.BlockPrefix = ""
	style.Strong.BlockSuffix = ""
	style.Strong.Bold = boolPointer(true)
	style.HorizontalRule.Color = colorPointer(theme.Color(RoleTruth))
	style.Link.Color = colorPointer(theme.Color(RoleBeauty))
	style.Link.Underline = boolPointer(true)
	style.LinkText.Color = colorPointer(theme.Color(RoleBeauty))
	style.Code.BlockPrefix = ""
	style.Code.BlockSuffix = ""
	style.Code.Prefix = " "
	style.Code.Suffix = " "
	style.Code.Color = colorPointer(theme.Color(RoleProvenance))
	style.CodeBlock.Color = colorPointer(theme.Color(RoleProvenance))
	return style
}

func colorPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func boolPointer(value bool) *bool { return &value }
