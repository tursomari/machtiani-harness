package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/readmesync"
	"github.com/tursomari/machtiani/agent/internal/session"
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

func printVersion() {
	fmt.Printf("mct-agent %s\ncommit: %s\nbuilt: %s\ndirty: %s\n", Version, Commit, BuiltAt, Dirty)
}

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "--version", "-version":
			printVersion()
			return 0
		case "run":
			return handleRunCommand(os.Args[2:])
		case "sync":
			return handleSyncCommand(os.Args[2:])
		}
	}

	printUsage()
	return 2
}

func handleRunCommand(args []string) int {
	cfg := session.Config{}
	fs := flag.NewFlagSet("mct-agent run", flag.ExitOnError)
	var paramFlags multiString
	var paramJSON multiString
	var apiKeyFlags multiString
	configureSessionFlags(fs, &cfg, &paramFlags, &paramJSON, &apiKeyFlags)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent run \"<issue or question>\" [flags]")
	}
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	apiOverrides, err := llm.ParseAPIKeyOverrides(apiKeyFlags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	parsedArgs := fs.Args()
	if len(parsedArgs) == 0 {
		fmt.Fprintln(os.Stderr, "Error: missing issue/question. Example: mct-agent run \"Explain X...\"")
		return 2
	}
	goal := strings.TrimSpace(parsedArgs[0])
	if goal == "" {
		fmt.Fprintln(os.Stderr, "Error: empty issue/question provided")
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

	opts := session.Options{
		Config:     cfg,
		Goal:       goal,
		ParamPairs: append([]string(nil), paramFlags...),
		ParamJSON:  append([]string(nil), paramJSON...),
		Build: session.BuildInfo{
			Version: Version,
			Commit:  Commit,
			BuiltAt: BuiltAt,
			Dirty:   Dirty,
		},
		GlobalConfig:     globalCfg,
		GlobalConfigPath: configPath,
		APIKeyOverrides:  apiOverrides,
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
	fs := flag.NewFlagSet("mct-agent sync", flag.ExitOnError)
	var paramFlags multiString
	var paramJSON multiString
	var apiKeyFlags multiString
	commitRef := fs.String("commit", "", "project commit hash to sync")
	configureSessionFlags(fs, &cfg, &paramFlags, &paramJSON, &apiKeyFlags)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent sync [--commit <hash>] [flags]")
	}
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
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

	if err := readmesync.Run(context.Background(), readmesync.Options{
		Commit:               commit,
		Verbose:              cfg.Verbose,
		MaxInputTokens:       cfg.MaxInputTokens,
		Runtime:              runtimes.Orchestrator,
		AnswerRuntime:        runtimes.Answer,
		FileDiscoveryRuntime: runtimes.FileDiscovery,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "Readme sync failed:", err)
		return 1
	}

	fmt.Printf("Readme synced for commit %s\n", shortCommit(commit))
	return 0
}

func configureSessionFlags(fs *flag.FlagSet, cfg *session.Config, paramFlags, paramJSON, apiKeyFlags *multiString) {
	fs.IntVar(&cfg.MaxSteps, "max-steps", 4, "maximum number of turns before finalizing")
	fs.StringVar(&cfg.OrchModel, "model", "", "Model alias defined in .machtiani/config.toml (alias for --orch-model)")
	fs.StringVar(&cfg.OrchModel, "orch-model", "", "Model alias for orchestration/planner steps (default: config or env)")
	fs.StringVar(&cfg.AnswerModel, "answer-model", "", "Model alias for final answer generation (defaults to --orch-model)")
	fs.StringVar(&cfg.PatcherModel, "patcher-model", "", "Reserved placeholder; patch instructions currently use the orchestrator model")
	fs.StringVar(&cfg.FileDiscoveryModel, "file-discovery-model", "", "Model alias for file discovery runs (default: orchestration model)")
	fs.StringVar(&cfg.AgentModel, "agent-model", "", "Legacy planner model alias (deprecated; use --orch-model)")
	fs.IntVar(&cfg.TimeoutPerTurn, "timeout-per-turn", 120, "per-turn timeout in seconds (set 0 for no timeout)")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print intended mct calls; don’t execute")
	fs.BoolVar(&cfg.Verbose, "verbose", false, "verbose agent logging")
	fs.BoolVar(&cfg.PersistTmpData, "persist-tmp-data", false, "keep temporary data (worktrees, trajectories) after execution; startup orphan cleanup always runs")
	fs.BoolVar(&cfg.ShellAgent, "shell-agent", false, "Enable shell-agent mode: invoke shell-agent subprocess binary for task execution")
	fs.StringVar(&cfg.ShellAgentModel, "shell-agent-model", "", "Model alias override for shell-agent subprocesses (default: config)")
	fs.StringVar(&cfg.FinalFile, "final-file", "", "path to write final answer-only artifact (default: .machtiani/sessions/<sessionID>/chat/agent-final-answer.md)")
	fs.StringVar(&cfg.TranscriptFile, "transcript-file", "", "path to write transcript file (default: .machtiani/sessions/<sessionID>/chat/agent-transcript.md)")
	fs.StringVar(&cfg.FileDiscoveryTrajectory, "file-discovery-trajectory", "", "path to write file-discovery trajectory JSONL (default: auto-named under session artifacts)")
	fs.StringVar(&cfg.FileDiscoveryOutputDir, "file-discovery-output-dir", "", "directory for file-discovery artifacts (default: .machtiani/sessions/<sessionID>/artifacts)")
	fs.IntVar(&cfg.MaxInputTokens, "max-input-tokens", 0, "maximum number of tokens allowed in constructed prompts (0 disables truncation)")
	fs.StringVar(&cfg.TrajectoryFile, "trajectory-file", "", "override path for unified trajectory JSONL (default: session-scoped path)")
	fs.BoolVar(&cfg.NoTrajectory, "no-trajectory", false, "disable unified trajectory JSONL emission")
	fs.BoolVar(&cfg.TrajectoryVerboseLLM, "trajectory-verbose-llm", false, "include expanded LLM details in the trajectory stream")
	fs.BoolVar(&cfg.TrajectoryStreamTokens, "trajectory-stream-tokens", false, "record LLM token streaming events in the trajectory (disabled by default)")
	fs.IntVar(&cfg.TrajectoryExcerpt, "trajectory-excerpt", 512, "excerpt length (in characters) for prompts/responses captured in the trajectory")
	fs.BoolVar(&cfg.TrajectoryOmitRepoRoot, "trajectory-omit-repo-root", false, "omit repo_root from trajectory events")
	fs.BoolVar(&cfg.PatchNoApply, "patch-no-apply", false, "skip applying generated patches to the worktree (default: apply)")
	fs.BoolVar(&cfg.Patch, "patch", false, "enable patch planning (disabled by default)")
	fs.StringVar(&cfg.OpenAIAPIKey, "openai-api-key", "", "OpenAI-compatible API key (overrides env, deprecated)")
	fs.StringVar(&cfg.OpenAIBaseURL, "openai-base-url", "", "OpenAI-compatible base URL (overrides env, deprecated)")
	fs.StringVar(&cfg.OpenAIModel, "openai-model", "", "Direct upstream model name (deprecated; prefer --model)")
	fs.StringVar(&cfg.SessionID, "session-id", "", "Existing session identifier to resume")
	if apiKeyFlags != nil {
		fs.Var(apiKeyFlags, "api-key", "Provider-specific API key override in provider:key format (repeatable)")
	}
	fs.Var(paramFlags, "param", "Additional request parameter key=value (repeatable)")
	fs.Var(paramJSON, "param-json", "Merge JSON object of additional parameters (repeatable)")
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: mct-agent <command> [options]")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  run   \"<issue or question>\" [flags]")
	fmt.Fprintln(os.Stderr, "  sync  [--commit <hash>] [flags]")
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
