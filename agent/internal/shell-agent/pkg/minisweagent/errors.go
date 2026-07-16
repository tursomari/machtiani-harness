package minisweagent

import "fmt"

// AgentError represents an error that the agent can reason about inside the main loop.
type AgentError interface {
	error
	IsTerminating() bool
}

// FormatError indicates the model did not return a correctly formatted command.
type FormatError struct {
	Message string
}

func (e *FormatError) Error() string       { return e.Message }
func (e *FormatError) IsTerminating() bool { return false }

// ExecutionTimeoutError indicates a command exceeded the allotted runtime.
type ExecutionTimeoutError struct {
	Message string
}

func (e *ExecutionTimeoutError) Error() string       { return e.Message }
func (e *ExecutionTimeoutError) IsTerminating() bool { return false }

// Submitted terminates the loop with a success result provided by the model or environment output.
type Submitted struct {
	Result string
}

func (e *Submitted) Error() string       { return e.Result }
func (e *Submitted) IsTerminating() bool { return true }

// LimitsExceeded terminates the loop when the configured budget has been exhausted.
type LimitsExceeded struct {
	Reason string
}

func (e *LimitsExceeded) Error() string       { return e.Reason }
func (e *LimitsExceeded) IsTerminating() bool { return true }

// StalledError terminates the loop when the same intent is repeated without progress.
type StalledError struct {
	Intent      string
	Repetitions int
}

func (e *StalledError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("agent stalled: intent %q repeated %d times", e.Intent, e.Repetitions)
}

func (e *StalledError) IsTerminating() bool { return true }
