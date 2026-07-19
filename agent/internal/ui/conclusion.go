package ui

import (
	"fmt"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/presentation"
)

const preferredConclusionRuleWidth = 96

// RenderSessionConclusion renders one complete outcome block. Width is the
// current terminal width; zero uses the formatter's normal fallback width.
func RenderSessionConclusion(event SessionConclusionEvent, theme Theme, width int) string {
	if width <= 0 {
		width = defaultWidth
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
	blank := func() { b.WriteByte('\n') }

	glyphs := theme.Presentation.Glyphs()
	outerWidth := conclusionOuterRuleWidth(width)
	writeOuterRule := func() {
		if outerWidth == 0 {
			return
		}
		write(presentation.StyledLine{
			presentation.Text("  "),
			presentation.Bold(presentation.RoleBeauty, strings.Repeat(glyphs.OuterRule, outerWidth)),
		})
	}
	b.WriteByte('\n')
	writeOuterRule()
	if outerWidth > 0 {
		blank()
	}

	closeAfterActions := true
	switch event.Outcome {
	case SessionConclusionShellInterrupted:
		write(presentation.StyledLine{
			presentation.Text("  "),
			presentation.Bold(presentation.RoleRupture, "SHELL-AGENT INTERRUPTED"),
		})
		blank()
		plain("  Shell-agent work is resumable.")
		blank()
		writeConclusionAction(&b, theme, width, event, "Resume the interrupted shell-agent work:", "")
	case SessionConclusionUserInputNeeded:
		write(presentation.StyledLine{
			presentation.Text("  "),
			presentation.Bold(presentation.RoleProvenance, "USER INPUT NEEDED"),
		})
		blank()
		if explanation := strings.TrimSpace(event.Explanation); explanation != "" {
			write(presentation.StyledLine{
				presentation.Text("  "),
				presentation.RoleText(presentation.RoleTruth, "Why this needs your input:"),
			})
			for _, line := range sanitizeLines(explanation) {
				plain("    " + strings.TrimSpace(line))
			}
			blank()
		}
		write(presentation.StyledLine{
			presentation.Text("  "),
			presentation.Bold(presentation.RoleBeauty, "Your decision:"),
		})
		for _, line := range sanitizeLines(event.Question) {
			write(presentation.StyledLine{
				presentation.Text("    "),
				presentation.Bold(presentation.RoleBeauty, "? "+strings.TrimSpace(line)),
			})
		}
		blank()
		writeConclusionAction(&b, theme, width, event, "Continue with your answer:", "<your answer>")
	default:
		closeAfterActions = false
		if answer := strings.Trim(event.RenderedAnswer, "\n"); strings.TrimSpace(answer) != "" {
			plain(answer)
			blank()
		}
		writeOuterRule()
		if outerWidth > 0 {
			blank()
		}
		if finalPath := strings.TrimSpace(event.FinalAnswerPath); finalPath != "" {
			write(presentation.StyledLine{
				presentation.Text("  "),
				presentation.RoleText(presentation.RoleProvenance, "Answer saved to:"),
			})
			pathSpan := presentation.RoleText(presentation.RoleBeauty, FormatHomePath(finalPath))
			pathSpan.Underline = true
			write(presentation.StyledLine{presentation.Text("    "), pathSpan})
			blank()
		}
		if event.Verbose {
			writeConclusionDetail(&b, theme, "Session ID:", strings.TrimSpace(event.SessionID), presentation.RoleProvenance, false)
			writeConclusionDetail(&b, theme, "Turns completed:", fmt.Sprintf("%d", event.TurnsCompleted), presentation.RoleTruth, true)
			writeConclusionDetail(&b, theme, "Goal so far:", fmt.Sprintf("%q", strings.TrimSpace(event.Goal)), presentation.RoleNormal, false)
			blank()
		}
		writeConclusionAction(&b, theme, width, event, "Continue this session:", "<your follow-up prompt>")
	}

	if closeAfterActions {
		blank()
		writeOuterRule()
	}
	return b.String()
}

func conclusionOuterRuleWidth(width int) int {
	available := width - 3 // two-column indent plus one safe auto-wrap column
	if available > preferredConclusionRuleWidth {
		available = preferredConclusionRuleWidth
	}
	if available < 8 {
		return 0
	}
	return available
}

func writeConclusionDetail(b *strings.Builder, theme Theme, label, value string, role presentation.Role, bold bool) {
	if value == "" {
		return
	}
	valueSpan := presentation.RoleText(role, " "+value)
	valueSpan.Bold = bold
	b.WriteString(theme.render(presentation.StyledLine{
		presentation.Text("  "),
		presentation.RoleText(presentation.RoleTruth, label),
		valueSpan,
	}))
	b.WriteByte('\n')
}

func writeConclusionAction(b *strings.Builder, theme Theme, width int, event SessionConclusionEvent, label, promptPlaceholder string) {
	b.WriteString(theme.render(presentation.StyledLine{
		presentation.Text("  "),
		presentation.Bold(presentation.RoleGoodness, label),
	}))
	b.WriteByte('\n')
	for _, line := range conclusionCommandBlock(theme, width, strings.TrimSpace(event.SessionID), promptPlaceholder) {
		b.WriteString(theme.render(line))
		b.WriteByte('\n')
	}
}

func conclusionCommandBlock(theme Theme, width int, sessionID, promptPlaceholder string) []presentation.StyledLine {
	command := "mct-agent run"
	if promptPlaceholder != "" {
		command += " -t \"" + promptPlaceholder + "\""
	}
	command += " --session-id " + sessionID

	singleVisible := "$ " + command
	useSingle := 4+runeLen(singleVisible)+2 <= width-1
	var visible []string
	if useSingle {
		visible = []string{singleVisible}
	} else {
		visible = []string{"mct-agent run \\"}
		if promptPlaceholder != "" {
			visible = append(visible, "  -t \""+promptPlaceholder+"\" \\")
		}
		visible = append(visible, "  --session-id "+sessionID)
	}

	longest := 0
	for _, line := range visible {
		if n := runeLen(line); n > longest {
			longest = n
		}
	}
	rule := presentation.StyledLine{
		presentation.Text("    "),
		presentation.RoleText(presentation.RoleGoodness, strings.Repeat(theme.Presentation.Glyphs().CommandRule, longest+2)),
	}
	lines := []presentation.StyledLine{rule}
	if useSingle {
		lines = append(lines, styleConclusionSingleCommand(command))
	} else {
		lines = append(lines, presentation.StyledLine{
			presentation.Text("    "),
			presentation.Bold(presentation.RoleProvenance, "mct-agent run"),
			presentation.Text(" \\"),
		})
		if promptPlaceholder != "" {
			lines = append(lines, presentation.StyledLine{
				presentation.Text("      -t \"" + promptPlaceholder + "\" \\"),
			})
		}
		lines = append(lines, presentation.StyledLine{
			presentation.Text("      --session-id " + sessionID),
		})
	}
	lines = append(lines, rule)
	return lines
}

func styleConclusionSingleCommand(command string) presentation.StyledLine {
	const executable = "mct-agent run"
	return presentation.StyledLine{
		presentation.Text("    "),
		presentation.Bold(presentation.RoleGoodness, "$ "),
		presentation.Bold(presentation.RoleProvenance, executable),
		presentation.Text(strings.TrimPrefix(command, executable)),
	}
}
