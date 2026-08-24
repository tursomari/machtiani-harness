package ui

import (
	"strconv"
	"strings"
	"sync"

	"github.com/mattn/go-runewidth"
	"github.com/tursomari/machtiani/agent/internal/magnifica"
	"github.com/tursomari/machtiani/agent/internal/presentation"
)

const preferredBannerRuleWidth = 72

type bannerQuote = magnifica.Quote

var (
	bannerQuotesOnce sync.Once
	bannerQuotes     []bannerQuote
	bannerQuotesErr  error
)

func loadBannerQuotes() ([]bannerQuote, error) {
	bannerQuotesOnce.Do(func() {
		bannerQuotes, bannerQuotesErr = magnifica.LoadEmbedded()
	})
	return bannerQuotes, bannerQuotesErr
}

func bannerQuoteForSession(sessionID string) bannerQuote {
	quotes, err := loadBannerQuotes()
	if err != nil || len(quotes) == 0 {
		return bannerQuote{Text: "Quo vadis, humanitas?"}
	}
	quote, err := magnifica.SelectQuoteForSession(sessionID, quotes)
	if err != nil {
		return bannerQuote{Text: "Quo vadis, humanitas?"}
	}
	return quote
}

// RenderSessionHeader renders the one-time session banner in ordinary
// scrollback. All emphasis and color flow through the shared semantic theme.
func RenderSessionHeader(event SessionStartedEvent, theme Theme, width int) string {
	if width <= 0 {
		width = defaultWidth
	}
	maxWidth := width - 1
	if maxWidth < 12 {
		return ""
	}

	var b strings.Builder
	write := func(line presentation.StyledLine) {
		b.WriteString(theme.render(line))
		b.WriteByte('\n')
	}
	plain := func(text string) {
		b.WriteString(text)
		b.WriteByte('\n')
	}

	b.WriteByte('\n')
	glyphs := theme.Presentation.Glyphs()
	title := "machtiani (mct)"
	if !event.MagnificaHumanitas {
		write(presentation.StyledLine{
			presentation.Bold(presentation.RoleTruth, truncateCellsWithDots(title, maxWidth)),
		})
	} else {
		prefix := title + " " + glyphs.Separator + " "
		quote := bannerQuoteForSession(event.SessionID).Text
		if runewidth.StringWidth(prefix) >= maxWidth {
			write(presentation.StyledLine{
				presentation.Bold(presentation.RoleTruth, truncateCellsWithDots(title, maxWidth)),
			})
		} else {
			quote = truncateCellsWithDots(quote, maxWidth-runewidth.StringWidth(prefix))
			quoteSpan := presentation.RoleText(presentation.RoleBeauty, quote)
			quoteSpan.Italic = true
			write(presentation.StyledLine{
				presentation.Bold(presentation.RoleTruth, title),
				presentation.RoleText(presentation.RoleBeauty, " "+glyphs.Separator+" "),
				quoteSpan,
			})
		}
	}
	b.WriteByte('\n')

	version := strings.TrimSpace(event.BuildVersion)
	if version == "" {
		version = "development"
	}
	commit := shortBannerCommit(event.BuildCommit)
	buildLine := "  version " + version
	if commit != "" {
		buildLine += " built from " + commit
	}
	writeBannerLabelValue(&b, theme, maxWidth, "version", strings.TrimPrefix(buildLine, "  version "))
	if event.ContextLength > 0 {
		writeBannerLabelValue(&b, theme, maxWidth, "context", formatBannerInteger(event.ContextLength)+" tokens")
	}
	helpPrefix := "  help "
	helpValue := truncateCellsWithDots("machtiani --help", maxWidth-runewidth.StringWidth(helpPrefix))
	write(presentation.StyledLine{
		presentation.Text("  "),
		presentation.Bold(presentation.RoleGoodness, "help"),
		presentation.Text(" "),
		presentation.RoleText(presentation.RoleProvenance, helpValue),
	})
	b.WriteByte('\n')
	write(presentation.StyledLine{
		presentation.Text("  "),
		presentation.Bold(presentation.RoleTruth, "PROMPT"),
	})
	ruleWidth := maxWidth - 2
	if ruleWidth > preferredBannerRuleWidth {
		ruleWidth = preferredBannerRuleWidth
	}
	if ruleWidth > 0 {
		write(presentation.StyledLine{
			presentation.Text("  "),
			presentation.RoleText(presentation.RoleTruth, strings.Repeat(glyphs.CommandRule, ruleWidth)),
		})
	}
	for _, line := range wrapBannerText(strings.TrimSpace(event.Goal), maxWidth-4) {
		plain("    " + line)
	}
	if ruleWidth > 0 {
		write(presentation.StyledLine{
			presentation.Text("  "),
			presentation.RoleText(presentation.RoleTruth, strings.Repeat(glyphs.CommandRule, ruleWidth)),
		})
	}
	b.WriteByte('\n')
	return b.String()
}

func writeBannerLabelValue(b *strings.Builder, theme Theme, maxWidth int, label, value string) {
	prefix := "  " + label + " "
	value = truncateCellsWithDots(value, maxWidth-runewidth.StringWidth(prefix))
	b.WriteString(theme.render(presentation.StyledLine{
		presentation.Text("  "),
		presentation.RoleText(presentation.RoleTruth, label),
		presentation.Text(" "),
		presentation.RoleText(presentation.RoleProvenance, value),
	}))
	b.WriteByte('\n')
}

func shortBannerCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if commit == "" || strings.EqualFold(commit, "unknown") {
		return ""
	}
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func formatBannerInteger(value int) string {
	digits := strconv.Itoa(value)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

func truncateCellsWithDots(text string, width int) string {
	text = strings.TrimSpace(text)
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(text) <= width {
		return text
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	trimmed := strings.TrimRight(runewidth.Truncate(text, width-3, ""), " ")
	return trimmed + "..."
}

func wrapBannerText(text string, width int) []string {
	if text == "" {
		return []string{"(empty prompt)"}
	}
	if width < 1 {
		return nil
	}
	var lines []string
	var current string
	for _, word := range strings.Fields(text) {
		for runewidth.StringWidth(word) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			part := runewidth.Truncate(word, width, "")
			if part == "" {
				break
			}
			lines = append(lines, part)
			word = strings.TrimPrefix(word, part)
		}
		if word == "" {
			continue
		}
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if runewidth.StringWidth(candidate) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}
