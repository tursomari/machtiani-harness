package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	"github.com/tursomari/machtiani/agent/internal/mct/readmesync"
	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/runner"
	"github.com/tursomari/machtiani/agent/internal/tempdir"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
	"github.com/tursomari/machtiani/agent/internal/workspace"
)

const (
	backgroundQuestionPrompt = "Give me the background of the project."
	backgroundFallbackAnswer = "No project documentation has been created yet. Please run `mct-agent sync` to generate initial project documentation."
	patchTranscriptDiffLimit = 0 // zero disables transcript diff truncation
	patchReviewDisabled      = true
)

var (
	readmeHeadCommitFn          = readmesync.HeadCommit
	readmeCommitForProjectFn    = readmesync.READMECommitForProject
	readmeCheckoutReadonlyFn    = readmesync.CheckoutReadonlyREADME
	loadPatchPlanFn             = LoadPatchPlan
	formatPatchPlanForDisplayFn = formatPatchPlanForDisplay
	tagFormatPattern            = regexp.MustCompile(`\[(?P<path>[^\[\]|]+?)\s*\|\s*(?P<start>[^:\]]+)\s*:\s*(?P<end>[^\]]+)\]`)
	rewriteMissingPattern       = regexp.MustCompile(`edit\[(\d+)\]\s+rewrite requires existing file`)
	workspaceRoot               string
)

func isLocalSessionEnvironment(cfg *llm.Config) bool {
	if cfg == nil || cfg.Environment == nil {
		return true
	}
	envType := strings.ToLower(strings.TrimSpace(cfg.Environment.Type))
	return envType == "" || envType == "local"
}

type plannerProgressTracker struct {
	successSet       map[string]struct{}
	successFiles     []string
	applied          int
	forceRepatchHint bool
	fileDedupSet     map[string]string
	pendingReview    *planner.PendingReview
	lastPatchedFile  string
	patchPlanPending bool
}

type patchPlanUpdater interface {
	GeneratePatchPlan(ctx context.Context, goal, transcript string) (*planner.PatchPlan, error)
	UpdatePatchPlan(ctx context.Context, goal, transcript string, existing *planner.PatchPlan, lastPatchedFile string) (*planner.PatchPlan, error)
}

type patchPlanNotifier interface {
	Notify(string)
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

func normalizePlannerPath(rawPath string) string {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return ""
	}
	cleaned := pathpkg.Clean(filepath.ToSlash(trimmed))
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func (p *plannerProgressTracker) setLastPatchedFile(path string) {
	if p == nil {
		return
	}
	p.lastPatchedFile = normalizePlannerPath(path)
}

func (p *plannerProgressTracker) getLastPatchedFile() string {
	if p == nil {
		return ""
	}
	return p.lastPatchedFile
}

func (p *plannerProgressTracker) needsPatchPlanUpdate() bool {
	if p == nil {
		return false
	}
	return p.patchPlanPending
}

func (p *plannerProgressTracker) clearPatchPlanPending() {
	if p == nil {
		return
	}
	p.patchPlanPending = false
}

func extractPrimaryPatchTarget(instr mctpatcher.Instructions) string {
	if len(instr.Edits) == 0 {
		return ""
	}
	return strings.TrimSpace(instr.Edits[0].Path)
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

func formatGoalText(originalPrompt, taskDescription string) string {
	originalPrompt = strings.TrimSpace(originalPrompt)
	if originalPrompt == "" {
		return ""
	}
	return originalPrompt
}

func startTranscriptIfNeeded(tr *transcript.Transcript, originalPrompt, taskDescription, sessionID string, cfg legacyConfig, resumeMode bool) (bool, error) {
	if tr == nil {
		return false, nil
	}
	starting := !resumeMode || tr.Content() == ""
	if starting {
		if err := tr.WriteHeader(originalPrompt, taskDescription, sessionID, cfg); err != nil {
			return false, err
		}
	}
	return starting, nil
}

func restoreTranscriptFromConversation(tr *transcript.Transcript, conversationRendered string, resumeMode bool) error {
	if tr == nil {
		return nil
	}
	if !resumeMode {
		return nil
	}
	if strings.TrimSpace(conversationRendered) == "" {
		return nil
	}
	existing := tr.Content()
	if existing == conversationRendered {
		return nil
	}
	sanitizedExisting := strings.ReplaceAll(existing, "\x00", "")
	if sanitizedExisting != conversationRendered {
		return tr.Restore(conversationRendered)
	}
	if existing != sanitizedExisting {
		return tr.Restore(conversationRendered)
	}
	return nil
}

func writeInitialBackgroundIfNeeded(tr *transcript.Transcript, repoRoot string, cfg legacyConfig, isChildSession bool, startingTranscript bool, writeTurn func(step int, question, savedPath string, retrieved []string, summary string, decision string) error) error {
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
	if writeTurn == nil {
		return tr.WriteTurn(0, backgroundQuestionPrompt, "", nil, prefillAnswer, "background")
	}
	return writeTurn(0, backgroundQuestionPrompt, "", nil, prefillAnswer, "background")
}

func writePatchPlanTranscriptEntry(tr *transcript.Transcript, plan *PatchPlan, action string) error {
	if tr == nil || plan == nil {
		return nil
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal patch plan for transcript: %w", err)
	}
	label := strings.ToUpper(strings.TrimSpace(action))
	if label == "" {
		label = "UPDATE"
	}
	var b strings.Builder
	b.WriteString("\n== PATCH PLAN ")
	b.WriteString(label)
	b.WriteString("\n\n")
	b.Write(data)
	b.WriteString("\n")
	if err := tr.AppendRaw(b.String()); err != nil {
		return fmt.Errorf("append patch plan transcript: %w", err)
	}
	// Remove older patch plan sections, keeping recent history.
	if err := tr.DeduplicatePatchPlan(); err != nil {
		fmt.Fprintf(os.Stderr, "[patch-plan] deduplicate failed: %v\n", err)
	}
	return nil
}

func invokePatchPlanUpdateHook(ctx context.Context, pl patchPlanUpdater, tr *transcript.Transcript, sessionID, goal, transcriptContent, lastPatchedFile string, notifier patchPlanNotifier, allowCreate bool) (*PatchPlan, error) {
	existing, err := LoadPatchPlan(sessionID)
	if err != nil {
		return nil, fmt.Errorf("load patch plan: %w", err)
	}

	var updated *PatchPlan
	action := "UPDATED"
	if existing == nil {
		if !allowCreate {
			return nil, nil
		}
		action = "CREATED"
		updated, err = pl.GeneratePatchPlan(ctx, goal, transcriptContent)
	} else {
		updated, err = pl.UpdatePatchPlan(ctx, goal, transcriptContent, existing, lastPatchedFile)
		if err != nil {
			return nil, fmt.Errorf("generate/update plan: %w", err)
		}
		if updated == nil {
			return existing, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("generate/update plan: %w", err)
	}
	if updated == nil {
		return nil, nil
	}
	if err := SavePatchPlan(sessionID, updated); err != nil {
		return nil, fmt.Errorf("save patch plan: %w", err)
	}
	if err := writePatchPlanTranscriptEntry(tr, updated, action); err != nil {
		return nil, err
	}
	if notifier != nil {
		total, complete := updated.Progress()
		status := strings.ToLower(action)
		notifier.Notify(fmt.Sprintf("Patch plan %s (%d/%d complete)", status, complete, total))
		if rendered := formatPatchPlanForDisplay(updated); strings.TrimSpace(rendered) != "" {
			notifier.Notify(rendered)
		}
	}
	return updated, nil
}

func updatePatchPlanIfNeeded(ctx context.Context, pl patchPlanUpdater, tr *transcript.Transcript, sessionID, goal, transcriptContent, lastPatchedFile string, notifier patchPlanNotifier, progress *plannerProgressTracker) (*PatchPlan, error) {
	if progress == nil || !progress.needsPatchPlanUpdate() {
		return nil, nil
	}
	updated, err := invokePatchPlanUpdateHook(ctx, pl, tr, sessionID, goal, transcriptContent, lastPatchedFile, notifier, false)
	if err != nil {
		return nil, err
	}
	progress.clearPatchPlanPending()
	return updated, nil
}

func formatPatchPlanForDisplay(plan *PatchPlan) string {
	if plan == nil {
		return ""
	}
	total, complete := plan.Progress()
	var b strings.Builder
	goal := strings.TrimSpace(plan.Goal)
	if goal != "" {
		b.WriteString(goal)
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Patch plan status: %d/%d complete", complete, total)
	for idx, item := range plan.Items {
		state := "[ ]"
		if item.Complete {
			state = "[x]"
		}
		desc := strings.TrimSpace(item.Description)
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&b, "\n  %d. %s %s", idx+1, state, desc)
	}
	return b.String()
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
	patchedFile := ""
	if len(p.pendingReview.Files) > 0 {
		patchedFile = normalizePlannerPath(p.pendingReview.Files[0])
	}
	previousFile := normalizePlannerPath(p.lastPatchedFile)
	if patchedFile != "" {
		if previousFile != "" && patchedFile != previousFile {
			p.patchPlanPending = true
		}
		p.lastPatchedFile = patchedFile
	}
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
	return runSession(ctx, opts)
}

func runSession(ctx context.Context, opts Options) Result {
	if opts.Context != nil {
		ctx = opts.Context
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rootCtx := ctx
	timerMgr := opts.ProcessTimerManager
	bootstrap, earlyResult, ok := prepareRunBootstrap(rootCtx, opts)
	if !ok {
		return earlyResult
	}
	opts = bootstrap.opts
	cfg := bootstrap.cfg
	sessionID := bootstrap.sessionID
	goal := bootstrap.goal
	resumePrompt := bootstrap.resumePrompt
	originalPrompt := bootstrap.originalPrompt
	taskDescription := bootstrap.taskDescription
	plannerOverlay := bootstrap.plannerOverlay
	conversationGoal := bootstrap.conversationGoal
	conversationPath := bootstrap.conversationPath
	resumeMode := bootstrap.resumeMode
	loadedState := bootstrap.loadedState
	metaInstructions := bootstrap.metaInstructions
	metaInstructionPath := bootstrap.metaInstructionPath
	runState := bootstrap.runState
	plannerProgress := runState.plannerProgress
	envBootstrap, err := prepareSessionEnvironment(sessionID, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error preparing session environment:", err)
		return Result{ExitCode: 1, Err: err}
	}
	sessionTempRoot := envBootstrap.sessionTempRoot
	workspaceRoot = envBootstrap.workspaceRoot
	useSnapshotWorkspace := envBootstrap.useSnapshotWorkspace
	var sessLock *sessionLock
	defer func() {
		if sessLock != nil {
			if err := sessLock.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "[session-lock] warning: failed to release lock: %v\n", err)
			}
		}
		if envBootstrap.restore != nil {
			envBootstrap.restore()
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
	if useSnapshotWorkspace {
		// Prepare the workspace repo snapshot using the resolved repo root.
		if _, _, err := workspace.EnsureRepoSnapshot(repoRoot, workspaceRoot); err != nil {
			fmt.Fprintln(os.Stderr, "Warning: unable to prepare workspace repo snapshot:", err)
		}
		if err := tempdir.SetSessionRoot(workspaceRoot); err != nil {
			fmt.Fprintln(os.Stderr, "Error preparing session temp root:", err)
			return Result{ExitCode: 1, Err: err}
		}
	}
	if trajectoryWriter != nil {
		fmt.Fprintln(os.Stderr, "[trajectory] unified stream:", trajectoryWriter.Config().Path)
	}

	display := ui.NewTerminalDisplay(os.Stdout, timerMgr, sessionID, strings.TrimSpace(cfg.parentSessionID))
	var failoverCancel context.CancelFunc
	var failoverDone <-chan struct{}
	var retryCancel context.CancelFunc
	var retryDone <-chan struct{}
	var cacheUsageCancel context.CancelFunc
	var cacheUsageDone <-chan struct{}
	var cacheDiagnosticsCancel context.CancelFunc
	var cacheDiagnosticsDone <-chan struct{}
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
		cancel, done, err = startLLMRetryLogger(display, trajectoryWriter.Config().Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[trajectory] retry listener setup error: %v\n", err)
		} else {
			retryCancel = cancel
			retryDone = done
		}
		cancel, done, err = startLLMCacheUsageLogger(display, trajectoryWriter.Config().Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[trajectory] cache usage listener setup error: %v\n", err)
		} else {
			cacheUsageCancel = cancel
			cacheUsageDone = done
		}
		cancel, done, err = startLLMCacheDiagnosticsLogger(display, trajectoryWriter.Config().Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[trajectory] cache diagnostics listener setup error: %v\n", err)
		} else {
			cacheDiagnosticsCancel = cancel
			cacheDiagnosticsDone = done
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
		if retryCancel != nil {
			retryCancel()
		}
		if retryDone != nil {
			<-retryDone
		}
		if cacheUsageCancel != nil {
			cacheUsageCancel()
		}
		if cacheUsageDone != nil {
			<-cacheUsageDone
		}
		if cacheDiagnosticsCancel != nil {
			cacheDiagnosticsCancel()
		}
		if cacheDiagnosticsDone != nil {
			<-cacheDiagnosticsDone
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
			sessTelemetry.Finish(runState.sessionStatus, runState.turnsCompleted, runState.sessionErr)
		} else if trajectoryWriter != nil {
			_ = trajectoryWriter.Close()
		}
	}()

	var notePrompts *llm.MCTPromptsConfig
	if opts.GlobalConfig.Prompts != nil {
		notePrompts = opts.GlobalConfig.Prompts.MCT
	}
	transcriptSetup, err := prepareTranscriptBootstrap(cfg, sessionID, conversationGoal, conversationPath, resumeMode, loadedState, trajectoryWriter, repoRoot, notePrompts, runState)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error preparing transcript:", err)
		return Result{ExitCode: 1, Err: err}
	}
	tr := transcriptSetup.transcript
	defer tr.Close()
	recorder := transcriptSetup.recorder
	defer func() {
		runState.persistSessionState()
	}()
	writeTurn := transcriptSetup.writeTurn
	appendConversationRaw := transcriptSetup.appendConversationRaw
	conv := transcriptSetup.conversation
	interruptedResult := runState.interruptedResult
	isContextCancelled := runState.isContextCancelled
	writePendingPatchTranscript := runState.writePendingPatchTranscript

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
		PlannerOverlay:    plannerOverlay,
		Prompts:           plannerPrompts,
	})

	isChildSession := strings.TrimSpace(cfg.parentSessionID) != ""
	startingTranscript, headerErr := startTranscriptIfNeeded(tr, originalPrompt, taskDescription, sessionID, cfg, resumeMode)
	if headerErr != nil {
		fmt.Fprintln(os.Stderr, "Error writing transcript header:", headerErr)
		return Result{ExitCode: 1, Err: headerErr}
	}

	if err := writeInitialBackgroundIfNeeded(tr, repoRoot, cfg, isChildSession, startingTranscript, writeTurn); err != nil {
		fmt.Fprintln(os.Stderr, "Transcript write error:", err)
		runState.sessionErr = err
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
			runState.turnsCompleted = len(outcome.Plan.Tasks)
			if outcome.Err != nil {
				runState.sessionErr = outcome.Err
				runState.sessionStatus = "error"
				runState.keepSessionState = true
				instructionDir := strings.TrimSpace(cfg.metaInstructionDir)
				if instructionDir == "" {
					if strings.TrimSpace(metaInstructionPath) != "" {
						instructionDir = filepath.Dir(metaInstructionPath)
					}
				}
				runState.pendingState = &SessionState{
					SessionID:          sessionID,
					Goal:               goal,
					OriginalPrompt:     originalPrompt,
					TaskDescription:    taskDescription,
					PlannerOverlay:     plannerOverlay,
					TurnsCompleted:     runState.turnsCompleted,
					TranscriptPath:     tr.Path(),
					Transcript:         tr.Content(),
					ConversationPath:   recorder.Path(),
					ConversationJSON:   recorder.JSON(),
					ParentSessionID:    strings.TrimSpace(cfg.parentSessionID),
					MetaModes:          metaModesFromPlan(outcome.Plan),
					MetaInstructionDir: instructionDir,
				}
				runState.applyPlannerProgress(runState.pendingState)
				display.EndSession()
				return Result{ExitCode: outcome.ExitCode, Status: runState.sessionStatus, Turns: runState.turnsCompleted, SessionID: sessionID, Err: outcome.Err}
			}
			runState.sessionStatus = "success"
			runState.sessionErr = nil
			if strings.TrimSpace(outcome.FinalAnswer) != "" {
				if err := writeTurn(1, "Meta-Orchestrator Summary", "", nil, outcome.FinalAnswer, "meta-summary"); err != nil {
					fmt.Fprintln(os.Stderr, "Transcript write error:", err)
					runState.sessionErr = err
					display.EndSession()
					return Result{ExitCode: 1, Err: err}
				}
			}
			finalAnswer := appendFinalAnswerExtras(outcome.FinalAnswer, sessionID, cfg.verbose)
			if err := writeFinalAnswer(sessionID, finalAnswer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
				fmt.Fprintln(os.Stderr, "Final file write error:", err)
				runState.sessionErr = err
				display.EndSession()
				return Result{ExitCode: 1, Err: err}
			}
			presentFinalAnswer(display, finalAnswer)
			display.EndSession()
			return Result{ExitCode: 0, Status: runState.sessionStatus, Turns: runState.turnsCompleted, SessionID: sessionID}
		}
	}

	display.StartSession(goal)
	if resumeMode {
		fmt.Fprintf(os.Stdout, "Resuming session %s (completed %d of %d turns)\n", sessionID, runState.turnsCompleted, cfg.maxSteps)
	}
	sessionClosed := false
	defer func() {
		if !sessionClosed {
			display.EndSession()
		}
	}()

	parentSpanID := ""
	shellAgentUsedThisTurn := false
	for {
		if err := rootCtx.Err(); err != nil {
			return runState.interruptedResult(err)
		}
		shellAgentUsedThisTurn = false
		step := runState.userTurnCounter + 1
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
			patchPlan  *PatchPlan
		)
		pl.UpdateProgress(plannerProgress.snapshot())
		planCtx, planCancel = makeTurnContext(rootCtx, cfg.timeoutPerTurn)
		trFull := tr.Content()
		// If there is a pending patch diff awaiting review, append it to
		// the planning context so the planner can decide accept/reject
		// without adding an extra transcript turn.
		if runState.pendingPatchDraft != nil {
			var b strings.Builder
			b.WriteString(trFull)
			b.WriteString("\n## Pending Patch Review\n\n")
			// Mirror transcript structure tersely so the planner has
			// consistent context shape.
			line := "Patcher: apply - " + strings.TrimSpace(runState.pendingPatchDraft.Description)
			b.WriteString(line)
			b.WriteString("\n\n=== Answer\n\n")
			b.WriteString(strings.TrimSpace(runState.pendingPatchDraft.Answer))
			b.WriteString("\n\n")
			b.WriteString("Planner decision: patch\n")
			trFull = b.String()
		}
		trimmedResumePrompt := strings.TrimSpace(resumePrompt)
		if trimmedResumePrompt != "" {
			feedback := extractUserFeedback(trimmedResumePrompt)
			resumePrompt = feedback
			if err := appendConversationRaw("user", feedback, "goal_update"); err != nil {
				fmt.Fprintln(os.Stderr, "Transcript write error:", err)
				runState.sessionErr = err
				finishTurn(sessTelemetry, turn, "user-feedback", "error", turnInfo, err)
				runState.turnsCompleted = runState.userTurnCounter
				return Result{ExitCode: 1, Err: err}
			}
			trFull = appendResumePromptContext(trFull, feedback)
			turnInfo["resume_prompt"] = true
			if cfg.patch {
				if _, err := invokePatchPlanUpdateHook(planCtx, pl, tr, sessionID, goal, trFull, "", display, true); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: patch plan hook (goal update) failed: %v\n", err)
				}
			}
		}
		planCtx = attachTrajectory(planCtx, trajectoryWriter, parentSpanID)
		patchPlanComplete := false
		if cfg.patch {
			if patchPlan == nil {
				if loadedPlan, err := LoadPatchPlan(sessionID); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to load patch plan: %v\n", err)
				} else {
					patchPlan = loadedPlan
				}
			}
			if patchPlan != nil {
				totalItems, completedItems := patchPlan.Progress()
				patchPlanComplete = patchPlan.AllComplete()
				turnInfo["patch_plan_items"] = totalItems
				turnInfo["patch_plan_complete_items"] = completedItems
			}
		}
		decision, question, perr = pl.Plan(planCtx, conv, goal, trFull, step, cfg.maxSteps, patchPlan)
		if trimmedResumePrompt != "" {
			resumePrompt = ""
		}
		if cfg.patch && perr == nil {
			for decision == planner.DecisionFinalize {
				if patchPlan == nil {
					if loadedPlan, err := LoadPatchPlan(sessionID); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to load patch plan: %v\n", err)
					} else {
						patchPlan = loadedPlan
					}
				}
				if patchPlan == nil {
					break
				}
				totalItems, completedItems := patchPlan.Progress()
				patchPlanComplete = patchPlan.AllComplete()
				turnInfo["patch_plan_items"] = totalItems
				turnInfo["patch_plan_complete_items"] = completedItems
				if patchPlanComplete {
					break
				}
				lastPatched := plannerProgress.getLastPatchedFile()
				refreshedPlan, err := invokePatchPlanUpdateHook(planCtx, pl, tr, sessionID, goal, trFull, lastPatched, display, false)
				if err != nil {
					perr = err
					break
				}
				if refreshedPlan != nil {
					patchPlan = refreshedPlan
				}
				plannerProgress.clearPatchPlanPending()
				if patchPlan == nil {
					break
				}
				totalItems, completedItems = patchPlan.Progress()
				patchPlanComplete = patchPlan.AllComplete()
				turnInfo["patch_plan_items"] = totalItems
				turnInfo["patch_plan_complete_items"] = completedItems
				if patchPlanComplete {
					break
				}
				decision, question, perr = pl.Plan(planCtx, conv, goal, trFull, step, cfg.maxSteps, patchPlan)
				if perr != nil {
					break
				}
			}
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
				runState.sessionErr = perr
				finishTurn(sessTelemetry, turn, "planner", "error", turnInfo, perr)
				runState.turnsCompleted = runState.userTurnCounter
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
				ctxF = attachTrajectory(ctxF, trajectoryWriter, parentSpanID)
				answer, ferr := pl.Finalize(ctxF, conv, goal)
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
					runState.sessionErr = ferr
					finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, ferr)
					runState.turnsCompleted = runState.userTurnCounter
					return Result{ExitCode: 1, Err: ferr}
				}
				if err := runState.completeSession(display, answer, step, runState.userTurnCounter, true); err != nil {
					fmt.Fprintln(os.Stderr, "Final file write error:", err)
					finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, err)
					runState.turnsCompleted = runState.userTurnCounter
					return Result{ExitCode: 1, Err: err}
				}
				sessionClosed = true
				turnDecision = "finalize"
				turnInfo["finalized"] = true
				finishTurn(sessTelemetry, turn, turnDecision, "success", turnInfo, nil)
				return Result{ExitCode: 0, Status: runState.sessionStatus, Turns: runState.userTurnCounter, SessionID: sessionID}
			}
			fmt.Fprintln(os.Stderr, "Planner error:", perr)
			runState.sessionErr = perr
			finishTurn(sessTelemetry, turn, "planner", "error", turnInfo, perr)
			runState.turnsCompleted = runState.userTurnCounter
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
			runState.sessionErr = errUnexpected
			finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, errUnexpected)
			runState.turnsCompleted = runState.userTurnCounter
			return Result{ExitCode: 1, Err: errUnexpected}
		}
		if !plannerProgress.hasPendingReview() && (decision == planner.DecisionAccept || decision == planner.DecisionReject) {
			errUnexpected := errors.New("planner returned accept/reject without a pending patch review")
			fmt.Fprintln(os.Stderr, "Planner error:", errUnexpected)
			runState.sessionErr = errUnexpected
			finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, errUnexpected)
			runState.turnsCompleted = runState.userTurnCounter
			return Result{ExitCode: 1, Err: errUnexpected}
		}

		if decision == planner.DecisionFinalize {
			ctx, cancelF := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
			ctx = attachTrajectory(ctx, trajectoryWriter, parentSpanID)
			answer, ferr := pl.Finalize(ctx, conv, goal)
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
				runState.sessionErr = ferr
				finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, ferr)
				runState.turnsCompleted = runState.userTurnCounter
				return Result{ExitCode: 1, Err: ferr}
			}
			if err := runState.completeSession(display, answer, step, runState.userTurnCounter, false); err != nil {
				fmt.Fprintln(os.Stderr, "Final file write error:", err)
				finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, err)
				runState.turnsCompleted = runState.userTurnCounter
				return Result{ExitCode: 1, Err: err}
			}
			sessionClosed = true
			turnDecision = "finalize"
			turnInfo["finalized"] = true
			finishTurn(sessTelemetry, turn, turnDecision, "success", turnInfo, nil)
			return Result{ExitCode: 0, Status: runState.sessionStatus, Turns: runState.userTurnCounter, SessionID: sessionID}
		}

		turnEnv := &runTurnEnv{
			rootCtx:                     rootCtx,
			cfg:                         cfg,
			sessionID:                   sessionID,
			goal:                        goal,
			repoRoot:                    repoRoot,
			step:                        step,
			sessionErr:                  &runState.sessionErr,
			turnsCompleted:              &runState.turnsCompleted,
			userTurnCounter:             &runState.userTurnCounter,
			pendingPatchDraft:           &runState.pendingPatchDraft,
			plannerProgress:             plannerProgress,
			display:                     display,
			sessTelemetry:               sessTelemetry,
			turn:                        turn,
			turnDecision:                turnDecision,
			turnInfo:                    turnInfo,
			parentSpanID:                parentSpanID,
			trajectoryWriter:            trajectoryWriter,
			writeTurn:                   writeTurn,
			interruptedResult:           interruptedResult,
			isContextCancelled:          isContextCancelled,
			writePendingPatchTranscript: writePendingPatchTranscript,
			mctRunner:                   &mctRunner,
			pRunner:                     pRunner,
			pl:                          pl,
			tr:                          tr,
			orchPromptOpts:              &orchPromptOpts,
			baseOrchMetadata:            baseOrchMetadata,
			patcherPromptOpts:           patcherPromptOpts,
			mctResponseDirectives:       mctResponseDirectives,
		}

		switch decision {
		case planner.DecisionAccept, planner.DecisionReject:
			outcome := executeReviewDecision(turnEnv, decision, question, pendingReview)
			shellAgentUsedThisTurn = outcome.shellAgentUsed
			switch outcome.action {
			case turnLoopReturn:
				return outcome.result
			case turnLoopFinalize:
				goto Finalize
			default:
				goto TurnDone
			}

		case planner.DecisionAsk:
			outcome := executeAskDecision(turnEnv, question)
			shellAgentUsedThisTurn = outcome.shellAgentUsed
			switch outcome.action {
			case turnLoopReturn:
				return outcome.result
			case turnLoopFinalize:
				goto Finalize
			default:
				goto TurnDone
			}

		case planner.DecisionPatch:
			outcome := executePatchDecision(turnEnv, question, patchPlan)
			shellAgentUsedThisTurn = outcome.shellAgentUsed
			switch outcome.action {
			case turnLoopReturn:
				return outcome.result
			case turnLoopFinalize:
				goto Finalize
			default:
				goto TurnDone
			}

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
		if err := syncTurnWorkspace(cfg, repoRoot, decision, shellAgentUsedThisTurn); err != nil {
			fmt.Fprintln(os.Stderr, "Error: snapshot->host sync failed:", err)
			return Result{ExitCode: 1, Err: err}
		}
		continue
	}

Finalize:
	if err := rootCtx.Err(); err != nil {
		runState.turnsCompleted = countTurns(tr.Content())
		runState.userTurnCounter = runState.turnsCompleted
		return interruptedResult(err)
	}
	if result := runState.maybeRunPreFinalizePatch(display, patcherPromptOpts, pl, conv, pRunner, parentSpanID); result != nil {
		return *result
	}
	turns := countTurns(tr.Content())
	ctx, cancelF := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
	ctx = attachTrajectory(ctx, trajectoryWriter, parentSpanID)
	answer, ferr := pl.Finalize(ctx, conv, goal)
	var finalCtxErr error
	if ctx != nil {
		finalCtxErr = ctx.Err()
	}
	if cancelF != nil {
		cancelF()
	}
	if ferr != nil {
		if isContextCancelled(ferr) || isContextCancelled(finalCtxErr) {
			runState.turnsCompleted = turns
			runState.userTurnCounter = turns
			return interruptedResult(ferr)
		}
		fmt.Fprintln(os.Stderr, "Finalizer error:", ferr)
		runState.sessionErr = ferr
		runState.turnsCompleted = turns
		return Result{ExitCode: 1, Err: ferr}
	}
	if err := runState.completeSession(display, answer, turns, turns, turns >= cfg.maxSteps); err != nil {
		fmt.Fprintln(os.Stderr, "Final file write error:", err)
		runState.turnsCompleted = turns
		return Result{ExitCode: 1, Err: err}
	}
	sessionClosed = true
	return Result{ExitCode: 0, Status: runState.sessionStatus, Turns: turns, SessionID: sessionID}
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
	b.WriteString("== GOAL UPDATE\n\n")
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

// appendPatchPlanToFinalAnswer loads the patch plan and appends it to the answer if valid.
// If the plan does not exist or cannot be loaded/parsed, the original answer is returned.
func appendPatchPlanToFinalAnswer(answer, sessionID string) string {
	plan, err := loadPatchPlanFn(sessionID)
	if err != nil || plan == nil {
		return answer
	}
	rendered := formatPatchPlanForDisplayFn(plan)
	if strings.TrimSpace(rendered) == "" {
		return answer
	}
	if strings.TrimSpace(answer) == "" {
		return rendered
	}
	return strings.TrimRight(answer, "\n") + "\n\n" + rendered
}

func appendFinalAnswerExtras(answer, sessionID string, verbose bool) string {
	answer = appendPatchPlanToFinalAnswer(answer, sessionID)
	return appendFullDiffsToFinalAnswer(answer, sessionID, verbose)
}

func appendFullDiffsToFinalAnswer(answer, sessionID string, verbose bool) string {
	fullDiffs, err := loadFullDiffsForSession(sessionID, verbose)
	if err != nil || strings.TrimSpace(fullDiffs) == "" {
		return answer
	}
	return appendFullDiffSection(answer, fullDiffs)
}

func loadFullDiffsForSession(sessionID string, verbose bool) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", nil
	}
	conversationPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(conversationPath)
	if err != nil {
		if os.IsNotExist(err) {
			if verbose {
				fmt.Fprintf(os.Stderr, "[full-diff] conversation not found at %s\n", conversationPath)
			}
			return "", nil
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "[full-diff] read conversation failed: %v\n", err)
		}
		return "", err
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[full-diff] parse conversation failed: %v\n", err)
		}
		return "", err
	}
	fullDiffs := conversation.ExtractFullDiffs(conv)
	if verbose && strings.TrimSpace(fullDiffs) == "" {
		fmt.Fprintln(os.Stderr, "[full-diff] no full diffs found")
	}
	return fullDiffs, nil
}

func appendFullDiffSection(base, fullDiffs string) string {
	trimmedDiffs := strings.TrimRight(fullDiffs, "\n")
	if strings.TrimSpace(trimmedDiffs) == "" {
		return base
	}
	const sectionHeader = "# Full Diffs of Patched Files"
	marker := "\n\n---\n\n" + sectionHeader
	if idx := strings.Index(base, marker); idx != -1 {
		base = strings.TrimRight(base[:idx], "\n")
	}
	section := marker + "\n\n" + trimmedDiffs
	if strings.TrimSpace(base) == "" {
		return strings.TrimLeft(section, "\n")
	}
	return strings.TrimRight(base, "\n") + section
}
