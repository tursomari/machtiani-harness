package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/readmesync"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/runner"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
	"github.com/tursomari/machtiani/agent/internal/conversation"
)

const (
	backgroundQuestionPrompt = "Give me the background of the project."
	backgroundFallbackAnswer = "No project documentation has been created yet. Please run `mct-agent sync` to generate initial project documentation."
)

var (
	readmeHeadCommitFn       = readmesync.HeadCommit
	readmeCommitForProjectFn = readmesync.READMECommitForProject
	readmeCheckoutReadonlyFn = readmesync.CheckoutReadonlyREADME
	tagFormatPattern         = regexp.MustCompile(`\[(?P<path>[^\[\]|]+?)\s*\|\s*(?P<start>[^:\]]+)\s*:\s*(?P<end>[^\]]+)\]`)
)

func isLocalSessionEnvironment(cfg *llm.Config) bool {
	if cfg == nil || cfg.Environment == nil {
		return true
	}
	envType := strings.ToLower(strings.TrimSpace(cfg.Environment.Type))
	return envType == "" || envType == "local"
}

type plannerProgressTracker struct {
	successSet   map[string]struct{}
	successFiles []string
	fileDedupSet map[string]string
}

func newPlannerProgressTracker(existing *conversation.PlannerProgressState) *plannerProgressTracker {
	tracker := &plannerProgressTracker{
		successSet:   make(map[string]struct{}),
		fileDedupSet: make(map[string]string),
	}
	if existing == nil {
		return tracker
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

func writeInitialBackgroundIfNeeded(tr *transcript.Transcript, repoRoot string, cfg legacyConfig, startingTranscript bool, writeTurn func(step int, question, savedPath string, retrieved []string, summary string, decision string) error, diagWriter io.Writer) error {
	if tr == nil || !startingTranscript {
		return nil
	}
	prefillAnswer := backgroundFallbackAnswer
	if backgroundText, err := loadProjectBackground(repoRoot); err != nil {
		fmt.Fprintf(diagWriter, "Warning: unable to load project background; falling back to sync prompt: %v\n", err)
	} else {
		if cfg.verbose {
			fmt.Fprintf(diagWriter, "Loaded internal README background (%d bytes).\n", len(backgroundText))
		}
		prefillAnswer = backgroundText
	}
	if writeTurn == nil {
		return tr.WriteTurn(0, backgroundQuestionPrompt, "", nil, prefillAnswer, "background")
	}
	return writeTurn(0, backgroundQuestionPrompt, "", nil, prefillAnswer, "background")
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

func (p *plannerProgressTracker) toState() *conversation.PlannerProgressState {
	if p == nil {
		return nil
	}
	if len(p.successFiles) == 0 {
		return nil
	}
	state := &conversation.PlannerProgressState{
		SuccessFiles: append([]string(nil), p.successFiles...),
	}
	return state
}

func (p *plannerProgressTracker) snapshot() planner.Progress {
	if p == nil {
		return planner.Progress{}
	}
	return planner.Progress{
		SuccessFiles: p.successList(),
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
	diagWriter := io.Writer(os.Stderr)
	if opts.Diagnostics != nil {
		diagWriter = opts.Diagnostics
	}
	bootstrap, earlyResult, ok := prepareRunBootstrap(rootCtx, opts, diagWriter)
	if !ok {
		return earlyResult
	}
	opts = bootstrap.opts
	cfg := bootstrap.cfg
	sessionID := bootstrap.sessionID
	goal := bootstrap.goal
	resumePrompt := bootstrap.resumePrompt
	resumeSuspendedInput := bootstrap.resumeSuspendedInput
	originalPrompt := bootstrap.originalPrompt
	taskDescription := bootstrap.taskDescription
	plannerOverlay := bootstrap.plannerOverlay
	conversationGoal := bootstrap.conversationGoal
	conversationPath := bootstrap.conversationPath
	resumeMode := bootstrap.resumeMode
	loadedState := bootstrap.loadedState
	modeInstructions := bootstrap.modeInstructions
	modeInstructionPath := bootstrap.modeInstructionPath
	resumableShellAgent := bootstrap.resumableShellAgent
	resumableShellAgentTrajectoryPath := bootstrap.resumableShellAgentTrajectoryPath
	_ = resumableShellAgentTrajectoryPath
	runState := bootstrap.runState
	plannerProgress := runState.plannerProgress
	envBootstrap, err := prepareSessionEnvironment(sessionID, cfg, diagWriter)
	if err != nil {
		fmt.Fprintln(diagWriter, "Error preparing session environment:", err)
		return Result{ExitCode: 1, Err: err}
	}
	sessionTempRoot := envBootstrap.sessionTempRoot
	var sessLock *sessionLock
	defer func() {
		if sessLock != nil {
			if err := sessLock.Close(); err != nil {
				fmt.Fprintf(diagWriter, "[session-lock] warning: failed to release lock: %v\n", err)
			}
		}
		if envBootstrap.restore != nil {
			envBootstrap.restore()
		}
	}()
	if err := cleanupOrphanedSessionDirs(sessionTempRoot, cfg.verbose, diagWriter); err != nil {
		fmt.Fprintf(diagWriter, "Warning: session cleanup: %v\n", err)
	}

	lock, lockErr := acquireSessionLock(sessionID, sessionTempRoot)
	if lockErr != nil {
		fmt.Fprintln(diagWriter, "Error acquiring session lock:", lockErr)
		return Result{ExitCode: 1, Err: lockErr}
	}
	sessLock = lock

	trajectoryWriter, repoRoot, trajErr := newTrajectoryWriter(cfg, sessionID)
	if trajErr != nil {
		fmt.Fprintln(diagWriter, "Trajectory setup error:", trajErr)
		return Result{ExitCode: 1, Err: trajErr}
	}
	if trajectoryWriter != nil {
		fmt.Fprintln(diagWriter, "[trajectory] unified stream:", trajectoryWriter.Config().Path)
	}

	display := opts.Display
	if display == nil {
		display = ui.NewFormatter(os.Stdout, nil, ui.Theme{}, timerMgr, sessionID)
	}
	var eventBus *ui.EventBus
	if f, ok := display.(*ui.Formatter); ok {
		eventBus = f.Bus
	}
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
		cancel, done, err := startLLMFailoverLogger(eventBus, trajectoryWriter.Config().Path, diagWriter)
		if err != nil {
			fmt.Fprintf(diagWriter, "[trajectory] failover listener setup error: %v\n", err)
		} else {
			failoverCancel = cancel
			failoverDone = done
		}
		cancel, done, err = startLLMRetryLogger(eventBus, trajectoryWriter.Config().Path, diagWriter)
		if err != nil {
			fmt.Fprintf(diagWriter, "[trajectory] retry listener setup error: %v\n", err)
		} else {
			retryCancel = cancel
			retryDone = done
		}
		cancel, done, err = startLLMCacheUsageLogger(eventBus, trajectoryWriter.Config().Path, diagWriter)
		if err != nil {
			fmt.Fprintf(diagWriter, "[trajectory] cache usage listener setup error: %v\n", err)
		} else {
			cacheUsageCancel = cancel
			cacheUsageDone = done
		}
		cancel, done, err = startLLMCacheDiagnosticsLogger(eventBus, trajectoryWriter.Config().Path, diagWriter)
		if err != nil {
			fmt.Fprintf(diagWriter, "[trajectory] cache diagnostics listener setup error: %v\n", err)
		} else {
			cacheDiagnosticsCancel = cancel
			cacheDiagnosticsDone = done
		}
		// Start shell-agent action streamer to surface shell actions in real-time
		cancel, done, err = startShellActionStreamer(eventBus, trajectoryWriter.Config().Path, diagWriter)
		if err != nil {
			fmt.Fprintf(diagWriter, "[trajectory] shell action listener setup error: %v\n", err)
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

	sessTelemetry := newSessionTelemetry(trajectoryWriter, sessionID, goal, cfg, repoRoot, opts.Build, diagWriter)
	defer func() {
		if sessTelemetry != nil {
			sessTelemetry.Finish(runState.sessionStatus, runState.turnsCompleted, runState.sessionErr)
		} else if trajectoryWriter != nil {
			_ = trajectoryWriter.Close()
		}
	}()

	transcriptSetup, err := prepareTranscriptBootstrap(cfg, sessionID, conversationGoal, conversationPath, resumeMode, loadedState, trajectoryWriter, repoRoot, runState, diagWriter)
	if err != nil {
		fmt.Fprintln(diagWriter, "Error preparing transcript:", err)
		return Result{ExitCode: 1, Err: err}
	}
	tr := transcriptSetup.transcript
	defer tr.Close()

	// Inject verbose flag and transcript into context for cache warning diagnostics.
	rootCtx = llm.WithVerbose(rootCtx, cfg.verbose)
	rootCtx = llm.WithTranscript(rootCtx, tr)
	_ = transcriptSetup.recorder // recorder is accessed via runState.recorder
	defer func() {
		runState.checkpointTurn(display, diagWriter)
	}()
	writeTurn := transcriptSetup.writeTurn
	appendConversationRaw := transcriptSetup.appendConversationRaw
	conv := transcriptSetup.conversation
	interruptedResult := runState.interruptedResult
	isContextCancelled := runState.isContextCancelled
	fmt.Fprintln(diagWriter, "Session:", sessionID)
	if cfg.verbose {
		fmt.Fprintln(diagWriter, "mct-agent starting; transcript:", tr.Path())
	}


	trajectoryPath, err := resolveFileDiscoveryTrajectory(cfg, sessionID)
	if err != nil {
		fmt.Fprintln(diagWriter, "File-discovery setup error:", err)
		return Result{ExitCode: 1, Err: err}
	}
	if cfg.verbose && strings.TrimSpace(trajectoryPath) != "" {
		fmt.Fprintln(diagWriter, "File discovery trajectory:", trajectoryPath)
	}

	paramPairs := append([]string(nil), opts.ParamPairs...)
	paramJSONVals := append([]string(nil), opts.ParamJSON...)

	models, err := resolveModelRuntimes(cfg, opts.GlobalConfig, paramPairs, paramJSONVals, opts.APIKeyOverrides)
	if err != nil {
		if miss, ok := err.(*missingConfigError); ok {
			fmt.Fprintln(diagWriter, "Missing model config: set:")
			for _, item := range miss.items {
				fmt.Fprintln(diagWriter, " - ", item)
			}
			return Result{ExitCode: 2, Err: err}
		}
		fmt.Fprintln(diagWriter, "Model resolution error:", err)
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
	shellAgentModel := strings.TrimSpace(cfg.shellAgentModel)
	if shellAgentModel == "" && strings.TrimSpace(models.shellAgent.alias) != "" && models.shellAgent.usingAlias {
		shellAgentModel = strings.TrimSpace(models.shellAgent.alias)
	}
	// When the shell-agent model is not explicitly set (e.g. running
	// with --mode code without --shell-agent), fall back to the
	// orchestrator model. This ensures --model is respected by the
	// shell-agent library and prevents provider mismatch errors when
	// the config's default_model uses a different provider.
	if shellAgentModel == "" && strings.TrimSpace(models.orchestrator.alias) != "" && models.orchestrator.usingAlias {
		shellAgentModel = strings.TrimSpace(models.orchestrator.alias)
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
		fmt.Fprintln(diagWriter, "mct resolution error:", err)
		return Result{ExitCode: 1, Err: err}
	}

	// Build the in-process shell-agent library objects. These are
	// reused across turns so the model and environment are created
	// once. The planner may dynamically decide to use the shell-agent
	// even without an explicit --shell-agent flag (e.g. "Ask" routing),
	// so the library is always built when possible.
	if cfg.maxCommandOutputBytes > 0 {
		if opts.GlobalConfig.Environment == nil {
			opts.GlobalConfig.Environment = &llm.EnvironmentConfig{}
		}
		opts.GlobalConfig.Environment.MaxCommandOutputBytes = cfg.maxCommandOutputBytes
	}
	lib, libErr := shellagent.BuildLibrary(&opts.GlobalConfig, opts.APIKeyOverrides, cfg.persistTmpData, shellAgentModel, cfg.answerTag, cfg.commandTag)
	if libErr != nil {
		// Non-fatal: the library is a prerequisite only when the
		// planner actually delegates to the shell-agent. If it's
		// never used the warning is harmless.
		fmt.Fprintln(diagWriter, "shell-agent library init warning:", libErr)
	} else {
		lib.ExtraInstructions = modeInstructions.ShellInstruction
		mctRunner.ShellAgentLibrary = lib
	}

	mctResponseDirectives := []string(nil)
	if cfg.enableTagFormat {
		mctResponseDirectives = []string{"use_tag_format"}
	}

	startingTranscript, headerErr := startTranscriptIfNeeded(tr, originalPrompt, taskDescription, sessionID, cfg, resumeMode)
	if headerErr != nil {
		fmt.Fprintln(diagWriter, "Error writing transcript header:", headerErr)
		return Result{ExitCode: 1, Err: headerErr}
	}

	if err := writeInitialBackgroundIfNeeded(tr, repoRoot, cfg, startingTranscript, writeTurn, diagWriter); err != nil {
		fmt.Fprintln(diagWriter, "Transcript write error:", err)
		runState.sessionErr = err
		return Result{ExitCode: 1, Err: err}
	}

	modeActive := strings.TrimSpace(cfg.mode) != ""
	if modeActive {
		modeCtx := modeContext{
			SessionID:       sessionID,
			Goal:            goal,
			ResumePrompt:    resumePrompt,
			Config:          cfg,
			Options:         opts,
			Display:         display,
			InstructionPath: modeInstructionPath,
			Instruction:     modeInstructions,
			DiagWriter:      diagWriter,
		}
		result, handled := applyMode(&modeCtx)
		if handled {
			// handled=true means error during mode configuration.
			runState.sessionErr = fmt.Errorf("mode configuration failed")
			if err := runState.transition(StateError); err != nil {
				fmt.Fprintf(diagWriter, "transition to StateError failed: %v\n", err)
				return Result{ExitCode: 1, Err: err}
			}
			runState.pendingState = &SessionState{
				SessionID:       sessionID,
				Goal:            goal,
				OriginalPrompt:  originalPrompt,
				TaskDescription: taskDescription,
				PlannerOverlay:  plannerOverlay,
				TurnsCompleted:  runState.turnsCompleted,
				Modes:           modesFromPlan(result.Plan),
			}
			runState.hydrateState(runState.pendingState, diagWriter)
			return Result{ExitCode: 1, Status: runState.sessionStatus, Turns: runState.turnsCompleted, SessionID: sessionID, Err: runState.sessionErr}
		}
		// applyMode configured the session (PlannerOverlay, mode
		// defaults, task overrides). Apply any overlay it set back into
		// the planner overlay variable used below.
		plannerOverlay = modeCtx.Options.PlannerOverlay
		opts = modeCtx.Options
		cfg = newLegacyConfig(opts.Config)
		runState.plannerOverlay = plannerOverlay

		// When the user resumes with a follow-up prompt, promote it to
		// the goal so that downstream analysis (e.g. AnalyzeUserDirectedAsk)
		// sees the latest user intent instead of the original session goal.
		if trimmedResume := strings.TrimSpace(resumePrompt); trimmedResume != "" {
			goal = trimmedResume
		}
	}

	var plannerPrompts *llm.PlannerPromptsConfig
	if opts.GlobalConfig.Prompts != nil {
		plannerPrompts = opts.GlobalConfig.Prompts.Planner
	}
	var pl Planner
	if opts.PlannerOverride != nil {
		pl = opts.PlannerOverride
	} else {
		pl = planner.NewClient(planner.ClientConfig{
			Model:             models.orchestrator.resolved,
			Extras:            models.orchestrator.extras,
			Alias:             models.orchestrator.alias,
			Verbose:           cfg.verbose,
			DryRun:            cfg.dryRun,
			InternetAccess:    false,
			RequestTimeoutSec: cfg.timeoutPerTurn,
			RepoRoot:          repoRoot,
			SessionID:         sessionID,
			PlannerOverlay:    plannerOverlay,
			Prompts:           plannerPrompts,
		})
	}

	display.StartSession(goal)
	if resumeMode {
		if display != nil {
			display.WriteString("Resuming session " + sessionID + " ...")
		} else {
			fmt.Fprintf(diagWriter, "Resuming session %s ...\n", sessionID)
		}
	}
	sessionClosed := false
	defer func() {
		if !sessionClosed {
			display.EndSession()
		}
	}()

	parentSpanID := ""
	for {
		if err := rootCtx.Err(); err != nil {
			return runState.interruptedResult(err)
		}
		step := runState.turnsCompleted + 1
		var turn *turnTelemetry
		if sessTelemetry != nil {
			turn = sessTelemetry.StartTurn(step, cfg.maxTurns)
			parentSpanID = turn.span.ID
		}

		turnDecision := "unknown"
		turnInfo := map[string]any{
			"step":      step,
			"max_steps": cfg.maxTurns,
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
		trimmedResumePrompt := strings.TrimSpace(resumePrompt)
		if trimmedResumePrompt != "" {
			feedback := trimmedResumePrompt
			metaType := ""
			if resumeSuspendedInput != nil {
				metaType = "user_input_response"
			}
			resumePrompt = feedback
			if err := appendConversationRaw("user", feedback, metaType); err != nil {
				fmt.Fprintln(diagWriter, "Transcript write error:", err)
				runState.sessionErr = err
				finishTurn(sessTelemetry, turn, "user-feedback", "error", turnInfo, err)
				
				return Result{ExitCode: 1, Err: err}
			}
			if resumeSuspendedInput != nil {
				trFull = appendUserInputContext(trFull, feedback)
				runState.clearSuspendedUserInput()
				resumeSuspendedInput = nil
				turnInfo["resume_user_input"] = true
			} else {
				trFull = appendResumePromptContext(trFull, feedback)
			}
			turnInfo["resume_prompt"] = true
		}
		isResumingFromShellAgent := resumableShellAgent
	if resumableShellAgent && step == loadedState.TurnsCompleted+1 {
			recoveredQuestion := ExtractResumableWorkRequestQuestion(conv)
			if recoveredQuestion != "" {
				decision = planner.DecisionAskWorker
				question = recoveredQuestion
				resumableShellAgent = false
				perr = nil
				goto PostPlan
			}
		}
		planCtx = attachTrajectory(planCtx, trajectoryWriter, parentSpanID)
		decision, question, perr = pl.Plan(planCtx, conv, goal, trFull, step, cfg.maxTurns)
	PostPlan:
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
				fmt.Fprintf(diagWriter, "Planner error: timed out after %ds. Increase --turn-timeout or set 0 for unlimited.\n", cfg.timeoutPerTurn)
				runState.sessionErr = perr
				finishTurn(sessTelemetry, turn, "planner", "error", turnInfo, perr)
				
				return Result{ExitCode: 1, Err: perr}
			}
			perrStr := strings.ToLower(perr.Error())
			if strings.Contains(perrStr, "unable to parse decision from model output") {
				fmt.Fprintln(diagWriter, "Planner error:", perr)
				turnDecision = "planner"
				turnInfo["is_retry"] = true
				turnInfo["retry_reason"] = "planner_parse_error"
				finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, perr)
				continue
			}
			if strings.Contains(perrStr, "deadline exceeded") || strings.Contains(perrStr, "timeout") || strings.Contains(perrStr, "temporary") || strings.Contains(perrStr, "no choices") {
				fmt.Fprintln(diagWriter, "Planner warning:", perr)
				fmt.Fprintln(diagWriter, "Falling back to finalizing with current transcript.")
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
						fmt.Fprintf(diagWriter, "Finalizer error: timed out after %ds. Increase --turn-timeout or set 0 for unlimited.\n", cfg.timeoutPerTurn)
					} else {
						fmt.Fprintln(diagWriter, "Finalizer error:", ferr)
					}
					runState.sessionErr = ferr
					finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, ferr)
					
					return Result{ExitCode: 1, Err: ferr}
				}
				if err := runState.completeSession(display, diagWriter, answer, step, runState.turnsCompleted, true); err != nil {
					fmt.Fprintln(diagWriter, "Final file write error:", err)
					finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, err)
					
					return Result{ExitCode: 1, Err: err}
				}
				sessionClosed = true
				turnDecision = "finalize"
				turnInfo["finalized"] = true
				finishTurn(sessTelemetry, turn, turnDecision, "success", turnInfo, nil)
				return Result{ExitCode: 0, Status: runState.sessionStatus, Turns: runState.turnsCompleted, SessionID: sessionID}
			}
			fmt.Fprintln(diagWriter, "Planner error:", perr)
			runState.sessionErr = perr
			finishTurn(sessTelemetry, turn, "planner", "error", turnInfo, perr)
			
			return Result{ExitCode: 1, Err: perr}
		}

		if cfg.verbose {
			fmt.Fprintf(diagWriter, "Step %d decision: %s\n", step, decision)
		}
		turnDecision = string(decision)
		turnInfo["planner_decision"] = string(decision)
		if question != "" && trajectoryWriter != nil {
			excerpt := trajectory.MakeTextExcerpt(question, trajectoryWriter.ExcerptLen())
			turnInfo = trajectory.MergeExcerptWithPrefix(turnInfo, excerpt, "planner_question")
		}
		if decision == planner.DecisionAskWorker {
			ctxAsk, cancelAsk := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
			ctxAsk = attachTrajectory(ctxAsk, trajectoryWriter, parentSpanID)
			userDirected, uerr := pl.AnalyzeUserDirectedAsk(ctxAsk, conv, goal, question, step, cfg.maxTurns)
			var ctxAskErr error
			if ctxAsk != nil {
				ctxAskErr = ctxAsk.Err()
			}
			if cancelAsk != nil {
				cancelAsk()
			}
			switch {
			case uerr == nil && userDirected.ShouldSuspend:
				turnInfo["ask_user_directed"] = true
				turnInfo["suspended"] = true
				turnInfo["suspension_kind"] = "user-directed-ask"
				if strings.TrimSpace(userDirected.Reason) != "" {
					turnInfo["suspension_reason"] = trimTo(userDirected.Reason, 200)
				}
				if trajectoryWriter != nil {
					turnInfo = trajectory.MergeExcerptWithPrefix(turnInfo, trajectory.MakeTextExcerpt(userDirected.Question, trajectoryWriter.ExcerptLen()), "suspension_question")
				}
				result, suspendErr := runState.suspendForUserInput(display, diagWriter, userDirected.Question, userDirected.Context, userDirected.Reason, userDirected.OriginalAsk)
				if suspendErr != nil {
					runState.sessionErr = suspendErr
					finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, suspendErr)
					
					return Result{ExitCode: 1, Err: suspendErr}
				}
				finishTurn(sessTelemetry, turn, turnDecision, "suspended", turnInfo, nil)
				return result
			case uerr != nil && (isContextCancelled(uerr) || isContextCancelled(ctxAskErr)):
				return interruptedResult(uerr)
			case uerr != nil && cfg.verbose:
				fmt.Fprintf(diagWriter, "Warning: user-directed ask analysis failed: %v\n", uerr)
			}
		}

		if decision == planner.DecisionAnswerUser {
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
					fmt.Fprintf(diagWriter, "Finalizer error: timed out after %ds. Increase --turn-timeout or set 0 for unlimited.\n", cfg.timeoutPerTurn)
				} else {
					fmt.Fprintln(diagWriter, "Finalizer error:", ferr)
				}
				runState.sessionErr = ferr
				finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, ferr)
				
				return Result{ExitCode: 1, Err: ferr}
			}
			if err := runState.completeSession(display, diagWriter, answer, step, runState.turnsCompleted, false); err != nil {
				fmt.Fprintln(diagWriter, "Final file write error:", err)
				finishTurn(sessTelemetry, turn, "finalize", "error", turnInfo, err)
				
				return Result{ExitCode: 1, Err: err}
			}
			sessionClosed = true
			turnDecision = "finalize"
			turnInfo["finalized"] = true
			finishTurn(sessTelemetry, turn, turnDecision, "success", turnInfo, nil)
			return Result{ExitCode: 0, Status: runState.sessionStatus, Turns: runState.turnsCompleted, SessionID: sessionID}
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
			
			plannerProgress:             plannerProgress,
			display:                     display,
			diagWriter:                  diagWriter,
			hasNewInput:                 bootstrap.hasNewInput,
			resumableShellAgentTrajectoryPath: bootstrap.resumableShellAgentTrajectoryPath,
			isResumingTurn:              isResumingFromShellAgent,
			shellAgentInterruptStep:     bootstrap.shellAgentInterruptStep,
			shellAgentStepLog:           bootstrap.shellAgentStepLog,
			sessTelemetry:               sessTelemetry,
			turn:                        turn,
			turnDecision:                turnDecision,
			turnInfo:                    turnInfo,
			parentSpanID:                parentSpanID,
			trajectoryWriter:            trajectoryWriter,
			writeTurn:                   writeTurn,
			interruptedResult:           interruptedResult,
			isContextCancelled:          isContextCancelled,
			mctRunner:                   &mctRunner,
			pl:                          pl,
			tr:                          tr,
			recorder:                    runState.recorder,
			orchPromptOpts:              &orchPromptOpts,
			baseOrchMetadata:            baseOrchMetadata,
			mctResponseDirectives:       mctResponseDirectives,
		}

		switch decision {
		case planner.DecisionAskWorker:
			outcome := executeAskDecision(turnEnv, question)
			switch outcome.action {
			case turnLoopReturn:
				return outcome.result
			case turnLoopAnswerUser:
				goto Finalize
			default:
				goto TurnDone
			}

		case planner.DecisionAnswerUser:
			goto Finalize
		case planner.DecisionAskUser:
			ctxAsk, cancelAsk := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
			ctxAsk = attachTrajectory(ctxAsk, trajectoryWriter, parentSpanID)
			userDirected, uerr := pl.AnalyzeUserDirectedAsk(ctxAsk, conv, goal, question, step, cfg.maxTurns)
			var ctxAskErr error
			if ctxAsk != nil {
				ctxAskErr = ctxAsk.Err()
			}
			if cancelAsk != nil {
				cancelAsk()
			}
			switch {
			case uerr == nil && userDirected.ShouldSuspend:
				turnInfo["ask_user_directed"] = true
				turnInfo["suspended"] = true
				turnInfo["suspension_kind"] = "user-directed-ask"
				if strings.TrimSpace(userDirected.Reason) != "" {
					turnInfo["suspension_reason"] = trimTo(userDirected.Reason, 200)
				}
				if trajectoryWriter != nil {
					turnInfo = trajectory.MergeExcerptWithPrefix(turnInfo, trajectory.MakeTextExcerpt(userDirected.Question, trajectoryWriter.ExcerptLen()), "suspension_question")
				}
				result, suspendErr := runState.suspendForUserInput(display, diagWriter, userDirected.Question, userDirected.Context, userDirected.Reason, userDirected.OriginalAsk)
				if suspendErr != nil {
					runState.sessionErr = suspendErr
					finishTurn(sessTelemetry, turn, turnDecision, "error", turnInfo, suspendErr)
					
					return Result{ExitCode: 1, Err: suspendErr}
				}
				finishTurn(sessTelemetry, turn, turnDecision, "suspended", turnInfo, nil)
				return result
			case uerr != nil && (isContextCancelled(uerr) || isContextCancelled(ctxAskErr)):
				return interruptedResult(uerr)
			case uerr != nil && cfg.verbose:
				fmt.Fprintf(diagWriter, "Warning: user-directed ask analysis failed: %v\n", uerr)
			}
			goto Finalize
		default:
			goto Finalize
		}

	TurnDone:
		continue
	}

Finalize:
	if err := rootCtx.Err(); err != nil {
		return interruptedResult(err)
	}
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
			return interruptedResult(ferr)
		}
		fmt.Fprintln(diagWriter, "Finalizer error:", ferr)
		runState.sessionErr = ferr
		return Result{ExitCode: 1, Err: ferr}
	}
	if err := runState.completeSession(display, diagWriter, answer, runState.turnsCompleted, runState.turnsCompleted, runState.turnsCompleted >= cfg.maxTurns); err != nil {
		fmt.Fprintln(diagWriter, "Final file write error:", err)
		return Result{ExitCode: 1, Err: err}
	}
	sessionClosed = true
	return Result{ExitCode: 0, Status: runState.sessionStatus, Turns: runState.turnsCompleted, SessionID: sessionID}
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
	b.WriteString("=== USER MESSAGE\n\n")
	b.WriteString(prompt)
	b.WriteString("\n")
	return b.String()
}

func appendUserInputContext(transcript, prompt string) string {
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
	b.WriteString("=== USER INPUT RESPONSE\n\n")
	b.WriteString(prompt)
	b.WriteString("\n")
	return b.String()
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


