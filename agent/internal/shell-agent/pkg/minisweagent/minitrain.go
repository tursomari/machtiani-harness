package minisweagent

import "context"

// QueryOption configures optional behaviour for model queries.
type QueryOption func(*queryOptions)

// RunOption configures optional behaviour for an agent run.
type RunOption func(*runOptions)

type queryOptions struct{}
type runOptions struct{}

// Model encapsulates interactions with a large language model.
type Model interface {
	Config() interface{}
	Cost() float64
	NCalls() int
	Query(ctx context.Context, messages []Message, opts ...QueryOption) (QueryResult, error)
	GetTemplateVars() map[string]interface{}
}

// QueryResult contains the model response and any additional metadata returned by the backend.
type QueryResult struct {
	Content string                 `json:"content"`
	Extra   map[string]interface{} `json:"extra"`
}

// Environment encapsulates command execution.
type Environment interface {
	Config() interface{}
	Execute(ctx context.Context, command, cwd string) (ExecuteResult, error)
	GetTemplateVars() map[string]interface{}
	GetSyncProgress() float64  // Returns 0.0 to 1.0; optional (default 1.0)
	GetSyncStatus() string     // Returns status description; optional (default "")
}

// ExecuteResult captures the outcome of a command executed by the environment.
type ExecuteResult struct {
	Output     string `json:"output"`
	ReturnCode int    `json:"returncode"`
	Metadata   string `json:"metadata,omitempty"`
}

// Agent orchestrates the reasoning-execution loop.
type Agent interface {
	Model() Model
	Env() Environment
	Messages() []Message
	Config() interface{}
	Run(ctx context.Context, task string, opts ...RunOption) (exitStatus, result string, err error)
}
