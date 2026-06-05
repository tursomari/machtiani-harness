package prompt

import (
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// ModelRuntime captures the resolved model configuration used for LLM calls
// and file discovery. It mirrors the data produced by the existing runtime
// resolution helpers in both the mct CLI and mct-agent.
type ModelRuntime struct {
	Resolved         llm.ResolvedModel
	Alias            string
	UsingAlias       bool
	Extras           map[string]any
	ParamPairs       []string
	ParamJSON        []string
	FallbackAliases  []string
	FallbackResolved []llm.ResolvedModel
	APIKeyOverrides  map[string]string
}

// RunOptions configures the execution of a prompt, including streaming
// callbacks and session scoping.
type RunOptions struct {
	Prompt                  string
	Mode                    string
	IncludeHistory          bool
	SessionID               string
	SourceFile              string
	ExplicitName            string
	FileDiscoveryTrajectory string
	Runtime                 ModelRuntime
	AnswerRuntime           ModelRuntime
	FileDiscoveryRuntime    ModelRuntime
	OnHeader                func(string)
	OnToken                 func(string)
	Verbose                 bool
	MaxInputTokens          int
	Readme                  *ReadmeOptions
	ShellAgent              bool
	ShellAgentModel         string
	ShellAgentSessionID     string // ShellAgentSessionID is the sub-session ID passed to the shell-agent for recoverability (format: <parent-session-id>/shell-agent/<turn>).
	GlobalConfigPath        string
	PersistTmpData          bool
	SessionTempRoot         string
	ResponseDirectives      []string
	Prompts                 *llm.MCTPromptsConfig

	// ShellAgentLibrary enables the in-process library path. When set
	// alongside ShellAgent=true, the prompt layer calls shellagent.Run
	// directly instead of spawning a subprocess.
	ShellAgentLibrary *ShellAgentLibraryConfig
}

// ReadmeOptions configure optional internal README management hooks.
type ReadmeOptions struct {
	Enabled          bool
	ProjectCommitSHA string
}

// ShellAgentLibraryConfig holds the live objects needed for the in-process
// shell-agent library path. When set on RunOptions, the prompt layer calls
// shellagent.Run directly instead of spawning a subprocess.
type ShellAgentLibraryConfig struct {
	Model   minisweagent.Model
	Env     minisweagent.Environment
	Config  *minisweagent.ShellAgentConfig
	Prompts *minisweagent.PromptsConfig

	// PrebuiltMessages, when non-nil, carries the pre-built message
	// prefix (system prompt + planner conversation messages). The
	// prompt layer appends the instance prompt and passes the complete
	// array to shellagent.Run.
	PrebuiltMessages []llm.Message

	// FewShotVariant controls the few-shot injection variant:
	// Default is "instance". Valid values are: "off", "none", "system", "instance".
	FewShotVariant string

	// TurnIndex is the current turn number (0-based) for per-turn injection logic.
	TurnIndex int

	// EnforceEarlyCommands, when true, opts the work_request into the
	// stricter early-turn behaviour: the shell-agent library emits a
	// turn-specific format-error message and runs `bash -n` against
	// extracted commands when PlannerTurn < 3. The flag defaults to
	// false so existing sessions keep the relaxed behaviour until a
	// caller explicitly opts in.
	EnforceEarlyCommands bool
}

// Result captures the outcome of a prompt execution.
type Result struct {
	Header           string
	Assistant        string
	FullText         string
	DirectiveBlock   string
	RetrievedFiles   []string
	SavedPath        string
	Filename         string
	SaveError        error
	TrajectoryPath   string
	FileDiscoveryRan bool
	ShellAgentUsed   bool
}
