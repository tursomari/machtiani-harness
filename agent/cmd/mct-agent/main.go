package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/readmesync"
	"github.com/tursomari/machtiani/agent/internal/session"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
	Dirty   = "unknown"

	readmeHeadCommitFn       = readmesync.HeadCommit
	readmeCommitForProjectFn = readmesync.READMECommitForProject
	sessionRunFn             = session.Run
)

type multiString []string

func (m *multiString) String() string {
	return strings.Join(*m, ",")
}

func (m *multiString) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func (m *multiString) Type() string {
	return "strings"
}

func printVersion() {
	fmt.Printf("mct-agent %s\ncommit: %s\nbuilt: %s\ndirty: %s\n", Version, Commit, BuiltAt, Dirty)
}

func main() {
	os.Exit(run())
}

type cliCommand struct {
	name        string
	description string
	handler     func(args []string) int
}

var cliCommands = []cliCommand{
	{name: "run", description: "Run an agent session with a prompt", handler: handleRunCommand},
	{name: "sync", description: "Sync the internal README with current git state", handler: handleSyncCommand},
	{name: "session", description: "Manage sessions (list, show)", handler: handleSessionCommand},
	{name: "config", description: "Validate configuration", handler: handleConfigCommand},
	{name: "shell-agent", description: "Run a shell-agent task directly (no planner)", handler: handleShellAgentCommand},
}

func newTopLevelFlagSet() *pflag.FlagSet {
	fs := pflag.NewFlagSet("mct-agent", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Bool("version", false, "print build metadata and exit")
	fs.BoolP("help", "h", false, "show usage information")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent <command> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Commands:")
		for _, cmd := range cliCommands {
			fmt.Fprintf(os.Stderr, "  %-10s %s\n", cmd.name, cmd.description)
		}
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Use 'mct-agent <command> --help' for more information about a command.")
	}
	return fs
}

func run() int {
	// Handle -version (single-dash, Go convention) before pflag parsing.
	// pflag does not treat -version as --version; it would try to
	// interpret it as a shorthand chain.
	if len(os.Args) >= 2 && os.Args[1] == "-version" {
		printVersion()
		return 0
	}

	// If the first argument looks like a subcommand (no leading dash),
	// dispatch directly without top-level flag parsing so that flags like
	// --help are handled by the subcommand's own FlagSet.
	if len(os.Args) >= 2 && !strings.HasPrefix(os.Args[1], "-") {
		subcmd := os.Args[1]
		for _, cmd := range cliCommands {
			if cmd.name == subcmd {
				return cmd.handler(os.Args[2:])
			}
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", subcmd)
		printUsage()
		return 2
	}

	// Parse top-level flags (--version, --help, -h).
	fs := newTopLevelFlagSet()
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	if v, _ := fs.GetBool("version"); v {
		printVersion()
		return 0
	}
	if h, _ := fs.GetBool("help"); h {
		fs.Usage()
		return 0
	}

	// No subcommand and no action flag — show usage.
	fs.Usage()
	return 2
}

type runFlagSetResult struct {
	fs          *pflag.FlagSet
	promptFile  *string
	paramFlags  *multiString
	paramJSON   *multiString
	apiKeyFlags *multiString
}

func newRunFlagSet(cfg *session.Config) runFlagSetResult {
	fs := pflag.NewFlagSet("mct-agent run", pflag.ContinueOnError)
	var paramFlags multiString
	var paramJSON multiString
	var apiKeyFlags multiString
	configureSessionFlags(fs, cfg, &paramFlags, &paramJSON, &apiKeyFlags)
	promptFile := fs.StringP("file", "f", "", "Read goal from file (mutually exclusive with --text)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent run -t \"<goal>\" | --file <path> [flags]\n\n")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	return runFlagSetResult{fs: fs, promptFile: promptFile, paramFlags: &paramFlags, paramJSON: &paramJSON, apiKeyFlags: &apiKeyFlags}
}

func handleRunCommand(args []string) int {
	cfg := session.Config{}
	r := newRunFlagSet(&cfg)
	fs, promptFile, apiKeyFlags := r.fs, r.promptFile, r.apiKeyFlags
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if cfg.MaxInputTokens < 0 {
		fmt.Fprintln(os.Stderr, "Error: --max-input-tokens must be zero or positive")
		return 2
	}

	// Validate goal input: exactly one of --text or --file is required.
	hasText := strings.TrimSpace(cfg.PromptText) != ""
	hasFile := strings.TrimSpace(*promptFile) != ""
	if hasText && hasFile {
		fmt.Fprintln(os.Stderr, "Error: --text and --file are mutually exclusive")
		return 2
	}
	if !hasText && !hasFile {
		fmt.Fprintln(os.Stderr, "Error: one of --text or --file is required")
		return 2
	}

	var goal string
	if hasFile {
		data, err := os.ReadFile(strings.TrimSpace(*promptFile))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			return 2
		}
		goal = strings.TrimSpace(string(data))
	} else {
		goal = strings.TrimSpace(cfg.PromptText)
	}

	apiOverrides, err := llm.ParseAPIKeyOverrides(*apiKeyFlags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	parsedArgs := fs.Args()
	if len(parsedArgs) > 0 {
		fmt.Fprintln(os.Stderr, "Error: unexpected positional arguments for 'run' command. Use -t or --file to specify the prompt.")
		return 2
	}
	if goal == "" {
		fmt.Fprintln(os.Stderr, "Error: goal is empty. Provide non-empty content via -t or --file.")
		return 2
	}

	globalCfg, configPath, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}

	warning, err := ensureInternalReadmeCurrent()
	if err != nil {
		var exitErr exitError
		if errors.As(err, &exitErr) {
			fmt.Fprintln(os.Stderr, exitErr.msg)
			return exitErr.code
		}
		fmt.Fprintln(os.Stderr, "Error: unable to validate internal README:", err)
		return 1
	}
	if warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}

	globalTimerMgr := ui.NewProcessTimerManager()

	opts := session.Options{
		Config:     cfg,
		Goal:       goal,
		ParamPairs: append([]string(nil), *r.paramFlags...),
		ParamJSON:  append([]string(nil), *r.paramJSON...),
		Build: session.BuildInfo{
			Version: Version,
			Commit:  Commit,
			BuiltAt: BuiltAt,
			Dirty:   Dirty,
		},
		GlobalConfig:        globalCfg,
		GlobalConfigPath:    configPath,
		APIKeyOverrides:     apiOverrides,
		ProcessTimerManager: globalTimerMgr,
	}
	opts.Config.APIKeyOverrides = llm.CopyAPIKeyOverridesForRuntime(apiOverrides)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	opts.Context = ctx

	res := sessionRunFn(ctx, opts)
	if res.Err != nil {
		fmt.Fprintf(os.Stderr, "Error during run: %v\n", res.Err)
		if res.ExitCode == 0 {
			return 1
		}
	}
	return res.ExitCode
}

type exitError struct {
	msg  string
	code int
}

func (e exitError) Error() string {
	return e.msg
}

func ensureInternalReadmeCurrent() (string, error) {
	headCommit, err := readmeHeadCommitFn()
	if err != nil {
		if mapped, ok := mapHeadCommitError(err); ok {
			return "", mapped
		}
		return "", exitError{
			msg:  fmt.Sprintf("Error: Unable to resolve project HEAD commit: %v", err),
			code: 1,
		}
	}
	headCommit = strings.TrimSpace(headCommit)
	if headCommit == "" {
		return "", exitError{
			msg:  "Error: Unable to resolve project HEAD commit: empty value",
			code: 1,
		}
	}

	shortHead := shortSHA(headCommit, 7)

	readmeCommit, err := readmeCommitForProjectFn(headCommit)
	if err != nil {
		if isReadmeMissing(err) {
			return "", exitError{
				msg:  formatSyncRequiredMessage(shortHead),
				code: 1,
			}
		}
		return "", exitError{
			msg:  fmt.Sprintf("Error: Unable to determine internal README state: %v", err),
			code: 1,
		}
	}
	readmeCommit = strings.TrimSpace(readmeCommit)
	if readmeCommit == "" {
		return "", exitError{
			msg:  formatSyncRequiredMessage(shortHead),
			code: 1,
		}
	}

	return "", nil
}

func mapHeadCommitError(err error) (exitError, bool) {
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "not a git repository"):
		return exitError{
			msg:  "Error: Not in a Git repository. Run 'git init' first.",
			code: 1,
		}, true
	case strings.Contains(lower, "no commits"):
		return exitError{
			msg:  "Error: Git repository has no commits yet. Make an initial commit before running 'mct-agent run'.",
			code: 1,
		}, true
	case strings.Contains(lower, "ambiguous argument 'head'"):
		return exitError{
			msg:  "Error: Git repository has no commits yet. Make an initial commit before running 'mct-agent run'.",
			code: 1,
		}, true
	}
	return exitError{}, false
}

func isReadmeMissing(err error) bool {
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "unknown revision") ||
		strings.Contains(lower, "ambiguous argument") ||
		strings.Contains(lower, "cannot change to") ||
		strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "not a git repository") ||
		strings.Contains(lower, "did not match any file")
}

func formatSyncRequiredMessage(shortHead string) string {
	trimmed := strings.TrimSpace(shortHead)
	if trimmed == "" {
		trimmed = "unknown"
	}
	return fmt.Sprintf("\nError: mct is not synced at current git state %s.\n\nRun \u001b[1mmct-agent sync\u001b[0m before proceeding.", trimmed)
}

func shortSHA(hash string, length int) string {
	trimmed := strings.TrimSpace(hash)
	if trimmed == "" || length <= 0 {
		return ""
	}
	if len(trimmed) > length {
		return trimmed[:length]
	}
	return trimmed
}

func handleSyncCommand(args []string) int {
	cfg := session.Config{}
	fs := pflag.NewFlagSet("mct-agent sync", pflag.ContinueOnError)
	var paramFlags multiString
	var paramJSON multiString
	var apiKeyFlags multiString
	commitRef := fs.String("commit", "", "project commit hash to sync")
	configureSessionFlags(fs, &cfg, &paramFlags, &paramJSON, &apiKeyFlags)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent sync [--commit <hash>] [flags]\n\n")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if cfg.MaxInputTokens < 0 {
		fmt.Fprintln(os.Stderr, "Error: --max-input-tokens must be zero or positive")
		return 2
	}
	apiOverrides, parseErr := llm.ParseAPIKeyOverrides(apiKeyFlags)
	if parseErr != nil {
		fmt.Fprintln(os.Stderr, parseErr)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "Error: unexpected positional arguments for sync command")
		fs.Usage()
		return 2
	}

	commit := strings.TrimSpace(*commitRef)
	var err error
	if commit == "" {
		commit, err = readmesync.HeadCommit()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error resolving project commit:", err)
			return 1
		}
	} else {
		commit, err = readmesync.ResolveCommit(commit)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	paramPairs := append([]string(nil), paramFlags...)
	paramJSONVals := append([]string(nil), paramJSON...)
	globalCfg, _, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	cfg.APIKeyOverrides = llm.CopyAPIKeyOverridesForRuntime(apiOverrides)

	runtimes, err := session.ResolvePromptRuntimes(cfg, globalCfg, paramPairs, paramJSONVals, apiOverrides)
	if err != nil {
		if missing, ok := session.MissingConfigItems(err); ok && len(missing) > 0 {
			fmt.Fprintln(os.Stderr, "Missing model config: set:")
			for _, item := range missing {
				fmt.Fprintln(os.Stderr, " - ", item)
			}
			return 2
		}
		fmt.Fprintln(os.Stderr, "Model resolution error:", err)
		return 1
	}

	var mctPrompts *llm.MCTPromptsConfig
	if globalCfg.Prompts != nil {
		mctPrompts = globalCfg.Prompts.MCT
	}
	if err := readmesync.Run(context.Background(), readmesync.Options{
		Commit:               commit,
		Verbose:              cfg.Verbose,
		MaxInputTokens:       cfg.MaxInputTokens,
		Runtime:              runtimes.Orchestrator,
		AnswerRuntime:        runtimes.Answer,
		FileDiscoveryRuntime: runtimes.FileDiscovery,
		Prompts:              mctPrompts,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "Readme sync failed:", err)
		return 1
	}

	fmt.Printf("Readme synced for commit %s\n", shortCommit(commit))
	return 0
}

func configureSessionFlags(fs *pflag.FlagSet, cfg *session.Config, paramFlags, paramJSON, apiKeyFlags *multiString) {
	fs.IntVar(&cfg.MaxSteps, "max-steps", 4, "maximum number of turns before finalizing")
	fs.StringVar(&cfg.OrchModel, "model", "", "Model alias defined in .machtiani/config.toml (alias for --orch-model)")
	fs.StringVar(&cfg.OrchModel, "orch-model", "", "Model alias for orchestration/planner steps (default: config or env)")
	fs.StringVar(&cfg.AnswerModel, "answer-model", "", "Model alias for final answer generation (defaults to --orch-model)")
	fs.StringVar(&cfg.FileDiscoveryModel, "file-discovery-model", "", "Model alias for file discovery runs (default: orchestration model)")
	fs.StringVar(&cfg.AgentModel, "agent-model", "", "Legacy planner model alias (deprecated; use --orch-model)")
	fs.IntVar(&cfg.TimeoutPerTurn, "timeout-per-turn", 120, "per-turn timeout in seconds (set 0 for no timeout)")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print intended mct calls; don't execute")
	fs.BoolVarP(&cfg.Verbose, "verbose", "v", false, "verbose agent logging")
	fs.BoolVar(&cfg.PersistTmpData, "persist-tmp-data", false, "keep temporary data (worktrees, trajectories) after execution; startup orphan cleanup always runs")
	fs.IntVar(&cfg.MaxCommandOutputBytes, "max-command-output-bytes", 65536, "maximum bytes of shell command output captured per step (default 64KB)")
	fs.BoolVar(&cfg.ShellAgent, "shell-agent", false, "Enable shell-agent mode: invoke shell-agent subprocess binary for task execution")
	fs.StringVar(&cfg.ShellAgentModel, "shell-agent-model", "", "Model alias override for shell-agent subprocesses (default: config)")
	fs.StringVar(&cfg.FinalFile, "final-file", "", "path to write final answer-only artifact (default: .machtiani/sessions/<sessionID>/chat/agent-final-answer.md)")
	fs.StringVar(&cfg.TranscriptFile, "transcript-file", "", "path to write transcript file (default: .machtiani/sessions/<sessionID>/chat/agent-transcript.adoc)")
	fs.StringVar(&cfg.FileDiscoveryTrajectory, "file-discovery-trajectory", "", "path to write file-discovery trajectory JSONL (default: auto-named under session artifacts)")
	fs.StringVar(&cfg.FileDiscoveryOutputDir, "file-discovery-output-dir", "", "directory for file-discovery artifacts (default: .machtiani/sessions/<sessionID>/artifacts)")
	fs.IntVar(&cfg.MaxInputTokens, "max-input-tokens", 0, "maximum number of tokens allowed in constructed prompts (0 disables truncation)")
	fs.StringVar(&cfg.TrajectoryFile, "trajectory-file", "", "override path for unified trajectory JSONL (default: session-scoped path)")
	fs.BoolVar(&cfg.NoTrajectory, "no-trajectory", false, "disable unified trajectory JSONL emission")
	fs.BoolVar(&cfg.TrajectoryVerboseLLM, "trajectory-verbose-llm", false, "include expanded LLM details in the trajectory stream")
	fs.BoolVar(&cfg.TrajectoryStreamTokens, "trajectory-stream-tokens", false, "record LLM token streaming events in the trajectory (disabled by default)")
	fs.IntVar(&cfg.TrajectoryExcerpt, "trajectory-excerpt", 512, "excerpt length (in characters) for prompts/responses captured in the trajectory")
	fs.BoolVar(&cfg.TrajectoryOmitRepoRoot, "trajectory-omit-repo-root", false, "omit repo_root from trajectory events")
	fs.StringVar(&cfg.OpenAIAPIKey, "openai-api-key", "", "OpenAI-compatible API key (overrides env, deprecated)")
	fs.StringVar(&cfg.OpenAIBaseURL, "openai-base-url", "", "OpenAI-compatible base URL (overrides env, deprecated)")
	fs.StringVar(&cfg.OpenAIModel, "openai-model", "", "Direct upstream model name (deprecated; prefer --model)")
	fs.StringVar(&cfg.SessionID, "session-id", "", "Existing session identifier to resume")
	fs.BoolVar(&cfg.EnableTagFormat, "enable-tag-format", false, "Enable tag-format response directives and validation (experimental)")
	fs.StringVar(&cfg.Mode, "mode", "", "Operating mode")
	fs.StringVar(&cfg.ModeInstructionDir, "mode-instruction-dir", "", "Directory containing mode custom instructions (overrides config)")
	fs.StringVarP(&cfg.PromptText, "text", "t", "", "prompt text (alternative to positional argument)")
	if apiKeyFlags != nil {
		fs.Var(apiKeyFlags, "api-key", "Provider-specific API key override in provider:key format (repeatable)")
	}
	fs.Var(paramFlags, "param", "Additional request parameter key=value (repeatable)")
	fs.Var(paramJSON, "param-json", "Merge JSON object of additional parameters (repeatable)")

	// Mark deprecated flags as hidden
	fs.MarkHidden("orch-model")
	fs.MarkHidden("agent-model")
	fs.MarkHidden("openai-api-key")
	fs.MarkHidden("openai-base-url")
	fs.MarkHidden("openai-model")
}
func handleConfigCommand(args []string) int {
	if len(args) < 1 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent config <subcommand>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  check    Validate configuration")
		return 2
	}
	switch args[0] {
	case "check":
		return handleConfigCheckCommand(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown config subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "Usage: mct-agent config check")
		return 2
	}
}

func handleConfigCheckCommand(args []string) int {
	cfg, path, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		return 1
	}

	var issues []string

	defaultModel := strings.TrimSpace(cfg.DefaultModel)
	if defaultModel == "" {
		issues = append(issues, "missing default_model")
	} else {
		if _, ok := cfg.Models[defaultModel]; !ok {
			issues = append(issues, fmt.Sprintf("invalid model alias: %q not found in [models]", defaultModel))
		}
	}

	if len(issues) > 0 {
		fmt.Fprintf(os.Stderr, "Config issues found in %s:\n", path)
		for _, issue := range issues {
			fmt.Fprintf(os.Stderr, "  - %s\n", issue)
		}
		return 1
	}

	fmt.Printf("Config OK: %s\n", path)
	if defaultModel != "" {
		fmt.Printf("Default model: %s\n", defaultModel)
	}
	return 0
}

func handleSessionCommand(args []string) int {
	if len(args) < 1 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent session <subcommand> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  list    List all sessions")
		fmt.Fprintln(os.Stderr, "  show    Show details for a specific session")
		fmt.Fprintln(os.Stderr, "  fork    Fork a session")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Use 'mct-agent session <subcommand> --help' for more information.")
		return 2
	}
	switch args[0] {
	case "list":
		return handleSessionListCommand(args[1:])
	case "show":
		return handleSessionShowCommand(args[1:])
	case "fork":
		return handleSessionForkCommand(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown session subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Usage: mct-agent session <subcommand> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  list    List all sessions")
		fmt.Fprintln(os.Stderr, "  show    Show details for a specific session")
		fmt.Fprintln(os.Stderr, "  fork    Fork a session")
		return 2
	}
}

func handleSessionListCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent session list", pflag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output sessions as JSON array")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent session list [flags]\n\n")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	sessions, err := session.ListSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing sessions: %v\n", err)
		return 1
	}

	if *jsonOutput {
		// Output as JSON array
		data, err := json.MarshalIndent(sessions, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling sessions to JSON: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	// Output as table
	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return 0
	}

	fmt.Printf("%-20s  %-30s  %-10s  %-5s  %s\n", "SESSION_ID", "GOAL", "STATUS", "TURNS", "UPDATED")
	fmt.Println(strings.Repeat("-", 80))
	for _, s := range sessions {
		goal := s.Goal
		if len(goal) > 30 {
			goal = goal[:27] + "..."
		}
		updated := s.UpdatedAt.Format("2006-01-02 15:04")
		fmt.Printf("%-20s  %-30s  %-10s  %-5d  %s\n", s.SessionID, goal, s.Status, s.TurnsCompleted, updated)
	}
	return 0
}

func handleSessionShowCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent session show", pflag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output session as JSON object")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent session show <session-id> [flags]\n\n")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: session-id is required")
		fs.Usage()
		return 2
	}

	sessionID := fs.Arg(0)
	state, err := session.LoadSessionState(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading session %s: %v\n", sessionID, err)
		return 1
	}

	if *jsonOutput {
		// Output as JSON object
		data, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling session to JSON: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	// Output as key-value pairs
	fmt.Printf("Session ID:      %s\n", state.SessionID)
	fmt.Printf("Goal:            %s\n", state.Goal)
	if state.OriginalPrompt != "" {
		fmt.Printf("Original Prompt: %s\n", state.OriginalPrompt)
	}
	if state.TaskDescription != "" {
		fmt.Printf("Task:            %s\n", state.TaskDescription)
	}
	fmt.Printf("Status:          %s\n", state.Status)
	fmt.Printf("Turns Completed: %d\n", state.TurnsCompleted)
	fmt.Printf("Updated:         %s\n", state.UpdatedAt.Format(time.RFC3339))
	if len(state.Modes) > 0 {
		fmt.Printf("Modes:           %s\n", strings.Join(state.Modes, ", "))
	}
	if state.ModeInstructionDir != "" {
		fmt.Printf("Mode Inst Dir:   %s\n", state.ModeInstructionDir)
	}
	if state.PlannerProgress != nil {
		fmt.Printf("Planner Progress:\n")
		if len(state.PlannerProgress.SuccessFiles) > 0 {
			fmt.Printf("  Success Files:   %d files\n", len(state.PlannerProgress.SuccessFiles))
		}
	}
	if state.SuspendedUserInput != nil {
		fmt.Printf("Suspended Input:\n")
		fmt.Printf("  Kind:     %s\n", state.SuspendedUserInput.Kind)
		fmt.Printf("  Question: %s\n", state.SuspendedUserInput.Question)
	}

	return 0
}

func handleSessionForkCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent session fork", pflag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mct-agent session fork <session-id>\n\n")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: session-id is required")
		fs.Usage()
		return 2
	}

	sessionID := fs.Arg(0)
	newSessionID, err := session.ForkSession(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error forking session %s: %v\n", sessionID, err)
		return 1
	}

	fmt.Println(newSessionID)
	return 0
}

func printUsage() {
	fs := newTopLevelFlagSet()
	fs.Usage()
}

func shortCommit(hash string) string {
	trimmed := strings.TrimSpace(hash)
	if trimmed == "" {
		return "unknown"
	}
	if len(trimmed) > 12 {
		trimmed = trimmed[:12]
	}
	return trimmed
}
