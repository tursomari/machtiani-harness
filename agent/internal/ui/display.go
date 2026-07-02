package ui

// SessionDisplay defines the interface for session-level display operations.
type SessionDisplay interface {
	StartSession(goal string)
	EndSession()
	BeginPrompt(prompt string, opts *PromptOptions) PromptStream
	ShowFinal(rendered string)
	StreamAction(line string)
	RenderModePlan(tasks []ModeTaskDisplay)
	UpdateModeTaskStatus(index int, title string, status string)
	Notify(message string)
	WriteString(s string)
}

var _ SessionDisplay = (*Formatter)(nil)
