package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	mctsync "github.com/tursomari/machtiani/agent/internal/mct"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	promptsvc "github.com/tursomari/machtiani/agent/internal/mct/prompt"
	"github.com/tursomari/machtiani/agent/internal/mct/readmesync"
	"github.com/tursomari/machtiani/agent/internal/parser"
	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/runner"
	"github.com/tursomari/machtiani/agent/internal/session/fulldiff"
	"github.com/tursomari/machtiani/agent/internal/tempdir"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
	"github.com/tursomari/machtiani/agent/internal/workspace"
	"time"
)

const (
	backgroundQuestionPrompt = "Give me the background of the project."
	backgroundFallbackAnswer = "No project documentation has been created yet. Please run `mct-agent sync` to generate initial project documentation."
	patchTranscriptDiffLimit = 0 // zero disables transcript diff truncation
	patchReviewDisabled      = true
)

var (
	readmeHeadCommitFn       = readmesync.HeadCommit
	readmeCommitForProjectFn = readmesync.READMECommitForProject
	readmeCheckoutReadonlyFn = readmesync.CheckoutReadonlyREADME
	tagFormatPattern         = regexp.MustCompile(`\[(?P<path>[^\[\]|]+?)\s*\|\s*(?P<start>[^:\]]+)\s*:\s*(?P<end>[^\]]+)\]`)
	rewriteMissingPattern    = regexp.MustCompile(`edit\[(\d+)\]\s+rewrite requires existing file`)
	workspaceRoot            string
)

type plannerProgressTracker struct {
	successSet       map[string]struct{}
	successFiles     []string
	applied          int
	forceRepatchHint bool
	fileDedupSet     map[string]string
	pendingReview    *planner.PendingReview
}

type patchTranscriptDraft struct {
	Step        int
	Description string
	Answer      string
}

func newPlannerProgressTracker(existing *PlannerProgressState) *plannerProgressTracker {
	tracker := &plannerProgressTracker{
		successSet:   make(map[string]struct{}),
		fileDedupSet: make(map[string]string),
	}
	if existing == nil {
		return tracker
	}
	tracker.applied = existing.AppliedPatches
	if existing.PendingReview != nil {
		tracker.pendingReview = existing.PendingReview.Clone()
	}
	for _, raw := range existing.SuccessFiles {
		norm := normalizePlannerPath(raw)
		if norm == "" {
			continue
		}
		if _, exists := tracker.successSet[norm]; exists {
			continue
		}
		tracker.successSet[norm] = struct{}{}
		tracker.successFiles = append(tracker.successFiles, norm)
	}
	return tracker
}

func normalizePlannerPath(path string) string {
	return filepath.ToSlash(strings.TrimSpace(path))
}

func (p *plannerProgressTracker) shouldDeduplicateFile(path string, contentHash string) bool {
	if p == nil {
		return false
	}
	norm := normalizePlannerPath(path)
	if norm == "" {
		return false
	}
	lastHash, exists := p.fileDedupSet[norm]
	return exists && lastHash == contentHash
}

func (p *plannerProgressTracker) updateDeduplicationState(path string, contentHash string) {
	if p == nil {
		return
	}
	norm := normalizePlannerPath(path)
	if norm == "" {
		return
	}
	p.fileDedupSet[norm] = contentHash
}

func (p *plannerProgressTracker) ShouldDeduplicateFile(path string, contentHash string) bool {
	return p.shouldDeduplicateFile(path, contentHash)
}

func (p *plannerProgressTracker) UpdateDeduplicationState(path string, contentHash string) {
	p.updateDeduplicationState(path, contentHash)
}

func (p *plannerProgressTracker) recordSuccess(files []string) {
	if p == nil {
		return
	}
	for _, raw := range files {
		norm := normalizePlannerPath(raw)
		if norm == "" {
			continue
		}
		if _, exists := p.successSet[norm]; exists {
			continue
		}
		p.successSet[norm] = struct{}{}
		p.successFiles = append(p.successFiles, norm)
	}
	p.applied++
}

func startTranscriptIfNeeded(tr *transcript.Transcript, goal, sessionID string, cfg legacyConfig, resumeMode bool) (bool, error) {
	if tr == nil {
		return false, nil
	}
	starting := !resumeMode || tr.Content() == ""
	if starting {
		if err := tr.WriteHeader(goal, sessionID, cfg); err != nil {
			return false, err
		}
	}
	return starting, nil
}

func writeInitialBackgroundIfNeeded(tr *transcript.Transcript, repoRoot string, cfg legacyConfig, isChildSession bool, startingTranscript bool) error {
	if tr == nil || !startingTranscript {
		return nil
	}
	if isChildSession && !cfg.includeBackgroundTurn {
		return nil
	}
	prefillAnswer := backgroundFallbackAnswer
	if backgroundText, err := loadProjectBackground(repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: unable to load project background; falling back to sync prompt: %v\n", err)
	} else {
		if cfg.verbose {
			fmt.Fprintf(os.Stderr, "Loaded internal README background (%d bytes).\n", len(backgroundText))
		}
		prefillAnswer = backgroundText
	}
	return tr.WriteTurn(0, backgroundQuestionPrompt, "", nil, prefillAnswer, "background")
}

func (p *plannerProgressTracker) appliedCount() int {
	if p == nil {
		return 0
	}
	return p.applied
}

func (p *plannerProgressTracker) noteForceRepatchHint() {
	if p == nil {
		return
	}
	p.forceRepatchHint = true
}

func (p *plannerProgressTracker) hasSuccess(path string) bool {
	if p == nil {
		return false
	}
	_, ok := p.successSet[normalizePlannerPath(path)]
	return ok
}

func (p *plannerProgressTracker) successList() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.successFiles...)
}

func (p *plannerProgressTracker) hasPendingReview() bool {
	if p == nil {
		return false
	}
	return p.pendingReview != nil
}

func (p *plannerProgressTracker) pendingReviewInfo() *planner.PendingReview {
	if p == nil || p.pendingReview == nil {
		return nil
	}
	return p.pendingReview
}

func (p *plannerProgressTracker) beginPendingReview(review *planner.PendingReview) {
	if p == nil {
		return
	}
	if review == nil {
		p.pendingReview = nil
		return
	}
	p.pendingReview = review.Clone()
}

func (p *plannerProgressTracker) commitPendingReview() *planner.PendingReview {
	if p == nil || p.pendingReview == nil {
		return nil
	}
	reviewCopy := p.pendingReview.Clone()
	p.recordSuccess(p.pendingReview.Files)
	p.pendingReview = nil
	return reviewCopy
}

func (p *plannerProgressTracker) discardPendingReview() *planner.PendingReview {
	if p == nil || p.pendingReview == nil {
		return nil
	}
	reviewCopy := p.pendingReview.Clone()
	p.pendingReview = nil
	return reviewCopy
}

func (p *plannerProgressTracker) toState() *PlannerProgressState {
	if p == nil {
		return nil
	}
	if p.applied == 0 && len(p.successFiles) == 0 && p.pendingReview == nil {
		return nil
	}
	state := &PlannerProgressState{
		SuccessFiles:   append([]string(nil), p.successFiles...),
		AppliedPatches: p.applied,
	}
	if p.pendingReview != nil {
		state.PendingReview = p.pendingReview.Clone()
	}
	return state
}

func (p *plannerProgressTracker) snapshot() planner.Progress {
	if p == nil {
		return planner.Progress{}
	}
	hint := p.forceRepatchHint
	p.forceRepatchHint = false
	return planner.Progress{
		SuccessFiles:        p.successList(),
		AppliedPatches:      p.appliedCount(),
		ForceRepatchExample: hint,
		PendingReview:       p.pendingReview.Clone(),
	}
}

func Run(ctx context.Context, opts Options) Result {
	if opts.Context != nil {
		ctx = opts.Context
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rootCtx := ctx

	inputPrompt := strings.TrimSpace(opts.Goal)
	if inputPrompt == "" {
		fmt.Fprintln(os.Stderr, "Error: empty issue/question provided")
		return Result{ExitCode: 2, Err: errors.New("empty goal")}
	}
	goal := inputPrompt

	cfgInput := opts.Config
	timerMgr := opts.ProcessTimerManager
	sessionID := strings.TrimSpace(cfgInput.SessionID)
	resumeMode := false
	var loadedState *SessionState
	resumePrompt := ""

	if sessionID != "" {
		state, err := LoadSessionState(sessionID)
		if err != nil {
			if errors.Is(err, ErrSessionStateNotFound) {
				fmt.Fprintf(os.Stderr, "Error: no saved session found for %s.\n", sessionID)
				return Result{ExitCode: 2, Err: err}
			}
			fmt.Fprintln(os.Stderr, "Error loading session state:", err)
			return Result{ExitCode: 1, Err: err}
		}
		resumeMode = true
		loadedState = state
		resumePrompt = strings.TrimSpace(inputPrompt)
		turnsCompletedFromState := state.TurnsCompleted

		storedGoal := strings.TrimSpace(state.Goal)
		if storedGoal != "" {
			goal = storedGoal
		}
		if goal == "" {
			goal = resumePrompt
		}
		_ = turnsCompletedFromState
		if strings.TrimSpace(cfgInput.TranscriptFile) == "" && strings.TrimSpace(state.TranscriptPath) != "" {
			cfgInput.TranscriptFile = state.TranscriptPath
		}
		if strings.TrimSpace(cfgInput.Mode) == "" && len(state.MetaModes) > 0 {
			cfgInput.Mode = strings.TrimSpace(state.MetaModes[0])
		}
		if strings.TrimSpace(cfgInput.MetaInstructionDir) == "" && strings.TrimSpace(state.MetaInstructionDir) != "" {
			cfgInput.MetaInstructionDir = strings.TrimSpace(state.MetaInstructionDir)
		}
		if strings.TrimSpace(cfgInput.ParentSessionID) == "" && strings.TrimSpace(state.ParentSessionID) != "" {
			cfgInput.ParentSessionID = strings.TrimSpace(state.ParentSessionID)
		}
	}
	if goal == "" {
		goal = resumePrompt
	}
	if goal == "" {
		fmt.Fprintln(os.Stderr, "Error: unable to determine session goal")
		return Result{ExitCode: 1, Err: errors.New("missing session goal")}
	}

	if sessionID == "" {
		sessionID = runner.GenerateSessionID()
	}
	_ = os.Setenv("MACHTIANI_SESSION_ID", sessionID)

	cfgInput.SessionID = sessionID

	cfg := newLegacyConfig(cfgInput)
	opts.Config = cfgInput
	applyTrajectoryEnvOverrides(&cfg)
	if err := cleanupOrphanedTempDirs(cfg.verbose); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to cleanup orphaned temp dirs: %v\n", err)
	}
	metaInstructions := llm.MetaInstructions{}
	metaInstructionPath := ""
	if strings.TrimSpace(cfg.mode) != "" && strings.TrimSpace(cfg.parentSessionID) == "" {
		doc, err := llm.LoadMetaInstructions(cfg.mode, cfg.metaInstructionDir, opts.GlobalConfig, opts.GlobalConfigPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error loading meta instructions:", err)
			return Result{ExitCode: 1, Err: err}
		}
		metaInstructions = doc
		metaInstructionPath = doc.Path
	}

	sessionStatus := "error"
	var sessionErr error
	turnsCompleted := 0
	if resumeMode && loadedState != nil && loadedState.TurnsCompleted > 0 {
		turnsCompleted = loadedState.TurnsCompleted
	}
	interrupted := false
	keepSessionState := false
	var pendingState *SessionState
	lastAnswer := ""
	retrieved := []string{}
	userTurnCounter := turnsCompleted
	var pendingPatchDraft *patchTranscriptDraft
	plannerProgress := newPlannerProgressTracker(nil)
	if loadedState != nil {
		plannerProgress = newPlannerProgressTracker(loadedState.PlannerProgress)
		if loadedState.PendingPatchTurn != nil {
			pendingPatchDraft = &patchTranscriptDraft{
				Step:        loadedState.PendingPatchTurn.Step,
				Description: strings.TrimSpace(loadedState.PendingPatchTurn.Description),
				Answer:      loadedState.PendingPatchTurn.Answer,
			}
		}
	}
	interruptedResult := func(err error) Result {
		interrupted = true
		if err == nil {
			err = context.Canceled
		}
		sessionErr = err
		sessionStatus = "interrupted"
		turnsCompleted = userTurnCounter
		return Result{ExitCode: 130, Status: sessionStatus, Turns: turnsCompleted, SessionID: sessionID, Err: err}
	}
	isContextCancelled := func(err error) bool {
		if err == nil {
			return false
		}
		if errors.Is(err, context.Canceled) {
			return true
		}
		if rootCtxErr := rootCtx.Err(); rootCtxErr != nil && errors.Is(rootCtxErr, context.Canceled) {
			return true
		}
		return false
	}
	origSessionTempRootRaw := os.Getenv("MACHTIANI_SESSION_TEMP_ROOT")
	origSessionTempRoot := strings.TrimSpace(origSessionTempRootRaw)
	sessionTempRoot := origSessionTempRoot
	if sessionTempRoot == "" {
		var err error
		sessionTempRoot, err = artifacts.SessionScratchDirectory(sessionID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error resolving session scratch directory:", err)
			return Result{ExitCode: 1, Err: err}
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
	workspaceRoot = filepath.Join(tmpRoot, "workspace-"+sessionID)
	// NOTE: workspace snapshot initialization happens after we resolve `repoRoot`
	// via `newTrajectoryWriter` (see below).
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
	if err := os.Setenv("MACHTIANI_TMP_ROOT", workspaceRoot); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: unable to export tmp root:", err)
	}
	if err := os.Setenv("MACHTIANI_SESSION_TEMP_ROOT", sessionTempRoot); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: unable to export session temp root:", err)
	}
	var sessLock *sessionLock
	cleanupSessionRoot := origSessionTempRoot == ""
	defer func() {
		if sessLock != nil {
			if err := sessLock.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "[session-lock] warning: failed to release lock: %v\n", err)
			}
		}
		if cleanupSessionRoot && !cfg.persistTmpData {
			if err := os.RemoveAll(sessionTempRoot); err != nil && cfg.verbose {
				fmt.Fprintf(os.Stderr, "Warning: failed to cleanup session temp root %s: %v\n", sessionTempRoot, err)
			}
		}
		tempdir.ClearSessionRoot()
		_ = os.Unsetenv("MACHTIANI_TMP_ROOT")
		_ = os.Unsetenv("MACHTIANI_PATCH_STRATEGY")
		if strings.TrimSpace(origSessionTempRootRaw) == "" {
			_ = os.Unsetenv("MACHTIANI_SESSION_TEMP_ROOT")
		} else {
			_ = os.Setenv("MACHTIANI_SESSION_TEMP_ROOT", origSessionTempRootRaw)
		}
	}()
	lock, lockErr := acquireSessionLock(sessionID, sessionTempRoot)
	if lockErr != nil {
		fmt.Fprintln(os.Stderr, "Error acquiring session lock:", lockErr)
		return Result{ExitCode: 1, Err: lockErr}
	}
	sessLock = lock

	trajectoryWriter, repoRoot, trajErr := newTrajectoryWriter(cfg, sessionID)
	if trajErr != nil {
		fmt.Fprintln(os.Stderr, "Trajectory setup error:", trajErr)
		return Result{ExitCode: 1, Err: trajErr}
	}
	// Prepare the workspace repo snapshot using the resolved repo root.
	workspaceRoot := filepath.Join(tmpRoot, "workspace-"+sessionID)
	if _, _, err := workspace.EnsureRepoSnapshot(repoRoot, workspaceRoot); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: unable to prepare workspace repo snapshot:", err)
	}
	if err := tempdir.SetSessionRoot(workspaceRoot); err != nil {
		fmt.Fprintln(os.Stderr, "Error preparing session temp root:", err)
		return Result{ExitCode: 1, Err: err}
	}
	if trajectoryWriter != nil {
		fmt.Fprintln(os.Stderr, "[trajectory] unified stream:", trajectoryWriter.Config().Path)
	}

	display := ui.NewTerminalDisplay(os.Stdout, timerMgr, sessionID, strings.TrimSpace(cfg.parentSessionID))
	var failoverCancel context.CancelFunc
	var failoverDone <-chan struct{}
	var shellActionCancel context.CancelFunc
	var shellActionDone <-chan struct{}
	if trajectoryWriter != nil {
		cancel, done, err := startLLMFailoverLogger(display, trajectoryWriter.Config().Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[trajectory] failover listener setup error: %v\n", err)
		} else {
			failoverCancel = cancel
			failoverDone = done
		}
		// Start shell-agent action streamer to surface shell actions in real-time
		cancel, done, err = startShellActionStreamer(display, trajectoryWriter.Config().Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[trajectory] shell action listener setup error: %v\n", err)
		} else {
			shellActionCancel = cancel
			shellActionDone = done
		}
	}
	defer func() {
		if failoverCancel != nil {
			failoverCancel()
		}
		if failoverDone != nil {
			<-failoverDone
		}
		if shellActionCancel != nil {
			shellActionCancel()
		}
		if shellActionDone != nil {
			<-shellActionDone
		}
	}()

	sessTelemetry := newSessionTelemetry(trajectoryWriter, sessionID, goal, cfg, repoRoot, opts.Build)
	defer func() {
		if sessTelemetry != nil {
			sessTelemetry.Finish(sessionStatus, turnsCompleted, sessionErr)
		} else if trajectoryWriter != nil {
			_ = trajectoryWriter.Close()
		}
	}()

	var tr *transcript.Transcript
	resumeTranscript := ""
	if resumeMode && loadedState != nil {
		resumeTranscript = loadedState.Transcript
		if strings.TrimSpace(resumeTranscript) == "" && strings.TrimSpace(loadedState.TranscriptPath) != "" {
			if data, readErr := os.ReadFile(loadedState.TranscriptPath); readErr == nil {
				resumeTranscript = string(data)
			} else if cfg.verbose {
				fmt.Fprintf(os.Stderr, "Warning: failed to read transcript for resume (%s): %v\n", loadedState.TranscriptPath, readErr)
			}
		}
	}
	printResumeHint := func(header string, turns int) {
		fmt.Fprintf(os.Stdout, "%s\nSession ID: %s\nTurns completed: %d\nGoal so far: %q\n\n", header, sessionID, turns, goal)
		fmt.Fprintf(os.Stdout, "To continue, provide your next instruction, for example:\n  mct-agent run \"<next instruction>\" --session-id %s\n", sessionID)
		if parentID := strings.TrimSpace(cfg.parentSessionID); parentID != "" {
			fmt.Fprintf(os.Stdout, "\nParent session detected (%s). To resume that session, rerun your original command with the parent session ID, for example:\n  mct-agent run \"<original prompt>\" --session-id %s\n", parentID, parentID)
		}
		fmt.Fprintln(os.Stdout)
	}
	applyPlannerProgress := func(state *SessionState) {
		if state == nil {
			return
		}
		state.PlannerProgress = plannerProgress.toState()
		if pendingPatchDraft != nil {
			state.PendingPatchTurn = &PendingPatchTurnState{
				Step:        pendingPatchDraft.Step,
				Description: pendingPatchDraft.Description,
				Answer:      pendingPatchDraft.Answer,
			}
		} else {
			state.PendingPatchTurn = nil
		}
		if state.PlannerProgress != nil {
			if err := UpdateMetaPlanProgress(sessionID, state.PlannerProgress); err != nil && cfg.verbose {
				fmt.Fprintf(os.Stderr, "Warning: failed to update meta plan progress for %s: %v\n", sessionID, err)
			}
		}
	}
	writePendingPatchTranscript := func(status string, decision string, note string, undo bool) error {
		if pendingPatchDraft == nil {
			return nil
		}
		desc := strings.TrimSpace(pendingPatchDraft.Description)
		if desc == "" {
			desc = "Patch applied"
		}
		// Use a simple, final status label per request: "success" or "reject".
		// Avoid emitting separate "apply" turns.
		normalized := strings.ToLower(strings.TrimSpace(status))
		if normalized != "success" && normalized != "reject" {
			normalized = status
		}
		suffix := desc
		if strings.TrimSpace(suffix) != "" {
			suffix = " - " + strings.TrimSpace(suffix)
		}
		question := fmt.Sprintf("Patcher: %s%s", normalized, suffix)
		summary := pendingPatchDraft.Answer
		if strings.ToLower(status) == "rejected" || strings.ToLower(status) == "reject" {
			summary = ""
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
		if err := tr.WriteTurn(pendingPatchDraft.Step, question, "", nil, summary, decision); err != nil {
			return err
		}
		if strings.ToLower(status) == "success" {
			baseline, err := patchersvc.EnsureBaseline(sessionID, repoRoot, time.Now())
			if err != nil {
				fmt.Fprintf(os.Stderr, "[full-diff] Failed to ensure baseline: %v\n", err)
			} else {
				files := []string(nil)
				if review := plannerProgress.pendingReviewInfo(); review != nil {
					files = append(files, review.Files...)
				}
				if len(files) == 0 {
					files = append(files, plannerProgress.successList()...)
				}
				fulldiff.Inject(pendingPatchDraft.Step, repoRoot, files, tr, plannerProgress, fulldiff.Options{Verbose: cfg.verbose, Baseline: baseline})
				// Synthetic turn: keep transcript step count in sync.
				// full-diff injection now emits 0..N transcript turns (one per file).
				if trajectoryWriter != nil {
					trajectoryWriter.Emit(rootCtx, trajectory.Event{
						Kind: "transcript_synthetic_full_diff",
						Payload: map[string]any{
							"op":    "full_diff",
							"step":  pendingPatchDraft.Step + 1,
							"files": files,
						},
					})
				}
				userTurnCounter += len(files)
			}
		}
		pendingPatchDraft = nil
		if pendingState != nil {
			pendingState.PendingPatchTurn = nil
		}
		return nil
	}

	tr, err := transcript.NewWithPath(cfg.transcriptFile, sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error preparing transcript:", err)
		return Result{ExitCode: 1, Err: err}
	}
	defer tr.Close()
	tr.SetTrajectory(trajectoryWriter)
	defer func() {
		if interrupted {
			state := SessionState{
				SessionID:      sessionID,
				Goal:           goal,
				TurnsCompleted: turnsCompleted,
			}
			if tr != nil {
				state.TranscriptPath = tr.Path()
				state.Transcript = tr.Content()
			}
			state.ParentSessionID = strings.TrimSpace(cfg.parentSessionID)
			if strings.TrimSpace(state.MetaInstructionDir) == "" {
				dir := strings.TrimSpace(cfg.metaInstructionDir)
				if dir == "" && strings.TrimSpace(metaInstructionPath) != "" {
					dir = filepath.Dir(metaInstructionPath)
				}
				state.MetaInstructionDir = dir
			}
			if len(state.MetaModes) == 0 && strings.TrimSpace(cfg.mode) != "" {
				state.MetaModes = []string{strings.ToLower(strings.TrimSpace(cfg.mode))}
			}
			applyPlannerProgress(&state)
			if err := SaveSessionState(state); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to save session state for %s: %v\n", sessionID, err)
			} else {
				printResumeHint("=== SESSION INTERRUPTED ===", turnsCompleted)
			}
			return
		}
		sid := strings.TrimSpace(sessionID)
		if sid == "" {
			return
		}
		if keepSessionState {
			state := SessionState{
				SessionID:      sid,
				Goal:           goal,
				TurnsCompleted: turnsCompleted,
			}
			if pendingState != nil {
				state = *pendingState
			} else if tr != nil {
				state.TranscriptPath = tr.Path()
				state.Transcript = tr.Content()
			}
			if state.TranscriptPath == "" && tr != nil {
				state.TranscriptPath = tr.Path()
			}
			if state.Transcript == "" && tr != nil {
				state.Transcript = tr.Content()
			}
			if strings.TrimSpace(state.MetaInstructionDir) == "" {
				dir := strings.TrimSpace(cfg.metaInstructionDir)
				if dir == "" && strings.TrimSpace(metaInstructionPath) != "" {
					dir = filepath.Dir(metaInstructionPath)
				}
				state.MetaInstructionDir = dir
			}
			if len(state.MetaModes) == 0 && strings.TrimSpace(cfg.mode) != "" {
				state.MetaModes = []string{strings.ToLower(strings.TrimSpace(cfg.mode))}
			}
			state.ParentSessionID = strings.TrimSpace(cfg.parentSessionID)
			applyPlannerProgress(&state)
			if err := SaveSessionState(state); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to save session state for %s: %v\n", sid, err)
			}
			return
		}
		if err := RemoveSessionState(sid); err != nil && cfg.verbose {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove session state for %s: %v\n", sid, err)
		}
	}()

	if strings.TrimSpace(resumeTranscript) != "" {
		if err := tr.Restore(resumeTranscript); err != nil {
			fmt.Fprintln(os.Stderr, "Error restoring transcript:", err)
			return Result{ExitCode: 1, Err: err}
		}
	}

	if cfg.verbose {
		fmt.Fprintln(os.Stderr, "mct-agent starting; transcript:", tr.Path())
		fmt.Fprintln(os.Stderr, "Session:", sessionID)
	}

	trajectoryPath, err := resolveFileDiscoveryTrajectory(cfg, sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "File-discovery setup error:", err)
		return Result{ExitCode: 1, Err: err}
	}
	if cfg.verbose && strings.TrimSpace(trajectoryPath) != "" {
		fmt.Fprintln(os.Stderr, "File discovery trajectory:", trajectoryPath)
	}

	paramPairs := append([]string(nil), opts.ParamPairs...)
	paramJSONVals := append([]string(nil), opts.ParamJSON...)

	models, err := resolveModelRuntimes(cfg, opts.GlobalConfig, paramPairs, paramJSONVals, opts.APIKeyOverrides)
	if err != nil {
		if miss, ok := err.(*missingConfigError); ok {
			fmt.Fprintln(os.Stderr, "Missing model config: set:")
			for _, item := range miss.items {
				fmt.Fprintln(os.Stderr, " - ", item)
			}
			return Result{ExitCode: 2, Err: err}
		}
		fmt.Fprintln(os.Stderr, "Model resolution error:", err)
		return Result{ExitCode: 1, Err: err}
	}
	metaLines := []string{
		describeModel("orchestrator", models.orchestrator),
		describeModel("answer", models.answer),
		describeModel("file discovery", models.fileDiscovery),
	}
	if cfg.shellAgent {
		metaLines = append(metaLines, describeModel("shell agent", models.shellAgent))
	}
	orchPromptOpts := promptOptions(metaLines...)
	baseOrchMetadata := []string(nil)
	if orchPromptOpts != nil {
		baseOrchMetadata = append([]string(nil), orchPromptOpts.Metadata...)
	}
	patcherPromptOpts := promptOptions(
		describeModel("patcher", models.patcher),
	)

	shellAgentModel := strings.TrimSpace(cfg.shellAgentModel)
	if shellAgentModel == "" && strings.TrimSpace(models.shellAgent.alias) != "" && models.shellAgent.usingAlias {
		shellAgentModel = strings.TrimSpace(models.shellAgent.alias)
	}

	mctRunner := runner.Runner{
		Verbose:                 cfg.verbose,
		DryRun:                  cfg.dryRun,
		Runtime:                 models.orchestrator.toPromptRuntime(),
		AnswerRuntime:           models.answer.toPromptRuntime(),
		FileDiscoveryRuntime:    models.fileDiscovery.toPromptRuntime(),
		FileDiscoveryTrajectory: trajectoryPath,
		ShellAgent:              cfg.shellAgent,
		ShellAgentModel:         shellAgentModel,
		GlobalConfigPath:        opts.GlobalConfigPath,
		PersistTmpData:          cfg.persistTmpData,
		SessionTempRoot:         sessionTempRoot,
		Prompts:                 opts.GlobalConfig.Prompts,
	}
	if err := mctRunner.Resolve(); err != nil {
		fmt.Fprintln(os.Stderr, "mct resolution error:", err)
		fmt.Fprintln(os.Stderr, "Hint: install 'mct' into PATH (see mct/README.md).")
		return Result{ExitCode: 1, Err: err}
	}

	mctResponseDirectives := []string(nil)
	if cfg.enableTagFormat {
		mctResponseDirectives = []string{"use_tag_format"}
	}

	// In full-mode, disable strict patch prompting/apply and rely on full rewrites only.
	effectiveStrict := cfg.patchStrict && !cfg.patchFull

	var pRunner *runner.PatcherRunner
	if cfg.patch {
		patchLogger := log.New(os.Stderr, "[patcher] ", log.LstdFlags)
		pr := &runner.PatcherRunner{
			Enabled:   true,
			Verbose:   cfg.verbose,
			DryRun:    cfg.dryRun,
			SessionID: sessionID,
			Runtime:   models.patcher.toPromptRuntime(),
			Service: patchersvc.NewService(
				patchersvc.WithLogger(patchLogger),
				patchersvc.WithStrictPatchMode(effectiveStrict),
			),
			// Apply patches to the host checkout (repoRoot) atomically.
			// The patcher service operates on WorkspaceRoot (snapshotRepoRoot).
			RepoRoot:        repoRoot,
			PersistTmpData:  cfg.persistTmpData,
			SessionTempRoot: sessionTempRoot,
			FullMode:        cfg.patchFull,
			WorkspaceFactory: func(root string) (string, func(), error) {
				return root, func() {}, nil
			},
		}
		if err := pr.Resolve(); err != nil {
			if cfg.verbose {
				fmt.Fprintln(os.Stderr, "[patcher] resolve warning:", err)
			}
		}
		pRunner = pr
	}
	if pRunner != nil {
		defer pRunner.Close()
	}

	var plannerPrompts *llm.PlannerPromptsConfig
	if opts.GlobalConfig.Prompts != nil {
		plannerPrompts = opts.GlobalConfig.Prompts.Planner
	}
	pl := planner.NewClient(planner.ClientConfig{
		Model:             models.orchestrator.resolved,
		Extras:            models.orchestrator.extras,
		Alias:             models.orchestrator.alias,
		Verbose:           cfg.verbose,
		DryRun:            cfg.dryRun,
		RequestTimeoutSec: cfg.timeoutPerTurn,
		PatchEnabled:      cfg.patch,
		StrictPatchMode:   effectiveStrict,
		RepoRoot:          repoRoot,
		SessionID:         sessionID,
		Prompts:           plannerPrompts,
	})

	isChildSession := strings.TrimSpace(cfg.parentSessionID) != ""
	startingTranscript, headerErr := startTranscriptIfNeeded(tr, goal, sessionID, cfg, resumeMode)
	if headerErr != nil {
		fmt.Fprintln(os.Stderr, "Error writing transcript header:", headerErr)
		return Result{ExitCode: 1, Err: headerErr}
	}

	if err := writeInitialBackgroundIfNeeded(tr, repoRoot, cfg, isChildSession, startingTranscript); err != nil {
		fmt.Fprintln(os.Stderr, "Transcript write error:", err)
		sessionErr = err
		return Result{ExitCode: 1, Err: err}
	}

	metaActive := strings.TrimSpace(cfg.mode) != "" && strings.TrimSpace(cfg.parentSessionID) == ""
	if metaActive {
		display.StartSession(goal)
		if resumeMode {
			fmt.Fprintf(os.Stdout, "Resuming meta session %s\n", sessionID)
		}
		metaCtx := metaContext{
			RootCtx:         rootCtx,
			SessionID:       sessionID,
			Goal:            goal,
			Config:          cfg,
			Options:         opts,
			Display:         display,
			InstructionPath: metaInstructionPath,
			Instruction:     metaInstructions,
			Transcript:      tr,
		}
		outcome, handled := metaOrchestrate(metaCtx)
		if handled {
			turnsCompleted = len(outcome.Plan.Tasks)
			if outcome.Err != nil {
				sessionErr = outcome.Err
				sessionStatus = "error"
				keepSessionState = true
				instructionDir := strings.TrimSpace(cfg.metaInstructionDir)
				if instructionDir == "" {
					if strings.TrimSpace(metaInstructionPath) != "" {
						instructionDir = filepath.Dir(metaInstructionPath)
					}
				}
				pendingState = &SessionState{
					SessionID:          sessionID,
					Goal:               goal,
					TurnsCompleted:     turnsCompleted,
					TranscriptPath:     tr.Path(),
					Transcript:         tr.Content(),
					ParentSessionID:    strings.TrimSpace(cfg.parentSessionID),
					MetaModes:          metaModesFromPlan(outcome.Plan),
					MetaInstructionDir: instructionDir,
				}
				applyPlannerProgress(pendingState)
				display.EndSession()
				return Result{ExitCode: outcome.ExitCode, Status: sessionStatus, Turns: turnsCompleted, SessionID: sessionID, Err: outcome.Err}
			}
			sessionStatus = "success"
			sessionErr = nil
			if strings.TrimSpace(outcome.FinalAnswer) != "" {
				if err := tr.WriteTurn(1, "Meta-Orchestrator Summary", "", nil, outcome.FinalAnswer, "meta-summary"); err != nil {
					fmt.Fprintln(os.Stderr, "Transcript write error:", err)
					sessionErr = err
					display.EndSession()
					return Result{ExitCode: 1, Err: err}
				}
			}
			if err := writeFinalAnswer(sessionID, outcome.FinalAnswer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
				fmt.Fprintln(os.Stderr, "Final file write error:", err)
				sessionErr = err
				display.EndSession()
				return Result{ExitCode: 1, Err: err}
			}
			presentFinalAnswer(display, outcome.FinalAnswer)
			display.EndSession()
			return Result{ExitCode: 0, Status: sessionStatus, Turns: turnsCompleted, SessionID: sessionID}
		}
	}

	display.StartSession(goal)
	if resumeMode {
		fmt.Fprintf(os.Stdout, "Resuming session %s (completed %d of %d turns)\n", sessionID, turnsCompleted, cfg.maxSteps)
	}
	sessionClosed := false
	defer func() {
		if !sessionClosed {
			display.EndSession()
		}
	}()

	parentSpanID := ""
	useShellAgent := false
	for {
		if err := rootCtx.Err(); err != nil {
			return interruptedResult(err)
		}
		step := userTurnCounter + 1
		var turn *turnTelemetry
		if sessTelemetry != nil {
			turn = sessTelemetry.StartTurn(step, cfg.maxSteps)
			parentSpanID = turn.span.ID
		}

		turnDecision := "unknown"
		turnInfo := map[string]any{
			"step":      step,
			"max_steps": cfg.maxSteps,
		}

		var (
			decision   planner.Decision
			question   string
			perr       error
			planCtx    context.Context
			planCancel context.CancelFunc
		)
		pl.UpdateProgress(plannerProgress.snapshot())
		planCtx, planCancel = makeTurnContext(rootCtx, cfg.timeoutPerTurn)
		trFull := tr.Content()
		// If there is a pending patch diff awaiting review, append it to
		// the planning context so the planner can decide accept/reject
		// without adding an extra transcript turn.
		if pendingPatchDraft != nil {
			var b strings.Builder
			b.WriteString(trFull)
			b.WriteString("\n## Pending Patch Review\n\n")
			// Mirror transcript structure tersely so the planner has
			// consistent context shape.
			line := "Patcher: apply - " + strings.TrimSpace(pendingPatchDraft.Description)
			b.WriteString(line)
			b.WriteString("\n\n=== Answer\n\n")
			b.WriteString(strings.TrimSpace(pendingPatchDraft.Answer))
			b.WriteString("\n\n")
			b.WriteString("Planner decision: patch\n")
			trFull = b.String()
		}
		trimmedResumePrompt := strings.TrimSpace(resumePrompt)
		if trimmedResumePrompt != "" {
			feedback := extractUserFeedback(trimmedResumePrompt)
			resumePrompt = feedback
			if err := tr.AppendRaw(fmt.Sprintf("\n=== USER FEEDBACK\n\n%s\n", feedback)); err != nil {
				fmt.Fprintln(os.Stderr, "Transcript write error:", err)
				sessionErr = err
				finishTurn(sessTelemetry, turn, "user-feedback", "error", turnInfo, err)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: err}
			}
			trFull = appendResumePromptContext(trFull, feedback)
			turnInfo["resume_prompt"] = true
		}
		planCtx = attachTrajectory(planCtx, trajectoryWriter, parentSpanID)
		decision, question, perr = pl.Plan(planCtx, goal, trFull, step, cfg.maxSteps)
		if trimmedResumePrompt != "" {
			resumePrompt = ""
		}
		var planCtxErr error
		if planCtx != nil {
			planCtxErr = planCtx.Err()
		}
		if planCancel != nil {
			planCancel()
		}
		if perr != nil {
			if isContextCancelled(perr) || isContextCancelled(planCtxErr) {
				return interruptedResult(perr)
			}
			if errors.Is(planCtxErr, context.DeadlineExceeded) {
				fmt.Fprintf(os.Stderr, "Planner error: timed out after %ds. Increase --timeout-per-turn or set 0 for unlimited.\n", cfg.timeoutPerTurn)
				sessionErr = perr
				finishTurn(sessTelemetry, turn, "planner", "error", turnInfo, perr)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: perr}
			}
			perrStr := strings.ToLower(perr.Error())
			if strings.Contains(perrStr, "unable to parse decision from model output") {
				fmt.Fprintln(os.Stderr, "Planner error:", perr)
				turnDecision = "planner"
				turnInfo["is_retry"] = true
				turnInfo["retry_reason"] = "planner_parse_error"
				finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, perr)
				continue
			}
			if strings.Contains(perrStr, "deadline exceeded") || strings.Contains(perrStr, "timeout") || strings.Contains(perrStr, "temporary") {
				fmt.Fprintln(os.Stderr, "Planner warning:", perr)
				fmt.Fprintln(os.Stderr, "Falling back to finalizing with current transcript.")
				ctxF, cancelF := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
				trFull := tr.Content()
				ctxF = attachTrajectory(ctxF, trajectoryWriter, parentSpanID)
				answer, ferr := pl.Finalize(ctxF, goal, trFull)
				var ctxFErr error
				if ctxF != nil {
					ctxFErr = ctxF.Err()
				}
				if cancelF != nil {
					cancelF()
				}
				if ferr != nil {
					if isContextCancelled(ferr) || isContextCancelled(ctxFErr) {
						return interruptedResult(ferr)
					}
					if errors.Is(ctxFErr, context.DeadlineExceeded) {
						fmt.Fprintf(os.Stderr, "Finalizer error: timed out after %ds. Increase --timeout-per-turn or set 0 for unlimited.\n", cfg.timeoutPerTurn)
					} else {
						fmt.Fprintln(os.Stderr, "Finalizer error:", ferr)
					}
					sessionErr = ferr
					finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, ferr)
					turnsCompleted = userTurnCounter
					return Result{ExitCode: 1, Err: ferr}
				}
				if err := tr.WriteFinal(answer, step, true); err != nil {
					fmt.Fprintln(os.Stderr, "Transcript write error:", err)
					sessionErr = err
					finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, err)
					turnsCompleted = userTurnCounter
					return Result{ExitCode: 1, Err: err}
				}
				if err := writeFinalAnswer(sessionID, answer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
					fmt.Fprintln(os.Stderr, "Final file write error:", err)
					sessionErr = err
					finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, err)
					turnsCompleted = userTurnCounter
					return Result{ExitCode: 1, Err: err}
				}
				presentFinalAnswer(display, answer)
				display.EndSession()
				sessionClosed = true
				turnDecision = "finalize"
				turnInfo["finalized"] = true
				finishTurn(sessTelemetry, turn, turnDecision, "success", turnInfo, nil)
				turnsCompleted = userTurnCounter
				sessionStatus = "success"
				keepSessionState = true
				pendingState = &SessionState{
					SessionID:      sessionID,
					Goal:           goal,
					TurnsCompleted: turnsCompleted,
				}
				if tr != nil {
					pendingState.TranscriptPath = tr.Path()
					pendingState.Transcript = tr.Content()
				}
				applyPlannerProgress(pendingState)
				printResumeHint("=== SESSION COMPLETE ===", turnsCompleted)
				return Result{ExitCode: 0, Status: sessionStatus, Turns: userTurnCounter, SessionID: sessionID}
			}
			fmt.Fprintln(os.Stderr, "Planner error:", perr)
			sessionErr = perr
			finishTurn(sessTelemetry, turn, "planner", "error", turnInfo, perr)
			turnsCompleted = userTurnCounter
			return Result{ExitCode: 1, Err: perr}
		}

		if cfg.verbose {
			fmt.Fprintf(os.Stderr, "Step %d decision: %s\n", step, decision)
		}
		turnDecision = string(decision)
		turnInfo["planner_decision"] = string(decision)
		if question != "" && trajectoryWriter != nil {
			excerpt := trajectory.MakeTextExcerpt(question, trajectoryWriter.ExcerptLen())
			turnInfo = trajectory.MergeExcerptWithPrefix(turnInfo, excerpt, "planner_question")
		}
		pendingReview := plannerProgress.pendingReviewInfo()
		if plannerProgress.hasPendingReview() && decision != planner.DecisionAccept && decision != planner.DecisionReject {
			errUnexpected := fmt.Errorf("pending patch review requires accept or reject, got %s", decision)
			fmt.Fprintln(os.Stderr, "Planner error:", errUnexpected)
			sessionErr = errUnexpected
			finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, errUnexpected)
			turnsCompleted = userTurnCounter
			return Result{ExitCode: 1, Err: errUnexpected}
		}
		if !plannerProgress.hasPendingReview() && (decision == planner.DecisionAccept || decision == planner.DecisionReject) {
			errUnexpected := errors.New("planner returned accept/reject without a pending patch review")
			fmt.Fprintln(os.Stderr, "Planner error:", errUnexpected)
			sessionErr = errUnexpected
			finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, errUnexpected)
			turnsCompleted = userTurnCounter
			return Result{ExitCode: 1, Err: errUnexpected}
		}

		if decision == planner.DecisionFinalize {
			ctx, cancelF := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
			trFull := tr.Content()
			ctx = attachTrajectory(ctx, trajectoryWriter, parentSpanID)
			answer, ferr := pl.Finalize(ctx, goal, trFull)
			var finalizeCtxErr error
			if ctx != nil {
				finalizeCtxErr = ctx.Err()
			}
			if cancelF != nil {
				cancelF()
			}
			if ferr != nil {
				if isContextCancelled(ferr) || isContextCancelled(finalizeCtxErr) {
					return interruptedResult(ferr)
				}
				if errors.Is(finalizeCtxErr, context.DeadlineExceeded) {
					fmt.Fprintf(os.Stderr, "Finalizer error: timed out after %ds. Increase --timeout-per-turn or set 0 for unlimited.\n", cfg.timeoutPerTurn)
				} else {
					fmt.Fprintln(os.Stderr, "Finalizer error:", ferr)
				}
				sessionErr = ferr
				finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, ferr)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: ferr}
			}
			if err := tr.WriteFinal(answer, step, step == cfg.maxSteps && decision != planner.DecisionFinalize); err != nil {
				fmt.Fprintln(os.Stderr, "Transcript write error:", err)
				sessionErr = err
				finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, err)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: err}
			}
			if err := writeFinalAnswer(sessionID, answer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
				fmt.Fprintln(os.Stderr, "Final file write error:", err)
				sessionErr = err
				finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, err)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: err}
			}
			presentFinalAnswer(display, answer)
			display.EndSession()
			sessionClosed = true
			turnDecision = "finalize"
			turnInfo["finalized"] = true
			finishTurn(sessTelemetry, turn, turnDecision, "success", turnInfo, nil)
			turnsCompleted = userTurnCounter
			sessionStatus = "success"
			keepSessionState = true
			pendingState = &SessionState{
				SessionID:      sessionID,
				Goal:           goal,
				TurnsCompleted: turnsCompleted,
			}
			if tr != nil {
				pendingState.TranscriptPath = tr.Path()
				pendingState.Transcript = tr.Content()
			}
			applyPlannerProgress(pendingState)
			printResumeHint("=== SESSION COMPLETE ===", turnsCompleted)
			return Result{ExitCode: 0, Status: sessionStatus, Turns: userTurnCounter, SessionID: sessionID}
		}

		switch decision {
		case planner.DecisionAccept:
			review := pendingReview
			if review == nil {
				errUnexpected := errors.New("no pending patch review to accept")
				fmt.Fprintln(os.Stderr, "Planner error:", errUnexpected)
				sessionErr = errUnexpected
				finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, errUnexpected)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: errUnexpected}
			}
			note := strings.TrimSpace(question)
			if err := writePendingPatchTranscript("SUCCESS", "patch", note, false); err != nil {
				fmt.Fprintln(os.Stderr, "Transcript write error:", err)
				sessionErr = err
				finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, err)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: err}
			}
			accepted := plannerProgress.commitPendingReview()
			userTurnCounter++
			turnsCompleted = userTurnCounter
			turnInfo["planner_pending_review"] = false
			turnInfo["patch_review_pending"] = false
			turnInfo["planner_applied_patches"] = plannerProgress.appliedCount()
			turnInfo["planner_success_file_count"] = len(plannerProgress.successList())
			turnInfo["planner_review_action"] = "accept"
			turnInfo["patch_finalize_pending"] = false
			if accepted != nil {
				turnInfo["patch_review_sequence"] = accepted.Sequence
				if accepted.Description != "" {
					turnInfo["patch_review_description"] = accepted.Description
				}
				if len(accepted.Files) > 0 {
					turnInfo["patch_review_files"] = append([]string(nil), accepted.Files...)
				}
			}
			if note != "" {
				turnInfo["planner_review_note"] = note
			}
			if accepted != nil {
				display.Notify(fmt.Sprintf("Patch %d accepted%s", accepted.Sequence, formatOptionalSuffix(accepted.Description)))
			}
			finishTurn(sessTelemetry, turn, turnDecision, "success", turnInfo, nil)
			if userTurnCounter >= cfg.maxSteps {
				goto Finalize
			}
			goto TurnDone

		case planner.DecisionReject:
			review := pendingReview
			if review == nil {
				errUnexpected := errors.New("no pending patch review to reject")
				fmt.Fprintln(os.Stderr, "Planner error:", errUnexpected)
				sessionErr = errUnexpected
				finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, errUnexpected)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: errUnexpected}
			}
			note := strings.TrimSpace(question)
			undoRequired := !cfg.dryRun && strings.TrimSpace(review.ReversePatchPath) != ""
			if undoRequired && pRunner == nil {
				err := errors.New("patch runner unavailable for undo")
				fmt.Fprintln(os.Stderr, "Patch undo error:", err)
				sessionErr = err
				finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, err)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: err}
			}
			if undoRequired {
				if err := pRunner.Undo(review.ReversePatchPath); err != nil {
					fmt.Fprintln(os.Stderr, "Patch undo error:", err)
					sessionErr = err
					finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, err)
					turnsCompleted = userTurnCounter
					return Result{ExitCode: 1, Err: err}
				}
			}
			if err := writePendingPatchTranscript("REJECTED", "patch", note, undoRequired); err != nil {
				fmt.Fprintln(os.Stderr, "Transcript write error:", err)
				sessionErr = err
				finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, err)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: err}
			}
			discarded := plannerProgress.discardPendingReview()
			userTurnCounter++
			turnsCompleted = userTurnCounter
			turnInfo["planner_pending_review"] = false
			turnInfo["planner_review_action"] = "reject"
			turnInfo["planner_applied_patches"] = plannerProgress.appliedCount()
			turnInfo["planner_success_file_count"] = len(plannerProgress.successList())
			turnInfo["patch_finalize_pending"] = false
			turnInfo["patch_review_pending"] = false
			if undoRequired {
				turnInfo["patch_undo_applied"] = true
				turnInfo["patch_reverse_path"] = review.ReversePatchPath
			}
			if discarded != nil {
				turnInfo["patch_review_sequence"] = discarded.Sequence
				if discarded.Description != "" {
					turnInfo["patch_review_description"] = discarded.Description
				}
				if len(discarded.Files) > 0 {
					turnInfo["patch_review_files"] = append([]string(nil), discarded.Files...)
				}
			}
			if note != "" {
				turnInfo["planner_review_note"] = note
			}
			if discarded != nil {
				display.Notify(fmt.Sprintf("Patch %d rejected%s", discarded.Sequence, formatOptionalSuffix(discarded.Description)))
			}
			finishTurn(sessTelemetry, turn, turnDecision, "success", turnInfo, nil)
			goto TurnDone

		case planner.DecisionAsk:
			if question == "" {
				errEmpty := errors.New("planner returned empty question")
				fmt.Fprintln(os.Stderr, "Planner returned empty question for 'ask' decision")
				sessionErr = errEmpty
				finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, errEmpty)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: errEmpty}
			}
			if cfg.verbose {
				fmt.Fprintln(os.Stderr, "Question:", question)
			}
			var showFileBanner string
			showFileRetrieved := []string(nil)
			showFileHandled := false
			if !cfg.dryRun {
				ctxDetect, cancelDetect := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
				ctxDetect = attachTrajectory(ctxDetect, trajectoryWriter, parentSpanID)
				detection, raw, err := promptsvc.DetectShowFileRequest(ctxDetect, mctRunner.Runtime, question)
				cancelDetect()
				if err != nil {
					if cfg.verbose {
						fmt.Fprintln(os.Stderr, "Show-file detection error:", err)
					}
					turnInfo["show_file_detection_error"] = trimTo(err.Error(), 200)
					if strings.TrimSpace(raw) != "" {
						turnInfo["show_file_detection_raw"] = trimTo(raw, 200)
					}
				} else {
					turnInfo["show_file_request"] = detection.IsShowFileRequest
					if strings.TrimSpace(raw) != "" {
						turnInfo["show_file_detection_raw"] = trimTo(raw, 200)
					}
					if detection.IsShowFileRequest {
						requested := filepath.Clean(strings.TrimSpace(detection.Filepath))
						turnInfo["show_file_path"] = requested
						if requested != "." && requested != "" && !filepath.IsAbs(requested) && !strings.HasPrefix(requested, "..") {
							candidate := filepath.Join(repoRoot, requested)
							content, rerr := os.ReadFile(candidate)
							if rerr != nil {
								if cfg.verbose {
									fmt.Fprintln(os.Stderr, "Show-file read error:", rerr)
								}
								turnInfo["show_file_read_error"] = trimTo(rerr.Error(), 200)
							} else {
								header := fmt.Sprintf("\n--- Current content of file: %s ---\n", requested)
								footer := "\n---\n"
								showFileBanner = header + string(content) + footer
								_ = tr.AppendRaw(showFileBanner)
								showFileRetrieved = []string{requested}
								showFileHandled = true
								turnInfo["show_file_appended"] = true
								turnInfo["show_file_bytes"] = len(content)
							}
						} else {
							turnInfo["show_file_invalid_path"] = true
						}
					}
				}
			}
			useShellAgent = cfg.shellAgent
			preflightNote := ""
			var preflightErr error
			preflightReply := ""
			if cfg.shellAgent {
				preflightNote = "routing: shell (config override — run commands via shell agent)"
			} else {
				ctxPre, cancelPre := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
				ctxPre = attachTrajectory(ctxPre, trajectoryWriter, parentSpanID)
				useShellAgent, preflightReply, preflightErr = promptsvc.PreflightShellRouting(ctxPre, mctRunner.Runtime, question)
				cancelPre()
				if preflightErr != nil && cfg.verbose {
					fmt.Fprintln(os.Stderr, "Preflight routing error:", preflightErr)
				}
				routeLabel := "shell"
				routeExplanation := "shell reply — run commands in the shell"
				if !useShellAgent {
					routeLabel = "file"
					routeExplanation = "retrieving relevant files and context"
				}
				switch {
				case strings.TrimSpace(preflightReply) != "":
					preflightNote = fmt.Sprintf("preflight routing: %s (reply: %s) — %s", routeLabel, trimTo(preflightReply, 120), routeExplanation)
				case preflightErr != nil:
					preflightNote = fmt.Sprintf("preflight routing: %s (error fallback: %s) — %s", routeLabel, trimTo(preflightErr.Error(), 120), routeExplanation)
				default:
					preflightNote = fmt.Sprintf("preflight routing: %s (empty reply) — %s", routeLabel, routeExplanation)
				}
			}
			// Preflight sync: refresh the snapshot from the host repo.
			// Only needed when the shell-agent is selected (it may execute commands
			// that depend on the latest host workspace state). File-discovery reads
			// context only and should use the session snapshot as-is.
			if useShellAgent {
				if root := strings.TrimSpace(tempdir.SessionRoot()); root != "" {
					if _, _, err := workspace.EnsureRepoSnapshot(repoRoot, root); err != nil {
						fmt.Fprintln(os.Stderr, "Error: host->snapshot refresh failed:", err)
						return Result{ExitCode: 1, Err: err}
					}
				}
			}
			mctRunner.ShellAgent = useShellAgent
			indicator := "shell"
			if showFileHandled {
				indicator = "show"
			} else if !useShellAgent {
				indicator = "file"
			}
			metadata := append([]string(nil), baseOrchMetadata...)
			if preflightNote != "" {
				metadata = append(metadata, preflightNote)
			}
			orchPromptOpts = &ui.PromptOptions{ModeIndicator: indicator, Metadata: metadata}
			turnInfo["mct_shell_agent"] = useShellAgent
			if !cfg.shellAgent {
				if strings.TrimSpace(preflightReply) != "" {
					turnInfo["mct_preflight_reply"] = trimTo(preflightReply, 200)
				}
				if preflightErr != nil {
					turnInfo["mct_preflight_error"] = trimTo(preflightErr.Error(), 200)
				}
			}
			stream := display.BeginPrompt(question, orchPromptOpts)
			if strings.TrimSpace(showFileBanner) != "" {
				stream.OnChunk(showFileBanner)
			}
			if showFileHandled {
				fullAns := strings.TrimSpace(showFileBanner)
				if fullAns == "" {
					fullAns = "[show-file]"
				}
				stream.Complete(fullAns)
				if err := tr.WriteTurn(step, question, "", showFileRetrieved, fullAns, "ask"); err != nil {
					fmt.Fprintln(os.Stderr, "Transcript write error:", err)
					sessionErr = err
					finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, err)
					return Result{ExitCode: 1, Err: err}
				}
				turnInfo["retrieved_count"] = len(showFileRetrieved)
				userTurnCounter++
				turnsCompleted = userTurnCounter
				finishTurn(sessTelemetry, turn, turnDecision, "success", turnInfo, nil)
				if userTurnCounter == cfg.maxSteps {
					goto Finalize
				}
				goto TurnDone
			}
			input := runner.PromptInput{
				Prompt:             question,
				Mode:               "default",
				IncludeHistory:     true,
				OnStreamHeader:     stream.OnChunk,
				OnStreamToken:      stream.OnChunk,
				MaxInputTokens:     cfg.maxInputTokens,
				ResponseDirectives: append([]string(nil), mctResponseDirectives...),
			}
			ctx2, cancel2 := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
			ctx2 = attachTrajectory(ctx2, trajectoryWriter, parentSpanID)
			result, merr := mctRunner.RunPrompt(ctx2, sessionID, input)
			var ctx2Err error
			if ctx2 != nil {
				ctx2Err = ctx2.Err()
			}
			if cancel2 != nil {
				cancel2()
			}
			if merr != nil {
				if isContextCancelled(merr) || isContextCancelled(ctx2Err) {
					stream.Abort("interrupted")
					return interruptedResult(merr)
				}
				msg := merr.Error()
				if errors.Is(ctx2Err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(merr.Error()), "signal: killed") {
					msg = fmt.Sprintf("timed out after %ds", cfg.timeoutPerTurn)
					fmt.Fprintf(os.Stderr, "mct prompt error: %s. Try increasing --timeout-per-turn or set 0 for unlimited.\n", msg)
				} else {
					fmt.Fprintln(os.Stderr, "mct prompt error:", merr)
				}
				stream.Abort(msg)
				sessionErr = merr
				finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, merr)
				turnsCompleted = userTurnCounter
				return Result{ExitCode: 1, Err: merr}
			}
			savedPath := strings.TrimSpace(result.SavedPath)
			if cfg.dryRun {
				lastAnswer = "[dry-run] mct would have produced a chat response here."
				retrieved = nil
			} else {
				if result.SaveError != nil {
					fmt.Fprintln(os.Stderr, "Warning: failed to save chat transcript:", result.SaveError)
				}
				if savedPath == "" {
					chatDir, err := artifacts.SessionChatDirectory(sessionID)
					if err != nil {
						fmt.Fprintln(os.Stderr, "Failed to resolve chat directory:", err)
						stream.Abort("failed to save chat transcript")
						return Result{ExitCode: 1, Err: err}
					}
					savedPath = filepath.Join(chatDir, "machtiani-response.md")
				}
				lastAnswer = result.FullText
				retrieved = append([]string(nil), result.RetrievedFiles...)
			}
			fullAns := result.Assistant
			if cfg.dryRun || strings.TrimSpace(fullAns) == "" {
				fullAns = lastAnswer
			}
			if cfg.enableTagFormat {
				stats, warnings := analyzeTagFormat(fullAns, retrieved)
				for k, v := range stats {
					turnInfo[k] = v
				}
				for _, warn := range warnings {
					fmt.Fprintf(os.Stderr, "[tag-format] %s\n", warn)
				}
			}
			stream.Complete(fullAns)
			transcriptQuestion := question
			if block := strings.TrimSpace(result.DirectiveBlock); block != "" {
				transcriptQuestion = transcriptQuestion + "\n\n" + block
			}
			if err := tr.WriteTurn(step, transcriptQuestion, savedPath, retrieved, fullAns, "ask"); err != nil {
				fmt.Fprintln(os.Stderr, "Transcript write error:", err)
				sessionErr = err
				finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, err)
				return Result{ExitCode: 1, Err: err}
			}
			askInfo := map[string]any{
				"retrieved_count": len(retrieved),
			}
			if savedPath != "" {
				askInfo["saved_chat_path"] = savedPath
			}
			if trajectoryWriter != nil && strings.TrimSpace(fullAns) != "" {
				excerpt := trajectory.MakeTextExcerpt(fullAns, trajectoryWriter.ExcerptLen())
				askInfo = trajectory.MergeExcerptWithPrefix(askInfo, excerpt, "answer")
			}
			for k, v := range askInfo {
				turnInfo[k] = v
			}
			userTurnCounter++
			turnsCompleted = userTurnCounter
			finishTurn(sessTelemetry, turn, turnDecision, "success", turnInfo, nil)
			if userTurnCounter == cfg.maxSteps {
				goto Finalize
			}
			goto TurnDone

		case planner.DecisionPatch:
			if cfg.verbose {
				fmt.Fprintln(os.Stderr, "[patcher] planner payload (raw):", trimTo(strings.TrimSpace(question), 1200))
			}
			stream := display.BeginPrompt("Patcher: create patch", patcherPromptOpts)
			patchTurnLabel := "Patcher: create patch"
			shouldFinalizeAfterPatch := false
			turnCounted := false
			markTurnCounted := func() {
				if turnCounted {
					return
				}
				userTurnCounter++
				turnCounted = true
				if userTurnCounter == cfg.maxSteps {
					shouldFinalizeAfterPatch = true
				}
			}
			patchOutcome := func(status string, err error, extra map[string]any) {
				if status == "success" {
					markTurnCounted()
				}
				if extra == nil {
					extra = map[string]any{}
				}
				if patchTurnLabel != "" {
					extra["patch_turn_label"] = patchTurnLabel
				}
				for k, v := range extra {
					turnInfo[k] = v
				}
				turnsCompleted = userTurnCounter
				finishTurn(sessTelemetry, turn, turnDecision, status, turnInfo, err)
			}
			recordPatchError := func(kind string, err error, extra map[string]any) {
				if extra == nil {
					extra = map[string]any{}
				}
				extra["patch_error_kind"] = kind
				if err != nil {
					extra["patch_error"] = err.Error()
				}
				extra["is_retry"] = !turnCounted
				extra["retry_reason"] = kind
				patchOutcome("error", err, extra)
			}
			jsonBytes, jerr := parser.ExtractPatchJSONPayload(question)
			if jerr != nil {
				stream.Abort("invalid patch payload")
				_ = tr.WriteTurn(step, "Patcher: invalid input JSON", "", nil, "Error extracting JSON: "+jerr.Error(), "patch-error")
				recordPatchError("invalid_patch_payload", jerr, nil)
				if shouldFinalizeAfterPatch {
					goto Finalize
				}
				continue
			}
			if cfg.verbose {
				fmt.Fprintln(os.Stderr, "[patcher] extracted JSON:", trimTo(string(jsonBytes), 1200))
			}
			if trajectoryWriter != nil {
				excerpt := trajectory.MakeTextExcerpt(string(jsonBytes), trajectoryWriter.ExcerptLen())
				turnInfo = trajectory.MergeExcerptWithPrefix(turnInfo, excerpt, "patch_instructions")
			}
			var instr mctpatcher.Instructions
			dec := json.NewDecoder(bytes.NewReader(jsonBytes))
			dec.DisallowUnknownFields()
			if derr := dec.Decode(&instr); derr != nil {
				stream.Abort("invalid patch payload")
				_ = tr.WriteTurn(step, "Patcher: invalid instructions", "", nil, "Error decoding JSON: "+derr.Error(), "patch-error")
				recordPatchError("invalid_patch_instructions", derr, nil)
				if shouldFinalizeAfterPatch {
					goto Finalize
				}
				continue
			}
			skipAllSuccess := false
			skipPaths := []string{}
			forceRepatch := instr.Metadata != nil && instr.Metadata.ForceRepatch
			if len(instr.Edits) > 0 {
				skipAllSuccess = true
				seenSkip := make(map[string]struct{})
				for i := range instr.Edits {
					edit := &instr.Edits[i]
					nPath := ""
					if norm, err := edit.NormalizedPath(repoRoot); err == nil {
						nPath = normalizePlannerPath(norm)
					} else {
						nPath = normalizePlannerPath(edit.Path)
					}
					if nPath == "" {
						skipAllSuccess = false
						continue
					}
					if !plannerProgress.hasSuccess(nPath) {
						skipAllSuccess = false
						continue
					}
					if _, exists := seenSkip[nPath]; !exists {
						seenSkip[nPath] = struct{}{}
						skipPaths = append(skipPaths, nPath)
					}
				}
			}
			// Guard disabled to allow repatching files multiple times in a session.
			if skipAllSuccess && len(skipPaths) > 0 && !forceRepatch && false {
				skipMsg := fmt.Sprintf("Skipping patch because all target files were already updated earlier this session: %s. Reload the latest file contents before generating another patch.", strings.Join(skipPaths, ", "))
				stream.Abort("patch skipped (already updated)")
				_ = tr.WriteTurn(step, "Patcher: skip (already updated)", "", nil, skipMsg, "patch-error")
				turnInfo["patch_skip_already_updated"] = skipPaths
				extra := map[string]any{"skip_files": skipPaths}
				recordPatchError("patch_skipped_already_success", fmt.Errorf("patch targeted previously updated files"), extra)
				plannerProgress.noteForceRepatchHint()
				if shouldFinalizeAfterPatch {
					goto Finalize
				}
				continue
			}
			if forceRepatch {
				turnInfo["patch_force_repatch"] = true
			}
			if instr.Metadata != nil {
				if desc := strings.TrimSpace(instr.Metadata.Description); desc != "" {
					patchTurnLabel = "Patcher: " + desc
				}
			}
			preparedInstr, modeAdjustments, precheckErr := preprocessPatchInstructions(repoRoot, instr)
			if len(modeAdjustments) > 0 {
				turnInfo["patch_precheck_adjustments"] = len(modeAdjustments)
				if cfg.verbose {
					for _, adj := range modeAdjustments {
						fmt.Fprintln(os.Stderr, "[patcher] pre-check:", adj.String())
					}
				}
			}
			if precheckErr != nil {
				summary := precheckErr.Summary()
				if summary == "" {
					summary = precheckErr.Error()
				}
				stream.Abort("patch instructions invalid")
				if err := tr.WriteTurn(step, patchTurnLabel, "", nil, summary, "patch-error"); err != nil {
					fmt.Fprintln(os.Stderr, "Transcript write error:", err)
					sessionErr = err
					recordPatchError("transcript_write_error", err, map[string]any{"patch_error_context": "precheck_summary"})
					return Result{ExitCode: 1, Err: err}
				}
				extra := map[string]any{"patch_precheck_conflicts": precheckErr.Count()}
				recordPatchError("patch_instruction_precheck", precheckErr, extra)
				if shouldFinalizeAfterPatch {
					goto Finalize
				}
				if !turnCounted {
					markTurnCounted()
					if shouldFinalizeAfterPatch {
						goto Finalize
					}
				}
				turnsCompleted = userTurnCounter
				continue
			}
			instr = preparedInstr
			if pRunner == nil {
				errDisabled := errors.New("patch runner disabled")
				stream.Abort("patch runner unavailable")
				_ = tr.WriteTurn(step, patchTurnLabel, "", nil, "Error: patch runner disabled", "patch-error")
				recordPatchError("patch_runner_unavailable", errDisabled, nil)
				if shouldFinalizeAfterPatch {
					goto Finalize
				}
				continue
			}
			if err := pRunner.Resolve(); err != nil {
				stream.Abort("patcher resolve failed")
				_ = tr.WriteTurn(step, patchTurnLabel, "", nil, "Error: "+err.Error(), "patch-error")
				recordPatchError("patch_runner_resolve", err, nil)
				if shouldFinalizeAfterPatch {
					goto Finalize
				}
				continue
			}
			autoFixApplied := false
			autoFixEdits := []int(nil)
			var result *mctpatcher.PatchResult
			var applyErr error
			var ctxPErr error
			for attempt := 0; attempt < 2; attempt++ {
				ctxP, cancelP := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
				ctxP = attachTrajectory(ctxP, trajectoryWriter, parentSpanID)
				result, applyErr = pRunner.Apply(ctxP, instr, cfg.verbose)
				if ctxP != nil {
					ctxPErr = ctxP.Err()
				}
				if cancelP != nil {
					cancelP()
				}
				if applyErr == nil {
					break
				}
				if isContextCancelled(applyErr) || isContextCancelled(ctxPErr) {
					stream.Abort("interrupted")
					return interruptedResult(applyErr)
				}
				var valErrFix *mctpatcher.ValidationError
				if !autoFixApplied && errors.As(applyErr, &valErrFix) {
					rewritten, edits := convertRewriteMissingToCreate(instr, valErrFix, cfg.verbose)
					if len(edits) > 0 {
						autoFixApplied = true
						autoFixEdits = edits
						instr = rewritten
						applyErr = nil
						continue
					}
				}
				break
			}
			if applyErr != nil {
				var cleanErr *mctpatcher.PatchNotCleanError
				var valErr *mctpatcher.ValidationError
				var genErr *mctpatcher.PatchGenerationError
				switch {
				case errors.As(applyErr, &cleanErr):
					stream.Abort("patch validation failed")
					summary := "Patch validation failed. See diagnostics below."
					if err := tr.WriteTurn(step, patchTurnLabel, "", nil, summary, "patch-error"); err != nil {
						fmt.Fprintln(os.Stderr, "Transcript write error:", err)
						sessionErr = err
						recordPatchError("transcript_write_error", err, map[string]any{"patch_error_context": "validation_summary"})
						return Result{ExitCode: 1, Err: err}
					}
					rec := transcript.PatchValidationRecord{
						Operation:  cleanErr.Diagnostics.Operation,
						Status:     "failed",
						PatchInput: trimTo(string(jsonBytes), 1000),
						Stderr:     trimTo(cleanErr.Diagnostics.Stderr, 800),
						Error:      strings.TrimSpace(cleanErr.Error()),
						Messages:   convertPatchMessages(cleanErr.Diagnostics.Messages),
						Conflicts:  formatContentConflicts(cleanErr.Diagnostics.ContentConflicts),
					}
					_ = tr.WritePatchValidation(step, rec)
					extra := map[string]any{
						"patch_validation_operation": cleanErr.Diagnostics.Operation,
						"patch_validation_status":    "failed",
						"patch_validation_messages":  len(cleanErr.Diagnostics.Messages),
						"conflicted_edits":           len(cleanErr.Diagnostics.ContentConflicts),
					}
					recordPatchError("patch_validation_failed", applyErr, extra)
					if shouldFinalizeAfterPatch {
						goto Finalize
					}
					// Allow session to progress past validation errors instead of retrying
					// Mark turn as counted so planner can decide next action in subsequent turn
					if !turnCounted {
						markTurnCounted()
						if shouldFinalizeAfterPatch {
							goto Finalize
						}
					}
					turnsCompleted = userTurnCounter
					continue
				case errors.As(applyErr, &valErr):
					stream.Abort("patch validation error")
					_ = tr.WriteTurn(step, patchTurnLabel, "", nil, valErr.Error(), "patch-error")
					recordPatchError("patch_validation_error", valErr, nil)
					if shouldFinalizeAfterPatch {
						goto Finalize
					}
					// Allow session to progress past validation errors instead of retrying
					// Mark turn as counted so planner can decide next action in subsequent turn
					if !turnCounted {
						markTurnCounted()
						if shouldFinalizeAfterPatch {
							goto Finalize
						}
					}
					turnsCompleted = userTurnCounter
					continue
				case errors.As(applyErr, &genErr):
					stream.Abort("patch generation error")
					_ = tr.WriteTurn(step, patchTurnLabel, "", nil, genErr.Error(), "patch-error")
					recordPatchError("patch_generation_error", genErr, nil)
					if shouldFinalizeAfterPatch {
						goto Finalize
					}
					// Allow session to progress past generation errors instead of retrying
					// Mark turn as counted so planner can decide next action in subsequent turn
					if !turnCounted {
						markTurnCounted()
						if shouldFinalizeAfterPatch {
							goto Finalize
						}
					}
					turnsCompleted = userTurnCounter
					continue
				default:
					stream.Abort("patcher execution error")
					_ = tr.WriteTurn(step, patchTurnLabel, "", nil, applyErr.Error(), "patch-error")
					recordPatchError("patch_apply_error", applyErr, nil)
					if shouldFinalizeAfterPatch {
						goto Finalize
					}
					// Allow session to progress past patcher errors instead of retrying
					// Mark turn as counted so planner can decide next action in subsequent turn
					if !turnCounted {
						markTurnCounted()
						if shouldFinalizeAfterPatch {
							goto Finalize
						}
					}
					turnsCompleted = userTurnCounter
					continue
				}
			}
			// Snapshot refresh occurs at the end of every loop iteration.
			if result == nil {
				errEmpty := errors.New("patcher returned empty result")
				stream.Abort("patcher returned no result")
				_ = tr.WriteTurn(step, patchTurnLabel, "", nil, errEmpty.Error(), "patch-error")
				recordPatchError("patch_empty_result", errEmpty, nil)
				if shouldFinalizeAfterPatch {
					goto Finalize
				}
				continue
			}
			if autoFixApplied {
				turnInfo["patch_auto_fix_rewrite_missing"] = map[string]any{
					"count": len(autoFixEdits),
					"edits": autoFixEdits,
				}
			}
			if desc := strings.TrimSpace(result.Description); desc != "" {
				patchTurnLabel = "Patcher: " + desc
			}
			workspaceStatus := "workspace_applied: no"
			if result.AppliedInWorkspace {
				workspaceStatus = "workspace_applied: yes"
			} else if cfg.dryRun {
				workspaceStatus = "workspace_applied: (dry-run)"
			}
			finalizeStatus := "finalize: atomic (done)"
			if cfg.dryRun {
				finalizeStatus = "finalize: (dry-run)"
			} else if cfg.patchNoApply {
				finalizeStatus = "finalize: skipped (--patch-no-apply)"
			}
			successDesc := strings.TrimSpace(result.Description)
			if successDesc == "" && instr.Metadata != nil {
				successDesc = strings.TrimSpace(instr.Metadata.Description)
			}
			if successDesc == "" {
				successDesc = strings.Join(result.FilesModified, ", ")
			}
			if successDesc == "" {
				successDesc = "Patch applied"
			}
			descTrimmed := strings.TrimSpace(successDesc)
			filesSummary := "(none)"
			if len(result.FilesModified) > 0 {
				filesSummary = strings.Join(result.FilesModified, ", ")
			}
			patchTurnLabel = "Patcher: [PATCH APPLIED] " + descTrimmed
			diffText, diffErr := patchDiffForTranscript(result.PatchPath, patchTranscriptDiffLimit)
			if diffErr != nil {
				diffText = fmt.Sprintf(
					"Patch diff unavailable (%v)\nPatch path: %s\nSequence: %d\nFiles modified: %s\ninsertions: %d\ndeletions: %d\n%s\n%s\nreverse_patch_path: %s",
					diffErr,
					strings.TrimSpace(result.PatchPath),
					result.Sequence,
					filesSummary,
					result.Insertions,
					result.Deletions,
					workspaceStatus,
					finalizeStatus,
					strings.TrimSpace(result.ReversePatchPath),
				)
			}
			ans := diffText

			// --- BEGIN: Automatic DecisionFullDiff Injection (Post-Patch) ---
			// NOTE: full diff is written after the patch turn so it appears immediately
			// after the patch in the transcript.
			// --- END: Automatic DecisionFullDiff Injection ---

			// Defer transcript write until accept/reject so only one final
			// patch turn is recorded (success or reject). Keep the diff in
			// memory and surface it for the planner via augmented context.
			pendingPatchDraft = &patchTranscriptDraft{
				Step:        step,
				Description: descTrimmed,
				Answer:      ans,
			}

			stream.Complete(ans)
			lastAnswer = ans
			review := &planner.PendingReview{
				PatchPath:        strings.TrimSpace(result.PatchPath),
				ReversePatchPath: strings.TrimSpace(result.ReversePatchPath),
				Description:      successDesc,
				Files:            append([]string(nil), result.FilesModified...),
				Sequence:         result.Sequence,
				Insertions:       result.Insertions,
				Deletions:        result.Deletions,
			}
			if len(result.FilesModified) > 0 {
				recordDiscoveryPending(sessionID, result.FilesModified, cfg.verbose)
				// Also refresh the persistent discovery workspace immediately so
				// consecutive patch decisions see updated files without waiting for
				// the next file-discovery invocation.
				_ = mctsync.RefreshSyncedWorkspace(sessionID, result.FilesModified, cfg.verbose)
			}
			plannerProgress.beginPendingReview(review)
			autoAcceptNote := ""
			autoAccepted := false
			var autoAcceptedReview *planner.PendingReview
			if patchReviewDisabled {
				autoAcceptNote = "Review disabled – patch auto-accepted."
				if err := writePendingPatchTranscript("SUCCESS", "patch", autoAcceptNote, false); err != nil {
					fmt.Fprintln(os.Stderr, "Transcript write error:", err)
					sessionErr = err
					patchOutcome("error", err, map[string]any{"planner_review_action": "auto-accept"})
					turnsCompleted = userTurnCounter
					return Result{ExitCode: 1, Err: err}
				}
				autoAcceptedReview = plannerProgress.commitPendingReview()
				autoAccepted = true
				if autoAcceptedReview != nil {
					review = autoAcceptedReview
				}
			}
			plannerSuccessFiles := plannerProgress.successList()
			turnInfo["planner_applied_patches"] = plannerProgress.appliedCount()
			turnInfo["planner_success_file_count"] = len(plannerSuccessFiles)
			reviewPending := plannerProgress.hasPendingReview()
			turnInfo["planner_pending_review"] = reviewPending
			turnInfo["patch_review_pending"] = reviewPending
			if autoAccepted {
				turnInfo["planner_review_action"] = "auto-accept"
				turnInfo["patch_finalize_pending"] = false
				if autoAcceptNote != "" {
					turnInfo["planner_review_note"] = autoAcceptNote
				}
				if autoAcceptedReview != nil {
					turnInfo["patch_review_sequence"] = autoAcceptedReview.Sequence
					if autoAcceptedReview.Description != "" {
						turnInfo["patch_review_description"] = autoAcceptedReview.Description
					}
					if len(autoAcceptedReview.Files) > 0 {
						turnInfo["patch_review_files"] = append([]string(nil), autoAcceptedReview.Files...)
					}
				}
				if review != nil {
					display.Notify(fmt.Sprintf("Patch %d auto-accepted (review disabled)%s", review.Sequence, formatOptionalSuffix(review.Description)))
				} else {
					display.Notify("Patch auto-accepted (review disabled)")
				}
			} else {
				turnInfo["patch_finalize_pending"] = true
				if review != nil && len(review.Files) > 0 {
					turnInfo["planner_pending_review_files"] = append([]string(nil), review.Files...)
				}
			}
			extra := map[string]any{}
			if trajectoryWriter != nil {
				extra = trajectory.MergeExcerptWithPrefix(extra, trajectory.MakeTextExcerpt(ans, trajectoryWriter.ExcerptLen()), "answer")
			}
			extra["patch_path"] = review.PatchPath
			extra["patch_sequence"] = result.Sequence
			extra["patch_insertions"] = result.Insertions
			extra["patch_deletions"] = result.Deletions
			extra["patch_files_modified"] = len(result.FilesModified)
			if len(result.FilesModified) > 0 && len(result.FilesModified) <= 10 {
				extra["patch_files"] = append([]string(nil), result.FilesModified...)
			}
			if len(result.FilesModified) > 0 {
				extra["success_files"] = append([]string(nil), result.FilesModified...)
			}
			extra["planner_applied_patches"] = plannerProgress.appliedCount()
			extra["planner_success_files_total"] = len(plannerSuccessFiles)
			if len(plannerSuccessFiles) > 0 {
				extra["planner_success_files"] = plannerSuccessFiles
			}
			extra["patch_workspace_applied"] = result.AppliedInWorkspace
			extra["patch_applied"] = result.AppliedInWorkspace
			extra["patch_review_pending"] = plannerProgress.hasPendingReview()
			extra["patch_finalize_pending"] = plannerProgress.hasPendingReview()
			if autoAccepted {
				extra["patch_review_action"] = "auto-accept"
				if autoAcceptNote != "" {
					extra["patch_review_note"] = autoAcceptNote
				}
			}
			if review.ReversePatchPath != "" {
				extra["patch_reverse_path"] = review.ReversePatchPath
			}
			extra["patch_review_sequence"] = result.Sequence
			patchOutcome("success", nil, extra)
			if shouldFinalizeAfterPatch && !plannerProgress.hasPendingReview() {
				goto Finalize
			}
			goto TurnDone

		case planner.DecisionFinalize:
			goto Finalize
		default:
			goto Finalize
		}

	TurnDone:
		// Postflight sync: ensure snapshot edits land in the host repo.
		// Only sync for decisions that may have modified the snapshot:
		// - DecisionPatch: No sync (patches already applied in workspace, no host changes expected)
		// - DecisionAccept: No sync (reviews don't modify snapshot)
		// - DecisionReject: No sync (review discards don't modify snapshot)
		// - DecisionAsk: Sync only for shell-agent mode (file-discovery doesn't modify snapshot)
		shouldSync := false
		switch decision {
		case planner.DecisionAsk:
			// For DecisionAsk, only sync if shell-agent was used (executed commands may have modified files)
			// File-discovery mode makes no modifications, so no sync needed
			if useShellAgent {
				shouldSync = true
			}
		case planner.DecisionPatch:
			// Patcher applies changes directly to the host checkout.
			// No snapshot->host sync needed.
			shouldSync = false
		case planner.DecisionAccept, planner.DecisionReject:
			// Reviews don't modify the snapshot.
			shouldSync = false
		}
		if shouldSync {
			if root := strings.TrimSpace(tempdir.SessionRoot()); root != "" {
				snapshotRepoRoot := filepath.Join(root, "repo")
				patchDir := filepath.Join(root, "patches")
				if err := workspace.SyncSnapshotToHost(snapshotRepoRoot, repoRoot, patchDir); err != nil {
					fmt.Fprintln(os.Stderr, "Error: snapshot->host sync failed:", err)
					return Result{ExitCode: 1, Err: err}
				}
			}
		}
		continue
	}

Finalize:
	if err := rootCtx.Err(); err != nil {
		turnsCompleted = countTurns(tr.Content())
		userTurnCounter = turnsCompleted
		return interruptedResult(err)
	}
	// One last planning opportunity before finalizing: if planner returns patch, run exactly one patch turn.
	{
		step := countTurns(tr.Content()) + 1
		parentSpanID := ""
		if sessTelemetry != nil {
			parentSpanID = sessTelemetry.span.ID
		}
		ctx, cancel := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
		trFull := tr.Content()
		ctx = attachTrajectory(ctx, trajectoryWriter, parentSpanID)
		pl.UpdateProgress(plannerProgress.snapshot())
		lastDec, lastBody, err := pl.Plan(ctx, goal, trFull, step, cfg.maxSteps)
		cancel()
		if err == nil && lastDec == planner.DecisionPatch {
			if pRunner == nil {
				if cfg.verbose {
					fmt.Fprintln(os.Stderr, "[patcher] skipping pre-finalize patch: patch runner disabled")
				}
			} else {
				stream := display.BeginPrompt("Patcher: pre-finalize patch", patcherPromptOpts)
				if cfg.verbose {
					fmt.Fprintln(os.Stderr, "[patcher] pre-finalize planner payload (raw):", trimTo(strings.TrimSpace(lastBody), 1200))
				}
				jsonBytes, jerr := parser.ExtractPatchJSONPayload(lastBody)
				handled := false
				if jerr != nil {
					stream.Abort("invalid patch payload")
					stream = nil
					_ = tr.WriteTurn(step, "Patcher: pre-finalize (invalid JSON)", "", nil, jerr.Error(), "patch-error")
					handled = true
				}
				if !handled {
					if cfg.verbose {
						fmt.Fprintln(os.Stderr, "[patcher] pre-finalize extracted JSON:", trimTo(string(jsonBytes), 1200))
					}
					var instr mctpatcher.Instructions
					dec := json.NewDecoder(bytes.NewReader(jsonBytes))
					dec.DisallowUnknownFields()
					if derr := dec.Decode(&instr); derr != nil {
						stream.Abort("invalid patch payload")
						stream = nil
						_ = tr.WriteTurn(step, "Patcher: pre-finalize (decode error)", "", nil, derr.Error(), "patch-error")
						handled = true
					} else if err := pRunner.Resolve(); err != nil {
						stream.Abort("patcher resolve failed")
						stream = nil
						_ = tr.WriteTurn(step, "Patcher: pre-finalize (resolve failed)", "", nil, err.Error(), "patch-error")
						handled = true
					} else {
						ctxP, cancelP := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
						ctxP = attachTrajectory(ctxP, trajectoryWriter, parentSpanID)
						result, applyErr := pRunner.Apply(ctxP, instr, cfg.verbose)
						var ctxPErr error
						if ctxP != nil {
							ctxPErr = ctxP.Err()
						}
						if cancelP != nil {
							cancelP()
						}
						if applyErr != nil {
							if isContextCancelled(applyErr) || isContextCancelled(ctxPErr) {
								if stream != nil {
									stream.Abort("interrupted")
									stream = nil
								}
								return interruptedResult(applyErr)
							}
							var cleanErr *mctpatcher.PatchNotCleanError
							if errors.As(applyErr, &cleanErr) {
								if stream != nil {
									stream.Abort("patch validation failed")
									stream = nil
								}
								rec := transcript.PatchValidationRecord{
									Operation:  cleanErr.Diagnostics.Operation,
									Status:     "failed",
									PatchInput: trimTo(string(jsonBytes), 1000),
									Stderr:     trimTo(cleanErr.Diagnostics.Stderr, 800),
									Error:      strings.TrimSpace(cleanErr.Error()),
									Messages:   convertPatchMessages(cleanErr.Diagnostics.Messages),
									Conflicts:  formatContentConflicts(cleanErr.Diagnostics.ContentConflicts),
								}
								_ = tr.WritePatchValidation(step, rec)
							} else {
								if stream != nil {
									stream.Abort("patcher execution error")
									stream = nil
								}
								_ = tr.WriteTurn(step, "Patcher: pre-finalize (error)", "", nil, trimTo(applyErr.Error(), 800), "patch-error")
							}
							handled = true
						} else if result == nil {
							if stream != nil {
								stream.Abort("patcher returned no result")
								stream = nil
							}
							_ = tr.WriteTurn(step, "Patcher: pre-finalize (empty result)", "", nil, "patcher returned empty result", "patch-error")
							handled = true
						} else {
							qline := "Patcher: pre-finalize"
							if result.Description != "" {
								qline = "Patcher: pre-finalize - " + strings.TrimSpace(result.Description)
							}
							workspaceStatus := "workspace_applied: no"
							if result.AppliedInWorkspace {
								workspaceStatus = "workspace_applied: yes"
							} else if cfg.dryRun {
								workspaceStatus = "workspace_applied: (dry-run)"
							}
							finalizeStatus := "finalize: pending"
							switch {
							case cfg.dryRun:
								finalizeStatus = "finalize: (dry-run)"
							case cfg.patchNoApply:
								finalizeStatus = "finalize: skipped (--patch-no-apply)"
							}
							diffText, diffErr := patchDiffForTranscript(result.PatchPath, patchTranscriptDiffLimit)
							if diffErr != nil {
								filesSummary := "(none)"
								if len(result.FilesModified) > 0 {
									filesSummary = strings.Join(result.FilesModified, ", ")
								}
								diffText = fmt.Sprintf(
									"Patch diff unavailable (%v)\nPatch path: %s\nSequence: %d\nFiles modified: %s\ninsertions: %d\ndeletions: %d\n%s\n%s",
									diffErr,
									strings.TrimSpace(result.PatchPath),
									result.Sequence,
									filesSummary,
									result.Insertions,
									result.Deletions,
									workspaceStatus,
									finalizeStatus,
								)
							}
							_ = tr.WriteTurn(step, qline, "", nil, diffText, "patch")
							if stream != nil {
								stream.Complete(diffText)
							}
							handled = true
						}
					}
				}
				if !handled && stream != nil {
					stream.Abort("no patch output")
				}
			}
		}
	}
	turns := countTurns(tr.Content())
	ctx, cancelF := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
	trFull := tr.Content()
	ctx = attachTrajectory(ctx, trajectoryWriter, parentSpanID)
	answer, ferr := pl.Finalize(ctx, goal, trFull)
	var finalCtxErr error
	if ctx != nil {
		finalCtxErr = ctx.Err()
	}
	if cancelF != nil {
		cancelF()
	}
	if ferr != nil {
		if isContextCancelled(ferr) || isContextCancelled(finalCtxErr) {
			turnsCompleted = turns
			userTurnCounter = turns
			return interruptedResult(ferr)
		}
		fmt.Fprintln(os.Stderr, "Finalizer error:", ferr)
		sessionErr = ferr
		turnsCompleted = turns
		return Result{ExitCode: 1, Err: ferr}
	}
	if err := tr.WriteFinal(answer, turns, turns >= cfg.maxSteps); err != nil {
		fmt.Fprintln(os.Stderr, "Transcript write error:", err)
		sessionErr = err
		turnsCompleted = turns
		return Result{ExitCode: 1, Err: err}
	}
	if err := writeFinalAnswer(sessionID, answer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
		fmt.Fprintln(os.Stderr, "Final file write error:", err)
		sessionErr = err
		turnsCompleted = turns
		return Result{ExitCode: 1, Err: err}
	}
	presentFinalAnswer(display, answer)
	display.EndSession()
	sessionClosed = true
	sessionStatus = "success"
	turnsCompleted = turns
	keepSessionState = true
	pendingState = &SessionState{
		SessionID:      sessionID,
		Goal:           goal,
		TurnsCompleted: turnsCompleted,
	}
	if tr != nil {
		pendingState.TranscriptPath = tr.Path()
		pendingState.Transcript = tr.Content()
	}
	applyPlannerProgress(pendingState)
	printResumeHint("=== SESSION COMPLETE ===", turnsCompleted)
	return Result{ExitCode: 0, Status: sessionStatus, Turns: turns, SessionID: sessionID}
}

func appendResumePromptContext(transcript, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return transcript
	}
	trimmedTranscript := strings.TrimRight(transcript, "\n")
	var b strings.Builder
	if trimmedTranscript != "" {
		b.WriteString(trimmedTranscript)
		b.WriteString("\n\n")
	}
	b.WriteString("== USER FEEDBACK\n\n")
	b.WriteString(prompt)
	b.WriteString("\n")
	return b.String()
}

func extractUserFeedback(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	if idx := strings.LastIndex(prompt, "\n\n\"\"\"\n"); idx != -1 {
		candidate := prompt[idx+len("\n\n\"\"\"\n"):]
		if end := strings.Index(candidate, "\n\"\"\""); end != -1 {
			candidate = candidate[:end]
		}
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	if idx := strings.LastIndex(prompt, "The user provided additional guidance:"); idx != -1 {
		candidate := prompt[idx+len("The user provided additional guidance:"):]
		candidate = strings.TrimSpace(candidate)
		candidate = strings.Trim(candidate, "\"\n")
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return prompt
}

func convertRewriteMissingToCreate(instr mctpatcher.Instructions, valErr *mctpatcher.ValidationError, verbose bool) (mctpatcher.Instructions, []int) {
	if valErr == nil {
		return instr, nil
	}
	matches := rewriteMissingPattern.FindAllStringSubmatch(valErr.Error(), -1)
	if len(matches) == 0 {
		return instr, nil
	}
	if len(instr.Edits) == 0 {
		return instr, nil
	}
	edits := make([]mctpatcher.Edit, len(instr.Edits))
	copy(edits, instr.Edits)
	changed := make([]int, 0, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if idx < 0 || idx >= len(edits) {
			continue
		}
		if edits[idx].Mode != mctpatcher.ModeRewrite {
			continue
		}
		if strings.TrimSpace(edits[idx].NewContent) == "" {
			continue
		}
		edits[idx].Mode = mctpatcher.ModeCreate
		changed = append(changed, idx)
	}
	if len(changed) == 0 {
		return instr, nil
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[patcher] auto-fix: switching rewrite->create for edits %v due to missing files\n", changed)
	}
	return mctpatcher.Instructions{Metadata: instr.Metadata, Edits: edits}, changed
}

func loadProjectBackground(repoRoot string) (string, error) {
	commit, err := readmeHeadCommitFn()
	if err != nil {
		return "", fmt.Errorf("resolve HEAD commit: %w", err)
	}
	readmeCommit, err := readmeCommitForProjectFn(commit)
	if err != nil {
		return "", fmt.Errorf("locate README for commit %s: %w", commit, err)
	}
	if err := readmeCheckoutReadonlyFn(commit); err != nil {
		return "", fmt.Errorf("checkout README for commit %s (readme commit %s): %w", commit, strings.TrimSpace(readmeCommit), err)
	}
	base := strings.TrimSpace(repoRoot)
	if base == "" {
		base = "."
	}
	path := filepath.Join(base, ".machtiani", "artifacts", "readme", "internal-readme.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read README at %s: %w", path, err)
	}
	background := string(data)
	if strings.TrimSpace(background) == "" {
		return "", fmt.Errorf("internal README at %s is empty", path)
	}
	return background, nil
}

func finishTurn(sessTelemetry *sessionTelemetry, tt *turnTelemetry, decision string, status string, info map[string]any, err error) {
	if sessTelemetry == nil {
		return
	}
	sessTelemetry.EndTurn(tt, decision, status, info, err)
}

func formatOptionalSuffix(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return ""
	}
	return fmt.Sprintf(" (%s)", desc)
}

func analyzeTagFormat(answer string, retrieved []string) (map[string]any, []string) {
	stats := map[string]any{
		"tag_format_enabled": true,
	}
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		stats["tag_format_detected"] = false
		stats["tag_format_total"] = 0
		stats["tag_format_valid_ratio"] = 0.0
		return stats, nil
	}

	matches := tagFormatPattern.FindAllStringSubmatch(answer, -1)
	total := len(matches)
	stats["tag_format_total"] = total
	if total == 0 {
		stats["tag_format_detected"] = false
		stats["tag_format_valid_ratio"] = 0.0
		return stats, nil
	}
	stats["tag_format_detected"] = true

	retrievedSet := make(map[string]struct{}, len(retrieved))
	for _, p := range retrieved {
		if n := normalizeTagPath(p); n != "" {
			retrievedSet[n] = struct{}{}
		}
	}

	valid := 0
	invalid := 0
	missingPaths := map[string]struct{}{}
	rangeIssues := map[string]struct{}{}
	parseIssues := map[string]struct{}{}

	for _, match := range matches {
		pathRaw := match[1]
		startRaw := strings.TrimSpace(match[2])
		endRaw := strings.TrimSpace(match[3])
		path := normalizeTagPath(pathRaw)
		start, serr := strconv.Atoi(startRaw)
		end, eerr := strconv.Atoi(endRaw)
		isValid := true
		if path == "" {
			isValid = false
			parseIssues[strings.TrimSpace(match[0])] = struct{}{}
		}
		if serr != nil || eerr != nil {
			isValid = false
			parseIssues[strings.TrimSpace(match[0])] = struct{}{}
		}
		if isValid && (start <= 0 || end <= 0 || end < start) {
			isValid = false
			rangeIssues[fmt.Sprintf("%s:%d:%d", path, start, end)] = struct{}{}
		}
		if isValid {
			if _, ok := retrievedSet[path]; !ok {
				isValid = false
				missingPaths[path] = struct{}{}
			}
		}
		if isValid {
			valid++
		} else {
			invalid++
		}
	}

	stats["tag_format_valid"] = valid
	if invalid > 0 {
		stats["tag_format_invalid"] = invalid
	}
	if total > 0 {
		stats["tag_format_valid_ratio"] = float64(valid) / float64(total)
	}

	if len(missingPaths) > 0 {
		paths := make([]string, 0, len(missingPaths))
		for p := range missingPaths {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		stats["tag_format_missing_paths"] = paths
	}
	if len(rangeIssues) > 0 {
		ranges := make([]string, 0, len(rangeIssues))
		for r := range rangeIssues {
			ranges = append(ranges, r)
		}
		sort.Strings(ranges)
		stats["tag_format_range_errors"] = ranges
	}
	if len(parseIssues) > 0 {
		fragments := make([]string, 0, len(parseIssues))
		for frag := range parseIssues {
			fragments = append(fragments, frag)
		}
		sort.Strings(fragments)
		stats["tag_format_parse_errors"] = fragments
	}

	warnings := make([]string, 0, 3)
	if invalid > 0 {
		warnings = append(warnings, fmt.Sprintf("%d of %d tag-format references failed validation", invalid, total))
	}
	if len(missingPaths) > 0 {
		paths := stats["tag_format_missing_paths"].([]string)
		warnings = append(warnings, fmt.Sprintf("tag references include paths not in prompt context: %s", strings.Join(paths, ", ")))
	}
	if len(rangeIssues) > 0 {
		ranges := stats["tag_format_range_errors"].([]string)
		warnings = append(warnings, fmt.Sprintf("tag references include invalid ranges: %s", strings.Join(ranges, ", ")))
	}
	if len(parseIssues) > 0 {
		fragments := stats["tag_format_parse_errors"].([]string)
		warnings = append(warnings, fmt.Sprintf("unable to parse tag references: %s", strings.Join(fragments, ", ")))
	}

	return stats, warnings
}

func normalizeTagPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimPrefix(trimmed, "./")
	return filepath.ToSlash(trimmed)
}

func recordDiscoveryPending(sessionID string, files []string, verbose bool) {
	if len(files) == 0 || strings.TrimSpace(sessionID) == "" {
		return
	}
	scratch, err := artifacts.SessionScratchDirectory(sessionID)
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[patcher] unable to resolve discovery scratch directory: %v\n", err)
		}
		return
	}
	dir := filepath.Join(scratch, "file-discovery")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[patcher] unable to create discovery scratch directory: %v\n", err)
		}
		return
	}
	pendingPath := filepath.Join(dir, "pending.txt")
	f, err := os.OpenFile(pendingPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[patcher] unable to append discovery pending file: %v\n", err)
		}
		return
	}
	defer f.Close()
	for _, rel := range files {
		norm := strings.TrimSpace(rel)
		if norm == "" {
			continue
		}
		cleaned := filepath.Clean(norm)
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, `..`+string(os.PathSeparator)) {
			continue
		}
		canonical := filepath.ToSlash(cleaned)
		if _, err := fmt.Fprintln(f, canonical); err != nil {
			if verbose {
				fmt.Fprintf(os.Stderr, "[patcher] unable to record pending file %s: %v\n", canonical, err)
			}
			return
		}
	}
}
