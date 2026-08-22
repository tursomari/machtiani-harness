package agents

import (
	"context"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/run"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"github.com/tursomari/machtiani/agent/internal/shellbridge"
)

// AgentRunConfig holds immutable configuration for an agent run.
type AgentRunConfig struct {
	Model                           minisweagent.Model
	Env                             minisweagent.Environment
	Task                            string
	SessionID                       string
	MaxInputTokens                  int
	PlannerTurn                     int
	EnforceEarlyCommands            bool
	MaxSteps                        int
	FinalizeRemainingSteps          int
	CommandSupervisorAfter          int
	CommandSupervisorTimeout        int
	CommandSupervisorFailureLimit   int
	CommandSupervisorMaxSteps       int
	CommandSupervisorDeadlineBuffer int
	// CommandSupervisorLogPath is an internal, temporary diagnostic sink. It is
	// deliberately not exposed through user configuration or CLI flags.
	CommandSupervisorLogPath string
	CommandReviewer          CommandReviewer
	ActionObserver           func(shellbridge.ActionMessage)
	Clock                    CommandClock
	AnswerTag                string
	CommandTag               string
	Verbose                  bool
	SystemPrompt             string
	SystemPromptCached       bool
	NewModel                 func() (minisweagent.Model, error)
	// CheckpointDir is the directory where per-step trajectory checkpoints are written.
	// When empty, no checkpointing occurs.
	CheckpointDir string
	State         *AgentRunState
}

// AgentRunState holds mutable runtime state for an agent run.
type AgentRunState struct {
	Messages                     []minisweagent.Message
	Prompts                      *minisweagent.PromptsConfig
	ExtraVars                    map[string]interface{}
	stepCounter                  int
	commandsExecuted             int
	lastNonEmptyOutput           string
	finalizeRequested            bool
	consecutiveFormatErrors      int
	consecutiveFinalizeReminders int
	InterruptStep                int
}

const maxFinalizeReminders = 3

// StepCounter returns the current step counter.
func (s *AgentRunState) StepCounter() int { return s.stepCounter }

// CommandsExecuted returns the number of commands executed.
func (s *AgentRunState) CommandsExecuted() int { return s.commandsExecuted }

// LastNonEmptyOutput returns the last non-empty output.
func (s *AgentRunState) LastNonEmptyOutput() string { return s.lastNonEmptyOutput }

// FinalizeRequested reports whether finalization has been requested.
func (s *AgentRunState) FinalizeRequested() bool { return s.finalizeRequested }

// ConsecutiveFormatErrors returns the count of consecutive format errors.
func (s *AgentRunState) ConsecutiveFormatErrors() int { return s.consecutiveFormatErrors }

// ConsecutiveFinalizeReminders returns the count of consecutive finalize reminders.
func (s *AgentRunState) ConsecutiveFinalizeReminders() int { return s.consecutiveFinalizeReminders }

// SetInterruptStep sets the interrupt step on the agent's state.
func (a *DefaultAgent) SetInterruptStep(step int) {
	a.State.InterruptStep = step
}

// ApplyResumeState restores all mutable run state fields on a DefaultAgent,
// allowing a previously suspended run to be resumed.
func (a *DefaultAgent) ApplyResumeState(
	stepCounter int,
	commandsExecuted int,
	lastNonEmptyOutput string,
	finalizeRequested bool,
	consecutiveFormatErrors int,
	consecutiveFinalizeReminders int,
	systemPrompt string,
	systemPromptCached bool,
) {
	a.State.stepCounter = stepCounter
	a.State.commandsExecuted = commandsExecuted
	a.State.lastNonEmptyOutput = lastNonEmptyOutput
	a.State.finalizeRequested = finalizeRequested
	a.State.consecutiveFormatErrors = consecutiveFormatErrors
	a.State.consecutiveFinalizeReminders = consecutiveFinalizeReminders
	a.RunConfig.SystemPrompt = systemPrompt
	a.RunConfig.SystemPromptCached = systemPromptCached
}

// GetResumeState returns a snapshot of the agent's mutable run state, suitable for
// persisting and later resuming an interrupted run.
func (a *DefaultAgent) GetResumeState() *run.ResumeState {
	return &run.ResumeState{
		Version:                      run.ResumeStateVersion,
		StepCounter:                  a.State.StepCounter(),
		CommandsExecuted:             a.State.CommandsExecuted(),
		LastNonEmptyOutput:           a.State.LastNonEmptyOutput(),
		FinalizeRequested:            a.State.FinalizeRequested(),
		ConsecutiveFormatErrors:      a.State.ConsecutiveFormatErrors(),
		ConsecutiveFinalizeReminders: a.State.ConsecutiveFinalizeReminders(),
		SystemPrompt:                 a.RunConfig.SystemPrompt,
		SystemPromptCached:           a.RunConfig.SystemPromptCached,
		AnswerTag:                    a.RunConfig.AnswerTag,
		CommandTag:                   a.RunConfig.CommandTag,
	}
}

// RestoreResumeState validates the resume state version and, if compatible, applies
// its fields to the agent via ApplyResumeState. If the version does not match, the
// method returns silently (callers should check ValidateVersion beforehand if they
// need an error).
func (a *DefaultAgent) RestoreResumeState(rs *run.ResumeState) {
	if err := rs.ValidateVersion(); err != nil {
		return
	}
	a.ApplyResumeState(
		rs.StepCounter,
		rs.CommandsExecuted,
		rs.LastNonEmptyOutput,
		rs.FinalizeRequested,
		rs.ConsecutiveFormatErrors,
		rs.ConsecutiveFinalizeReminders,
		rs.SystemPrompt,
		rs.SystemPromptCached,
	)
	if strings.TrimSpace(rs.AnswerTag) != "" {
		a.RunConfig.AnswerTag = rs.AnswerTag
		a.RunConfig.NormalizeAnswerTag()
	}
	if strings.TrimSpace(rs.CommandTag) != "" {
		a.RunConfig.CommandTag = rs.CommandTag
		a.RunConfig.NormalizeCommandTag()
	}
}

// ShellAgentConfig builds a minisweagent.ShellAgentConfig from the AgentRunConfig fields.
func (rc *AgentRunConfig) ShellAgentConfig() *minisweagent.ShellAgentConfig {
	return &minisweagent.ShellAgentConfig{
		MaxSteps:                        rc.MaxSteps,
		FinalizeRemainingSteps:          rc.FinalizeRemainingSteps,
		CommandSupervisorAfter:          rc.CommandSupervisorAfter,
		CommandSupervisorTimeout:        rc.CommandSupervisorTimeout,
		CommandSupervisorFailureLimit:   rc.CommandSupervisorFailureLimit,
		CommandSupervisorMaxSteps:       rc.CommandSupervisorMaxSteps,
		CommandSupervisorDeadlineBuffer: rc.CommandSupervisorDeadlineBuffer,
	}
}

// NormalizeAnswerTag trims whitespace of c.AnswerTag, defaults it to "answer" if empty,
// and populates c.State.ExtraVars with both "answer_tag" and "AnswerTag" keys if c.State
// and c.State.ExtraVars are non-nil.
func (c *AgentRunConfig) NormalizeAnswerTag() {
	c.AnswerTag = strings.TrimSpace(c.AnswerTag)
	if c.AnswerTag == "" {
		c.AnswerTag = "answer"
	}
	if c.State != nil && c.State.ExtraVars != nil {
		c.State.ExtraVars["answer_tag"] = c.AnswerTag
		c.State.ExtraVars["AnswerTag"] = c.AnswerTag
	}
}

// NormalizeCommandTag trims whitespace of c.CommandTag, defaults it to "command" if empty,
// and populates c.State.ExtraVars with both "command_tag" and "CommandTag" keys if c.State
// and c.State.ExtraVars are non-nil.
func (c *AgentRunConfig) NormalizeCommandTag() {
	c.CommandTag = strings.TrimSpace(c.CommandTag)
	if c.CommandTag == "" {
		c.CommandTag = "command"
	}
	if c.State != nil && c.State.ExtraVars != nil {
		c.State.ExtraVars["command_tag"] = c.CommandTag
		c.State.ExtraVars["CommandTag"] = c.CommandTag
	}
}

// NewAgentRunConfig creates a new AgentRunConfig with sensible defaults matching the
// current DefaultAgent constructor defaults. It also creates and attaches an AgentRunState
// initialized with empty Messages and an ExtraVars map pre-populated with tag keys.
func NewAgentRunConfig(model minisweagent.Model, env minisweagent.Environment, task string) *AgentRunConfig {
	cfg := &AgentRunConfig{
		Model:                model,
		Env:                  env,
		Task:                 task,
		SessionID:            minisweagent.GenerateSessionID(),
		MaxInputTokens:       0,
		PlannerTurn:          0,
		EnforceEarlyCommands: false,
		AnswerTag:            "answer",
		CommandTag:           "command",
		Verbose:              false,
		SystemPrompt:         "",
		SystemPromptCached:   false,
		NewModel:             nil,
	}

	cfg.State = &AgentRunState{
		Messages: make([]minisweagent.Message, 0),
		ExtraVars: map[string]interface{}{
			"answer_tag":  cfg.AnswerTag,
			"AnswerTag":   cfg.AnswerTag,
			"command_tag": cfg.CommandTag,
			"CommandTag":  cfg.CommandTag,
		},
	}

	cfg.NormalizeAnswerTag()
	cfg.NormalizeCommandTag()

	return cfg
}

// DefaultAgentOption is a functional option for configuring a DefaultAgent at construction time.
type DefaultAgentOption func(*DefaultAgent)

// WithVerbose sets the Verbose flag.
func WithVerbose(v bool) DefaultAgentOption {
	return func(a *DefaultAgent) {
		a.RunConfig.Verbose = v
	}
}

// WithMaxInputTokens sets the MaxInputTokens limit. Negative values are clamped to 0.
func WithMaxInputTokens(limit int) DefaultAgentOption {
	return func(a *DefaultAgent) {
		if limit < 0 {
			limit = 0
		}
		a.RunConfig.MaxInputTokens = limit
	}
}

// WithTask sets the Task description.
func WithTask(task string) DefaultAgentOption {
	return func(a *DefaultAgent) {
		a.RunConfig.Task = task
	}
}

// WithPlannerTurn sets the PlannerTurn index. Negative values are clamped to 0.
func WithPlannerTurn(turn int) DefaultAgentOption {
	return func(a *DefaultAgent) {
		if turn < 0 {
			turn = 0
		}
		a.RunConfig.PlannerTurn = turn
	}
}

// WithEnforceEarlyCommands sets the EnforceEarlyCommands flag.
func WithEnforceEarlyCommands(enforce bool) DefaultAgentOption {
	return func(a *DefaultAgent) {
		a.RunConfig.EnforceEarlyCommands = enforce
	}
}

// WithActionObserver installs a synchronous observer for command
// announcements. The observer runs after announcement and before execution.
func WithActionObserver(observer func(shellbridge.ActionMessage)) DefaultAgentOption {
	return func(a *DefaultAgent) {
		a.RunConfig.ActionObserver = observer
	}
}

// WithAnswerTag sets the AnswerTag, normalizing it through RunConfig.
func WithAnswerTag(tag string) DefaultAgentOption {
	return func(a *DefaultAgent) {
		a.RunConfig.AnswerTag = tag
		a.RunConfig.NormalizeAnswerTag()
		if a.State.ExtraVars == nil {
			a.State.ExtraVars = map[string]interface{}{}
		}
		a.State.ExtraVars["answer_tag"] = a.RunConfig.AnswerTag
		a.State.ExtraVars["AnswerTag"] = a.RunConfig.AnswerTag
	}
}

// WithCommandTag sets the CommandTag, normalizing it through RunConfig.
func WithCommandTag(tag string) DefaultAgentOption {
	return func(a *DefaultAgent) {
		a.RunConfig.CommandTag = tag
		a.RunConfig.NormalizeCommandTag()
		if a.State.ExtraVars == nil {
			a.State.ExtraVars = map[string]interface{}{}
		}
		a.State.ExtraVars["command_tag"] = a.RunConfig.CommandTag
		a.State.ExtraVars["CommandTag"] = a.RunConfig.CommandTag
	}
}

// WithNewModel sets the per-run model factory. When fn is nil the option is a no-op.
func WithNewModel(fn func() (minisweagent.Model, error)) DefaultAgentOption {
	return func(a *DefaultAgent) {
		if fn == nil {
			return
		}
		a.RunConfig.NewModel = fn
	}
}

// WithCommandReviewer installs the callback used for each running-command
// review. A nil callback leaves supervision inactive while preserving the hard
// command deadline.
func WithCommandReviewer(reviewer CommandReviewer) DefaultAgentOption {
	return func(a *DefaultAgent) {
		a.RunConfig.CommandReviewer = reviewer
	}
}

// WithCommandClock overrides command scheduling for deterministic tests.
func WithCommandClock(clock CommandClock) DefaultAgentOption {
	return func(a *DefaultAgent) {
		if clock != nil {
			a.RunConfig.Clock = clock
		}
	}
}

// CommandReviewer investigates a command that is still running and returns an
// explicit disposition when it remains active.
type CommandReviewer func(context.Context, CommandReviewRequest) (CommandReviewResult, error)

type CommandReviewReason string

const (
	CommandReviewRegular  CommandReviewReason = "regular"
	CommandReviewDeadline CommandReviewReason = "deadline"
)

type CommandDisposition string

const (
	CommandDispositionContinue CommandDisposition = "continue"
	CommandDispositionCancel   CommandDisposition = "cancel"
)

// CommandReviewRequest contains the live process and deadline state supplied
// to one supervisor review.
type CommandReviewRequest struct {
	Command             string
	CommandNumber       int
	ReviewNumber        int
	Reason              CommandReviewReason
	ConsecutiveFailures int
	PID                 int
	ProcessGroupID      int
	StartedAt           time.Time
	Deadline            time.Time
	Remaining           time.Duration
	Output              minisweagent.CommandOutputSnapshot
}

// CommandReviewResult is the supervisor's structured final decision.
type CommandReviewResult struct {
	Disposition CommandDisposition `json:"disposition"`
	Summary     string             `json:"summary"`
}

// WithSessionID sets the session identifier. When sessionID is empty the option is a no-op.
func WithSessionID(sessionID string) DefaultAgentOption {
	return func(a *DefaultAgent) {
		if sessionID == "" {
			return
		}
		a.RunConfig.SessionID = sessionID
	}
}

// WithCheckpointDir sets the directory where per-step trajectory checkpoints are written.
// When dir is empty the option is a no-op.
func WithCheckpointDir(dir string) DefaultAgentOption {
	return func(a *DefaultAgent) {
		a.RunConfig.CheckpointDir = dir
	}
}
