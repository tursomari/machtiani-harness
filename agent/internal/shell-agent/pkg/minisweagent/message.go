package minisweagent

// Message represents a single chat message exchanged between the agent and the model.
type Message struct {
	Role     string                 `json:"role"`
	Content  string                 `json:"content"`
	Extra    map[string]interface{} `json:"extra,omitempty"`
	Metadata map[string]any         `json:"metadata,omitempty"`
}

// Trajectory captures the full conversation history and final outcome of an agent run.
type Trajectory struct {
	Messages   []Message              `json:"messages"`
	ExitStatus string                 `json:"exit_status"`
	Result     string                 `json:"result"`
	ExtraInfo  map[string]interface{} `json:"extra_info,omitempty"`
}

// NewTrajectory constructs a trajectory from the final agent state.
func NewTrajectory(messages []Message, exitStatus, result string) Trajectory {
	return Trajectory{
		Messages:   messages,
		ExitStatus: exitStatus,
		Result:     result,
	}
}
