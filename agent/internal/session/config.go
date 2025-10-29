package session

import "github.com/tursomari/machtiani/agent/internal/llm"

type Config struct {
	MaxSteps                int
	OrchModel               string
	AnswerModel             string
	PatcherModel            string
	FileDiscoveryModel      string
	AgentModel              string
	TimeoutPerTurn          int
	DryRun                  bool
	Verbose                 bool
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
	PatchNoApply            bool
	Patch                   bool
	OpenAIAPIKey            string
	OpenAIBaseURL           string
	OpenAIModel             string
	ShellAgent              bool
	ShellAgentModel         string
}

type BuildInfo struct {
	Version string
	Commit  string
	BuiltAt string
	Dirty   string
}

// Options groups the inputs required to run an agent session.
type Options struct {
	Config           Config
	Goal             string
	ParamPairs       []string
	ParamJSON        []string
	Build            BuildInfo
	GlobalConfig     llm.Config
	GlobalConfigPath string
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
	patcherModel            string
	fileDiscoveryModel      string
	agentModel              string
	timeoutPerTurn          int
	dryRun                  bool
	verbose                 bool
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
	patchNoApply            bool
	patch                   bool
	openAIAPIKey            string
	openAIBaseURL           string
	openAIModel             string
	shellAgent              bool
	shellAgentModel         string
}

func newLegacyConfig(cfg Config) legacyConfig {
	return legacyConfig{
		maxSteps:                cfg.MaxSteps,
		orchModel:               cfg.OrchModel,
		answerModel:             cfg.AnswerModel,
		patcherModel:            cfg.PatcherModel,
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
		patchNoApply:            cfg.PatchNoApply,
		patch:                   cfg.Patch,
		openAIAPIKey:            cfg.OpenAIAPIKey,
		openAIBaseURL:           cfg.OpenAIBaseURL,
		openAIModel:             cfg.OpenAIModel,
		shellAgent:              cfg.ShellAgent,
		shellAgentModel:         cfg.ShellAgentModel,
	}
}
