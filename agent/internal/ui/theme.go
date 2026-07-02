// Package ui provides the terminal user interface components for mct-agent.
//
// Theme holds all configurable formatting values for the terminal display,
// centralizing color codes, prompt prefixes, and layout constants that were
// previously hardcoded across terminal_display.go and timer_manager.go.

package ui

// Theme centralizes all display formatting configuration.
type Theme struct {
	ResetColor          string
	GrayColor           string
	ErrorColor          string
	WarningColor        string
	InfoColor           string
	PromptFirstLinePrefix string
	PromptSpacerPrefix  string
	FinalAnswerHeader   string
	ModePlanHeader      string
	ModeTaskPrefix      string
	NotificationPrefix  string
	EmptyPlaceholder    string
	EmptyResponseText   string
	ErrorPrefix         string
	ChoicePrompt        string
	ConfirmPromptSuffix string
	InputPromptSuffix   string
	PromptWindowLines   int
	PromptContentLines  int
}

// DefaultTheme returns a Theme with defaults matching the current terminal_display.go.
func DefaultTheme() Theme {
	return Theme{
		ResetColor:          "\033[0m",
		GrayColor:           "\033[37m",
		ErrorColor:          "\033[31m",
		WarningColor:        "\033[33m",
		InfoColor:           "\033[37m",
		PromptFirstLinePrefix: "`-- ",
		PromptSpacerPrefix:  "    ",
		FinalAnswerHeader:   "===> FINAL RESPONSE <===",
		ModePlanHeader:      "[meta] planned tasks:",
		ModeTaskPrefix:      "[meta]",
		NotificationPrefix:  "|   ",
		PromptWindowLines:   9,
		PromptContentLines:  8,
		EmptyPlaceholder:    "...",
		EmptyResponseText:   "(empty response)",
		ErrorPrefix:         "error: ",
		ChoicePrompt:        "Choice: ",
		ConfirmPromptSuffix: " (y/N): ",
		InputPromptSuffix:   ": ",
	}
}
