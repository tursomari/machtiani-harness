package prompt

import "github.com/tursomari/machtiani/agent/internal/llm"

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
	GlobalConfigPath        string
	PersistTmpData          bool
	SessionTempRoot         string
	ResponseDirectives      []string
	Prompts                 *llm.MCTPromptsConfig
}

// ReadmeOptions configure optional internal README management hooks.
type ReadmeOptions struct {
	Enabled          bool
	ProjectCommitSHA string
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
