package ui

// DisplayEvent is the interface implemented by all event types.
// Each event returns a unique string discriminator via Type().
type DisplayEvent interface {
	Type() string
}

// NotificationLevel classifies the severity of a notification event.
type NotificationLevel string

const (
	NotificationInfo    NotificationLevel = "info"
	NotificationWarning NotificationLevel = "warning"
	NotificationError   NotificationLevel = "error"
)

// SessionStartedEvent signals the beginning of an agent session.
type SessionStartedEvent struct {
	Goal string
}

func (e SessionStartedEvent) Type() string { return "SessionStarted" }

// SessionEndedEvent signals the end of an agent session.
type SessionEndedEvent struct{}

func (e SessionEndedEvent) Type() string { return "SessionEnded" }

// PromptStartedEvent is emitted when a new prompt stream begins.
type PromptStartedEvent struct {
	StreamID string
	Prompt   string
	Opts     *PromptOptions
}

func (e PromptStartedEvent) Type() string { return "PromptStarted" }

// ChunkReceivedEvent carries a incremental text chunk from an active prompt stream.
type ChunkReceivedEvent struct {
	StreamID string
	Text     string
}

func (e ChunkReceivedEvent) Type() string { return "ChunkReceived" }

// PromptCompletedEvent signals a prompt stream has finished successfully.
type PromptCompletedEvent struct {
	StreamID  string
	FinalText string
}

func (e PromptCompletedEvent) Type() string { return "PromptCompleted" }

// PromptAbortedEvent signals a prompt stream has been aborted.
type PromptAbortedEvent struct {
	StreamID string
	Message  string
}

func (e PromptAbortedEvent) Type() string { return "PromptAborted" }

// ActionExecutedEvent represents an action being performed by the agent.
type ActionExecutedEvent struct {
	Step        int
	Description string
}

func (e ActionExecutedEvent) Type() string { return "ActionExecuted" }

// NotificationEvent carries a notification message with a severity level.
type NotificationEvent struct {
	Level   NotificationLevel
	Message string
}

func (e NotificationEvent) Type() string { return "Notification" }

// FinalAnswerEvent carries the final rendered answer text.
type FinalAnswerEvent struct {
	RenderedText string
}

func (e FinalAnswerEvent) Type() string { return "FinalAnswer" }

// ModeTaskPlanDisplayEvent renders an initial set of mode tasks.
type ModeTaskPlanDisplayEvent struct {
	Tasks []ModeTaskDisplay
}

func (e ModeTaskPlanDisplayEvent) Type() string { return "ModeTaskPlanDisplay" }

// ModeTaskStatusUpdateEvent carries a status update for a specific mode task.
type ModeTaskStatusUpdateEvent struct {
	Index  int
	Title  string
	Status string
}

func (e ModeTaskStatusUpdateEvent) Type() string { return "ModeTaskStatusUpdate" }

// RawStringEvent carries a raw text string for direct output.
type RawStringEvent struct {
	Text string
}

func (e RawStringEvent) Type() string { return "RawString" }
