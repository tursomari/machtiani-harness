package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/presentation"
	"github.com/tursomari/machtiani/agent/internal/runner"
	"github.com/tursomari/machtiani/agent/internal/tempdir"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

type Planner interface {
	Plan(ctx context.Context, conv *conversation.Conversation, goal string, transcript string, step, maxSteps int) (planner.Decision, string, error)
	Finalize(ctx context.Context, conv *conversation.Conversation, goal string) (string, error)
	UpdateProgress(progress planner.Progress)
	AnalyzeUserDirectedAsk(ctx context.Context, conv *conversation.Conversation, goal, ask string, step, maxSteps int) (planner.UserDirectedAskOutcome, error)
}

type runBootstrap struct {
	opts                              Options
	cfg                               legacyConfig
	sessionID                         string
	goal                              string
	resumePrompt                      string
	originalPrompt                    string
	taskDescription                   string
	plannerOverlay                    string
	conversationGoal                  string
	conversationPath                  string
	resumeMode                        bool
	loadedState                       *SessionState
	resumeSuspendedInput              *conversation.SuspendedUserInputState
	modeInstructions                  llm.ModeInstructions
	modeInstructionPath               string
	runState                          *runLifecycleState
	resumableShellAgent               bool
	resumableShellAgentTrajectoryPath string
	hasNewInput                       bool   // from Options; determines shell-agent ResumeAttempt
	shellAgentInterruptStep           int    // from Options; deterministic interrupt step for shell agent
	shellAgentStepLog                 string // from Options; path for step-log JSONL file (empty disables)
}

type sessionEnvironmentBootstrap struct {
	sessionTempRoot      string
	workspaceRoot        string
	useSnapshotWorkspace bool
	restore              func()
}

type transcriptBootstrap struct {
	transcript            *transcript.Transcript
	recorder              *conversationRecorder
	conversation          *conversation.Conversation
	writeTurn             transcriptTurnWriter
	appendConversationRaw func(role, content, metaType string) error
}

func prepareRunBootstrap(rootCtx context.Context, opts Options, diagWriter io.Writer) (*runBootstrap, Result, bool) {
	cfgInput := opts.Config
	sessionID := strings.TrimSpace(cfgInput.SessionID)

	inputPrompt := strings.TrimSpace(opts.Goal)
	if inputPrompt == "" && sessionID == "" {
		fmt.Fprintln(diagWriter, "Error: empty issue/question provided")
		return nil, Result{ExitCode: 2, Err: errors.New("empty goal")}, false
	}

	originalPrompt := opts.OriginalPrompt
	if strings.TrimSpace(originalPrompt) == "" {
		originalPrompt = opts.Goal
	}
	taskDescription := opts.TaskDescription
	plannerOverlay := opts.PlannerOverlay
	goal := inputPrompt
	resumeMode := false
	var loadedState *SessionState
	resumePrompt := ""

	if sessionID != "" {
		// Load conversation from disk if available, for migration
		// from per-message metadata to top-level fields.
		var conv *conversation.Conversation
		if convPath, convPathErr := artifacts.SessionConversationFile(sessionID); convPathErr == nil {
			if data, readErr := os.ReadFile(convPath); readErr == nil {
				conv, _ = conversation.Unmarshal(data)
			}
		}
		state, err := sessionStateFromConversation(conv, sessionID)
		if err != nil {
			fmt.Fprintln(diagWriter, "Error loading session state:", err)
			return nil, Result{ExitCode: 1, Err: err}, false
		}
		resumeMode = true
		loadedState = state
		resumePrompt = strings.TrimSpace(inputPrompt)

		if loadedState != nil && loadedState.OriginalGoal == "" {
			loadedState.OriginalGoal = loadedState.Goal
		}
		if storedGoal := strings.TrimSpace(state.Goal); storedGoal != "" {
			goal = storedGoal
		}
		if storedOriginal := strings.TrimSpace(state.OriginalPrompt); storedOriginal != "" {
			originalPrompt = state.OriginalPrompt
		}
		if storedTask := strings.TrimSpace(state.TaskDescription); storedTask != "" {
			taskDescription = state.TaskDescription
		}
		if storedPlannerOverlay := strings.TrimSpace(state.PlannerOverlay); storedPlannerOverlay != "" {
			plannerOverlay = state.PlannerOverlay
		}
		if goal == "" {
			goal = resumePrompt
		}
		if strings.TrimSpace(cfgInput.Mode) == "" && len(state.Modes) > 0 {
			cfgInput.Mode = strings.TrimSpace(state.Modes[0])
		}
		if strings.TrimSpace(cfgInput.ModeInstructionDir) == "" && strings.TrimSpace(state.ModeInstructionDir) != "" {
			cfgInput.ModeInstructionDir = strings.TrimSpace(state.ModeInstructionDir)
		}
	}

	if goal == "" {
		goal = resumePrompt
	}
	if goal == "" {
		fmt.Fprintln(diagWriter, "Error: unable to determine session goal")
		return nil, Result{ExitCode: 1, Err: errors.New("missing session goal")}, false
	}
	if strings.TrimSpace(originalPrompt) == "" {
		originalPrompt = goal
	}

	opts.OriginalPrompt = originalPrompt
	opts.TaskDescription = taskDescription
	opts.PlannerOverlay = plannerOverlay
	conversationGoal := formatGoalText(originalPrompt, taskDescription)

	if sessionID == "" {
		if inherited := os.Getenv("MACHTIANI_SESSION_ID"); inherited != "" {
			sessionID = inherited
		} else {
			sessionID = runner.GenerateSessionID()
		}
	}
	_ = os.Setenv("MACHTIANI_SESSION_ID", sessionID)

	// Ignore inherited MACHTIANI_SESSION_TEMP_ROOT for new sessions
	// to prevent child processes from colliding with a parent session lock,
	// unless the caller explicitly provided a temp root via the environment.
	if strings.TrimSpace(cfgInput.SessionID) == "" && os.Getenv("MACHTIANI_SESSION_ID") != "" && os.Getenv("MACHTIANI_SESSION_TEMP_ROOT") == "" {
		_ = os.Unsetenv("MACHTIANI_SESSION_TEMP_ROOT")
	}

	conversationPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		fmt.Fprintln(diagWriter, "Error resolving conversation path:", err)
		return nil, Result{ExitCode: 1, Err: err}, false
	}

	cfgInput.SessionID = sessionID
	cfg := newLegacyConfig(cfgInput)
	opts.Config = cfgInput
	applyTrajectoryEnvOverrides(&cfg)

	modeInstructions := llm.ModeInstructions{}
	modeInstructionPath := ""
	if strings.TrimSpace(cfg.mode) != "" {
		doc, err := llm.LoadModeInstructions(cfg.mode, cfg.modeInstructionDir, opts.GlobalConfig, opts.GlobalConfigPath)
		if err != nil {
			fmt.Fprintln(diagWriter, "Error loading mode instructions:", err)
			return nil, Result{ExitCode: 1, Err: err}, false
		}
		modeInstructions = doc
		modeInstructionPath = doc.Path
	}

	runState := newRunLifecycleState(rootCtx, cfg, sessionID, goal, originalPrompt, taskDescription, plannerOverlay, modeInstructionPath, opts.ShellAgentInterruptStep, opts.ShellAgentStepLog, loadedState)

	bootstrap := &runBootstrap{
		opts:                    opts,
		cfg:                     cfg,
		sessionID:               sessionID,
		goal:                    goal,
		resumePrompt:            resumePrompt,
		originalPrompt:          originalPrompt,
		taskDescription:         taskDescription,
		plannerOverlay:          plannerOverlay,
		conversationGoal:        conversationGoal,
		conversationPath:        conversationPath,
		resumeMode:              resumeMode,
		loadedState:             loadedState,
		resumeSuspendedInput:    loadedStateSuspendedInput(loadedState),
		modeInstructions:        modeInstructions,
		modeInstructionPath:     modeInstructionPath,
		runState:                runState,
		shellAgentInterruptStep: opts.ShellAgentInterruptStep,
		shellAgentStepLog:       opts.ShellAgentStepLog,
	}
	bootstrap.hasNewInput = opts.HasNewInput

	if loadedState != nil && loadedState.ShellAgentResumable && !opts.HasNewInput {
		resumeTrajectoryPath, resumeErr := validatedShellAgentResumeTrajectory(sessionID, loadedState)
		if resumeErr != nil {
			fmt.Fprintf(diagWriter, "stale shell-agent resume marker ignored for session %s: %v\n", sessionID, resumeErr)
		} else {
			bootstrap.resumableShellAgent = true
			bootstrap.resumableShellAgentTrajectoryPath = resumeTrajectoryPath
			fmt.Fprintf(diagWriter, "resumable shell-agent work request detected for session %s at %s\n", sessionID, resumeTrajectoryPath)
		}
	}
	return bootstrap, Result{}, true
}

func validatedShellAgentResumeTrajectory(sessionID string, state *SessionState) (string, error) {
	if state == nil || !state.ShellAgentResumable {
		return "", errors.New("shell-agent work is not marked resumable")
	}
	turn := state.TurnsCompleted + 1
	path, err := artifacts.ShellAgentTrajectoryPath(sessionID, turn)
	if err != nil {
		return "", err
	}
	if _, err := loadTrajectoryForResume(sessionID, turn); err != nil {
		return path, fmt.Errorf("resume trajectory unavailable at %s: %w", path, err)
	}
	return path, nil
}

func loadedStateSuspendedInput(state *SessionState) *conversation.SuspendedUserInputState {
	if state == nil || state.SuspendedUserInput == nil {
		return nil
	}
	return state.SuspendedUserInput.Clone()
}

func prepareSessionEnvironment(sessionID string, cfg legacyConfig, diagWriter io.Writer) (*sessionEnvironmentBootstrap, error) {
	origSessionTempRootRaw := os.Getenv("MACHTIANI_SESSION_TEMP_ROOT")
	origSessionTempRoot := strings.TrimSpace(origSessionTempRootRaw)
	origTmpRootRaw := os.Getenv("MACHTIANI_TMP_ROOT")

	sessionTempRoot := origSessionTempRoot
	if sessionTempRoot == "" {
		var err error
		sessionTempRoot, err = artifacts.SessionScratchDirectory(sessionID)
		if err != nil {
			return nil, fmt.Errorf("resolve session scratch directory: %w", err)
		}
	}

	globalConfig, _, _ := llm.LoadGlobalConfig()
	tmpRoot := ".machtiani/tmp"
	if abs, err := filepath.Abs(tmpRoot); err == nil {
		tmpRoot = abs
	}
	workspaceRoot := filepath.Join(tmpRoot, "workspace-"+sessionID)
	useSnapshotWorkspace := !isLocalSessionEnvironment(&globalConfig)

	// In non-local environments the child tools should create temp data inside
	// the snapshot workspace, not beside the host-side session lock. During the
	// run MACHTIANI_TMP_ROOT is therefore repurposed from "scratch root override"
	// to "workspace root".
	if useSnapshotWorkspace {
		if err := os.Setenv("MACHTIANI_TMP_ROOT", workspaceRoot); err != nil {
			fmt.Fprintln(diagWriter, "Warning: unable to export tmp root:", err)
		}
	} else if err := os.Unsetenv("MACHTIANI_TMP_ROOT"); err != nil {
		fmt.Fprintln(diagWriter, "Warning: unable to clear tmp root:", err)
	}
	if err := os.Setenv("MACHTIANI_SESSION_TEMP_ROOT", sessionTempRoot); err != nil {
		fmt.Fprintln(diagWriter, "Warning: unable to export session temp root:", err)
	}

	markerMaxAge, markerMaxAgeErr := shellAgentMarkerMaxAge()
	if markerMaxAgeErr != nil {
		fmt.Fprintf(diagWriter, "Warning: %v; using default %s\n", markerMaxAgeErr, defaultShellAgentMarkerMaxAge)
	}
	if err := cleanupStaleShellAgentMarkers(sessionTempRoot, markerMaxAge, cfg.verbose, diagWriter); err != nil {
		fmt.Fprintf(diagWriter, "Warning: failed to cleanup shell-agent markers: %v\n", err)
	}

	cleanupSessionRoot := origSessionTempRoot == ""
	restore := func() {
		if strings.TrimSpace(workspaceRoot) != "" && !cfg.persistTmpData {
			if err := os.RemoveAll(workspaceRoot); err != nil && cfg.verbose {
				fmt.Fprintf(diagWriter, "Warning: failed to cleanup workspace root %s: %v\n", workspaceRoot, err)
			}
		}
		if cleanupSessionRoot && !cfg.persistTmpData {
			if err := os.RemoveAll(sessionTempRoot); err != nil && cfg.verbose {
				fmt.Fprintf(diagWriter, "Warning: failed to cleanup session temp root %s: %v\n", sessionTempRoot, err)
			}
		}
		tempdir.ClearSessionRoot()
		if strings.TrimSpace(origTmpRootRaw) == "" {
			_ = os.Unsetenv("MACHTIANI_TMP_ROOT")
		} else {
			_ = os.Setenv("MACHTIANI_TMP_ROOT", origTmpRootRaw)
		}
		if strings.TrimSpace(origSessionTempRootRaw) == "" {
			_ = os.Unsetenv("MACHTIANI_SESSION_TEMP_ROOT")
		} else {
			_ = os.Setenv("MACHTIANI_SESSION_TEMP_ROOT", origSessionTempRootRaw)
		}
	}

	return &sessionEnvironmentBootstrap{
		sessionTempRoot:      sessionTempRoot,
		workspaceRoot:        workspaceRoot,
		useSnapshotWorkspace: useSnapshotWorkspace,
		restore:              restore,
	}, nil
}

func prepareTranscriptBootstrap(cfg legacyConfig, sessionID, conversationGoal, conversationPath string, resumeMode bool, loadedState *SessionState, hasNewInput bool, trajectoryWriter *trajectory.Writer, repoRoot string, runState *runLifecycleState, diagWriter io.Writer) (*transcriptBootstrap, error) {
	resumeTranscript := loadResumeTranscript(cfg, loadedState, resumeMode, sessionID, diagWriter)
	tr, err := transcript.NewWithPath(cfg.transcriptFile, sessionID)
	if err != nil {
		return nil, err
	}
	tr.SetTrajectory(trajectoryWriter)
	runState.tr = tr
	runState.repoRoot = repoRoot
	runState.trajectoryWriter = trajectoryWriter

	recorder := newConversationRecorder(tr, sessionID, conversationGoal, conversationPath, resumeMode, loadedState, hasNewInput)
	runState.recorder = recorder

	if strings.TrimSpace(resumeTranscript) != "" {
		if err := tr.Restore(resumeTranscript); err != nil {
			_ = tr.Close()
			return nil, err
		}
	}
	if err := recorder.Load(); err != nil {
		_ = tr.Close()
		return nil, err
	}
	if err := restoreTranscriptFromConversation(tr, recorder.Rendered(), resumeMode); err != nil {
		_ = tr.Close()
		return nil, err
	}
	if err := recorder.Save(); err != nil {
		_ = tr.Close()
		return nil, err
	}

	return &transcriptBootstrap{
		transcript:            tr,
		recorder:              recorder,
		conversation:          recorder.Conversation(),
		writeTurn:             recorder.WriteTurn,
		appendConversationRaw: recorder.AppendRaw,
	}, nil
}

// loadResumeTranscript regenerates the transcript text for a resumed session
// by reading conversation.json from disk and rendering it. The on-disk
// conversation is authoritative; when the file is missing we return empty
// string and let the subsequent recorder Load() handle the error path.
func loadResumeTranscript(cfg legacyConfig, loadedState *SessionState, resumeMode bool, sessionID string, diagWriter io.Writer) string {
	if !resumeMode || loadedState == nil {
		return ""
	}
	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		if cfg.verbose {
			fmt.Fprintf(diagWriter, "Warning: failed to resolve conversation path for resume: %v\n", err)
		}
		return ""
	}
	data, err := os.ReadFile(convPath)
	if err != nil {
		if cfg.verbose && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(diagWriter, "Warning: failed to read conversation for resume (%s): %v\n", convPath, err)
		}
		return ""
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		if cfg.verbose {
			fmt.Fprintf(diagWriter, "Warning: failed to decode conversation for resume: %v\n", err)
		}
		return ""
	}
	rendered, err := conv.ToTranscript()
	if err != nil {
		if cfg.verbose {
			fmt.Fprintf(diagWriter, "Warning: failed to render transcript for resume: %v\n", err)
		}
		return ""
	}
	return rendered
}

func newRunLifecycleState(rootCtx context.Context, cfg legacyConfig, sessionID, goal, originalPrompt, taskDescription, plannerOverlay, modeInstructionPath string, interruptStep int, stepLogPath string, loadedState *SessionState) *runLifecycleState {
	turnsCompleted := 0
	if loadedState != nil && loadedState.TurnsCompleted > 0 {
		turnsCompleted = loadedState.TurnsCompleted
	}
	plannerProgress := newPlannerProgressTracker(nil)
	if loadedState != nil {
		plannerProgress = newPlannerProgressTracker(loadedState.PlannerProgress)
	}
	var runtimeBaseElapsed time.Duration
	var runtimeTokenUsage ui.TokenUsageUpdatedEvent
	if loadedState != nil && loadedState.RuntimeStats != nil {
		stats := loadedState.RuntimeStats.Clone()
		runtimeBaseElapsed = time.Duration(stats.ActiveElapsedMS) * time.Millisecond
		runtimeTokenUsage = ui.TokenUsageUpdatedEvent{
			InputHit:  stats.InputHitTokens,
			InputMiss: stats.InputMissTokens,
			Output:    stats.OutputTokens,
		}
	}
	originalGoalVal := originalPrompt
	if loadedState != nil && loadedState.OriginalGoal != "" {
		originalGoalVal = loadedState.OriginalGoal
	} else if loadedState != nil && loadedState.Goal != "" {
		originalGoalVal = loadedState.Goal
	}
	r := &runLifecycleState{
		rootCtx:                 rootCtx,
		cfg:                     cfg,
		sessionID:               sessionID,
		goal:                    goal,
		originalPrompt:          originalPrompt,
		originalGoal:            originalGoalVal,
		taskDescription:         taskDescription,
		plannerOverlay:          plannerOverlay,
		modeInstructionPath:     modeInstructionPath,
		mode:                    cfg.mode,
		plannerProgress:         plannerProgress,
		runtimeBaseElapsed:      runtimeBaseElapsed,
		runtimeTokenUsage:       runtimeTokenUsage,
		suspendedUserInput:      loadedStateSuspendedInput(loadedState),
		sessionStatus:           "error",
		turnsCompleted:          turnsCompleted,
		shellAgentInterruptStep: interruptStep,
		shellAgentStepLog:       stepLogPath,
	}
	if strings.TrimSpace(r.mode) != "" && r.recorder != nil && r.recorder.HasConversation() {
		r.recorder.conversation.Modes = []string{strings.ToLower(strings.TrimSpace(r.mode))}
	}
	return r
}

type conversationRecorder struct {
	tr                       *transcript.Transcript
	sessionID                string
	conversationGoal         string
	conversationPath         string
	resumeMode               bool
	loadedState              *SessionState
	conversation             *conversation.Conversation
	conversationRendered     string
	conversationJSON         string
	shellAgentTrajectoryPath string
	shellAgentResumable      bool
	hasNewInput              bool
	resumedSession           bool
}

var errConversationTranscriptDesync = errors.New("conversation transcript desync")

func newConversationRecorder(tr *transcript.Transcript, sessionID, conversationGoal, conversationPath string, resumeMode bool, loadedState *SessionState, hasNewInput bool) *conversationRecorder {
	c := &conversationRecorder{
		tr:               tr,
		sessionID:        sessionID,
		conversationGoal: conversationGoal,
		conversationPath: conversationPath,
		resumeMode:       resumeMode,
		loadedState:      loadedState,
		hasNewInput:      hasNewInput,
	}
	return c
}

func (c *conversationRecorder) SetShellAgentMetadata(trajectoryPath string, resumable bool) {
	c.shellAgentTrajectoryPath = trajectoryPath
	c.shellAgentResumable = resumable
}

func (c *conversationRecorder) PreWriteTurn(step int, question string, shellAgentTrajectoryPath string) error {
	if c.conversation == nil {
		return nil
	}
	c.conversation.ShellAgentResumable = true
	c.conversation.ShellAgentTrajectoryPath = shellAgentTrajectoryPath
	c.conversation.Goal = c.conversationGoal
	if c.loadedState != nil {
		c.conversation.ShellAgentInterruptStep = c.loadedState.ShellAgentInterruptStep
		c.conversation.OriginalPrompt = c.loadedState.OriginalPrompt
		c.conversation.SuspendedUserInput = c.loadedState.SuspendedUserInput.Clone()
		c.conversation.PlannerProgress = c.loadedState.PlannerProgress.Clone()
		c.conversation.Modes = append([]string(nil), c.loadedState.Modes...)
		c.conversation.ModeInstructionDir = c.loadedState.ModeInstructionDir
		c.conversation.PlannerOverlay = c.loadedState.PlannerOverlay
		c.conversation.TaskDescription = c.loadedState.TaskDescription
		if !c.resumedSession {
			c.conversation.Status = c.loadedState.Status
		}
	}
	shellAgentSessionID := fmt.Sprintf("%s/shell-agent/%d", c.sessionID, step)
	c.conversation.AddMessage("assistant", question, map[string]any{
		"type":                   "work_request",
		"turn":                   step,
		"decision":               "ask",
		"shell_agent_session_id": shellAgentSessionID,
	})
	rendered, delta, err := c.renderDelta()
	if errors.Is(err, errConversationTranscriptDesync) {
		c.conversationRendered = rendered
		return c.Save()
	}
	if err != nil {
		return err
	}
	if delta != "" && c.tr != nil {
		if err := c.tr.AppendBlock(delta); err != nil {
			return err
		}
	}
	c.conversationRendered = rendered
	return c.Save()
}

func (c *conversationRecorder) Load() error {
	if c.resumeMode {
		data, err := os.ReadFile(c.conversationPath)
		if err != nil {
			return fmt.Errorf("read conversation json for resume (%s): %w", c.conversationPath, err)
		}
		conv, err := conversation.Unmarshal(data)
		if err != nil {
			return err
		}
		c.conversation = conv
		c.conversationJSON = string(data)
		if c.hasNewInput && c.conversation.Status == "success" {
			c.conversation.Status = ""
			c.resumedSession = true
			if saveErr := c.Save(); saveErr != nil {
				return saveErr
			}
		}
	}
	if c.conversation == nil {
		c.conversation = conversation.New(c.sessionID, c.conversationGoal)
	}
	if strings.TrimSpace(c.conversation.OriginalGoal) == "" {
		c.conversation.OriginalGoal = c.conversationGoal
	}
	rendered, err := c.conversation.ToTranscript()
	if err != nil {
		return err
	}
	c.conversationRendered = rendered
	return nil
}

func (c *conversationRecorder) Save() error {
	if c.conversation == nil {
		return nil
	}
	data, err := c.conversation.Marshal()
	if err != nil {
		return err
	}
	c.conversationJSON = string(data)
	if strings.TrimSpace(c.conversationPath) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.conversationPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(c.conversationPath, data, 0o644)
}

func (c *conversationRecorder) EnsureSaved() {
	if strings.TrimSpace(c.conversationJSON) == "" {
		_ = c.Save()
	}
}

func (c *conversationRecorder) Rendered() string {
	return c.conversationRendered
}

func (c *conversationRecorder) JSON() string {
	return c.conversationJSON
}

func (c *conversationRecorder) Path() string {
	return c.conversationPath
}

func (c *conversationRecorder) Conversation() *conversation.Conversation {
	return c.conversation
}

func (c *conversationRecorder) HasConversation() bool {
	return c.conversation != nil
}

func (c *conversationRecorder) renderDelta() (string, string, error) {
	if c.conversation == nil {
		return "", "", nil
	}
	rendered, err := c.conversation.ToTranscript()
	if err != nil {
		return "", "", err
	}
	if c.conversationRendered != "" && !strings.HasPrefix(rendered, c.conversationRendered) {
		return rendered, "", errConversationTranscriptDesync
	}
	delta := rendered[len(c.conversationRendered):]
	return rendered, delta, nil
}

func (c *conversationRecorder) WriteTurn(step int, question, savedPath string, retrieved []string, summary string, decision string) error {
	if c.conversation == nil {
		if c.tr == nil {
			return nil
		}
		return c.tr.WriteTurn(step, question, savedPath, retrieved, summary, decision)
	}
	c.conversation.ShellAgentResumable = c.shellAgentResumable
	c.conversation.ShellAgentTrajectoryPath = c.shellAgentTrajectoryPath
	c.conversation.TurnsCompleted = step
	c.conversation.Goal = c.conversationGoal
	if c.loadedState != nil {
		c.conversation.ShellAgentInterruptStep = c.loadedState.ShellAgentInterruptStep
		c.conversation.OriginalPrompt = c.loadedState.OriginalPrompt
		c.conversation.SuspendedUserInput = c.loadedState.SuspendedUserInput.Clone()
		c.conversation.PlannerProgress = c.loadedState.PlannerProgress.Clone()
		c.conversation.Modes = append([]string(nil), c.loadedState.Modes...)
		c.conversation.ModeInstructionDir = c.loadedState.ModeInstructionDir
		c.conversation.PlannerOverlay = c.loadedState.PlannerOverlay
		c.conversation.TaskDescription = c.loadedState.TaskDescription
		if !c.resumedSession {
			c.conversation.Status = c.loadedState.Status
		}
	}
	shellAgentSessionID := fmt.Sprintf("%s/shell-agent/%d", c.sessionID, step)
	c.conversation.AddMessage("assistant", question, map[string]any{
		"type":                   "work_request",
		"turn":                   step,
		"decision":               decision,
		"shell_agent_session_id": shellAgentSessionID,
	})
	c.conversation.AddMessage("assistant", summary, map[string]any{
		"type":            "work_result",
		"turn":            step,
		"retrieved_files": retrieved,
		"chat_path":       savedPath,
	})
	rendered, delta, err := c.renderDelta()
	if errors.Is(err, errConversationTranscriptDesync) {
		if c.tr != nil {
			if err := c.tr.WriteTurn(step, question, savedPath, retrieved, summary, decision); err != nil {
				return err
			}
		}
		c.conversationRendered = rendered
		return c.Save()
	}
	if err != nil {
		return err
	}
	if delta != "" && c.tr != nil {
		if err := c.tr.AppendBlock(delta); err != nil {
			return err
		}
	}
	c.conversationRendered = rendered
	return c.Save()
}

func (c *conversationRecorder) AppendRaw(role, content, metaType string) error {
	if c.conversation == nil || c.tr == nil {
		if c.tr == nil {
			return nil
		}
		switch strings.ToLower(strings.TrimSpace(metaType)) {
		case "user_input_request":
			return c.tr.AppendRaw(fmt.Sprintf("\n=== USER INPUT REQUEST\n\n%s\n", content))
		case "user_input_response":
			return c.tr.AppendRaw(fmt.Sprintf("\n=== USER INPUT RESPONSE\n\n%s\n", content))
		case "":
			if strings.EqualFold(strings.TrimSpace(role), "user") {
				return c.tr.AppendRaw(fmt.Sprintf("\n=== USER MESSAGE\n\n%s\n", content))
			}
			if strings.EqualFold(strings.TrimSpace(role), "assistant") {
				return c.tr.AppendRaw(fmt.Sprintf("\n=== ASSISTANT MESSAGE\n\n%s\n", content))
			}
			return c.tr.AppendRaw(content)
		default:
			return c.tr.AppendRaw(content)
		}
	}
	var metadata map[string]any
	if strings.TrimSpace(metaType) != "" {
		metadata = map[string]any{"type": metaType}
	}
	c.conversation.AddMessage(role, content, metadata)
	rendered, delta, err := c.renderDelta()
	if errors.Is(err, errConversationTranscriptDesync) {
		if err := c.appendRawToTranscript(role, content, metaType); err != nil {
			return err
		}
		c.conversationRendered = rendered
		return c.Save()
	}
	if err != nil {
		return err
	}
	if delta != "" {
		if err := c.tr.AppendBlock(delta); err != nil {
			return err
		}
	}
	c.conversationRendered = rendered
	return c.Save()
}

func (c *conversationRecorder) WriteFinal(answer string, step int, capped bool) error {
	if c.conversation == nil || c.tr == nil {
		if c.tr == nil {
			return nil
		}
		return c.tr.WriteFinal(answer, step, capped)
	}
	c.conversation.AddMessage("assistant", answer, map[string]any{
		"type":   "final",
		"turns":  step,
		"capped": capped,
	})
	rendered, delta, err := c.renderDelta()
	if errors.Is(err, errConversationTranscriptDesync) {
		if err := c.tr.WriteFinal(answer, step, capped); err != nil {
			return err
		}
		c.conversationRendered = rendered
		return c.Save()
	}
	if err != nil {
		return err
	}
	if delta != "" {
		if err := c.tr.AppendBlock(delta); err != nil {
			return err
		}
	}
	c.conversationRendered = rendered
	return c.Save()
}

func (c *conversationRecorder) appendRawToTranscript(role, content, metaType string) error {
	if c.tr == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(metaType)) {
	case "user_input_request":
		return c.tr.AppendRaw(fmt.Sprintf("\n=== USER INPUT REQUEST\n\n%s\n", content))
	case "user_input_response":
		return c.tr.AppendRaw(fmt.Sprintf("\n=== USER INPUT RESPONSE\n\n%s\n", content))
	case "":
		if strings.EqualFold(strings.TrimSpace(role), "user") {
			return c.tr.AppendRaw(fmt.Sprintf("\n=== USER MESSAGE\n\n%s\n", content))
		}
		if strings.EqualFold(strings.TrimSpace(role), "assistant") {
			return c.tr.AppendRaw(fmt.Sprintf("\n=== ASSISTANT MESSAGE\n\n%s\n", content))
		}
		return c.tr.AppendRaw(content)
	default:
		return c.tr.AppendRaw(content)
	}
}

// HasResumableShellAgentWorkRequest returns true when the conversation
// contains at least one work_request message with shell_agent_resumable set
// to true that has no corresponding work_result message for the same turn.
func HasResumableShellAgentWorkRequest(conv *conversation.Conversation) bool {
	if conv == nil {
		return false
	}
	return conv.ShellAgentResumable
}

// ExtractResumableWorkRequestQuestion returns the Content of a work_request
// message that has the metadata field shell_agent_resumable set to true and
// no corresponding work_result for the same turn. If none is found it returns
// an empty string.
func ExtractResumableWorkRequestQuestion(conv *conversation.Conversation) string {
	if conv == nil {
		return ""
	}
	if conv.ShellAgentResumable {
		for i := len(conv.Messages) - 1; i >= 0; i-- {
			msg := conv.Messages[i]
			if msgMetaType(msg.Metadata) != "work_request" {
				continue
			}
			turn := msgTurn(msg)
			if turn < 0 {
				continue
			}
			hasResult := false
			for j := i + 1; j < len(conv.Messages); j++ {
				nxt := conv.Messages[j]
				if msgMetaType(nxt.Metadata) != "work_result" {
					continue
				}
				if msgTurn(nxt) == turn {
					hasResult = true
					break
				}
			}
			if !hasResult {
				return msg.Content
			}
		}
	}
	return ""
}

func msgMetaType(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	v, ok := meta["type"]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(s))
}

func msgTurn(msg conversation.Message) int {
	if msg.Turn != nil {
		return *msg.Turn
	}
	return -1
}

func msgMetaBool(val any) bool {
	switch v := val.(type) {
	case bool:
		return v
	case string:
		return strings.ToLower(strings.TrimSpace(v)) == "true"
	}
	return false
}

type runLifecycleState struct {
	rootCtx             context.Context
	cfg                 legacyConfig
	sessionID           string
	goal                string
	originalPrompt      string
	originalGoal        string
	taskDescription     string
	plannerOverlay      string
	modeInstructionPath string
	mode                string
	recorder            *conversationRecorder
	plannerProgress     *plannerProgressTracker
	runtimeMu           sync.Mutex
	runtimeBaseElapsed  time.Duration
	runtimeRunStarted   time.Time
	runtimeTokenUsage   ui.TokenUsageUpdatedEvent
	presentation        presentation.Theme

	repoRoot                string
	trajectoryWriter        *trajectory.Writer
	tr                      *transcript.Transcript
	sessionStatus           string
	sessionErr              error
	turnsCompleted          int
	interrupted             bool
	pendingState            *SessionState
	suspendedUserInput      *conversation.SuspendedUserInputState
	shellAgentInterruptStep int
	shellAgentStepLog       string
}

func (r *runLifecycleState) clearSuspendedUserInput() {
	if r == nil {
		return
	}
	r.suspendedUserInput = nil
	if r.pendingState != nil {
		r.pendingState.SuspendedUserInput = nil
	}
}

func (r *runLifecycleState) transition(target SessionStatus) error {
	err := SessionStatus(r.sessionStatus).Transition(target)
	if err != nil {
		return err
	}
	r.sessionStatus = string(target)
	return nil
}

func (r *runLifecycleState) startRuntimeClock() {
	if r == nil {
		return
	}
	r.runtimeMu.Lock()
	defer r.runtimeMu.Unlock()
	if r.runtimeRunStarted.IsZero() {
		r.runtimeRunStarted = time.Now()
	}
}

func (r *runLifecycleState) runtimeElapsedLocked(now time.Time) time.Duration {
	elapsed := r.runtimeBaseElapsed
	if !r.runtimeRunStarted.IsZero() {
		elapsed += now.Sub(r.runtimeRunStarted)
	}
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func (r *runLifecycleState) runtimeElapsedSnapshot() time.Duration {
	if r == nil {
		return 0
	}
	r.runtimeMu.Lock()
	defer r.runtimeMu.Unlock()
	return r.runtimeElapsedLocked(time.Now())
}

func (r *runLifecycleState) runtimeTokenUsageSnapshot() ui.TokenUsageUpdatedEvent {
	if r == nil {
		return ui.TokenUsageUpdatedEvent{}
	}
	r.runtimeMu.Lock()
	defer r.runtimeMu.Unlock()
	return r.runtimeTokenUsage
}

func (r *runLifecycleState) updateRuntimeTokenUsage(usage ui.TokenUsageUpdatedEvent) {
	if r == nil {
		return
	}
	r.runtimeMu.Lock()
	defer r.runtimeMu.Unlock()
	if usage.InputHit < 0 {
		usage.InputHit = 0
	}
	if usage.InputMiss < 0 {
		usage.InputMiss = 0
	}
	if usage.Output < 0 {
		usage.Output = 0
	}
	r.runtimeTokenUsage = usage
}

func (r *runLifecycleState) runtimeStatsSnapshot() *conversation.RuntimeStatsState {
	if r == nil {
		return nil
	}
	r.runtimeMu.Lock()
	defer r.runtimeMu.Unlock()
	elapsed := r.runtimeElapsedLocked(time.Now())
	return conversation.NewRuntimeStatsState(
		elapsed.Milliseconds(),
		r.runtimeTokenUsage.InputHit,
		r.runtimeTokenUsage.InputMiss,
		r.runtimeTokenUsage.Output,
	)
}

func (r *runLifecycleState) interruptedResult(err error) Result {
	r.interrupted = true
	if err == nil {
		err = context.Canceled
	}
	r.sessionErr = err
	r.transition(StateInterrupted)
	return Result{ExitCode: 130, Status: r.sessionStatus, Turns: r.turnsCompleted, SessionID: r.sessionID, Err: err}
}

func (r *runLifecycleState) interruptedResultWithHint(bus *ui.EventBus, diagWriter io.Writer, err error) Result {
	result := r.interruptedResult(err)
	if r.hasResumableShellAgent() {
		r.printShellAgentResumeHint(bus, diagWriter)
		if bus != nil {
			bus.Emit(ui.SessionEndedEvent{})
		}
	}
	return result
}

func (r *runLifecycleState) hasResumableShellAgent() bool {
	if r == nil || r.recorder == nil {
		return false
	}
	return r.recorder.shellAgentResumable
}

func (r *runLifecycleState) isContextCancelled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	if rootCtxErr := r.rootCtx.Err(); rootCtxErr != nil && errors.Is(rootCtxErr, context.Canceled) {
		return true
	}
	return false
}

func (r *runLifecycleState) printResumeHint(bus *ui.EventBus, diagWriter io.Writer, header string, turns int) {
	command := fmt.Sprintf("mct-agent run \"<next instruction>\" --session-id %s", r.sessionID)
	continueHint := "Continue with your next instruction:\n" + formatCommandBlock(command)
	if !r.cfg.verbose {
		if bus != nil {
			bus.Emit(ui.ContinuationHintEvent{Command: command})
		} else {
			fmt.Fprintln(diagWriter)
			fmt.Fprintln(diagWriter, continueHint)
		}
		return
	}

	if bus != nil {
		bus.Emit(ui.ContinuationHintEvent{
			Header: header,
			DetailLines: []string{
				fmt.Sprintf("Session ID: %s", r.sessionID),
				fmt.Sprintf("Turns completed: %d", turns),
				fmt.Sprintf("Goal so far: %q", r.goal),
			},
			Command: command,
		})
	} else {
		fmt.Fprintln(diagWriter)
		fmt.Fprintf(diagWriter, "%s\nSession ID: %s\nTurns completed: %d\nGoal so far: %q\n\n", header, r.sessionID, turns, r.goal)
		fmt.Fprintln(diagWriter, continueHint)
		fmt.Fprintln(diagWriter)
	}
}

func (r *runLifecycleState) printShellAgentResumeHint(bus *ui.EventBus, diagWriter io.Writer) {
	command := fmt.Sprintf("mct-agent run --session-id %s", r.sessionID)
	if bus != nil {
		bus.Emit(ui.ContinuationHintEvent{
			Header:      "=== SHELL-AGENT INTERRUPTED ===",
			DetailLines: []string{"Shell-agent work is resumable."},
			Instruction: "Resume the interrupted shell-agent work:",
			Command:     command,
		})
		return
	}
	fmt.Fprintln(diagWriter)
	fmt.Fprintln(diagWriter, "=== SHELL-AGENT INTERRUPTED ===")
	fmt.Fprintln(diagWriter, "Shell-agent work is resumable.")
	fmt.Fprintln(diagWriter)
	fmt.Fprintln(diagWriter, "Resume the interrupted shell-agent work:")
	fmt.Fprintln(diagWriter, formatCommandBlock(command))
	fmt.Fprintln(diagWriter)
}

func formatCommandBlock(command string) string {
	command = strings.TrimSpace(command)
	rule := strings.Repeat("-", len("  $ "+command))
	return "  " + rule + "\n  $ " + command + "\n  " + rule
}

func (r *runLifecycleState) printUserInputHint(bus *ui.EventBus, diagWriter io.Writer, question, context string) {
	if bus != nil {
		bus.Emit(ui.UserInputHintEvent{
			SessionID: r.sessionID,
			Context:   strings.TrimSpace(context),
			Question:  strings.TrimSpace(question),
			Command:   fmt.Sprintf("mct-agent run \"<your answer>\" --session-id %s", r.sessionID),
		})
	} else {
		fmt.Fprintln(diagWriter, "=== USER INPUT NEEDED ===")
		fmt.Fprintf(diagWriter, "Session ID: %s\n", r.sessionID)
		if strings.TrimSpace(context) != "" {
			fmt.Fprintf(diagWriter, "%s\n\n", strings.TrimSpace(context))
		}
		fmt.Fprintf(diagWriter, "%s\n\n", strings.TrimSpace(question))
		fmt.Fprintf(diagWriter, "To continue, answer with:\n  mct-agent run \"<your answer>\" --session-id %s\n", r.sessionID)
		fmt.Fprintln(diagWriter)
	}
}

func (r *runLifecycleState) applyPlannerProgress(state *SessionState, diagWriter io.Writer) {
	if state == nil {
		return
	}
	state.PlannerProgress = r.plannerProgress.toState()
	if state.PlannerProgress != nil {
		if err := UpdateModePlanProgress(r.sessionID, state.PlannerProgress); err != nil && r.cfg.verbose {
			fmt.Fprintf(diagWriter, "Warning: failed to update mode plan progress for %s: %v\n", r.sessionID, err)
		}
	}
}

func (r *runLifecycleState) modeInstructionDir() string {
	dir := strings.TrimSpace(r.cfg.modeInstructionDir)
	if dir == "" && strings.TrimSpace(r.modeInstructionPath) != "" {
		dir = filepath.Dir(r.modeInstructionPath)
	}
	return dir
}

func (r *runLifecycleState) baseSessionState() SessionState {
	state := SessionState{
		SessionID:       r.sessionID,
		Goal:            r.goal,
		OriginalPrompt:  r.originalPrompt,
		OriginalGoal:    r.originalGoal,
		TaskDescription: r.taskDescription,
		PlannerOverlay:  r.plannerOverlay,
		Status:          r.sessionStatus,
		TurnsCompleted:  r.turnsCompleted,
		RuntimeStats:    r.runtimeStatsSnapshot(),
	}
	if r.suspendedUserInput != nil {
		state.SuspendedUserInput = r.suspendedUserInput.Clone()
	}
	if r.recorder != nil {
		state.ShellAgentResumable = r.recorder.shellAgentResumable
		state.ShellAgentTrajectoryPath = r.recorder.shellAgentTrajectoryPath
	}
	state.ShellAgentInterruptStep = r.shellAgentInterruptStep
	return state
}

func (r *runLifecycleState) hydrateState(state *SessionState, diagWriter io.Writer) {
	if state == nil {
		return
	}
	state.RuntimeStats = r.runtimeStatsSnapshot()
	// Persist the conversation to disk so that future resumes can
	// regenerate the transcript without relying on inline state copies.
	if r.recorder != nil && r.recorder.HasConversation() {
		r.recorder.EnsureSaved()
	}
	if strings.TrimSpace(state.ModeInstructionDir) == "" {
		state.ModeInstructionDir = r.modeInstructionDir()
	}
	if len(state.Modes) == 0 && strings.TrimSpace(r.cfg.mode) != "" {
		state.Modes = []string{strings.ToLower(strings.TrimSpace(r.cfg.mode))}
		if r.recorder != nil && r.recorder.HasConversation() {
			r.recorder.conversation.Modes = state.Modes
		}
	}
	if state.Status == "" {
		state.Status = r.sessionStatus
	}
	if r.recorder != nil && r.recorder.HasConversation() {
		r.recorder.conversation.Status = state.Status
	}
	if state.SuspendedUserInput == nil && r.suspendedUserInput != nil {
		state.SuspendedUserInput = r.suspendedUserInput.Clone()
	}
	if r.recorder != nil && r.recorder.HasConversation() {
		r.recorder.conversation.SuspendedUserInput = state.SuspendedUserInput
	}
	if r.recorder != nil && r.recorder.HasConversation() {
		r.recorder.conversation.RuntimeStats = state.RuntimeStats.Clone()
	}
	if r.recorder != nil && r.recorder.HasConversation() {
		_ = r.recorder.Save()
	}
	r.applyPlannerProgress(state, diagWriter)
}

func (r *runLifecycleState) persistSessionState(diagWriter io.Writer) {
	sid := strings.TrimSpace(r.sessionID)
	if sid == "" {
		return
	}

	state := r.baseSessionState()
	if r.pendingState != nil {
		state = *r.pendingState
	}
	r.hydrateState(&state, diagWriter)

}

// checkpointTurn saves conversation.json.
func (r *runLifecycleState) checkpointTurn(diagWriter io.Writer) {
	if r.recorder != nil {
		r.recorder.EnsureSaved()
	}
	r.persistSessionState(diagWriter)
}

func formatUserInputRequestContent(question, context string) string {
	question = strings.TrimSpace(question)
	context = strings.TrimSpace(context)
	if context == "" {
		return question
	}
	return question + "\n\nContext:\n" + context
}

func (r *runLifecycleState) suspendForUserInput(bus *ui.EventBus, diagWriter io.Writer, question, context, reason, originalAsk string) (Result, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return Result{}, errors.New("user input question required")
	}
	content := formatUserInputRequestContent(question, context)
	if err := r.recorder.AppendRaw("assistant", content, "user_input_request"); err != nil {
		return Result{}, err
	}
	r.sessionErr = nil
	if err := r.transition(StateSuspendedUserInput); err != nil {
		return Result{}, err
	}
	r.suspendedUserInput = &conversation.SuspendedUserInputState{
		Kind:        "user-directed-ask",
		Question:    strings.TrimSpace(question),
		Context:     strings.TrimSpace(context),
		Reason:      strings.TrimSpace(reason),
		OriginalAsk: strings.TrimSpace(originalAsk),
	}
	state := r.baseSessionState()
	r.pendingState = &state
	r.hydrateState(r.pendingState, diagWriter)
	r.printUserInputHint(bus, diagWriter, question, context)
	if bus != nil {
		bus.Emit(ui.SessionEndedEvent{})
	}
	return Result{ExitCode: 0, Status: r.sessionStatus, Turns: r.turnsCompleted, SessionID: r.sessionID}, nil
}
