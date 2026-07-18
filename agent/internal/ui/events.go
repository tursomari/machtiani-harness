package ui

import "time"

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
	SessionID      string
	Identity       FooterIdentity
	Goal           string
	CWD            string
	Turn           int
	Elapsed        time.Duration
	TokenUsage     TokenUsageUpdatedEvent
	MaxInputTokens int
	Models         FooterModelMetadata
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
	Step             int
	StepLimit        int
	RemainingSteps   int
	CommandsExecuted int
	Command          string
	Description      string
}

func (e ActionExecutedEvent) Type() string { return "ActionExecuted" }

// NotificationEvent carries a notification message with a severity level.
type NotificationEvent struct {
	Level   NotificationLevel
	Message string
}

func (e NotificationEvent) Type() string { return "Notification" }

// TokenUsageUpdatedEvent carries cumulative LLM token usage for the session
// plus the active prompt size from the latest planner call.
type TokenUsageUpdatedEvent struct {
	InputHit           int
	InputMiss          int
	Output             int
	ActivePromptTokens int
}

func (e TokenUsageUpdatedEvent) Type() string { return "TokenUsageUpdated" }

// FooterIdentity identifies the operation summarized by the footer.
type FooterIdentity struct {
	Label string
	Value string
}

// TurnStatusUpdatedEvent carries the current agent turn for footer display.
type TurnStatusUpdatedEvent struct {
	Turn int
}

func (e TurnStatusUpdatedEvent) Type() string { return "TurnStatusUpdated" }

// FooterModelDisplay describes one role/model pair in the compact footer.
type FooterModelDisplay struct {
	Role      string
	Label     string
	Reasoning string
}

// FooterModelMetadata carries ordered model labels for compact footer display.
type FooterModelMetadata struct {
	Models []FooterModelDisplay
}

// FooterModelsUpdatedEvent replaces the footer's current model-role list.
type FooterModelsUpdatedEvent struct {
	Models FooterModelMetadata
}

func (e FooterModelsUpdatedEvent) Type() string { return "FooterModelsUpdated" }

// FinalAnswerEvent carries the final rendered answer text.
type FinalAnswerEvent struct {
	RenderedText string
}

func (e FinalAnswerEvent) Type() string { return "FinalAnswer" }

// ContinuationHintEvent renders the concise instruction used to resume a
// session. DetailLines are populated only for verbose output.
type ContinuationHintEvent struct {
	Header          string
	DetailLines     []string
	FinalAnswerPath string
	Instruction     string
	Command         string
}

func (e ContinuationHintEvent) Type() string { return "ContinuationHint" }

// UserInputHintEvent renders a suspended-session question and its resume
// command without requiring callers to embed terminal styling in raw strings.
type UserInputHintEvent struct {
	SessionID string
	Context   string
	Question  string
	Command   string
}

func (e UserInputHintEvent) Type() string { return "UserInputHint" }

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
