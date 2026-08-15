// Package ui provides the terminal user interface components for Machtiani.
//
// Theme holds all configurable formatting values for the terminal display,
// centralizing color codes, prompt prefixes, and layout constants that were
// previously hardcoded across terminal_display.go and timer_manager.go.

package ui

import "github.com/tursomari/machtiani/agent/internal/presentation"

// Theme centralizes all display formatting configuration.
type Theme struct {
	Presentation          presentation.Theme
	PromptFirstLinePrefix string
	PromptSpacerPrefix    string
	ModePlanHeader        string
	ModeTaskPrefix        string
	NotificationPrefix    string
	EmptyPlaceholder      string
	EmptyResponseText     string
	ErrorPrefix           string
	ChoicePrompt          string
	ConfirmPromptSuffix   string
	InputPromptSuffix     string
	PromptWindowLines     int
	PromptContentLines    int
}

// DefaultTheme returns a Theme with defaults matching the current terminal_display.go.
func DefaultTheme(styles ...presentation.Theme) Theme {
	var resolved presentation.Theme
	if len(styles) > 0 {
		resolved = styles[0]
	}
	return Theme{
		Presentation:          resolved,
		PromptFirstLinePrefix: "`-- ",
		PromptSpacerPrefix:    "    ",
		ModePlanHeader:        "[meta] planned tasks:",
		ModeTaskPrefix:        "[meta]",
		NotificationPrefix:    "|   ",
		PromptWindowLines:     9,
		PromptContentLines:    8,
		EmptyPlaceholder:      "...",
		EmptyResponseText:     "(empty response)",
		ErrorPrefix:           "error: ",
		ChoicePrompt:          "Choice: ",
		ConfirmPromptSuffix:   " (y/N): ",
		InputPromptSuffix:     ": ",
	}
}

func (t Theme) render(line presentation.StyledLine) string {
	return t.Presentation.RenderLine(line)
}
