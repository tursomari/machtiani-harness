package session

import (
	"context"
	"io"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

type Config struct {
	MaxTurns                int
	OrchModel               string
	AnswerModel             string
	FileDiscoveryModel      string
	AgentModel              string
	TurnTimeout          int
	DryRun                  bool
	Verbose                 bool
	PersistTmpData          bool
	MaxCommandOutputBytes   int
	FinalFile               string
	TranscriptFile          string
	FileDiscoveryTrajectory string
	FileDiscoveryOutputDir  string
	MaxInputTokens          int
	TrajectoryFile          string
	NoTrajectory            bool
	TrajectoryVerboseLLM    bool
	TrajectoryStreamTokens  bool
	TrajectoryExcerpt       int
	TrajectoryOmitRepoRoot  bool
	OpenAIAPIKey          string
	OpenAIBaseURL         string
	OpenAIModel           string
	ShellAgent            bool
	ShellAgentModel       string
	// AnswerTag overrides the final-answer tag name used by the
	// shell-agent parser and the prompt templates. Empty input is
	// normalised to "answer" downstream; validation of the tag name
	// happens at the CLI boundary in agent/cmd/mct-agent.
	AnswerTag             string
	APIKeyOverrides       map[string]string
	// CommandTag overrides the command tag name used by the
	// shell-agent parser and prompt templates. Defaults to "command".
	CommandTag string
	SessionID             string
	EnableTagFormat       bool
	PromptText            string
	Mode                  string
	ModeInstructionDir    string
	ShellAgentInterruptStep int
	ShellAgentStepLog      string `json:"shell_agent_step_log,omitempty"`
}

type BuildInfo struct {
	Version string
	Commit  string
	BuiltAt string
	Dirty   string
}

// Options groups the inputs required to run an agent session.
type Options struct {
	Config              Config
	Goal                string
	OriginalPrompt      string
	TaskDescription     string
	PlannerOverlay      string
	ParamPairs          []string
	ParamJSON           []string
	Build               BuildInfo
	GlobalConfig        llm.Config
	GlobalConfigPath    string
	APIKeyOverrides     map[string]string
	Context             context.Context
	ProcessTimerManager *ui.ProcessTimerManager
	Diagnostics         io.Writer
	PlannerOverride     Planner
	HasNewInput         bool // true when -t/-f provided on resume (shell-agent starts fresh, no resume attempt)
	ShellAgentInterruptStep int  // > 0 triggers deterministic interrupt after this many shell-agent steps
	ShellAgentStepLog      string // path for step-log JSONL file (empty disables)
}

type Result struct {
	ExitCode  int
	Status    string
	Turns     int
	SessionID string
	Err       error
}

type legacyConfig struct {
	maxTurns                int
	orchModel               string
	answerModel             string
	fileDiscoveryModel      string
	agentModel              string
	timeoutPerTurn          int
	dryRun                  bool
	verbose                 bool
	persistTmpData          bool
	maxCommandOutputBytes   int
	finalFile               string
	transcriptFile          string
	fileDiscoveryTrajectory string
	fileDiscoveryOutputDir  string
	maxInputTokens          int
	trajectoryFile          string
	noTrajectory            bool
	trajectoryVerboseLLM    bool
	trajectoryStreamTokens  bool
	trajectoryExcerpt       int
	trajectoryOmitRepoRoot  bool
	openAIAPIKey            string
	openAIBaseURL           string
	openAIModel             string
	shellAgent              bool
	shellAgentModel         string
	// answerTag is propagated alongside ShellAgentModel. The legacy
	// config struct mirrors Config.AnswerTag (see above).
	answerTag               string
	commandTag              string
	apiKeyOverrides         map[string]string
	enableTagFormat         bool
	sessionID               string
	promptText              string
	mode                    string
	modeInstructionDir      string
	shellAgentStepLog       string
}

func newLegacyConfig(cfg Config) legacyConfig {
	return legacyConfig{
		maxTurns:                cfg.MaxTurns,
		orchModel:               cfg.OrchModel,
		answerModel:             cfg.AnswerModel,
		fileDiscoveryModel:      cfg.FileDiscoveryModel,
		agentModel:              cfg.AgentModel,
		timeoutPerTurn:          cfg.TurnTimeout,
		dryRun:                  cfg.DryRun,
		verbose:                 cfg.Verbose,
		finalFile:               cfg.FinalFile,
		transcriptFile:          cfg.TranscriptFile,
		fileDiscoveryTrajectory: cfg.FileDiscoveryTrajectory,
		fileDiscoveryOutputDir:  cfg.FileDiscoveryOutputDir,
		maxInputTokens:          cfg.MaxInputTokens,
		trajectoryFile:          cfg.TrajectoryFile,
		noTrajectory:            cfg.NoTrajectory,
		trajectoryVerboseLLM:    cfg.TrajectoryVerboseLLM,
		trajectoryStreamTokens:  cfg.TrajectoryStreamTokens,
		trajectoryExcerpt:       cfg.TrajectoryExcerpt,
		trajectoryOmitRepoRoot:  cfg.TrajectoryOmitRepoRoot,
		openAIAPIKey:            cfg.OpenAIAPIKey,
		openAIBaseURL:           cfg.OpenAIBaseURL,
		openAIModel:             cfg.OpenAIModel,
		shellAgent:              cfg.ShellAgent,
		shellAgentModel:         cfg.ShellAgentModel,
		answerTag:               cfg.AnswerTag,
		commandTag:              cfg.CommandTag,
		apiKeyOverrides:         llm.CopyAPIKeyOverridesForRuntime(cfg.APIKeyOverrides),
		persistTmpData:          cfg.PersistTmpData,
		maxCommandOutputBytes:   cfg.MaxCommandOutputBytes,
		enableTagFormat:         cfg.EnableTagFormat,
		sessionID:               cfg.SessionID,
		promptText:              cfg.PromptText,
		mode:                    cfg.Mode,
		modeInstructionDir:      cfg.ModeInstructionDir,
		shellAgentStepLog:       cfg.ShellAgentStepLog,
	}
}
