package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/runner"
	"github.com/tursomari/machtiani/agent/internal/session/fulldiff"
	"github.com/tursomari/machtiani/agent/internal/tempdir"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

type runBootstrap struct {
	opts                 Options
	cfg                  legacyConfig
	sessionID            string
	goal                 string
	resumePrompt         string
	originalPrompt       string
	taskDescription      string
	plannerOverlay       string
	conversationGoal     string
	conversationPath     string
	resumeMode           bool
	loadedState          *SessionState
	resumeSuspendedInput *SuspendedUserInputState
	metaInstructions     llm.MetaInstructions
	metaInstructionPath  string
	runState             *runLifecycleState
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

func prepareRunBootstrap(rootCtx context.Context, opts Options) (*runBootstrap, Result, bool) {
	inputPrompt := strings.TrimSpace(opts.Goal)
	if inputPrompt == "" {
		fmt.Fprintln(os.Stderr, "Error: empty issue/question provided")
		return nil, Result{ExitCode: 2, Err: errors.New("empty goal")}, false
	}

	originalPrompt := opts.OriginalPrompt
	if strings.TrimSpace(originalPrompt) == "" {
		originalPrompt = opts.Goal
	}
	taskDescription := opts.TaskDescription
	plannerOverlay := opts.PlannerOverlay
	goal := inputPrompt

	cfgInput := opts.Config
	sessionID := strings.TrimSpace(cfgInput.SessionID)
	resumeMode := false
	var loadedState *SessionState
	resumePrompt := ""

	if sessionID != "" {
		state, err := LoadSessionState(sessionID)
		if err != nil {
			if errors.Is(err, ErrSessionStateNotFound) {
				fmt.Fprintf(os.Stderr, "Error: no saved session found for %s.\n", sessionID)
				return nil, Result{ExitCode: 2, Err: err}, false
			}
			fmt.Fprintln(os.Stderr, "Error loading session state:", err)
			return nil, Result{ExitCode: 1, Err: err}, false
		}
		resumeMode = true
		loadedState = state
		resumePrompt = strings.TrimSpace(inputPrompt)

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
		if strings.TrimSpace(cfgInput.Mode) == "" && len(state.MetaModes) > 0 {
			cfgInput.Mode = strings.TrimSpace(state.MetaModes[0])
		}
		if strings.TrimSpace(cfgInput.MetaInstructionDir) == "" && strings.TrimSpace(state.MetaInstructionDir) != "" {
			cfgInput.MetaInstructionDir = strings.TrimSpace(state.MetaInstructionDir)
		}
	}

	if goal == "" {
		goal = resumePrompt
	}
	if goal == "" {
		fmt.Fprintln(os.Stderr, "Error: unable to determine session goal")
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
		sessionID = runner.GenerateSessionID()
	}
	_ = os.Setenv("MACHTIANI_SESSION_ID", sessionID)

	conversationPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error resolving conversation path:", err)
		return nil, Result{ExitCode: 1, Err: err}, false
	}

	cfgInput.SessionID = sessionID
	cfg := newLegacyConfig(cfgInput)
	opts.Config = cfgInput
	applyTrajectoryEnvOverrides(&cfg)
	if err := cleanupOrphanedTempDirs(cfg.verbose); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to cleanup orphaned temp dirs: %v\n", err)
	}

	metaInstructions := llm.MetaInstructions{}
	metaInstructionPath := ""
	if strings.TrimSpace(cfg.mode) != "" {
		doc, err := llm.LoadMetaInstructions(cfg.mode, cfg.metaInstructionDir, opts.GlobalConfig, opts.GlobalConfigPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error loading meta instructions:", err)
			return nil, Result{ExitCode: 1, Err: err}, false
		}
		metaInstructions = doc
		metaInstructionPath = doc.Path
	}

	runState := newRunLifecycleState(rootCtx, cfg, sessionID, goal, originalPrompt, taskDescription, plannerOverlay, metaInstructionPath, loadedState)

	return &runBootstrap{
		opts:                 opts,
		cfg:                  cfg,
		sessionID:            sessionID,
		goal:                 goal,
		resumePrompt:         resumePrompt,
		originalPrompt:       originalPrompt,
		taskDescription:      taskDescription,
		plannerOverlay:       plannerOverlay,
		conversationGoal:     conversationGoal,
		conversationPath:     conversationPath,
		resumeMode:           resumeMode,
		loadedState:          loadedState,
		resumeSuspendedInput: loadedStateSuspendedInput(loadedState),
		metaInstructions:     metaInstructions,
		metaInstructionPath:  metaInstructionPath,
		runState:             runState,
	}, Result{}, true
}

func loadedStateSuspendedInput(state *SessionState) *SuspendedUserInputState {
	if state == nil || state.SuspendedUserInput == nil {
		return nil
	}
	return state.SuspendedUserInput.Clone()
}

func prepareSessionEnvironment(sessionID string, cfg legacyConfig) (*sessionEnvironmentBootstrap, error) {
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
	tmpRoot := ""
	if globalConfig.Environment != nil {
		tmpRoot = strings.TrimSpace(globalConfig.Environment.TmpRoot)
	}
	if tmpRoot == "" {
		tmpRoot = ".machtiani/tmp"
	}
	if abs, err := filepath.Abs(tmpRoot); err == nil {
		tmpRoot = abs
	}
	workspaceRoot := filepath.Join(tmpRoot, "workspace-"+sessionID)
	useSnapshotWorkspace := !isLocalSessionEnvironment(&globalConfig)

	patchStrategy := ""
	if globalConfig.Patcher != nil {
		patchStrategy = strings.TrimSpace(globalConfig.Patcher.Strategy)
	}
	if patchStrategy == "" {
		patchStrategy = "export-only"
	}
	if err := os.Setenv("MACHTIANI_PATCH_STRATEGY", patchStrategy); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: unable to export patch strategy:", err)
	}
	// In non-local environments the child tools should create temp data inside
	// the snapshot workspace, not beside the host-side session lock. During the
	// run MACHTIANI_TMP_ROOT is therefore repurposed from "scratch root override"
	// to "workspace root".
	if useSnapshotWorkspace {
		if err := os.Setenv("MACHTIANI_TMP_ROOT", workspaceRoot); err != nil {
			fmt.Fprintln(os.Stderr, "Warning: unable to export tmp root:", err)
		}
	} else if err := os.Unsetenv("MACHTIANI_TMP_ROOT"); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: unable to clear tmp root:", err)
	}
	if err := os.Setenv("MACHTIANI_SESSION_TEMP_ROOT", sessionTempRoot); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: unable to export session temp root:", err)
	}

	markerMaxAge, markerMaxAgeErr := shellAgentMarkerMaxAge()
	if markerMaxAgeErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v; using default %s\n", markerMaxAgeErr, defaultShellAgentMarkerMaxAge)
	}
	if err := cleanupStaleShellAgentMarkers(sessionTempRoot, markerMaxAge, cfg.verbose); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to cleanup shell-agent markers: %v\n", err)
	}

	cleanupSessionRoot := origSessionTempRoot == ""
	restore := func() {
		if strings.TrimSpace(workspaceRoot) != "" && !cfg.persistTmpData {
			if err := os.RemoveAll(workspaceRoot); err != nil && cfg.verbose {
				fmt.Fprintf(os.Stderr, "Warning: failed to cleanup workspace root %s: %v\n", workspaceRoot, err)
			}
		}
		if cleanupSessionRoot && !cfg.persistTmpData {
			if err := os.RemoveAll(sessionTempRoot); err != nil && cfg.verbose {
				fmt.Fprintf(os.Stderr, "Warning: failed to cleanup session temp root %s: %v\n", sessionTempRoot, err)
			}
		}
		tempdir.ClearSessionRoot()
		if strings.TrimSpace(origTmpRootRaw) == "" {
			_ = os.Unsetenv("MACHTIANI_TMP_ROOT")
		} else {
			_ = os.Setenv("MACHTIANI_TMP_ROOT", origTmpRootRaw)
		}
		_ = os.Unsetenv("MACHTIANI_PATCH_STRATEGY")
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

func prepareTranscriptBootstrap(cfg legacyConfig, sessionID, conversationGoal, conversationPath string, resumeMode bool, loadedState *SessionState, trajectoryWriter *trajectory.Writer, repoRoot string, notePrompts *llm.MCTPromptsConfig, runState *runLifecycleState) (*transcriptBootstrap, error) {
	resumeTranscript := loadResumeTranscript(cfg, loadedState, resumeMode, sessionID)
	runState.notePrompts = notePrompts

	tr, err := transcript.NewWithPath(cfg.transcriptFile, sessionID)
	if err != nil {
		return nil, err
	}
	tr.SetTrajectory(trajectoryWriter)
	runState.tr = tr
	runState.repoRoot = repoRoot
	runState.trajectoryWriter = trajectoryWriter

	recorder := newConversationRecorder(tr, sessionID, conversationGoal, conversationPath, resumeMode, loadedState)
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
func loadResumeTranscript(cfg legacyConfig, loadedState *SessionState, resumeMode bool, sessionID string) string {
	if !resumeMode || loadedState == nil {
		return ""
	}
	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		if cfg.verbose {
			fmt.Fprintf(os.Stderr, "Warning: failed to resolve conversation path for resume: %v\n", err)
		}
		return ""
	}
	data, err := os.ReadFile(convPath)
	if err != nil {
		if cfg.verbose && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "Warning: failed to read conversation for resume (%s): %v\n", convPath, err)
		}
		return ""
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		if cfg.verbose {
			fmt.Fprintf(os.Stderr, "Warning: failed to decode conversation for resume: %v\n", err)
		}
		return ""
	}
	rendered, err := conv.ToTranscript()
	if err != nil {
		if cfg.verbose {
			fmt.Fprintf(os.Stderr, "Warning: failed to render transcript for resume: %v\n", err)
		}
		return ""
	}
	return rendered
}

func newRunLifecycleState(rootCtx context.Context, cfg legacyConfig, sessionID, goal, originalPrompt, taskDescription, plannerOverlay, metaInstructionPath string, loadedState *SessionState) *runLifecycleState {
	turnsCompleted := 0
	if loadedState != nil && loadedState.TurnsCompleted > 0 {
		turnsCompleted = loadedState.TurnsCompleted
	}
	plannerProgress := newPlannerProgressTracker(nil)
	if loadedState != nil {
		plannerProgress = newPlannerProgressTracker(loadedState.PlannerProgress)
	}
	return &runLifecycleState{
		rootCtx:             rootCtx,
		cfg:                 cfg,
		sessionID:           sessionID,
		goal:                goal,
		originalPrompt:      originalPrompt,
		taskDescription:     taskDescription,
		plannerOverlay:      plannerOverlay,
		metaInstructionPath: metaInstructionPath,
		plannerProgress:     plannerProgress,
		pendingPatchDraft:   pendingPatchDraftFromState(loadedState),
		suspendedUserInput:  loadedStateSuspendedInput(loadedState),
		sessionStatus:       "error",
		turnsCompleted:      turnsCompleted,
		userTurnCounter:     turnsCompleted,
	}
}

func pendingPatchDraftFromState(loadedState *SessionState) *patchTranscriptDraft {
	if loadedState == nil || loadedState.PendingPatchTurn == nil {
		return nil
	}
	return &patchTranscriptDraft{
		Step:        loadedState.PendingPatchTurn.Step,
		Description: strings.TrimSpace(loadedState.PendingPatchTurn.Description),
		Answer:      loadedState.PendingPatchTurn.Answer,
	}
}

type conversationRecorder struct {
	tr                   *transcript.Transcript
	sessionID            string
	conversationGoal     string
	conversationPath     string
	resumeMode           bool
	loadedState          *SessionState
	conversation         *conversation.Conversation
	conversationRendered string
	conversationJSON     string
}

var errConversationTranscriptDesync = errors.New("conversation transcript desync")

func newConversationRecorder(tr *transcript.Transcript, sessionID, conversationGoal, conversationPath string, resumeMode bool, loadedState *SessionState) *conversationRecorder {
	return &conversationRecorder{
		tr:               tr,
		sessionID:        sessionID,
		conversationGoal: conversationGoal,
		conversationPath: conversationPath,
		resumeMode:       resumeMode,
		loadedState:      loadedState,
	}
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
	if c.conversation == nil || c.tr == nil {
		if c.tr == nil {
			return nil
		}
		return c.tr.WriteTurn(step, question, savedPath, retrieved, summary, decision)
	}
	c.conversation.AddMessage("assistant", question, map[string]any{
		"type":     "work_request",
		"turn":     step,
		"decision": decision,
	})
	c.conversation.AddMessage("assistant", summary, map[string]any{
		"type":            "work_result",
		"turn":            step,
		"retrieved_files": retrieved,
		"chat_path":       savedPath,
	})
	rendered, delta, err := c.renderDelta()
	if errors.Is(err, errConversationTranscriptDesync) {
		if err := c.tr.WriteTurn(step, question, savedPath, retrieved, summary, decision); err != nil {
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

func (c *conversationRecorder) RecordFullDiff(step int, file, diff, note string) error {
	if c.conversation == nil {
		return nil
	}
	c.conversation.AddMessage("assistant", note, map[string]any{
		"type": "full_diff",
		"turn": step,
		"file": strings.TrimSpace(file),
		"diff": diff,
	})
	return c.Save()
}

// RecordPatchValidation writes a patch validation block to both the
// conversation and the transcript. The canonical block is rendered once and
// stored verbatim in the conversation so that regenerating the transcript
// from the conversation produces identical output.
func (c *conversationRecorder) RecordPatchValidation(step int, record transcript.PatchValidationRecord) error {
	block := transcript.FormatPatchValidation(step, record)
	if strings.TrimSpace(block) == "" {
		return nil
	}
	if c.conversation != nil {
		c.conversation.AddMessage("assistant", block, map[string]any{
			"type": "patch_validation",
			"turn": step,
		})
	}
	if c.tr != nil {
		if err := c.tr.AppendBlock(block); err != nil {
			return err
		}
	}
	c.resyncRendered()
	return c.Save()
}

// RecordPatchPlanCreated records an initial patch plan in the conversation
// and transcript.
func (c *conversationRecorder) RecordPatchPlanCreated(step int, planDetails string) error {
	block := transcript.FormatPatchPlanCreated(step, planDetails)
	return c.recordPatchPlanBlock(step, block, "patch_plan_created")
}

// RecordPatchPlanUpdated records an updated patch plan in the conversation
// and transcript.
func (c *conversationRecorder) RecordPatchPlanUpdated(step int, planDetails string) error {
	block := transcript.FormatPatchPlanUpdated(step, planDetails)
	return c.recordPatchPlanBlock(step, block, "patch_plan_updated")
}

func (c *conversationRecorder) recordPatchPlanBlock(step int, block, metaType string) error {
	if strings.TrimSpace(block) == "" {
		return nil
	}
	if c.conversation != nil {
		c.conversation.AddMessage("assistant", block, map[string]any{
			"type": metaType,
			"turn": step,
		})
	}
	if c.tr != nil {
		if err := c.tr.AppendBlock(block); err != nil {
			return err
		}
		// The on-disk transcript dedupes older patch plan sections so that
		// only the most recent plan remains.
		if err := c.tr.DeduplicatePatchPlan(); err != nil {
			// Non-fatal: dedup is best-effort housekeeping.
			fmt.Fprintf(os.Stderr, "[patch-plan] deduplicate failed: %v\n", err)
		}
	}
	c.resyncRendered()
	return c.Save()
}

// resyncRendered recomputes the rendered transcript snapshot from the stored
// conversation. Callers should use this after writes that bypass the
// incremental renderDelta path (for example, pre-rendered blocks or blocks
// that trigger in-conversation dedup of earlier events).
func (c *conversationRecorder) resyncRendered() {
	if c.conversation == nil {
		return
	}
	rendered, err := c.conversation.ToTranscript()
	if err != nil {
		fmt.Fprintf(os.Stderr, "session: resyncRendered failed: %v\n", err)
		return
	}
	c.conversationRendered = rendered
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

type runLifecycleState struct {
	rootCtx             context.Context
	cfg                 legacyConfig
	sessionID           string
	goal                string
	originalPrompt      string
	taskDescription     string
	plannerOverlay      string
	metaInstructionPath string
	recorder            *conversationRecorder
	plannerProgress     *plannerProgressTracker
	pendingPatchDraft   *patchTranscriptDraft
	notePrompts         *llm.MCTPromptsConfig
	repoRoot            string
	trajectoryWriter    *trajectory.Writer
	tr                  *transcript.Transcript
	sessionStatus       string
	sessionErr          error
	turnsCompleted      int
	userTurnCounter     int
	interrupted         bool
	pendingState        *SessionState
	suspendedUserInput  *SuspendedUserInputState
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

func (r *runLifecycleState) interruptedResult(err error) Result {
	r.interrupted = true
	if err == nil {
		err = context.Canceled
	}
	r.sessionErr = err
	r.sessionStatus = "interrupted"
	r.turnsCompleted = r.userTurnCounter
	return Result{ExitCode: 130, Status: r.sessionStatus, Turns: r.turnsCompleted, SessionID: r.sessionID, Err: err}
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

func (r *runLifecycleState) printResumeHint(header string, turns int) {
	fmt.Fprintf(os.Stdout, "%s\nSession ID: %s\nTurns completed: %d\nGoal so far: %q\n\n", header, r.sessionID, turns, r.goal)
	fmt.Fprintf(os.Stdout, "To continue, provide your next instruction, for example:\n  mct-agent run \"<next instruction>\" --session-id %s\n", r.sessionID)
	fmt.Fprintln(os.Stdout)
}

func (r *runLifecycleState) printUserInputHint(question, context string) {
	fmt.Fprintln(os.Stdout, "=== USER INPUT NEEDED ===")
	fmt.Fprintf(os.Stdout, "Session ID: %s\n", r.sessionID)
	if strings.TrimSpace(context) != "" {
		fmt.Fprintf(os.Stdout, "%s\n\n", strings.TrimSpace(context))
	}
	fmt.Fprintf(os.Stdout, "%s\n\n", strings.TrimSpace(question))
	fmt.Fprintf(os.Stdout, "To continue, answer with:\n  mct-agent run \"<your answer>\" --session-id %s\n", r.sessionID)
	fmt.Fprintln(os.Stdout)
}

func (r *runLifecycleState) applyPlannerProgress(state *SessionState) {
	if state == nil {
		return
	}
	state.PlannerProgress = r.plannerProgress.toState()
	if r.pendingPatchDraft != nil {
		state.PendingPatchTurn = &PendingPatchTurnState{
			Step:        r.pendingPatchDraft.Step,
			Description: r.pendingPatchDraft.Description,
			Answer:      r.pendingPatchDraft.Answer,
		}
	} else {
		state.PendingPatchTurn = nil
	}
	if state.PlannerProgress != nil {
		if err := UpdateMetaPlanProgress(r.sessionID, state.PlannerProgress); err != nil && r.cfg.verbose {
			fmt.Fprintf(os.Stderr, "Warning: failed to update meta plan progress for %s: %v\n", r.sessionID, err)
		}
	}
}

func (r *runLifecycleState) metaInstructionDir() string {
	dir := strings.TrimSpace(r.cfg.metaInstructionDir)
	if dir == "" && strings.TrimSpace(r.metaInstructionPath) != "" {
		dir = filepath.Dir(r.metaInstructionPath)
	}
	return dir
}

func (r *runLifecycleState) baseSessionState() SessionState {
	state := SessionState{
		SessionID:       r.sessionID,
		Goal:            r.goal,
		OriginalPrompt:  r.originalPrompt,
		TaskDescription: r.taskDescription,
		PlannerOverlay:  r.plannerOverlay,
		Status:          r.sessionStatus,
		TurnsCompleted:  r.turnsCompleted,
	}
	if r.suspendedUserInput != nil {
		state.SuspendedUserInput = r.suspendedUserInput.Clone()
	}
	return state
}

func (r *runLifecycleState) hydrateState(state *SessionState) {
	if state == nil {
		return
	}
	// Persist the conversation to disk so that future resumes can
	// regenerate the transcript without relying on inline state copies.
	if r.recorder != nil && r.recorder.HasConversation() {
		r.recorder.EnsureSaved()
	}
	if strings.TrimSpace(state.MetaInstructionDir) == "" {
		state.MetaInstructionDir = r.metaInstructionDir()
	}
	if len(state.MetaModes) == 0 && strings.TrimSpace(r.cfg.mode) != "" {
		state.MetaModes = []string{strings.ToLower(strings.TrimSpace(r.cfg.mode))}
	}
	if state.Status == "" {
		state.Status = r.sessionStatus
	}
	if state.SuspendedUserInput == nil && r.suspendedUserInput != nil {
		state.SuspendedUserInput = r.suspendedUserInput.Clone()
	}
	r.applyPlannerProgress(state)
}

func (r *runLifecycleState) persistSessionState() {
	sid := strings.TrimSpace(r.sessionID)
	if sid == "" {
		return
	}

	state := r.baseSessionState()
	if r.pendingState != nil {
		state = *r.pendingState
	}
	r.hydrateState(&state)

	if err := SaveSessionState(state); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save session state for %s: %v\n", sid, err)
		return
	}

	if r.interrupted {
		r.printResumeHint("=== SESSION INTERRUPTED ===", r.turnsCompleted)
	}
}

func formatUserInputRequestContent(question, context string) string {
	question = strings.TrimSpace(question)
	context = strings.TrimSpace(context)
	if context == "" {
		return question
	}
	return question + "\n\nContext:\n" + context
}

func (r *runLifecycleState) suspendForUserInput(display *ui.TerminalDisplay, question, context, reason, originalAsk string) (Result, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return Result{}, errors.New("user input question required")
	}
	content := formatUserInputRequestContent(question, context)
	if err := r.recorder.AppendRaw("assistant", content, "user_input_request"); err != nil {
		return Result{}, err
	}
	if display != nil {
		display.EndSession()
	}
	r.sessionErr = nil
	r.turnsCompleted = r.userTurnCounter
	r.sessionStatus = "suspended_user_input"
	r.suspendedUserInput = &SuspendedUserInputState{
		Kind:        "user-directed-ask",
		Question:    strings.TrimSpace(question),
		Context:     strings.TrimSpace(context),
		Reason:      strings.TrimSpace(reason),
		OriginalAsk: strings.TrimSpace(originalAsk),
	}
	state := r.baseSessionState()
	r.pendingState = &state
	r.hydrateState(r.pendingState)
	r.printUserInputHint(question, context)
	return Result{ExitCode: 0, Status: r.sessionStatus, Turns: r.turnsCompleted, SessionID: r.sessionID}, nil
}

func (r *runLifecycleState) writePendingPatchTranscript(status string, decision string, note string, undo bool) error {
	if r.pendingPatchDraft == nil {
		return nil
	}
	desc := strings.TrimSpace(r.pendingPatchDraft.Description)
	if desc == "" {
		desc = "Patch applied"
	}
	normalized := strings.ToLower(strings.TrimSpace(status))
	if normalized != "success" && normalized != "reject" {
		normalized = status
	}
	suffix := desc
	if strings.TrimSpace(suffix) != "" {
		suffix = " - " + strings.TrimSpace(suffix)
	}
	question := fmt.Sprintf("Patcher: %s%s", normalized, suffix)
	summary := r.pendingPatchDraft.Answer
	if strings.ToLower(status) == "rejected" || strings.ToLower(status) == "reject" {
		summary = ""
	}
	if strings.ToLower(status) == "success" {
		question = desc
		if noteText := transcript.PatchSuccessNoteText(r.notePrompts); noteText != "" {
			question = desc + "\n\n" + noteText
		}
		decision = ""
	}
	additional := []string{}
	if trimmedNote := strings.TrimSpace(note); trimmedNote != "" {
		additional = append(additional, "Planner review note: "+trimmedNote)
	}
	if undo {
		additional = append(additional, "Planner applied reverse patch to undo the changes.")
	}
	if len(additional) > 0 {
		summary = strings.TrimRight(summary, "\n")
		if summary != "" {
			summary += "\n\n"
		}
		summary += strings.Join(additional, "\n")
	}
	if err := r.recorder.WriteTurn(r.pendingPatchDraft.Step, question, "", nil, summary, decision); err != nil {
		return err
	}
	if strings.ToLower(status) == "success" {
		baseline, err := patchersvc.EnsureBaseline(r.sessionID, r.repoRoot, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "[full-diff] Failed to ensure baseline: %v\n", err)
		} else {
			files := []string(nil)
			if review := r.plannerProgress.pendingReviewInfo(); review != nil {
				files = append(files, review.Files...)
			}
			if len(files) == 0 {
				files = append(files, r.plannerProgress.successList()...)
			}
			fulldiff.Inject(r.pendingPatchDraft.Step, r.repoRoot, files, r.tr, r.plannerProgress, fulldiff.Options{
				Verbose:        r.cfg.verbose,
				Baseline:       baseline,
				FullDiffNote:   transcript.FullDiffNoteText(r.notePrompts),
				RecordFullDiff: r.recorder.RecordFullDiff,
			})
			if r.trajectoryWriter != nil {
				r.trajectoryWriter.Emit(r.rootCtx, trajectory.Event{
					Kind: "transcript_synthetic_full_diff",
					Payload: map[string]any{
						"op":    "full_diff",
						"step":  r.pendingPatchDraft.Step + 1,
						"files": files,
					},
				})
			}
			r.userTurnCounter += len(files)
		}
	}
	r.pendingPatchDraft = nil
	if r.pendingState != nil {
		r.pendingState.PendingPatchTurn = nil
	}
	return nil
}
