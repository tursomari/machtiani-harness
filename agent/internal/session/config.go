package session

import (
	"context"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

type Config struct {
	MaxSteps                int
	OrchModel               string
	AnswerModel             string
	FileDiscoveryModel      string
	AgentModel              string
	TimeoutPerTurn          int
	DryRun                  bool
	Verbose                 bool
	PersistTmpData          bool
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
	APIKeyOverrides       map[string]string
	SessionID             string
	EnableTagFormat       bool
	PromptText            string
	Mode                  string
	ModeInstructionDir    string
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
}

type Result struct {
	ExitCode  int
	Status    string
	Turns     int
	SessionID string
	Err       error
}

type legacyConfig struct {
	maxSteps                int
	orchModel               string
	answerModel             string
	fileDiscoveryModel      string
	agentModel              string
	timeoutPerTurn          int
	dryRun                  bool
	verbose                 bool
	persistTmpData          bool
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
	apiKeyOverrides         map[string]string
	enableTagFormat         bool
	sessionID               string
	promptText              string
	mode                    string
	modeInstructionDir      string
}

func newLegacyConfig(cfg Config) legacyConfig {
	return legacyConfig{
		maxSteps:                cfg.MaxSteps,
		orchModel:               cfg.OrchModel,
		answerModel:             cfg.AnswerModel,
		fileDiscoveryModel:      cfg.FileDiscoveryModel,
		agentModel:              cfg.AgentModel,
		timeoutPerTurn:          cfg.TimeoutPerTurn,
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
		apiKeyOverrides:         llm.CopyAPIKeyOverridesForRuntime(cfg.APIKeyOverrides),
		persistTmpData:          cfg.PersistTmpData,
		enableTagFormat:         cfg.EnableTagFormat,
		sessionID:               cfg.SessionID,
		promptText:              cfg.PromptText,
		mode:                    cfg.Mode,
		modeInstructionDir:      cfg.ModeInstructionDir,
	}
}
