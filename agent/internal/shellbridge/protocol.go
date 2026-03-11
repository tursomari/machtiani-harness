package shellbridge

// ActionPrefix marks stdout lines emitted by shell-agent to announce the next
// shell command. The parent process intercepts these lines and converts them
// into trajectory events so they can be surfaced in real time.
const ActionPrefix = "MCT_SHELL_ACTION "

// ActionMessage is serialized as JSON after ActionPrefix. It carries the
// natural-language description and resolved shell command for an execution
// step.
type ActionMessage struct {
	Description      string `json:"description,omitempty"`
	Command          string `json:"command,omitempty"`
	ModelCallsUsed   int    `json:"model_calls_used,omitempty"`
	StepLimit        int    `json:"step_limit,omitempty"`
	RemainingSteps   int    `json:"remaining_steps,omitempty"`
	CommandsExecuted int    `json:"commands_executed,omitempty"`
}
