package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/core/readmesync"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/presentation"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
	"github.com/tursomari/machtiani/agent/internal/session"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shellaction"
	"github.com/tursomari/machtiani/agent/internal/ui"
	"golang.org/x/term"
)

var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
	Dirty   = "unknown"

	readmeHeadCommitFn       = readmesync.HeadCommit
	readmeCommitForProjectFn = readmesync.READMECommitForProject
	readmeSyncRunFn          = readmesync.Run
	sessionRunFn             = session.Run
)

const (
	attachPollInterval = 250 * time.Millisecond
	attachQuietGrace   = 750 * time.Millisecond
)

type attachRead func(path string) ([]byte, error)
type attachProbe func(sessionScratchDirectory string) (bool, error)

type attachSessionTarget struct {
	sessionID        string
	sessionsRoot     string
	scratchRoot      string
	sessionDirectory string
	scratchDirectory string
}

func newAttachSessionTarget(sessionID, sessionsRoot, scratchRoot string) attachSessionTarget {
	return attachSessionTarget{
		sessionID:        sessionID,
		sessionsRoot:     sessionsRoot,
		scratchRoot:      scratchRoot,
		sessionDirectory: filepath.Join(sessionsRoot, sessionID),
		scratchDirectory: filepath.Join(scratchRoot, sessionID),
	}
}

func (t attachSessionTarget) conversationPath() string {
	return filepath.Join(t.sessionDirectory, "artifacts", "conversation.json")
}

// resolveAttachSession searches the current session scope before the global
// scope. A match in the current scope wins even when the same ID exists
// globally, while ambiguity in either searched scope is returned immediately.
func resolveAttachSession(query string) (attachSessionTarget, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return attachSessionTarget{}, errors.New("session id required")
	}

	defaultSessionsRoot, err := artifacts.SessionsRoot()
	if err != nil {
		return attachSessionTarget{}, fmt.Errorf("resolve sessions root: %w", err)
	}
	if sessionID, found, err := resolveAttachSessionIDAt(query, defaultSessionsRoot); err != nil {
		return attachSessionTarget{}, err
	} else if found {
		defaultScratchRoot, err := artifacts.ScratchRoot()
		if err != nil {
			return attachSessionTarget{}, fmt.Errorf("resolve scratch root: %w", err)
		}
		return newAttachSessionTarget(sessionID, defaultSessionsRoot, defaultScratchRoot), nil
	}

	globalSessionsRoot, globalScratchRoot, err := attachGlobalRoots()
	if err != nil {
		return attachSessionTarget{}, err
	}
	if filepath.Clean(globalSessionsRoot) != filepath.Clean(defaultSessionsRoot) {
		if sessionID, found, err := resolveAttachSessionIDAt(query, globalSessionsRoot); err != nil {
			return attachSessionTarget{}, err
		} else if found {
			return newAttachSessionTarget(sessionID, globalSessionsRoot, globalScratchRoot), nil
		}
	}
	if target, found, err := resolveAttachSessionInGlobalStores(query); err != nil {
		return attachSessionTarget{}, err
	} else if found {
		return target, nil
	}

	return attachSessionTarget{}, fmt.Errorf("unknown session: %q", query)
}

func resolveAttachSessionIDAt(query, sessionsRoot string) (string, bool, error) {
	exactMatches, prefixMatches, err := attachSessionMatchesAt(query, sessionsRoot)
	if err != nil {
		return "", false, err
	}

	if len(exactMatches) == 1 {
		return exactMatches[0], true, nil
	}
	switch len(prefixMatches) {
	case 0:
		return "", false, nil
	case 1:
		return prefixMatches[0], true, nil
	default:
		sort.Strings(prefixMatches)
		return "", false, fmt.Errorf("ambiguous session id %q: %d candidates: %s", query, len(prefixMatches), strings.Join(prefixMatches, ", "))
	}
}

func attachSessionMatchesAt(query, sessionsRoot string) (exactMatches, prefixMatches []string, err error) {
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("read sessions directory: %w", err)
	}

	canonicalQuery := canonicalAttachSessionID(query)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		canonicalName := canonicalAttachSessionID(name)
		if canonicalName == canonicalQuery {
			exactMatches = append(exactMatches, name)
		}
		if strings.HasPrefix(canonicalName, canonicalQuery) {
			prefixMatches = append(prefixMatches, name)
		}
	}

	return exactMatches, prefixMatches, nil
}

type attachStoreSessionMatch struct {
	storeName string
	sessionID string
	target    attachSessionTarget
}

func resolveAttachSessionInGlobalStores(query string) (attachSessionTarget, bool, error) {
	homeRoot, err := projectstore.HomeRoot()
	if err != nil {
		return attachSessionTarget{}, false, err
	}
	storeEntries, err := os.ReadDir(homeRoot)
	if os.IsNotExist(err) {
		return attachSessionTarget{}, false, nil
	}
	if err != nil {
		return attachSessionTarget{}, false, fmt.Errorf("read Machtiani home: %w", err)
	}

	var exactMatches []attachStoreSessionMatch
	var prefixMatches []attachStoreSessionMatch
	for _, storeEntry := range storeEntries {
		if !storeEntry.IsDir() {
			continue
		}
		storeRoot := filepath.Join(homeRoot, storeEntry.Name())
		sessionsRoot := filepath.Join(storeRoot, projectstore.SessionsDirName)
		info, err := os.Stat(sessionsRoot)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return attachSessionTarget{}, false, fmt.Errorf("inspect sessions directory %s: %w", sessionsRoot, err)
		}
		if !info.IsDir() {
			continue
		}
		scope, err := projectstore.ReadConfigScope(storeRoot)
		if err != nil {
			return attachSessionTarget{}, false, err
		}
		if scope != projectstore.ScopeGlobal {
			continue
		}

		exactIDs, prefixIDs, err := attachSessionMatchesAt(query, sessionsRoot)
		if err != nil {
			return attachSessionTarget{}, false, err
		}
		scratchRoot := filepath.Join(storeRoot, projectstore.ScratchDirName)
		for _, sessionID := range exactIDs {
			exactMatches = append(exactMatches, attachStoreSessionMatch{
				storeName: storeEntry.Name(),
				sessionID: sessionID,
				target:    newAttachSessionTarget(sessionID, sessionsRoot, scratchRoot),
			})
		}
		for _, sessionID := range prefixIDs {
			prefixMatches = append(prefixMatches, attachStoreSessionMatch{
				storeName: storeEntry.Name(),
				sessionID: sessionID,
				target:    newAttachSessionTarget(sessionID, sessionsRoot, scratchRoot),
			})
		}
	}

	sortAttachStoreSessionMatches(exactMatches)
	sortAttachStoreSessionMatches(prefixMatches)
	if len(exactMatches) == 1 {
		return exactMatches[0].target, true, nil
	}
	if len(exactMatches) > 1 {
		return attachSessionTarget{}, false, attachStoreSessionAmbiguityError(query, exactMatches)
	}
	switch len(prefixMatches) {
	case 0:
		return attachSessionTarget{}, false, nil
	case 1:
		return prefixMatches[0].target, true, nil
	default:
		return attachSessionTarget{}, false, attachStoreSessionAmbiguityError(query, prefixMatches)
	}
}

func sortAttachStoreSessionMatches(matches []attachStoreSessionMatch) {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].storeName == matches[j].storeName {
			return matches[i].sessionID < matches[j].sessionID
		}
		return matches[i].storeName < matches[j].storeName
	})
}

func attachStoreSessionAmbiguityError(query string, matches []attachStoreSessionMatch) error {
	candidates := make([]string, 0, len(matches))
	for _, match := range matches {
		candidates = append(candidates, match.target.sessionDirectory)
	}
	return fmt.Errorf("ambiguous session id %q: %d candidates: %s", query, len(candidates), strings.Join(candidates, ", "))
}

func canonicalAttachSessionID(value string) string {
	return strings.ReplaceAll(strings.ToUpper(value), "-", "")
}

func attachGlobalRoots() (sessionsRoot, scratchRoot string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve user home: %w", err)
	}
	root := filepath.Join(home, ".machtiani")
	return filepath.Join(root, "sessions"), filepath.Join(root, "tmp"), nil
}

type attachDependencies struct {
	readFile       attachRead
	readActions    attachRead
	readTrajectory attachRead
	probe          attachProbe
	pollInterval   time.Duration
	quietGrace     time.Duration
	noShellSteps   bool
	focused        bool
	theme          presentation.Theme
	isTerminal     func(io.Writer) bool
	footerWidth    func(io.Writer) int
}

// attachTerminalTeeWriter mirrors the session runner's capture writer. Keeping
// Fd available is important: attach uses it to decide whether to draw the TTY
// banner and live following/footer overlay.
type attachTerminalTeeWriter struct {
	terminal *os.File
	capture  io.Writer
}

func (w attachTerminalTeeWriter) Write(p []byte) (int, error) {
	n, err := w.terminal.Write(p)
	if err != nil {
		return n, err
	}
	if w.capture != nil {
		if _, captureErr := w.capture.Write(p); captureErr != nil {
			return n, captureErr
		}
	}
	return n, nil
}

func (w attachTerminalTeeWriter) Fd() uintptr {
	return w.terminal.Fd()
}

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
	fmt.Printf("machtiani %s\ncommit: %s\nbuilt: %s\ndirty: %s\n", Version, Commit, BuiltAt, Dirty)
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
	{name: "config", description: "Create and manage configuration", handler: handleConfigCommand},
	{name: "shell-agent", description: "Run a shell-agent task directly (no planner)", handler: handleShellAgentCommand},
}

func newTopLevelFlagSet() *pflag.FlagSet {
	fs := pflag.NewFlagSet("machtiani", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Bool("version", false, "print build metadata and exit")
	fs.BoolP("help", "h", false, "show usage information")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: machtiani <command> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Commands:")
		for _, cmd := range cliCommands {
			fmt.Fprintf(os.Stderr, "  %-10s %s\n", cmd.name, cmd.description)
		}
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Use 'machtiani <command> --help' for more information about a command.")
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

	if handled, code := maybeAutomaticUpdate(os.Args[1:]); handled {
		return code
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
	fs := pflag.NewFlagSet("machtiani run", pflag.ContinueOnError)
	var paramFlags multiString
	var paramJSON multiString
	var apiKeyFlags multiString
	configureSessionFlags(fs, cfg, &paramFlags, &paramJSON, &apiKeyFlags, true)
	promptFile := fs.StringP("file", "f", "", "Read goal from file (mutually exclusive with --prompt)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: machtiani run -p \"<your prompt>\" | --file <path> | --attach --resume <session-id> [flags]\n\n")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	return runFlagSetResult{fs: fs, promptFile: promptFile, paramFlags: &paramFlags, paramJSON: &paramJSON, apiKeyFlags: &apiKeyFlags}
}

func handleRunCommand(args []string) int {
	if containsRemovedMaxInputFlag(args) {
		fmt.Fprintln(os.Stderr, "Error: --max-input-tokens was removed; use --context-length to set the total session context window")
		return 2
	}
	cfg := session.Config{}
	if globalCfg, _, err := llm.LoadGlobalConfig(); err == nil {
		if globalCfg.Planner != nil {
			cfg.MaxTurns = globalCfg.Planner.MaxTurns
			cfg.TurnTimeout = globalCfg.Planner.TurnTimeout
		}
		if globalCfg.Environment != nil {
			cfg.MaxCommandOutputBytes = globalCfg.Environment.MaxCommandOutputBytes
		}
		if globalCfg.Trajectory != nil {
			cfg.TrajectoryFile = globalCfg.Trajectory.File
			cfg.NoTrajectory = !globalCfg.Trajectory.Enabled
			cfg.TrajectoryVerboseLLM = globalCfg.Trajectory.VerboseLLM
			cfg.TrajectoryStreamTokens = globalCfg.Trajectory.StreamTokens
			cfg.TrajectoryExcerpt = globalCfg.Trajectory.Excerpt
			cfg.TrajectoryOmitRepoRoot = globalCfg.Trajectory.OmitRepoRoot
		}
		cfg.Verbose = globalCfg.Verbose
		cfg.PersistTmpData = globalCfg.PersistTmpData
		cfg.DryRun = globalCfg.DryRun
		cfg.ShellAgent = globalCfg.ShellAgentEnabled
		cfg.ShellAgentModel = globalCfg.ShellAgentModel
		cfg.AnswerModel = globalCfg.AnswerModel
		cfg.FileDiscoveryModel = globalCfg.FileDiscoveryModel
		cfg.AnswerTag = globalCfg.AnswerTag
		cfg.CommandTag = globalCfg.Tag
		cfg.FinalFile = globalCfg.FinalFile
		cfg.TranscriptFile = globalCfg.TranscriptFile
		cfg.FileDiscoveryTrajectory = globalCfg.FileDiscoveryTrajectory
		cfg.FileDiscoveryOutputDir = globalCfg.FileDiscoveryOutputDir
		cfg.EnableTagFormat = globalCfg.EnableTagFormat
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 150
	}
	r := newRunFlagSet(&cfg)
	fs, promptFile, apiKeyFlags := r.fs, r.promptFile, r.apiKeyFlags
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if cfg.Attach && fs.Changed("session-id") {
		fmt.Fprintln(os.Stderr, "Error: --attach requires --resume/-r; the deprecated --session-id flag cannot be combined with it")
		return 2
	}
	if flags := changedSessionSelectorFlags(fs); len(flags) > 1 {
		fmt.Fprintf(os.Stderr, "Error: session flags are mutually exclusive; use only one of --session-id or --resume (received %s)\n", strings.Join(flags, ", "))
		return 2
	}
	markExplicitModelOverrides(fs, &cfg)
	if cfg.Attach {
		if strings.TrimSpace(cfg.SessionID) == "" {
			fmt.Fprintln(os.Stderr, "Error: --attach requires --resume/-r with a session id")
			return 2
		}
		conflicts := []struct {
			flag    string
			present bool
		}{
			{flag: "prompt", present: strings.TrimSpace(cfg.PromptText) != "" || fs.Changed("prompt")},
			{flag: "file", present: strings.TrimSpace(*promptFile) != "" || fs.Changed("file")},
			{flag: "exec", present: cfg.Print},
			{flag: "mode", present: strings.TrimSpace(cfg.Mode) != "" || fs.Changed("mode")},
		}
		for _, conflict := range conflicts {
			if conflict.present {
				fmt.Fprintf(os.Stderr, "Error: --attach is display-only; --%s cannot be combined with it\n", conflict.flag)
				return 2
			}
		}
		for _, flag := range []string{
			"model",
			"orch-model",
			"agent-model",
			"answer-model",
			"file-discovery-model",
			"shell-agent-model",
			"openai-api-key",
			"openai-base-url",
			"openai-model",
		} {
			if fs.Changed(flag) {
				fmt.Fprintf(os.Stderr, "Error: --attach is display-only; --%s cannot be combined with it\n", flag)
				return 2
			}
		}
		if len(fs.Args()) > 0 {
			fmt.Fprintln(os.Stderr, "Error: unexpected positional arguments for 'run' command. Use -p or --file to specify the prompt.")
			return 2
		}
		return runAttachWithDisplay(cfg.SessionID, cfg.NoShellSteps, cfg.Focused, cfg.NoCursor, os.Stdout, os.Stderr)
	}
	if cfg.ContextLength != 0 && cfg.ContextLength < llm.MinimumContextLength {
		fmt.Fprintf(os.Stderr, "Error: --context-length must be at least %d\n", llm.MinimumContextLength)
		return 2
	}
	// Validate --answer-tag at the CLI boundary. The value is normalised
	// to "answer" downstream if empty; the validation here catches
	// malformed input from the user.
	if err := shellagent.ValidateAnswerTag(cfg.AnswerTag); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 2
	}

	// Mutual exclusion: --tag and --answer-tag cannot both be set.
	if cfg.CommandTag != "" && cfg.AnswerTag != "" {
		fmt.Fprintln(os.Stderr, "Error: --tag and --answer-tag are mutually exclusive")
		return 2
	}

	// Compose effective answer and command tags from the raw flags.
	effectiveAnswerTag, effectiveCommandTag, err := shellagent.ComposeEffectiveTags(cfg.AnswerTag, cfg.CommandTag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 2
	}
	cfg.AnswerTag = effectiveAnswerTag
	cfg.CommandTag = effectiveCommandTag

	// Default command tag to "command" if still empty.
	if cfg.CommandTag == "" {
		cfg.CommandTag = "command"
	}

	// Validate goal input: --prompt and --file are mutually exclusive.
	// When not resuming a session, exactly one of --prompt or --file is required.
	hasText := strings.TrimSpace(cfg.PromptText) != ""
	hasFile := strings.TrimSpace(*promptFile) != ""
	var hasNewInput bool
	if hasText && hasFile {
		fmt.Fprintln(os.Stderr, "Error: --prompt and --file are mutually exclusive")
		return 2
	}
	if cfg.SessionID == "" {
		if !hasText && !hasFile {
			fmt.Fprintln(os.Stderr, "Error: one of --prompt or --file is required")
			return 2
		}
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

	if cfg.SessionID != "" {
		hasText := strings.TrimSpace(cfg.PromptText) != ""
		hasFile := strings.TrimSpace(*promptFile) != ""
		if hasText || hasFile {
			hasNewInput = true
		}
	}

	apiOverrides, err := llm.ParseAPIKeyOverrides(*apiKeyFlags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	parsedArgs := fs.Args()
	if len(parsedArgs) > 0 {
		fmt.Fprintln(os.Stderr, "Error: unexpected positional arguments for 'run' command. Use -p or --file to specify the prompt.")
		return 2
	}
	if goal == "" && cfg.SessionID == "" {
		fmt.Fprintln(os.Stderr, "Error: goal is empty. Provide non-empty content via -p or --file.")
		return 2
	}

	globalCfg, configPath, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	if err := llm.ValidateConfigError(globalCfg, configPath, llm.ValidationOptions{}); err != nil {
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
		GlobalConfig:            globalCfg,
		GlobalConfigPath:        configPath,
		APIKeyOverrides:         apiOverrides,
		ProcessTimerManager:     globalTimerMgr,
		ShellAgentInterruptStep: cfg.ShellAgentInterruptStep,
		ShellAgentStepLog:       cfg.ShellAgentStepLog,
		HasNewInput:             hasNewInput,
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

func runAttach(sessionID string, stdout, stderr io.Writer) int {
	return runAttachWithDisplay(sessionID, false, false, false, stdout, stderr)
}

func runAttachWithDisplay(sessionID string, noShellSteps, focused, noCursor bool, stdout, stderr io.Writer) int {
	var captureFile *os.File
	if capturePath := strings.TrimSpace(os.Getenv("MACHTIANI_TUI_CAPTURE")); capturePath != "" {
		terminal, ok := stdout.(*os.File)
		if !ok {
			fmt.Fprintf(stderr, "Warning: unable to capture attach output because stdout is not a file\n")
		} else if file, err := os.Create(capturePath); err != nil {
			fmt.Fprintf(stderr, "Warning: unable to open capture file %q for writing: %v\n", capturePath, err)
		} else {
			captureFile = file
			stdout = attachTerminalTeeWriter{terminal: terminal, capture: captureFile}
			defer captureFile.Close()
		}
	}

	theme, err := resolveAttachTheme(stdout, noCursor)
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	return runAttachWithDependencies(sessionID, stdout, stderr, attachDependencies{
		readFile:       os.ReadFile,
		readActions:    os.ReadFile,
		readTrajectory: os.ReadFile,
		probe:          session.IsSessionActiveAt,
		pollInterval:   attachPollInterval,
		quietGrace:     attachQuietGrace,
		noShellSteps:   noShellSteps,
		focused:        focused,
		theme:          theme,
		footerWidth:    attachFooterWidth,
	})
}

// resolveAttachTheme is intentionally the same configuration-to-presentation
// path used by interactive commands: ui.theme/ui.glyphs/ui.motion feed the
// presentation resolver, whose MACHTIANI_THEME, TERM, and NO_COLOR handling
// then applies uniformly to run and attach.
func resolveAttachTheme(stdout io.Writer, noCursor bool) (presentation.Theme, error) {
	themeName := string(presentation.ProfileTerminal)
	glyphMode := string(presentation.GlyphUnicode)
	motionMode := string(presentation.MotionFull)
	if globalCfg, _, err := llm.LoadGlobalConfig(); err == nil && globalCfg.UI != nil {
		themeName = globalCfg.UI.Theme
		glyphMode = globalCfg.UI.Glyphs
		motionMode = globalCfg.UI.Motion
	}
	if noCursor {
		motionMode = string(presentation.MotionNone)
	}
	return presentation.ResolveWithGlyphsAndMotion(themeName, glyphMode, motionMode, stdout)
}

func runAttachWithDependencies(sessionID string, stdout, stderr io.Writer, deps attachDependencies) int {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		fmt.Fprintln(stderr, "Error: session id required")
		return 1
	}
	if deps.readFile == nil {
		deps.readFile = os.ReadFile
	}
	if deps.readActions == nil {
		deps.readActions = os.ReadFile
	}
	if deps.readTrajectory == nil {
		deps.readTrajectory = os.ReadFile
	}
	if deps.probe == nil {
		deps.probe = session.IsSessionActiveAt
	}
	if deps.isTerminal == nil {
		deps.isTerminal = attachIsTerminal
	}
	if deps.footerWidth == nil {
		deps.footerWidth = attachFooterWidth
	}
	if deps.pollInterval <= 0 {
		deps.pollInterval = attachPollInterval
	}
	if deps.quietGrace <= 0 {
		deps.quietGrace = attachQuietGrace
	}

	target, err := resolveAttachSession(sessionID)
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	sessionID = target.sessionID
	conversationPath := target.conversationPath()
	data, err := deps.readFile(conversationPath)
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	uiTheme := ui.DefaultTheme(deps.theme)
	styledReplay := !deps.focused && deps.isTerminal(stdout)
	var footerTrajectory []byte
	if styledReplay {
		footerTrajectory = readAttachFooterTrajectory(conv, target.sessionDirectory, deps.readTrajectory)
	}
	if styledReplay {
		banner := ui.RenderSessionHeader(ui.SessionStartedEvent{
			SessionID:          sessionID,
			Goal:               conv.OriginalGoal,
			BuildVersion:       Version,
			BuildCommit:        Commit,
			MagnificaHumanitas: conv.MagnificaHumanitas != nil,
			ShowBanner:         true,
		}, uiTheme, 0)
		if _, err := io.WriteString(stdout, banner); err != nil {
			fmt.Fprintln(stderr, "Error:", err)
			return 1
		}
	}
	var initialActions []conversation.ShellActionRecord
	lastActionSequence := map[int]int64{}
	if !deps.noShellSteps {
		initialActions, err = readAttachShellActions(conv, target.sessionDirectory, deps.readActions)
		if err != nil {
			fmt.Fprintln(stderr, "Error:", err)
			return 1
		}
		rememberAttachActionSequences(lastActionSequence, initialActions)
	}
	replay, err := conversation.RenderReplayWithOptions(conv, conversation.ReplayOptions{
		NoShellSteps:       deps.noShellSteps,
		ShellActions:       initialActions,
		SuppressConclusion: true,
		StyledShellSteps:   styledReplay,
		Theme:              deps.theme,
	})
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	if !deps.focused {
		if _, err := io.WriteString(stdout, replay+"\n\n"); err != nil {
			fmt.Fprintln(stderr, "Error:", err)
			return 1
		}
	}

	conclusionPrinted := false
	if answer, ok := conversation.LastFinalMessage(conv); ok {
		if err := writeAttachConclusion(stdout, sessionID, answer, uiTheme); err != nil {
			fmt.Fprintln(stderr, "Error:", err)
			return 1
		}
		conclusionPrinted = true
	}

	overlay := &attachOverlay{
		out:              stdout,
		theme:            deps.theme,
		enabled:          styledReplay,
		footerTrajectory: footerTrajectory,
	}
	defer overlay.finish()

	previous := append([]conversation.Message(nil), conv.Messages...)
	lastContent := time.Now()
	active, err := deps.probe(target.scratchDirectory)
	if err != nil {
		overlay.clear()
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	var inactiveSince time.Time
	if !active {
		inactiveSince = lastContent
	}
	start := time.Now()
	if active && !conclusionPrinted {
		overlay.draw(conv, start, 0, deps.footerWidth(stdout))
	}

	ticker := time.NewTicker(deps.pollInterval)
	defer ticker.Stop()
	for now := range ticker.C {
		data, err := deps.readFile(conversationPath)
		if err != nil {
			overlay.clear()
			fmt.Fprintln(stderr, "Error:", err)
			return 1
		}
		next, err := conversation.Unmarshal(data)
		if err != nil {
			overlay.clear()
			fmt.Fprintln(stderr, "Error:", err)
			return 1
		}
		newMessages := conversation.IdentifyNewMessages(previous, next.Messages)
		var newActions []conversation.ShellActionRecord
		if !deps.noShellSteps {
			actions, actionsErr := readAttachShellActions(next, target.sessionDirectory, deps.readActions)
			if actionsErr != nil {
				overlay.clear()
				fmt.Fprintln(stderr, "Error:", actionsErr)
				return 1
			}
			newActions = filterNewAttachActions(actions, lastActionSequence)
		}
		delta, err := conversation.RenderReplayDeltaWithOptions(next, previous, conversation.ReplayOptions{
			NoShellSteps:       deps.noShellSteps,
			ShellActions:       newActions,
			SuppressConclusion: true,
			StyledShellSteps:   styledReplay,
			Theme:              deps.theme,
		})
		if err != nil {
			overlay.clear()
			fmt.Fprintln(stderr, "Error:", err)
			return 1
		}
		if delta != "" && !deps.focused {
			overlay.clear()
			if _, err := io.WriteString(stdout, delta+"\n\n"); err != nil {
				fmt.Fprintln(stderr, "Error:", err)
				return 1
			}
		}
		if !conclusionPrinted {
			if answer, ok := conversation.LastFinalMessage(next); ok {
				overlay.clear()
				if err := writeAttachConclusion(stdout, sessionID, answer, uiTheme); err != nil {
					fmt.Fprintln(stderr, "Error:", err)
					return 1
				}
				conclusionPrinted = true
			}
		}
		if len(newMessages) > 0 || len(newActions) > 0 {
			lastContent = now
		}
		if len(next.Messages) >= len(previous) {
			previous = append(previous[:0], next.Messages...)
		}

		active, err = deps.probe(target.scratchDirectory)
		if err != nil {
			overlay.clear()
			fmt.Fprintln(stderr, "Error:", err)
			return 1
		}
		if active {
			inactiveSince = time.Time{}
			if !conclusionPrinted {
				overlay.draw(next, now, now.Sub(start), deps.footerWidth(stdout))
			}
			continue
		}
		if inactiveSince.IsZero() {
			inactiveSince = now
			overlay.clear()
			continue
		}
		if now.Sub(inactiveSince) >= deps.quietGrace && now.Sub(lastContent) >= deps.quietGrace {
			if styledReplay {
				overlay.finish()
				if err := writeAttachFooter(stdout, next, footerTrajectory, deps.theme, now, deps.footerWidth(stdout)); err != nil {
					fmt.Fprintln(stderr, "Error:", err)
					return 1
				}
			}
			return 0
		}
	}
	return 0
}

type attachOverlay struct {
	out              io.Writer
	theme            presentation.Theme
	enabled          bool
	footerTrajectory []byte
	lines            int
	finished         bool
}

// draw rewrites attach's single live overlay block. The optional activity line
// and the two run-style footer lines are written without a trailing newline so
// ClearLinesAbove can replace the complete block in place on the next poll.
func (s *attachOverlay) draw(conv *conversation.Conversation, now time.Time, elapsed time.Duration, width int) {
	if s == nil || !s.enabled {
		return
	}
	snapshot := conversation.PersistedFooterSnapshotWithTrajectoryAt(conv, s.footerTrajectory, now)
	snapshot.Width = width
	lines := make([]string, 0, 4)
	if activity := ui.RenderAttachStatusLine(s.theme, elapsed); activity != "" {
		lines = append(lines, activity, "")
	}
	lines = append(lines, ui.RenderFinalFooter(snapshot, s.theme)...)
	if s.lines > 0 {
		ui.ClearLinesAbove(s.lines, s.out)
	}
	fmt.Fprint(s.out, strings.Join(lines, "\n"))
	s.lines = len(lines)
}

// clear removes every line in the live overlay and leaves the cursor at the
// block's former first row, ready for ordinary output or the stable footer.
func (s *attachOverlay) clear() {
	if s == nil || !s.enabled || s.lines == 0 {
		return
	}
	ui.ClearLinesAbove(s.lines, s.out)
	s.lines = 0
}

// finish clears the transient block exactly once. The final footer writer is
// responsible for newline-terminating its two stable lines.
func (s *attachOverlay) finish() {
	if s == nil || s.finished {
		return
	}
	s.clear()
	s.finished = true
}

func attachIsTerminal(out io.Writer) bool {
	file, ok := out.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

// attachFooterWidth mirrors the formatter's terminal-width behavior so attach
// chooses the same compaction candidate as run. A non-terminal writer leaves
// width at zero, which RenderFinalFooter intentionally maps to its default.
func attachFooterWidth(out io.Writer) int {
	file, ok := out.(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return 0
	}
	width, _, err := term.GetSize(int(file.Fd()))
	if err != nil || width <= 0 {
		return 0
	}
	return width
}

func writeAttachConclusion(stdout io.Writer, sessionID, answer string, theme ui.Theme) error {
	_, err := io.WriteString(stdout, ui.RenderSessionConclusion(ui.SessionConclusionEvent{
		Outcome:        ui.SessionConclusionCompleted,
		RenderedAnswer: answer,
		SessionID:      sessionID,
	}, theme, 0))
	return err
}

// writeAttachFooter prints the run-style final footer using only the values
// recorded with the attached conversation. It is called only for a TTY attach
// after the source session has become quiet.
func writeAttachFooter(stdout io.Writer, conv *conversation.Conversation, footerTrajectory []byte, theme presentation.Theme, now time.Time, width int) error {
	snapshot := conversation.PersistedFooterSnapshotWithTrajectoryAt(conv, footerTrajectory, now)
	snapshot.Width = width
	for _, line := range ui.RenderFinalFooter(snapshot, theme) {
		ui.ClearCurrentLine(stdout)
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			return err
		}
	}
	return nil
}

func readAttachFooterTrajectory(conv *conversation.Conversation, sessionDirectory string, readFile attachRead) []byte {
	if conv == nil || (conv.Footer != nil && conv.Footer.CWD != "") || readFile == nil {
		return nil
	}
	path, err := artifacts.SessionTrajectoryFileAt(sessionDirectory, "agent")
	if err != nil {
		return nil
	}
	data, err := readFile(path)
	if err != nil {
		return nil
	}
	return data
}

func readAttachShellActions(conv *conversation.Conversation, sessionDirectory string, readFile attachRead) ([]conversation.ShellActionRecord, error) {
	paths, err := attachShellActionPaths(conv, sessionDirectory)
	if err != nil {
		return nil, err
	}
	var records []conversation.ShellActionRecord
	for turn, path := range paths {
		data, readErr := readFile(path)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return nil, fmt.Errorf("read shell actions for turn %d: %w", turn, readErr)
		}
		parsed, parseErr := shellaction.ParseJournal(data)
		if parseErr != nil {
			return nil, fmt.Errorf("parse shell actions for turn %d: %w", turn, parseErr)
		}
		records = append(records, parsed...)
	}
	return records, nil
}

func attachShellActionPaths(conv *conversation.Conversation, sessionDirectory string) (map[int]string, error) {
	paths := map[int]string{}
	if conv == nil {
		return paths, nil
	}
	for _, message := range conv.Messages {
		if turn, ok := conversation.MessageTurn(message); ok {
			paths[turn] = attachShellAgentActionsPath(sessionDirectory, turn)
		}
		if trajectoryPath, ok := attachMetadataString(message.Metadata, "shell_agent_trajectory_path"); ok {
			if turn, ok := shellAgentTurnFromPath(trajectoryPath); ok {
				paths[turn] = filepath.Join(filepath.Dir(trajectoryPath), "actions.jsonl")
			}
		}
		if shellSessionID, ok := attachMetadataString(message.Metadata, "shell_agent_session_id"); ok {
			if turn, ok := shellAgentTurnFromSessionID(shellSessionID); ok {
				paths[turn] = attachShellAgentActionsPath(sessionDirectory, turn)
			}
		}
	}
	if trajectoryPath := strings.TrimSpace(conv.ShellAgentTrajectoryPath); trajectoryPath != "" {
		if turn, ok := shellAgentTurnFromPath(trajectoryPath); ok {
			paths[turn] = filepath.Join(filepath.Dir(trajectoryPath), "actions.jsonl")
		}
	}
	return paths, nil
}

func attachShellAgentActionsPath(sessionDirectory string, turn int) string {
	return filepath.Join(sessionDirectory, "shell-agent", strconv.Itoa(turn), "actions.jsonl")
}

func attachMetadataString(metadata map[string]any, key string) (string, bool) {
	if metadata == nil {
		return "", false
	}
	value, ok := metadata[key].(string)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func shellAgentTurnFromPath(path string) (int, bool) {
	turn, err := strconv.Atoi(filepath.Base(filepath.Dir(strings.TrimSpace(path))))
	return turn, err == nil && turn >= 0
}

func shellAgentTurnFromSessionID(sessionID string) (int, bool) {
	const marker = "/shell-agent/"
	index := strings.LastIndex(sessionID, marker)
	if index < 0 {
		return 0, false
	}
	turn, err := strconv.Atoi(strings.Trim(strings.TrimSpace(sessionID[index+len(marker):]), "/"))
	return turn, err == nil && turn >= 0
}

func filterNewAttachActions(records []conversation.ShellActionRecord, last map[int]int64) []conversation.ShellActionRecord {
	newRecords := make([]conversation.ShellActionRecord, 0, len(records))
	for _, record := range records {
		if record.Sequence <= last[record.Turn] {
			continue
		}
		newRecords = append(newRecords, record)
	}
	rememberAttachActionSequences(last, newRecords)
	return newRecords
}

func rememberAttachActionSequences(last map[int]int64, records []conversation.ShellActionRecord) {
	for _, record := range records {
		if record.Sequence > last[record.Turn] {
			last[record.Turn] = record.Sequence
		}
	}
}

func changedSessionSelectorFlags(fs *pflag.FlagSet) []string {
	var changed []string
	for _, name := range []string{"session-id", "resume"} {
		if fs.Changed(name) {
			changed = append(changed, "--"+name)
		}
	}
	return changed
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
			msg:  "Error: Git repository has no commits yet. Make an initial commit before running 'machtiani run'.",
			code: 1,
		}, true
	case strings.Contains(lower, "ambiguous argument 'head'"):
		return exitError{
			msg:  "Error: Git repository has no commits yet. Make an initial commit before running 'machtiani run'.",
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
	return fmt.Sprintf("\nError: mct is not synced at current git state %s.\n\nRun \u001b[1mmachtiani sync\u001b[0m before proceeding.", trimmed)
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
	if containsRemovedMaxInputFlag(args) {
		fmt.Fprintln(os.Stderr, "Error: --max-input-tokens was removed; use --context-length to set the total session context window")
		return 2
	}
	cfg := session.Config{}
	if globalCfg, _, err := llm.LoadGlobalConfig(); err == nil && globalCfg.Planner != nil {
		cfg.MaxTurns = globalCfg.Planner.MaxTurns
		cfg.TurnTimeout = globalCfg.Planner.TurnTimeout
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 150
	}
	fs := pflag.NewFlagSet("machtiani sync", pflag.ContinueOnError)
	var paramFlags multiString
	var paramJSON multiString
	var apiKeyFlags multiString
	commitRef := fs.String("commit", "", "project commit hash to sync")
	includeDocs := fs.Bool("include-docs", false, "include documentation/markdown changes when deciding whether to regenerate the internal README")
	configureSessionFlags(fs, &cfg, &paramFlags, &paramJSON, &apiKeyFlags, false)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: machtiani sync [--commit <hash>] [flags]\n\n")
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
	if cfg.ContextLength != 0 && cfg.ContextLength < llm.MinimumContextLength {
		fmt.Fprintf(os.Stderr, "Error: --context-length must be at least %d\n", llm.MinimumContextLength)
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

	globalCfg, configPath, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	if err := llm.ValidateConfigError(globalCfg, configPath, llm.ValidationOptions{}); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}

	commit := strings.TrimSpace(*commitRef)
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
	syncBudget, err := llm.ResolveInputBudgetForChain(runtimes.Answer.Resolved, runtimes.Answer.FallbackResolved, cfg.ContextLength)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Context budget error:", err)
		return 1
	}
	discoveryBudget, err := llm.ResolveInputBudgetForChain(runtimes.FileDiscovery.Resolved, runtimes.FileDiscovery.FallbackResolved, cfg.ContextLength)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Discovery context budget error:", err)
		return 1
	}
	if cfg.Verbose {
		fmt.Fprintf(os.Stderr, "llm.context_budget.resolved context_length=%d source=%s max_input_tokens=%d stage=file-discovery model_alias=%s applied_timeout_sec=%d truncated=false\n", discoveryBudget.ContextLength, discoveryBudget.Source, discoveryBudget.MaxInputTokens, runtimes.FileDiscovery.Resolved.Alias, cfg.TurnTimeout)
		fmt.Fprintf(os.Stderr, "llm.context_budget.resolved context_length=%d source=%s max_input_tokens=%d stage=answer model_alias=%s truncated=false\n", syncBudget.ContextLength, syncBudget.Source, syncBudget.MaxInputTokens, runtimes.Answer.Resolved.Alias)
	}

	var mctPrompts *llm.MCTPromptsConfig
	if globalCfg.Prompts != nil {
		mctPrompts = globalCfg.Prompts.MCT
	}
	themeName := string(presentation.ProfileTerminal)
	glyphMode := string(presentation.GlyphUnicode)
	motionMode := string(presentation.MotionFull)
	if globalCfg.UI != nil {
		themeName = globalCfg.UI.Theme
		glyphMode = globalCfg.UI.Glyphs
		motionMode = globalCfg.UI.Motion
	}
	presentationTheme, err := presentation.ResolveWithGlyphsAndMotion(themeName, glyphMode, motionMode, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error resolving UI theme:", err)
		return 2
	}
	eventBus := ui.NewEventBus(64)
	formatter := ui.NewFormatter(os.Stdout, eventBus, ui.DefaultTheme(presentationTheme), ui.NewProcessTimerManager(), "sync-"+shortCommit(commit))
	stdout := formatter.CoordinateWriter(os.Stdout)
	stderr := formatter.CoordinateWriter(os.Stderr)
	tracker := newSyncFooterTracker(eventBus, runtimes)
	activityTracker := ui.NewActivityTracker(eventBus)
	eventBus.Emit(ui.SessionStartedEvent{
		Identity: ui.FooterIdentity{Label: "sync", Value: shortCommit(commit)},
	})
	ctx := llm.WithUsageObserver(context.Background(), tracker.Observe)
	finishSyncActivity := activityTracker.Begin("sync", ui.ActivitySync)
	syncErr := readmeSyncRunFn(ctx, readmesync.Options{
		Commit:               commit,
		Verbose:              cfg.Verbose,
		ContextLength:        cfg.ContextLength,
		TurnTimeout:          cfg.TurnTimeout,
		IncludeDocs:          *includeDocs,
		Runtime:              runtimes.Orchestrator,
		AnswerRuntime:        runtimes.Answer,
		FileDiscoveryRuntime: runtimes.FileDiscovery,
		Prompts:              mctPrompts,
	})
	finishSyncActivity()
	if syncErr != nil {
		fmt.Fprintln(stderr, "Readme sync failed:", syncErr)
	} else {
		fmt.Fprintf(stdout, "Readme synced for commit %s\n", shortCommit(commit))
	}
	eventBus.Emit(ui.SessionEndedEvent{})
	<-formatter.Done()
	eventBus.Close()
	if syncErr != nil {
		return 1
	}
	return 0
}

func markExplicitModelOverrides(fs *pflag.FlagSet, cfg *session.Config) {
	if fs == nil || cfg == nil {
		return
	}
	cfg.ModelOverrides = session.ModelOverrideFlags{
		Orchestrator:  fs.Changed("model") || fs.Changed("orch-model") || fs.Changed("agent-model"),
		Answer:        fs.Changed("answer-model"),
		FileDiscovery: fs.Changed("file-discovery-model"),
		ShellAgent:    fs.Changed("shell-agent-model"),
		Direct:        fs.Changed("openai-api-key") || fs.Changed("openai-base-url") || fs.Changed("openai-model"),
	}
}

func configureSessionFlags(fs *pflag.FlagSet, cfg *session.Config, paramFlags, paramJSON, apiKeyFlags *multiString, includeDisplayFlags bool) {
	fs.IntVar(&cfg.MaxTurns, "max-turns", cfg.MaxTurns, "maximum number of turns before finalizing (default 150)")
	fs.StringVar(&cfg.OrchModel, "model", "", "Model alias defined in the selected Machtiani config (alias for --orch-model)")
	fs.StringVar(&cfg.OrchModel, "orch-model", "", "Model alias for orchestration/planner steps (default: config or env)")
	fs.StringVar(&cfg.AnswerModel, "answer-model", cfg.AnswerModel, "Model alias for final answer generation (defaults to --orch-model)")
	fs.StringVar(&cfg.FileDiscoveryModel, "file-discovery-model", cfg.FileDiscoveryModel, "Model alias for file discovery runs (default: orchestration model)")
	fs.StringVar(&cfg.AgentModel, "agent-model", "", "Legacy planner model alias (deprecated; use --orch-model)")
	fs.IntVar(&cfg.TurnTimeout, "turn-timeout", cfg.TurnTimeout, "per-turn timeout in seconds (set 0 for no timeout, default 120)")
	fs.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "print intended mct calls; don't execute")
	fs.BoolVarP(&cfg.Verbose, "verbose", "v", cfg.Verbose, "verbose agent logging")
	fs.BoolVar(&cfg.PersistTmpData, "persist-tmp-data", cfg.PersistTmpData, "keep temporary data (worktrees, trajectories) after execution; startup orphan cleanup always runs")
	fs.IntVar(&cfg.MaxCommandOutputBytes, "max-command-output-bytes", cfg.MaxCommandOutputBytes, "maximum bytes of shell command output captured per step (default 64KB)")
	fs.BoolVar(&cfg.ShellAgent, "shell-agent", cfg.ShellAgent, "Enable shell-agent mode: invoke shell-agent subprocess binary for task execution")
	fs.StringVar(&cfg.ShellAgentModel, "shell-agent-model", cfg.ShellAgentModel, "Model alias override for shell-agent subprocesses (default: config)")
	fs.StringVar(&cfg.AnswerTag, "answer-tag", cfg.AnswerTag, `Override the final-answer tag name used by the shell-agent parser and prompt templates. Must not contain "<", ">", "/", "{{", or "}}". Empty input keeps the default ("answer").`)
	fs.StringVar(&cfg.CommandTag, "tag", cfg.CommandTag, "single suffix for both answer and command tags (e.g. --tag foo produces answer-foo and command-foo)")
	fs.StringVar(&cfg.FinalFile, "final-file", cfg.FinalFile, "path to write final answer-only artifact (default: project-store sessions/<sessionID>/chat/agent-final-answer.md)")
	fs.StringVar(&cfg.TranscriptFile, "transcript-file", cfg.TranscriptFile, "path to write transcript file (default: project-store sessions/<sessionID>/chat/agent-transcript.adoc)")
	fs.StringVar(&cfg.FileDiscoveryTrajectory, "file-discovery-trajectory", cfg.FileDiscoveryTrajectory, "path to write file-discovery trajectory JSONL (default: auto-named under session artifacts)")
	fs.StringVar(&cfg.FileDiscoveryOutputDir, "file-discovery-output-dir", cfg.FileDiscoveryOutputDir, "directory for file-discovery artifacts (default: project-store sessions/<sessionID>/artifacts)")
	fs.IntVar(&cfg.ContextLength, "context-length", cfg.ContextLength, "total input-plus-output token context for this session")
	fs.StringVar(&cfg.TrajectoryFile, "trajectory-file", cfg.TrajectoryFile, "override path for unified trajectory JSONL (default: session-scoped path)")
	fs.BoolVar(&cfg.NoTrajectory, "no-trajectory", cfg.NoTrajectory, "disable unified trajectory JSONL emission")
	fs.BoolVar(&cfg.NoBanner, "no-banner", cfg.NoBanner, "disable the interactive session banner")
	fs.BoolVar(&cfg.MagnificaHumanitas, "magnifica-humanitas", cfg.MagnificaHumanitas, "select and persist a Magnifica Humanitas quote for the session")
	fs.BoolVar(&cfg.NoCursor, "no-cursor", cfg.NoCursor, "disable the animated activity cursor")
	if includeDisplayFlags {
		fs.BoolVar(&cfg.Focused, "focused", cfg.Focused, "show only the session banner, conclusion, warnings, and errors")
		fs.BoolVar(&cfg.NoShellSteps, "no-shell-steps", cfg.NoShellSteps, "hide shell step and command blocks")
		fs.BoolVarP(&cfg.Print, "exec", "x", cfg.Print, "one-shot mode: run once and print only raw final-answer markdown to stdout (no styling, banner, rules, save path, or resume line); warnings and errors stay on stderr")
	}
	fs.BoolVar(&cfg.TrajectoryVerboseLLM, "trajectory-verbose-llm", cfg.TrajectoryVerboseLLM, "include expanded LLM details in the trajectory stream")
	fs.BoolVar(&cfg.TrajectoryStreamTokens, "trajectory-stream-tokens", cfg.TrajectoryStreamTokens, "record LLM token streaming events in the trajectory (disabled by default)")
	fs.IntVar(&cfg.TrajectoryExcerpt, "trajectory-excerpt", cfg.TrajectoryExcerpt, "excerpt length (in characters) for prompts/responses captured in the trajectory")
	fs.BoolVar(&cfg.TrajectoryOmitRepoRoot, "trajectory-omit-repo-root", cfg.TrajectoryOmitRepoRoot, "omit repo_root from trajectory events")
	fs.BoolVar(&cfg.LogLLMInputs, "log-llm-inputs", cfg.LogLLMInputs, "write full redacted LLM prompts/context to the session log; may contain source material and grow quickly")
	fs.StringVar(&cfg.OpenAIAPIKey, "openai-api-key", "", "OpenAI-compatible API key (overrides env, deprecated)")
	fs.StringVar(&cfg.OpenAIBaseURL, "openai-base-url", "", "OpenAI-compatible base URL (overrides env, deprecated)")
	fs.StringVar(&cfg.OpenAIModel, "openai-model", "", "Direct upstream model name (deprecated; prefer --model)")
	fs.StringVar(&cfg.SessionID, "session-id", "", "Existing session identifier to resume (deprecated; use --resume or -r)")
	fs.StringVarP(&cfg.SessionID, "resume", "r", "", "Existing session identifier to resume")
	fs.BoolVar(&cfg.Attach, "attach", cfg.Attach, "attach to an existing session: render the persisted conversation as a read-only replay (requires --resume; takes no session lock)")
	_ = fs.MarkDeprecated("session-id", "use 'machtiani run --resume <session-id>' or -r")
	fs.BoolVar(&cfg.EnableTagFormat, "enable-tag-format", cfg.EnableTagFormat, "Enable tag-format response directives and validation (experimental)")
	fs.IntVar(&cfg.ShellAgentInterruptStep, "shell-agent-interrupt-step", 0, "deterministic interrupt after this many shell-agent steps (0 = disabled)")
	fs.StringVar(&cfg.ShellAgentStepLog, "shell-agent-step-log", "", "path for step-log JSONL file (empty disables)")
	fs.StringVar(&cfg.Mode, "mode", "", "Operating mode")
	fs.StringVarP(&cfg.PromptText, "prompt", "p", "", "inline prompt text (mutually exclusive with --file)")
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

func containsRemovedMaxInputFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--max-input-tokens" || strings.HasPrefix(arg, "--max-input-tokens=") {
			return true
		}
	}
	return false
}
func handleConfigCommand(args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
			printConfigUsage()
			return 0
		}
		return handleConfigManager(args)
	}
	switch args[0] {
	case "check":
		return handleManagedConfigCheck(args[1:])
	case "show":
		return handleManagedConfigShow(args[1:])
	case "add":
		return handleConfigAddCommand(args[1:])
	case "catalog":
		return handleConfigCatalogCommand(args[1:])
	case "provider":
		return handleConfigProviderCommand(args[1:])
	case "model":
		return handleConfigModelCommand(args[1:])
	case "cache":
		return handleConfigCacheCommand(args[1:])
	case "scope":
		return handleConfigScopeCommand(args[1:])
	case "url", "api-key", "reasoning":
		fmt.Fprintf(os.Stderr, "The 'config %s' command was removed; use 'config provider set' or 'config model set'.\n", args[0])
		return 2
	default:
		fmt.Fprintf(os.Stderr, "Unknown config subcommand: %s\n", args[0])
		printConfigUsage()
		return 2
	}
}

func printConfigUsage() {
	fmt.Fprintln(os.Stderr, "Usage: machtiani config [--global | --project | --path <file>]")
	fmt.Fprintln(os.Stderr, "       machtiani config <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  add         Add a provider/model set")
	fmt.Fprintln(os.Stderr, "  catalog     List and inspect built-in provider presets")
	fmt.Fprintln(os.Stderr, "  provider    Manage providers")
	fmt.Fprintln(os.Stderr, "  model       Manage models and the default selection")
	fmt.Fprintln(os.Stderr, "  cache       Manage global and per-model caching")
	fmt.Fprintln(os.Stderr, "  scope       Show or select global/project configuration")
	fmt.Fprintln(os.Stderr, "  check       Validate the selected configuration file")
	fmt.Fprintln(os.Stderr, "  show        Print the effective configuration")
}

func handleConfigCheckCommand(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "Usage: machtiani config check")
		return 2
	}
	cfg, path, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		return 1
	}

	if err := llm.ValidateConfigError(cfg, path, llm.ValidationOptions{RequireAllCredentials: true, RequireDefaultModel: true}); err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		return 1
	}

	fmt.Printf("Config OK: %s\n", path)
	defaultModel := strings.TrimSpace(cfg.DefaultModel)
	if defaultModel != "" {
		fmt.Printf("Default model: %s\n", defaultModel)
	}
	return 0
}

func handleSessionCommand(args []string) int {
	if len(args) < 1 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "Usage: machtiani session <subcommand> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  list    List sessions")
		fmt.Fprintln(os.Stderr, "  show    Show details for a specific session")
		fmt.Fprintln(os.Stderr, "  archive Archive a session by ID or date range")
		fmt.Fprintln(os.Stderr, "  unarchive Unarchive a session by ID or date range")
		fmt.Fprintln(os.Stderr, "  menu    Open the session management menu")
		fmt.Fprintln(os.Stderr, "  fork    Fork a session")
		fmt.Fprintln(os.Stderr, "  delete  Delete a session")
		fmt.Fprintln(os.Stderr, "  prune   Remove disposable session diagnostics and deprecated state")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Use 'machtiani session <subcommand> --help' for more information.")
		return 2
	}
	switch args[0] {
	case "list":
		return handleSessionListCommand(args[1:])
	case "show":
		return handleSessionShowCommand(args[1:])
	case "archive":
		return handleSessionArchiveCommand(args[1:])
	case "unarchive":
		return handleSessionUnarchiveCommand(args[1:])
	case "menu":
		return handleSessionMenuCommand(args[1:])
	case "fork":
		return handleSessionForkCommand(args[1:])
	case "delete":
		return handleSessionDeleteCommand(args[1:])
	case "prune":
		return handleSessionPruneCommand(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown session subcommand: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Usage: machtiani session <subcommand> [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Subcommands:")
		fmt.Fprintln(os.Stderr, "  list    List sessions")
		fmt.Fprintln(os.Stderr, "  show    Show details for a specific session")
		fmt.Fprintln(os.Stderr, "  archive Archive a session by ID or date range")
		fmt.Fprintln(os.Stderr, "  unarchive Unarchive a session by ID or date range")
		fmt.Fprintln(os.Stderr, "  menu    Open the session management menu")
		fmt.Fprintln(os.Stderr, "  fork    Fork a session")
		fmt.Fprintln(os.Stderr, "  delete  Delete a session")
		fmt.Fprintln(os.Stderr, "  prune   Remove disposable session diagnostics and deprecated state")
		return 2
	}
}

func handleSessionListCommand(args []string) int {
	fs := pflag.NewFlagSet("machtiani session list", pflag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output sessions as JSON array")
	archivedOnly := fs.Bool("archived", false, "Show only archived sessions (intersects with --forked)")
	forkedOnly := fs.Bool("forked", false, "Show only forked sessions (intersects with --archived)")
	listAll := fs.Bool("all", false, "Show all sessions (overrides --archived and --forked)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: machtiani session list [flags]\n\n")
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

	listMode := session.ListActive
	switch {
	case *listAll:
		listMode = session.ListAll
	case *archivedOnly && *forkedOnly:
		listMode = session.ListArchivedForked
	case *archivedOnly:
		listMode = session.ListArchived
	case *forkedOnly:
		listMode = session.ListForked
	}
	sessions, err := session.ListSessionsWithOptions(session.SessionListOptions{Mode: listMode})
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

	headings := []string{"SESSION_ID", "GOAL", "STATUS", "TURNS", "UPDATED"}
	goalWidth := sessionListGoalWidth(sessions)
	table := tabwriter.NewWriter(os.Stdout, 0, 4, 1, ' ', 0)
	fmt.Fprintln(table, strings.Join(headings, "\t"))
	separator := make([]string, len(headings))
	for i, heading := range headings {
		separator[i] = strings.Repeat("-", len(heading))
	}
	fmt.Fprintln(table, strings.Join(separator, "\t"))
	for _, s := range sessions {
		sessionID := sessionListDisplayID(s)
		goal := truncateSessionGoal(s.Goal, goalWidth)
		updated := s.UpdatedAt.Format("2006-01-02")
		fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%s\n", sessionID, goal, s.Status, s.TurnsCompleted, updated)
	}
	if err := table.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing session table: %v\n", err)
		return 1
	}
	return 0
}

func sessionListDisplayID(state session.SessionState) string {
	sessionID := state.SessionID
	if state.Archived {
		sessionID += " [archived]"
	}
	if state.ForkedFrom != "" {
		sessionID += " [forked]"
	}
	return sessionID
}

func sessionListGoalWidth(sessions []session.SessionState) int {
	const (
		maxTableWidth = 80
		maxGoalWidth  = 30
		columnPadding = 1
		updatedWidth  = len("2006-01-02")
	)
	maxSessionIDWidth := len("SESSION_ID")
	maxStatusWidth := len("STATUS")
	maxTurnsWidth := len("TURNS")
	for _, state := range sessions {
		maxSessionIDWidth = max(maxSessionIDWidth, len(sessionListDisplayID(state)))
		maxStatusWidth = max(maxStatusWidth, len(state.Status))
		maxTurnsWidth = max(maxTurnsWidth, len(strconv.Itoa(state.TurnsCompleted)))
	}
	fixedWidth := maxSessionIDWidth + maxStatusWidth + maxTurnsWidth + updatedWidth + 4*columnPadding
	return max(len("GOAL"), min(maxGoalWidth, maxTableWidth-fixedWidth))
}

func truncateSessionGoal(goal string, width int) string {
	runes := []rune(strings.TrimSpace(goal))
	if len(runes) <= width {
		return string(runes)
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func handleSessionShowCommand(args []string) int {
	fs := pflag.NewFlagSet("machtiani session show", pflag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "Output session as JSON object")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: machtiani session show <session-id> [flags]\n\n")
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

	sessionQuery := fs.Arg(0)
	sessionID, err := session.ResolveSessionID(sessionQuery)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving session %s: %v\n", sessionQuery, err)
		return 1
	}
	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving conversation path for session %s: %v\n", sessionID, err)
		return 1
	}
	convData, err := os.ReadFile(convPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading conversation file for session %s: %v\n", sessionID, err)
		return 1
	}
	conv, err := conversation.Unmarshal(convData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing conversation for session %s: %v\n", sessionID, err)
		return 1
	}
	state, err := session.SessionStateFromConversation(conv, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deriving session state for %s: %v\n", sessionID, err)
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
	fmt.Printf("Archived:        %t\n", state.Archived)
	if state.ForkedFrom != "" {
		fmt.Printf("Forked from:     %s\n", state.ForkedFrom)
	}
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
	fs := pflag.NewFlagSet("machtiani session fork", pflag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: machtiani session fork <session-id> [destination-id]\n\n")
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
	if fs.NArg() > 2 {
		fmt.Fprintln(os.Stderr, "Error: too many arguments")
		fs.Usage()
		return 2
	}

	destinationSessionID := ""
	if fs.NArg() == 2 {
		destinationSessionID = strings.TrimSpace(fs.Arg(1))
		if destinationSessionID == "" {
			fmt.Fprintln(os.Stderr, "Error: destination-id is required when provided")
			return 2
		}
		if err := session.ValidateForkDestinationID(destinationSessionID); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 2
		}
	}

	sessionQuery := fs.Arg(0)
	sessionID, err := session.ResolveSessionID(sessionQuery)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving session %s: %v\n", sessionQuery, err)
		return 1
	}
	if destinationSessionID == sessionID {
		fmt.Fprintln(os.Stderr, "Error: destination must differ from source")
		return 2
	}
	newSessionID, err := session.ForkSession(sessionID, destinationSessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error forking session %s: %v\n", sessionID, err)
		return 1
	}

	fmt.Println(newSessionID)
	return 0
}

func handleSessionDeleteCommand(args []string) int {
	fs := pflag.NewFlagSet("machtiani session delete", pflag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: machtiani session delete <session-id>\n\n")
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

	sessionQuery := fs.Arg(0)
	sessionID, err := session.ResolveSessionID(sessionQuery)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving session %s: %v\n", sessionQuery, err)
		return 1
	}
	if err := session.DeleteSession(sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting session %s: %v\n", sessionID, err)
		return 1
	}

	fmt.Printf("Deleted session %s\n", sessionID)
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
