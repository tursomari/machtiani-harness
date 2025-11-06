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
	"strings"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	promptsvc "github.com/tursomari/machtiani/agent/internal/mct/prompt"
	"github.com/tursomari/machtiani/agent/internal/mct/readmesync"
	"github.com/tursomari/machtiani/agent/internal/parser"
	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/runner"
	"github.com/tursomari/machtiani/agent/internal/tempdir"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

const (
	backgroundQuestionPrompt = "Give me the background of the project."
	backgroundFallbackAnswer = "No project documentation has been created yet. Please run `mct-agent sync` to generate initial project documentation."
)

var (
	readmeHeadCommitFn       = readmesync.HeadCommit
	readmeCommitForProjectFn = readmesync.READMECommitForProject
	readmeCheckoutReadonlyFn = readmesync.CheckoutReadonlyREADME
)

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

		storedGoal := strings.TrimSpace(state.Goal)
		if storedGoal != "" {
			goal = storedGoal
		}
		if resumePrompt != "" {
			if goal != "" {
				goal = strings.TrimSpace(goal + "\n\n" + resumePrompt)
			} else {
				goal = resumePrompt
			}
		}
		if strings.TrimSpace(cfgInput.TranscriptFile) == "" && strings.TrimSpace(state.TranscriptPath) != "" {
			cfgInput.TranscriptFile = state.TranscriptPath
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

	cfgInput.SessionID = sessionID

	cfg := newLegacyConfig(cfgInput)
	applyTrajectoryEnvOverrides(&cfg)
	if err := cleanupOrphanedTempDirs(cfg.verbose); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to cleanup orphaned temp dirs: %v\n", err)
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
	if err := tempdir.SetSessionRoot(sessionTempRoot); err != nil {
		fmt.Fprintln(os.Stderr, "Error preparing session temp root:", err)
		return Result{ExitCode: 1, Err: err}
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
	if trajectoryWriter != nil {
		fmt.Fprintln(os.Stderr, "[trajectory] unified stream:", trajectoryWriter.Config().Path)
	}

	display := ui.NewTerminalDisplay(os.Stdout)
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
		fmt.Fprintf(os.Stdout, "%s\nSession ID: %s\nTurns completed: %d\nGoal so far: %q\n\nTo continue, provide your next instruction, for example:\n  mct-agent run \"<next instruction>\" --session-id %s\n\n", header, sessionID, turns, goal, sessionID)
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
	}
	if err := mctRunner.Resolve(); err != nil {
		fmt.Fprintln(os.Stderr, "mct resolution error:", err)
		fmt.Fprintln(os.Stderr, "Hint: install 'mct' into PATH (see mct/README.md).")
		return Result{ExitCode: 1, Err: err}
	}

	var pRunner *runner.PatcherRunner
	if cfg.patch {
		patchLogger := log.New(os.Stderr, "[patcher] ", log.LstdFlags)
		pr := &runner.PatcherRunner{
			Enabled:         true,
			Verbose:         cfg.verbose,
			DryRun:          cfg.dryRun,
			SessionID:       sessionID,
			Runtime:         models.patcher.toPromptRuntime(),
			Service:         patchersvc.NewService(patchersvc.WithLogger(patchLogger)),
			RepoRoot:        repoRoot,
			PersistTmpData:  cfg.persistTmpData,
			SessionTempRoot: sessionTempRoot,
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

	pl := planner.NewClient(planner.ClientConfig{
		Model:             models.orchestrator.resolved,
		Extras:            models.orchestrator.extras,
		Alias:             models.orchestrator.alias,
		Verbose:           cfg.verbose,
		DryRun:            cfg.dryRun,
		RequestTimeoutSec: cfg.timeoutPerTurn,
		PatchEnabled:      cfg.patch,
	})

	if !resumeMode || tr.Content() == "" {
		if err := tr.WriteHeader(goal, sessionID, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "Error writing transcript header:", err)
			return Result{ExitCode: 1, Err: err}
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
		if err := tr.WriteTurn(0, backgroundQuestionPrompt, "", nil, prefillAnswer, "background"); err != nil {
			fmt.Fprintln(os.Stderr, "Transcript write error:", err)
			sessionErr = err
			return Result{ExitCode: 1, Err: err}
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
		forcedResumePrompt := strings.TrimSpace(resumePrompt)
		if forcedResumePrompt != "" {
			decision = planner.DecisionAsk
			question = forcedResumePrompt
			resumePrompt = ""
			turnInfo["resume_prompt"] = true
		} else {
			planCtx, planCancel = makeTurnContext(rootCtx, cfg.timeoutPerTurn)
			trFull := tr.Content()
			planCtx = attachTrajectory(planCtx, trajectoryWriter, parentSpanID)
			decision, question, perr = pl.Plan(planCtx, goal, trFull, step, cfg.maxSteps)
			planCancel()
		}
		var planCtxErr error
		if planCtx != nil {
			planCtxErr = planCtx.Err()
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
				cancelF()
				if ferr != nil {
					if isContextCancelled(ferr) || isContextCancelled(ctxF.Err()) {
						return interruptedResult(ferr)
					}
					if errors.Is(ctxF.Err(), context.DeadlineExceeded) {
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

		if decision == planner.DecisionFinalize {
			ctx, cancelF := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
			trFull := tr.Content()
			ctx = attachTrajectory(ctx, trajectoryWriter, parentSpanID)
			answer, ferr := pl.Finalize(ctx, goal, trFull)
			cancelF()
			if ferr != nil {
				if isContextCancelled(ferr) || isContextCancelled(ctx.Err()) {
					return interruptedResult(ferr)
				}
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
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
			printResumeHint("=== SESSION COMPLETE ===", turnsCompleted)
			return Result{ExitCode: 0, Status: sessionStatus, Turns: userTurnCounter, SessionID: sessionID}
		}

		switch decision {
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
			useShellAgent := cfg.shellAgent
			preflightNote := ""
			var preflightErr error
			preflightReply := ""
			if cfg.shellAgent {
				preflightNote = "routing: shell (config override — something else, such as running a command in the shell)"
			} else {
				ctxPre, cancelPre := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
				ctxPre = attachTrajectory(ctxPre, trajectoryWriter, parentSpanID)
				useShellAgent, preflightReply, preflightErr = promptsvc.PreflightShellRouting(ctxPre, mctRunner.Runtime, question)
				cancelPre()
				if preflightErr != nil && cfg.verbose {
					fmt.Fprintln(os.Stderr, "Preflight routing error:", preflightErr)
				}
				routeLabel := "shell"
				routeExplanation := "something else, such as running a command in the shell"
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
			mctRunner.ShellAgent = useShellAgent
			indicator := "shell"
			if !useShellAgent {
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
			input := runner.PromptInput{
				Prompt:         question,
				Mode:           "default",
				IncludeHistory: true,
				OnStreamHeader: stream.OnChunk,
				OnStreamToken:  stream.OnChunk,
				MaxInputTokens: cfg.maxInputTokens,
			}
			ctx2, cancel2 := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
			ctx2 = attachTrajectory(ctx2, trajectoryWriter, parentSpanID)
			result, merr := mctRunner.RunPrompt(ctx2, sessionID, input)
			cancel2()
			if merr != nil {
				if isContextCancelled(merr) || isContextCancelled(ctx2.Err()) {
					stream.Abort("interrupted")
					return interruptedResult(merr)
				}
				msg := merr.Error()
				if errors.Is(ctx2.Err(), context.DeadlineExceeded) || strings.Contains(strings.ToLower(merr.Error()), "signal: killed") {
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
			stream.Complete(fullAns)
			if err := tr.WriteTurn(step, question, savedPath, retrieved, fullAns, "ask"); err != nil {
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
			continue

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
			if instr.Metadata != nil {
				if desc := strings.TrimSpace(instr.Metadata.Description); desc != "" {
					patchTurnLabel = "Patcher: " + desc
				}
			}
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
			ctxP, cancelP := makeTurnContext(rootCtx, cfg.timeoutPerTurn)
			ctxP = attachTrajectory(ctxP, trajectoryWriter, parentSpanID)
			result, applyErr := pRunner.Apply(ctxP, instr, cfg.verbose)
			cancelP()
			if applyErr != nil {
				if isContextCancelled(applyErr) || isContextCancelled(ctxP.Err()) {
					stream.Abort("interrupted")
					return interruptedResult(applyErr)
				}
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
					}
					_ = tr.WritePatchValidation(step, rec)
					extra := map[string]any{
						"patch_validation_operation": cleanErr.Diagnostics.Operation,
						"patch_validation_status":    "failed",
						"patch_validation_messages":  len(cleanErr.Diagnostics.Messages),
					}
					recordPatchError("patch_validation_failed", applyErr, extra)
					if shouldFinalizeAfterPatch {
						goto Finalize
					}
					continue
				case errors.As(applyErr, &valErr):
					stream.Abort("patch validation error")
					_ = tr.WriteTurn(step, patchTurnLabel, "", nil, valErr.Error(), "patch-error")
					recordPatchError("patch_validation_error", valErr, nil)
					if shouldFinalizeAfterPatch {
						goto Finalize
					}
					continue
				case errors.As(applyErr, &genErr):
					stream.Abort("patch generation error")
					_ = tr.WriteTurn(step, patchTurnLabel, "", nil, genErr.Error(), "patch-error")
					recordPatchError("patch_generation_error", genErr, nil)
					if shouldFinalizeAfterPatch {
						goto Finalize
					}
					continue
				default:
					stream.Abort("patcher execution error")
					_ = tr.WriteTurn(step, patchTurnLabel, "", nil, applyErr.Error(), "patch-error")
					recordPatchError("patch_apply_error", applyErr, nil)
					if shouldFinalizeAfterPatch {
						goto Finalize
					}
					continue
				}
			}
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
			if desc := strings.TrimSpace(result.Description); desc != "" {
				patchTurnLabel = "Patcher: " + desc
			}
			resMap := map[string]any{
				"patch_path":        result.PatchPath,
				"sequence":          result.Sequence,
				"applies":           true,
				"files_modified":    result.FilesModified,
				"insertions":        result.Insertions,
				"deletions":         result.Deletions,
				"workspace_applied": result.AppliedInWorkspace,
			}
			if strings.TrimSpace(result.Description) != "" {
				resMap["description"] = strings.TrimSpace(result.Description)
			}
			if strings.TrimSpace(result.ReversePatchPath) != "" {
				resMap["reverse_patch_path"] = strings.TrimSpace(result.ReversePatchPath)
			}
			resJSON, _ := json.Marshal(resMap)
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
			ans := fmt.Sprintf(
				"Patch created: %s\nsequence: %d\nfiles_modified: %v\ninsertions: %d\ndeletions: %d\n%s\n%s\nreverse_patch_path: %s\n\ninput:\n%s\n\noutput:\n%s\n",
				result.PatchPath,
				result.Sequence,
				strings.Join(result.FilesModified, ", "),
				result.Insertions,
				result.Deletions,
				workspaceStatus,
				finalizeStatus,
				strings.TrimSpace(result.ReversePatchPath),
				trimTo(string(jsonBytes), 1000),
				trimTo(string(resJSON), 1000),
			)
			if err := tr.WriteTurn(step, patchTurnLabel, "", nil, ans, "patch"); err != nil {
				fmt.Fprintln(os.Stderr, "Transcript write error:", err)
				sessionErr = err
				return Result{ExitCode: 1, Err: err}
			}
			stream.Complete(ans)
			lastAnswer = ans
			extra := map[string]any{}
			if trajectoryWriter != nil {
				extra = trajectory.MergeExcerptWithPrefix(extra, trajectory.MakeTextExcerpt(ans, trajectoryWriter.ExcerptLen()), "answer")
			}
			extra["patch_path"] = strings.TrimSpace(result.PatchPath)
			extra["patch_sequence"] = result.Sequence
			extra["patch_insertions"] = result.Insertions
			extra["patch_deletions"] = result.Deletions
			extra["patch_files_modified"] = len(result.FilesModified)
			if len(result.FilesModified) > 0 && len(result.FilesModified) <= 10 {
				extra["patch_files"] = append([]string(nil), result.FilesModified...)
			}
			extra["patch_workspace_applied"] = result.AppliedInWorkspace
			extra["patch_applied"] = result.AppliedInWorkspace
			extra["patch_finalize_pending"] = false
			if strings.TrimSpace(result.ReversePatchPath) != "" {
				extra["patch_reverse_path"] = strings.TrimSpace(result.ReversePatchPath)
			}
			patchOutcome("success", nil, extra)
			if shouldFinalizeAfterPatch {
				goto Finalize
			}
			continue

		case planner.DecisionFinalize:
			goto Finalize
		default:
			goto Finalize
		}
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
						cancelP()
						if applyErr != nil {
							if isContextCancelled(applyErr) || isContextCancelled(ctxP.Err()) {
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
							resMap := map[string]any{
								"patch_path":        result.PatchPath,
								"sequence":          result.Sequence,
								"applies":           true,
								"files_modified":    result.FilesModified,
								"insertions":        result.Insertions,
								"deletions":         result.Deletions,
								"workspace_applied": result.AppliedInWorkspace,
							}
							if strings.TrimSpace(result.Description) != "" {
								resMap["description"] = strings.TrimSpace(result.Description)
							}
							resJSON, _ := json.Marshal(resMap)
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
							ans := fmt.Sprintf(
								"Patch created: %s\nsequence: %d\nfiles_modified: %v\ninsertions: %d\ndeletions: %d\n%s\n%s\n\ninput:\n%s\n\noutput:\n%s\n",
								result.PatchPath,
								result.Sequence,
								strings.Join(result.FilesModified, ", "),
								result.Insertions,
								result.Deletions,
								workspaceStatus,
								finalizeStatus,
								trimTo(string(jsonBytes), 1000),
								trimTo(string(resJSON), 1000),
							)
							_ = tr.WriteTurn(step, qline, "", nil, ans, "patch")
							if stream != nil {
								stream.Complete(ans)
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
	cancelF()
	if ferr != nil {
		if isContextCancelled(ferr) || isContextCancelled(ctx.Err()) {
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
	printResumeHint("=== SESSION COMPLETE ===", turnsCompleted)
	return Result{ExitCode: 0, Status: sessionStatus, Turns: turns, SessionID: sessionID}
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
