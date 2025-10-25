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

	"github.com/tursomari/machtiani/agent/internal/gitops"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	"github.com/tursomari/machtiani/agent/internal/mct/readmesync"
	"github.com/tursomari/machtiani/agent/internal/parser"
	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/runner"
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
	goal := strings.TrimSpace(opts.Goal)
	if goal == "" {
		fmt.Fprintln(os.Stderr, "Error: empty issue/question provided")
		return Result{ExitCode: 2, Err: errors.New("empty goal")}
	}

	cfg := newLegacyConfig(opts.Config)
	applyTrajectoryEnvOverrides(&cfg)

	sessionStatus := "error"
	var sessionErr error
	turnsCompleted := 0

	sessionID := runner.GenerateSessionID()
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
	if trajectoryWriter != nil {
		cancel, done, err := startLLMFailoverLogger(display, trajectoryWriter.Config().Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[trajectory] failover listener setup error: %v\n", err)
		} else {
			failoverCancel = cancel
			failoverDone = done
		}
	}
	defer func() {
		if failoverCancel != nil {
			failoverCancel()
		}
		if failoverDone != nil {
			<-failoverDone
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

	tr, err := transcript.NewWithPath(cfg.transcriptFile, sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error preparing transcript:", err)
		return Result{ExitCode: 1, Err: err}
	}
	defer tr.Close()
	tr.SetTrajectory(trajectoryWriter)

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

	models, err := resolveModelRuntimes(cfg, paramPairs, paramJSONVals)
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
	orchPromptOpts := promptOptions(
		describeModel("orchestrator", models.orchestrator),
		describeModel("answer", models.answer),
		describeModel("file discovery", models.fileDiscovery),
	)
	patcherPromptOpts := promptOptions(
		describeModel("patcher", models.patcher),
	)

	mctRunner := runner.Runner{
		Verbose:                 cfg.verbose,
		DryRun:                  cfg.dryRun,
		Runtime:                 models.orchestrator.toPromptRuntime(),
		AnswerRuntime:           models.answer.toPromptRuntime(),
		FileDiscoveryRuntime:    models.fileDiscovery.toPromptRuntime(),
		FileDiscoveryTrajectory: trajectoryPath,
		ShellAgent:              cfg.shellAgent,
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
			Enabled:   true,
			Verbose:   cfg.verbose,
			DryRun:    cfg.dryRun,
			SessionID: sessionID,
			Runtime:   models.patcher.toPromptRuntime(),
			Service:   patchersvc.NewService(patchersvc.WithLogger(patchLogger)),
		}
		if err := pr.Resolve(); err != nil {
			if cfg.verbose {
				fmt.Fprintln(os.Stderr, "[patcher] resolve warning:", err)
			}
		}
		pRunner = pr
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

	lastAnswer := ""
	retrieved := []string{}
	userTurnCounter := 0

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

	display.StartSession(goal)
	sessionClosed := false
	defer func() {
		if !sessionClosed {
			display.EndSession()
		}
	}()

	parentSpanID := ""
	for {
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

		ctx, cancel := makeTurnContext(cfg.timeoutPerTurn)
		trFull := tr.Content()
		ctx = attachTrajectory(ctx, trajectoryWriter, parentSpanID)
		decision, question, perr := pl.Plan(ctx, goal, trFull, step, cfg.maxSteps)
		cancel()
		if perr != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
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
				ctxF, cancelF := makeTurnContext(cfg.timeoutPerTurn)
				trFull := tr.Content()
				ctxF = attachTrajectory(ctxF, trajectoryWriter, parentSpanID)
				answer, ferr := pl.Finalize(ctxF, goal, trFull)
				cancelF()
				if ferr != nil {
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
			ctx, cancelF := makeTurnContext(cfg.timeoutPerTurn)
			trFull := tr.Content()
			ctx = attachTrajectory(ctx, trajectoryWriter, parentSpanID)
			answer, ferr := pl.Finalize(ctx, goal, trFull)
			cancelF()
			if ferr != nil {
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
			stream := display.BeginPrompt(question, orchPromptOpts)
			input := runner.PromptInput{
				Prompt:         question,
				Mode:           "default",
				IncludeHistory: true,
				OnStreamHeader: stream.OnChunk,
				OnStreamToken:  stream.OnChunk,
				MaxInputTokens: cfg.maxInputTokens,
			}
			ctx2, cancel2 := makeTurnContext(cfg.timeoutPerTurn)
			ctx2 = attachTrajectory(ctx2, trajectoryWriter, parentSpanID)
			result, merr := mctRunner.RunPrompt(ctx2, sessionID, input)
			cancel2()
			if merr != nil {
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
			ctxP, cancelP := makeTurnContext(cfg.timeoutPerTurn)
			ctxP = attachTrajectory(ctxP, trajectoryWriter, parentSpanID)
			result, applyErr := pRunner.Apply(ctxP, instr, cfg.verbose)
			cancelP()
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
				"patch_path":     result.PatchPath,
				"applies":        true,
				"files_modified": result.FilesModified,
				"insertions":     result.Insertions,
				"deletions":      result.Deletions,
			}
			if strings.TrimSpace(result.Description) != "" {
				resMap["description"] = strings.TrimSpace(result.Description)
			}
			resJSON, _ := json.Marshal(resMap)
			ans := fmt.Sprintf("Patch created: %s\nfiles_modified: %v\ninsertions: %d\ndeletions: %d\n\ninput:\n%s\n\noutput:\n%s\n",
				result.PatchPath,
				strings.Join(result.FilesModified, ", "),
				result.Insertions,
				result.Deletions,
				trimTo(string(jsonBytes), 1000),
				trimTo(string(resJSON), 1000),
			)
			if !cfg.patchNoApply && !cfg.dryRun {
				if aerr := gitApply(result.PatchPath, cfg.verbose); aerr != nil {
					if cfg.verbose {
						fmt.Fprintln(os.Stderr, "[git] apply error (pre-finalize):", aerr)
					}
					stream.Abort("git apply failed")
					_ = tr.WriteTurn(step, patchTurnLabel, "", nil, trimTo(aerr.Error(), 800), "patch-error")
					recordPatchError("git_apply_error", aerr, nil)
					if shouldFinalizeAfterPatch {
						goto Finalize
					}
					continue
				}
				ans = ans + "applied: yes\n"
			} else if cfg.dryRun {
				if cfg.verbose {
					fmt.Fprintln(os.Stderr, "[git] apply (pre-finalize dry-run) skipping")
				}
				ans = ans + "applied: (dry-run)\n"
			} else {
				ans = ans + "applied: skipped (use without --patch-no-apply)\n"
			}
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
			extra["patch_insertions"] = result.Insertions
			extra["patch_deletions"] = result.Deletions
			extra["patch_files_modified"] = len(result.FilesModified)
			if len(result.FilesModified) > 0 && len(result.FilesModified) <= 10 {
				extra["patch_files"] = append([]string(nil), result.FilesModified...)
			}
			extra["patch_applied"] = !cfg.patchNoApply && !cfg.dryRun
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
	// One last planning opportunity before finalizing: if planner returns patch, run exactly one patch turn.
	{
		step := countTurns(tr.Content()) + 1
		parentSpanID := ""
		if sessTelemetry != nil {
			parentSpanID = sessTelemetry.span.ID
		}
		ctx, cancel := makeTurnContext(cfg.timeoutPerTurn)
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
						ctxP, cancelP := makeTurnContext(cfg.timeoutPerTurn)
						ctxP = attachTrajectory(ctxP, trajectoryWriter, parentSpanID)
						result, applyErr := pRunner.Apply(ctxP, instr, cfg.verbose)
						cancelP()
						if applyErr != nil {
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
								"patch_path":     result.PatchPath,
								"applies":        true,
								"files_modified": result.FilesModified,
								"insertions":     result.Insertions,
								"deletions":      result.Deletions,
							}
							if strings.TrimSpace(result.Description) != "" {
								resMap["description"] = strings.TrimSpace(result.Description)
							}
							resJSON, _ := json.Marshal(resMap)
							ans := fmt.Sprintf("Patch created: %s\nfiles_modified: %v\ninsertions: %d\ndeletions: %d\n\ninput:\n%s\n\noutput:\n%s\n",
								result.PatchPath,
								strings.Join(result.FilesModified, ", "),
								result.Insertions,
								result.Deletions,
								trimTo(string(jsonBytes), 1000),
								trimTo(string(resJSON), 1000),
							)
							if !cfg.patchNoApply && !cfg.dryRun {
								if aerr := gitApply(result.PatchPath, cfg.verbose); aerr != nil {
									if cfg.verbose {
										fmt.Fprintln(os.Stderr, "[git] apply error (pre-finalize):", aerr)
									}
									if stream != nil {
										stream.Abort("git apply failed")
										stream = nil
									}
									_ = tr.WriteTurn(step, "Patcher: pre-finalize apply failed", "", nil, trimTo(aerr.Error(), 800), "patch-error")
									handled = true
								} else {
									ans = ans + "applied: yes\n"
								}
							} else if cfg.dryRun {
								if cfg.verbose {
									fmt.Fprintln(os.Stderr, "[git] apply (pre-finalize dry-run) skipping")
								}
								ans = ans + "applied: (dry-run)\n"
							} else {
								ans = ans + "applied: skipped (use without --patch-no-apply)\n"
							}
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
	ctx, cancelF := makeTurnContext(cfg.timeoutPerTurn)
	trFull := tr.Content()
	ctx = attachTrajectory(ctx, trajectoryWriter, parentSpanID)
	answer, ferr := pl.Finalize(ctx, goal, trFull)
	cancelF()
	if ferr != nil {
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

func gitApply(path string, verbose bool) error {
	return gitops.ApplyPatch(path, verbose)
}
