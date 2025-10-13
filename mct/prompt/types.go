package prompt

import "github.com/tursomari/machtiani/mct/llm"

// ModelRuntime captures the resolved model configuration used for LLM calls
// and file discovery. It mirrors the data produced by the existing runtime
// resolution helpers in both the mct CLI and mct-agent.
type ModelRuntime struct {
	Resolved   llm.ResolvedModel
	Alias      string
	UsingAlias bool
	Extras     map[string]any
	ParamPairs []string
	ParamJSON  []string
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
	FileDiscoveryRuntime    ModelRuntime
	OnHeader                func(string)
	OnToken                 func(string)
	Verbose                 bool
	MaxInputTokens          int
}

// Result captures the outcome of a prompt execution.
type Result struct {
	Header         string
	Assistant      string
	FullText       string
	RetrievedFiles []string
	SavedPath      string
	Filename       string
	SaveError      error
}
