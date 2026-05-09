// Package session coordinates the interaction between the planner, patcher, and discovery runners.
// The orchestrator manages turn sequences, ensuring the conversation.json state reflects
// the results of file discovery and the status of pending patch applications.
package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	mctsync "github.com/tursomari/machtiani/agent/internal/mct"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	promptsvc "github.com/tursomari/machtiani/agent/internal/mct/prompt"
	"github.com/tursomari/machtiani/agent/internal/parser"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/runner"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/tempdir"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
	"github.com/tursomari/machtiani/agent/internal/workspace"
)

type transcriptTurnWriter func(step int, question, savedPath string, retrieved []string, summary string, decision string) error

type pendingPatchTranscriptWriter func(status string, decision string, note string, undo bool) error

type turnLoopAction string

const (
	turnLoopContinue turnLoopAction = "continue"
	turnLoopFinalize turnLoopAction = "finalize"
	turnLoopReturn   turnLoopAction = "return"
)

type turnExecutionResult struct {
	action         turnLoopAction
	result         Result
	shellAgentUsed bool
}

type runTurnEnv struct {
	rootCtx                     context.Context
	cfg                         legacyConfig
	sessionID                   string
	goal                        string
	repoRoot                    string
	step                        int
	sessionErr                  *error
	turnsCompleted              *int
	userTurnCounter             *int
	pendingPatchDraft           **patchTranscriptDraft
	plannerProgress             *plannerProgressTracker
	display                     *ui.TerminalDisplay
	sessTelemetry               *sessionTelemetry
	turn                        *turnTelemetry
	turnDecision                string
	turnInfo                    map[string]any
	parentSpanID                string
	trajectoryWriter            *trajectory.Writer
	writeTurn                   transcriptTurnWriter
	interruptedResult           func(error) Result
	isContextCancelled          func(error) bool
	writePendingPatchTranscript pendingPatchTranscriptWriter
	mctRunner                   *runner.Runner
	pRunner                     *runner.PatcherRunner
	pl                          *planner.Client
	tr                          *transcript.Transcript
	recorder                    *conversationRecorder
	orchPromptOpts              **ui.PromptOptions
	baseOrchMetadata            []string
	patcherPromptOpts           *ui.PromptOptions
	mctResponseDirectives       []string
}

func executeReviewDecision(env *runTurnEnv, decision planner.Decision, question string, review *planner.PendingReview) turnExecutionResult {
	if env == nil {
		return turnExecutionResult{}
	}
	if review == nil {
		var errUnexpected error
		if decision == planner.DecisionAccept {
			errUnexpected = errors.New("no pending patch review to accept")
		} else {
			errUnexpected = errors.New("no pending patch review to reject")
		}
		fmt.Fprintln(os.Stderr, "Planner error:", errUnexpected)
		*env.sessionErr = errUnexpected
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, errUnexpected)
		*env.turnsCompleted = *env.userTurnCounter
		return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: errUnexpected}}
	}

	note := strings.TrimSpace(question)
	if decision == planner.DecisionAccept {
		if err := env.writePendingPatchTranscript("SUCCESS", "patch", note, false); err != nil {
			fmt.Fprintln(os.Stderr, "Transcript write error:", err)
			*env.sessionErr = err
			finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, err)
			*env.turnsCompleted = *env.userTurnCounter
			return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}}
		}
		accepted := env.plannerProgress.commitPendingReview()
		*env.userTurnCounter++
		*env.turnsCompleted = *env.userTurnCounter
		env.turnInfo["planner_pending_review"] = false
		env.turnInfo["patch_review_pending"] = false
		env.turnInfo["planner_applied_patches"] = env.plannerProgress.appliedCount()
		env.turnInfo["planner_success_file_count"] = len(env.plannerProgress.successList())
		env.turnInfo["planner_review_action"] = "accept"
		env.turnInfo["patch_finalize_pending"] = false
		if accepted != nil {
			env.turnInfo["patch_review_sequence"] = accepted.Sequence
			if accepted.Description != "" {
				env.turnInfo["patch_review_description"] = accepted.Description
			}
			if len(accepted.Files) > 0 {
				env.turnInfo["patch_review_files"] = append([]string(nil), accepted.Files...)
			}
		}
		if note != "" {
			env.turnInfo["planner_review_note"] = note
		}
		if accepted != nil {
			env.display.Notify(fmt.Sprintf("Patch %d accepted%s", accepted.Sequence, formatOptionalSuffix(accepted.Description)))
		}
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "success", env.turnInfo, nil)
		if *env.userTurnCounter >= env.cfg.maxSteps {
			return turnExecutionResult{action: turnLoopFinalize}
		}
		return turnExecutionResult{action: turnLoopContinue}
	}

	undoRequired := !env.cfg.dryRun && strings.TrimSpace(review.ReversePatchPath) != ""
	if undoRequired && env.pRunner == nil {
		err := errors.New("patch runner unavailable for undo")
		fmt.Fprintln(os.Stderr, "Patch undo error:", err)
		*env.sessionErr = err
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, err)
		*env.turnsCompleted = *env.userTurnCounter
		return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}}
	}
	if undoRequired {
		if err := env.pRunner.Undo(review.ReversePatchPath); err != nil {
			fmt.Fprintln(os.Stderr, "Patch undo error:", err)
			*env.sessionErr = err
			finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, err)
			*env.turnsCompleted = *env.userTurnCounter
			return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}}
		}
	}
	if err := env.writePendingPatchTranscript("REJECTED", "patch", note, undoRequired); err != nil {
		fmt.Fprintln(os.Stderr, "Transcript write error:", err)
		*env.sessionErr = err
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, err)
		*env.turnsCompleted = *env.userTurnCounter
		return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}}
	}
	discarded := env.plannerProgress.discardPendingReview()
	*env.userTurnCounter++
	*env.turnsCompleted = *env.userTurnCounter
	env.turnInfo["planner_pending_review"] = false
	env.turnInfo["planner_review_action"] = "reject"
	env.turnInfo["planner_applied_patches"] = env.plannerProgress.appliedCount()
	env.turnInfo["planner_success_file_count"] = len(env.plannerProgress.successList())
	env.turnInfo["patch_finalize_pending"] = false
	env.turnInfo["patch_review_pending"] = false
	if undoRequired {
		env.turnInfo["patch_undo_applied"] = true
		env.turnInfo["patch_reverse_path"] = review.ReversePatchPath
	}
	if discarded != nil {
		env.turnInfo["patch_review_sequence"] = discarded.Sequence
		if discarded.Description != "" {
			env.turnInfo["patch_review_description"] = discarded.Description
		}
		if len(discarded.Files) > 0 {
			env.turnInfo["patch_review_files"] = append([]string(nil), discarded.Files...)
		}
	}
	if note != "" {
		env.turnInfo["planner_review_note"] = note
	}
	if discarded != nil {
		env.display.Notify(fmt.Sprintf("Patch %d rejected%s", discarded.Sequence, formatOptionalSuffix(discarded.Description)))
	}
	finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "success", env.turnInfo, nil)
	return turnExecutionResult{action: turnLoopContinue}
}

func executeAskDecision(env *runTurnEnv, question string) turnExecutionResult {
	if env == nil {
		return turnExecutionResult{}
	}
	if question == "" {
		errEmpty := errors.New("planner returned empty question")
		fmt.Fprintln(os.Stderr, "Planner returned empty question for 'ask' decision")
		*env.sessionErr = errEmpty
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, errEmpty)
		*env.turnsCompleted = *env.userTurnCounter
		return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: errEmpty}}
	}
	if env.cfg.verbose {
		fmt.Fprintln(os.Stderr, "Question:", question)
	}
	noShellAsk, shellAsk, hasSplitAsk := splitAskLines(question)
	collapsedLegacyBothAsk := false
	if hasSplitAsk {
		question = collapseSplitAskLines(noShellAsk, shellAsk)
		hasSplitAsk = false
		collapsedLegacyBothAsk = true
	}
	preflightQuestion := question
	useShellAgent := env.cfg.shellAgent
	preflightNote := ""
	var preflightErr error
	preflightReply := ""
	runSplitShell := hasSplitAsk
	if hasSplitAsk {
		useShellAgent = false
		if env.cfg.shellAgent {
			preflightNote = "routing: both (config override — running split ask)"
		} else {
			preflightNote = "routing: both (split ask — run no-shell + shell agent)"
		}
	} else if env.cfg.shellAgent {
		preflightNote = "routing: shell (config override — run commands via shell agent)"
	} else {
		ctxPre, cancelPre := makeTurnContext(env.rootCtx, env.cfg.timeoutPerTurn)
		ctxPre = attachTrajectory(ctxPre, env.trajectoryWriter, env.parentSpanID)
		useShellAgent, preflightReply, preflightErr = promptsvc.PreflightShellRouting(ctxPre, env.mctRunner.Runtime, preflightQuestion)
		cancelPre()
		if preflightErr != nil && env.cfg.verbose {
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
	useShellAgent, forcedSingleShellRoute := applySingleAskRoutingPolicy(hasSplitAsk, useShellAgent)
	if collapsedLegacyBothAsk {
		env.turnInfo["mct_legacy_both_collapsed"] = true
		if preflightNote == "" {
			preflightNote = "routing policy: legacy both ask -> shell agent"
		} else {
			preflightNote = preflightNote + " | routing policy: legacy both ask -> shell agent"
		}
	}
	if forcedSingleShellRoute {
		env.turnInfo["mct_single_ask_shell_forced"] = true
		if preflightNote == "" {
			preflightNote = "routing policy: single ask -> shell agent"
		} else {
			preflightNote = preflightNote + " | routing policy: single ask -> shell agent"
		}
	}
	shellAgentUsedThisTurn := useShellAgent || runSplitShell
	var shellNoticePrompts *llm.MCTPromptsConfig
	if env.mctRunner.Prompts != nil {
		shellNoticePrompts = env.mctRunner.Prompts.MCT
	}
	if runSplitShell {
		if strings.TrimSpace(shellAsk) != "" {
			shellAsk = promptsvc.AppendShellAgentPromptNotice(shellAsk, shellNoticePrompts)
		}
	} else if useShellAgent {
		question = promptsvc.AppendShellAgentPromptNotice(question, shellNoticePrompts)
	}
	if useShellAgent || runSplitShell {
		if root := strings.TrimSpace(tempdir.SessionRoot()); root != "" {
			if _, _, err := workspace.EnsureRepoSnapshot(env.repoRoot, root); err != nil {
				fmt.Fprintln(os.Stderr, "Error: host->snapshot refresh failed:", err)
				return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}, shellAgentUsed: shellAgentUsedThisTurn}
			}
		}
	}
	env.mctRunner.ShellAgent = useShellAgent
	indicator := "shell"
	if runSplitShell {
		indicator = "both"
	} else if !useShellAgent {
		indicator = "file"
	}
	metadata := append([]string(nil), env.baseOrchMetadata...)
	if preflightNote != "" {
		metadata = append(metadata, preflightNote)
	}
	if env.orchPromptOpts != nil {
		*env.orchPromptOpts = &ui.PromptOptions{ModeIndicator: indicator, Metadata: metadata}
	}
	env.turnInfo["mct_shell_agent"] = useShellAgent || runSplitShell
	if runSplitShell {
		env.turnInfo["mct_shell_agent_split"] = true
	}
	if !env.cfg.shellAgent {
		if strings.TrimSpace(preflightReply) != "" {
			env.turnInfo["mct_preflight_reply"] = trimTo(preflightReply, 200)
		}
		if preflightErr != nil {
			env.turnInfo["mct_preflight_error"] = trimTo(preflightErr.Error(), 200)
		}
	}
	orchPromptOpts := (*ui.PromptOptions)(nil)
	if env.orchPromptOpts != nil {
		orchPromptOpts = *env.orchPromptOpts
	}
	stream := env.display.BeginPrompt(question, orchPromptOpts)
	if runSplitShell {
		runNoShell := true

		var (
			shellResult promptsvc.ShellAgentOnlyResult
			shellErr    error
			shellDone   chan struct{}
			shellCancel context.CancelFunc
		)
		shellDone = make(chan struct{})
		shellAskTrimmed := strings.TrimSpace(shellAsk)
		if shellAskTrimmed == "" {
			close(shellDone)
		} else {
			ctxShell, cancelShell := makeTurnContext(env.rootCtx, env.cfg.timeoutPerTurn)
			ctxShell = attachTrajectory(ctxShell, env.trajectoryWriter, env.parentSpanID)
			shellCancel = cancelShell
			shellOpts := promptsvc.RunOptions{
				Prompt:               shellAskTrimmed,
				Mode:                 "answer-only",
				IncludeHistory:       true,
				SessionID:            env.sessionID,
				Runtime:              env.mctRunner.Runtime,
				AnswerRuntime:        env.mctRunner.AnswerRuntime,
				FileDiscoveryRuntime: env.mctRunner.FileDiscoveryRuntime,
				Verbose:              env.cfg.verbose,
				MaxInputTokens:       env.cfg.maxInputTokens,
				ShellAgent:           true,
				ShellAgentModel:      strings.TrimSpace(env.mctRunner.ShellAgentModel),
				GlobalConfigPath:     env.mctRunner.GlobalConfigPath,
				PersistTmpData:       env.mctRunner.PersistTmpData,
				SessionTempRoot:      env.mctRunner.SessionTempRoot,
				ResponseDirectives:   append([]string(nil), env.mctResponseDirectives...),
			}
			if env.mctRunner.Prompts != nil {
				shellOpts.Prompts = env.mctRunner.Prompts.MCT
			}
			if env.mctRunner.ShellAgentLibrary != nil {
				conv := env.recorder.Conversation()
				prebuilt, err := shellagent.BuildShellAgentMessages(conv.ToLLMMessages(), env.mctRunner.ShellAgentLibrary.Prompts, env.mctRunner.ShellAgentLibrary.ExtraInstructions)
				if err != nil && env.cfg.verbose {
					fmt.Fprintln(os.Stderr, "shell-agent library: build prebuilt messages:", err)
				}
				if err == nil {
					shellOpts.ShellAgentLibrary = &promptsvc.ShellAgentLibraryConfig{
						Model:             env.mctRunner.ShellAgentLibrary.Model,
						Env:               env.mctRunner.ShellAgentLibrary.Env,
						Config:            env.mctRunner.ShellAgentLibrary.Config,
						Prompts:           env.mctRunner.ShellAgentLibrary.Prompts,
						PrebuiltMessages:  prebuilt,
					}
				}
			}
			go func() {
				defer close(shellDone)
				if shellCancel != nil {
					defer shellCancel()
				}
				shellResult, shellErr = promptsvc.RunShellAgentOnly(ctxShell, shellOpts)
			}()
		}

		var (
			result  promptsvc.Result
			merr    error
			ctx2Err error
		)
		if runNoShell {
			input := runner.PromptInput{
				Prompt:             question,
				Mode:               "default",
				IncludeHistory:     true,
				OnStreamHeader:     stream.OnChunk,
				OnStreamToken:      stream.OnChunk,
				MaxInputTokens:     env.cfg.maxInputTokens,
				ResponseDirectives: append([]string(nil), env.mctResponseDirectives...),
			}
			ctx2, cancel2 := makeTurnContext(env.rootCtx, env.cfg.timeoutPerTurn)
			ctx2 = attachTrajectory(ctx2, env.trajectoryWriter, env.parentSpanID)
			result, merr = env.mctRunner.RunPrompt(ctx2, env.sessionID, input)
			if ctx2 != nil {
				ctx2Err = ctx2.Err()
			}
			if cancel2 != nil {
				cancel2()
			}
			if merr != nil {
				if shellCancel != nil {
					shellCancel()
				}
				if shellDone != nil {
					<-shellDone
				}
				if env.isContextCancelled(merr) || env.isContextCancelled(ctx2Err) {
					stream.Abort("interrupted")
					return turnExecutionResult{action: turnLoopReturn, result: env.interruptedResult(merr), shellAgentUsed: shellAgentUsedThisTurn}
				}
				msg := merr.Error()
				if errors.Is(ctx2Err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(merr.Error()), "signal: killed") {
					msg = fmt.Sprintf("timed out after %ds", env.cfg.timeoutPerTurn)
					fmt.Fprintf(os.Stderr, "mct prompt error: %s. Try increasing --timeout-per-turn or set 0 for unlimited.\n", msg)
				} else {
					fmt.Fprintln(os.Stderr, "mct prompt error:", merr)
				}
				stream.Abort(msg)
				*env.sessionErr = merr
				finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, merr)
				*env.turnsCompleted = *env.userTurnCounter
				return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: merr}, shellAgentUsed: shellAgentUsedThisTurn}
			}
		}

		if shellDone != nil {
			<-shellDone
		}
		if shellErr != nil {
			env.turnInfo["shell_agent_error"] = trimTo(shellErr.Error(), 200)
		}
		if strings.TrimSpace(shellResult.TrajectoryPath) != "" {
			env.turnInfo["shell_agent_trajectory"] = trimTo(shellResult.TrajectoryPath, 200)
		}

		savedPath := ""
		lastAnswer := ""
		var retrieved []string
		if env.cfg.dryRun {
			lastAnswer = "[dry-run] mct would have produced a chat response here."
			retrieved = nil
		} else if runNoShell {
			if result.SaveError != nil {
				fmt.Fprintln(os.Stderr, "Warning: failed to save chat transcript:", result.SaveError)
			}
			savedPath = strings.TrimSpace(result.SavedPath)
			if savedPath == "" {
				chatDir, err := artifacts.SessionChatDirectory(env.sessionID)
				if err != nil {
					fmt.Fprintln(os.Stderr, "Failed to resolve chat directory:", err)
					stream.Abort("failed to save chat transcript")
					return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}, shellAgentUsed: shellAgentUsedThisTurn}
				}
				savedPath = filepath.Join(chatDir, "machtiani-response.md")
			}
			lastAnswer = result.FullText
			retrieved = append([]string(nil), result.RetrievedFiles...)
		}
		fullAns := result.Assistant
		if env.cfg.dryRun || strings.TrimSpace(fullAns) == "" {
			fullAns = lastAnswer
		}
		if env.cfg.enableTagFormat {
			stats, warnings := analyzeTagFormat(fullAns, retrieved)
			for k, v := range stats {
				env.turnInfo[k] = v
			}
			for _, warn := range warnings {
				fmt.Fprintf(os.Stderr, "[tag-format] %s\n", warn)
			}
		}
		stream.Complete(fullAns)
		transcriptQuestion := question
		if block := strings.TrimSpace(result.DirectiveBlock); block != "" && runNoShell {
			transcriptQuestion = transcriptQuestion + "\n\n" + block
		}
		if err := env.writeTurn(env.step, transcriptQuestion, savedPath, retrieved, fullAns, "ask"); err != nil {
			fmt.Fprintln(os.Stderr, "Transcript write error:", err)
			*env.sessionErr = err
			finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, err)
			return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}, shellAgentUsed: shellAgentUsedThisTurn}
		}
		env.turnInfo["retrieved_count"] = len(retrieved)
		*env.userTurnCounter++
		*env.turnsCompleted = *env.userTurnCounter
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "success", env.turnInfo, nil)
		if *env.userTurnCounter == env.cfg.maxSteps {
			return turnExecutionResult{action: turnLoopFinalize, shellAgentUsed: shellAgentUsedThisTurn}
		}
		return turnExecutionResult{action: turnLoopContinue, shellAgentUsed: shellAgentUsedThisTurn}
	}

	input := runner.PromptInput{
		Prompt:             question,
		Mode:               "default",
		IncludeHistory:     true,
		OnStreamHeader:     stream.OnChunk,
		OnStreamToken:      stream.OnChunk,
		MaxInputTokens:     env.cfg.maxInputTokens,
		ResponseDirectives: append([]string(nil), env.mctResponseDirectives...),
	}
	if useShellAgent && env.mctRunner.ShellAgentLibrary != nil {
		conv := env.recorder.Conversation()
		prebuilt, err := shellagent.BuildShellAgentMessages(conv.ToLLMMessages(), env.mctRunner.ShellAgentLibrary.Prompts, env.mctRunner.ShellAgentLibrary.ExtraInstructions)
		if err != nil && env.cfg.verbose {
			fmt.Fprintln(os.Stderr, "shell-agent library: build prebuilt messages:", err)
		}
		if err == nil {
			input.ShellAgentLibrary = &promptsvc.ShellAgentLibraryConfig{
				Model:            env.mctRunner.ShellAgentLibrary.Model,
				Env:              env.mctRunner.ShellAgentLibrary.Env,
				Config:           env.mctRunner.ShellAgentLibrary.Config,
				Prompts:          env.mctRunner.ShellAgentLibrary.Prompts,
				PrebuiltMessages: prebuilt,
			}
		}
	}
	ctx2, cancel2 := makeTurnContext(env.rootCtx, env.cfg.timeoutPerTurn)
	ctx2 = attachTrajectory(ctx2, env.trajectoryWriter, env.parentSpanID)
	result, merr := env.mctRunner.RunPrompt(ctx2, env.sessionID, input)
	var ctx2Err error
	if ctx2 != nil {
		ctx2Err = ctx2.Err()
	}
	if cancel2 != nil {
		cancel2()
	}
	if merr != nil {
		if env.isContextCancelled(merr) || env.isContextCancelled(ctx2Err) {
			stream.Abort("interrupted")
			return turnExecutionResult{action: turnLoopReturn, result: env.interruptedResult(merr), shellAgentUsed: shellAgentUsedThisTurn}
		}
		msg := merr.Error()
		if errors.Is(ctx2Err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(merr.Error()), "signal: killed") {
			msg = fmt.Sprintf("timed out after %ds", env.cfg.timeoutPerTurn)
			fmt.Fprintf(os.Stderr, "mct prompt error: %s. Try increasing --timeout-per-turn or set 0 for unlimited.\n", msg)
		} else {
			fmt.Fprintln(os.Stderr, "mct prompt error:", merr)
		}
		stream.Abort(msg)
		*env.sessionErr = merr
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, merr)
		*env.turnsCompleted = *env.userTurnCounter
		return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: merr}, shellAgentUsed: shellAgentUsedThisTurn}
	}
	savedPath := strings.TrimSpace(result.SavedPath)
	lastAnswer := ""
	var retrieved []string
	if env.cfg.dryRun {
		lastAnswer = "[dry-run] mct would have produced a chat response here."
		retrieved = nil
	} else {
		if result.SaveError != nil {
			fmt.Fprintln(os.Stderr, "Warning: failed to save chat transcript:", result.SaveError)
		}
		if savedPath == "" {
			chatDir, err := artifacts.SessionChatDirectory(env.sessionID)
			if err != nil {
				fmt.Fprintln(os.Stderr, "Failed to resolve chat directory:", err)
				stream.Abort("failed to save chat transcript")
				return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}, shellAgentUsed: shellAgentUsedThisTurn}
			}
			savedPath = filepath.Join(chatDir, "machtiani-response.md")
		}
		lastAnswer = result.FullText
		retrieved = append([]string(nil), result.RetrievedFiles...)
	}
	fullAns := result.Assistant
	if env.cfg.dryRun || strings.TrimSpace(fullAns) == "" {
		fullAns = lastAnswer
	}
	if env.cfg.enableTagFormat {
		stats, warnings := analyzeTagFormat(fullAns, retrieved)
		for k, v := range stats {
			env.turnInfo[k] = v
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
	if err := env.writeTurn(env.step, transcriptQuestion, savedPath, retrieved, fullAns, "ask"); err != nil {
		fmt.Fprintln(os.Stderr, "Transcript write error:", err)
		*env.sessionErr = err
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, err)
		return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}, shellAgentUsed: shellAgentUsedThisTurn}
	}
	askInfo := map[string]any{"retrieved_count": len(retrieved)}
	if savedPath != "" {
		askInfo["saved_chat_path"] = savedPath
	}
	if env.trajectoryWriter != nil && strings.TrimSpace(fullAns) != "" {
		excerpt := trajectory.MakeTextExcerpt(fullAns, env.trajectoryWriter.ExcerptLen())
		askInfo = trajectory.MergeExcerptWithPrefix(askInfo, excerpt, "answer")
	}
	for k, v := range askInfo {
		env.turnInfo[k] = v
	}
	*env.userTurnCounter++
	*env.turnsCompleted = *env.userTurnCounter
	finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "success", env.turnInfo, nil)
	if *env.userTurnCounter == env.cfg.maxSteps {
		return turnExecutionResult{action: turnLoopFinalize, shellAgentUsed: shellAgentUsedThisTurn}
	}
	return turnExecutionResult{action: turnLoopContinue, shellAgentUsed: shellAgentUsedThisTurn}
}

func executePatchDecision(env *runTurnEnv, question string, patchPlan *PatchPlan) turnExecutionResult {
	if env == nil {
		return turnExecutionResult{}
	}
	if env.cfg.patch {
		if patchPlan == nil {
			if existingPlan, err := LoadPatchPlan(env.sessionID); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to load patch plan before patching: %v\n", err)
			} else {
				patchPlan = existingPlan
			}
		}
		if patchPlan == nil {
			hookCtx, hookCancel := makeTurnContext(env.rootCtx, env.cfg.timeoutPerTurn)
			hookCtx = attachTrajectory(hookCtx, env.trajectoryWriter, env.parentSpanID)
			generatedPlan, hookErr := invokePatchPlanUpdateHook(hookCtx, env.pl, env.tr, env.recorder, env.sessionID, env.goal, env.tr.Content(), env.plannerProgress.getLastPatchedFile(), env.display, true)
			if hookCancel != nil {
				hookCancel()
			}
			if hookErr != nil {
				fmt.Fprintln(os.Stderr, "Patch plan creation error:", hookErr)
				*env.sessionErr = hookErr
				finishTurn(env.sessTelemetry, env.turn, string(planner.DecisionPatch), "error", env.turnInfo, hookErr)
				return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: hookErr}}
			}
			patchPlan = generatedPlan
		}
		if patchPlan == nil {
			err := errors.New("patch plan missing; cannot apply patch")
			fmt.Fprintln(os.Stderr, "Patch plan error:", err)
			*env.sessionErr = err
			finishTurn(env.sessTelemetry, env.turn, string(planner.DecisionPatch), "error", env.turnInfo, err)
			return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}}
		}
		if env.plannerProgress.needsPatchPlanUpdate() {
			hookCtx, hookCancel := makeTurnContext(env.rootCtx, env.cfg.timeoutPerTurn)
			hookCtx = attachTrajectory(hookCtx, env.trajectoryWriter, env.parentSpanID)
			refreshedPlan, hookErr := invokePatchPlanUpdateHook(hookCtx, env.pl, env.tr, env.recorder, env.sessionID, env.goal, env.tr.Content(), env.plannerProgress.getLastPatchedFile(), env.display, false)
			if hookCancel != nil {
				hookCancel()
			}
			if hookErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: patch plan update failed before patching: %v\n", hookErr)
			} else if refreshedPlan != nil {
				patchPlan = refreshedPlan
			}
			env.plannerProgress.clearPatchPlanPending()
		}
		if patchPlan != nil {
			totalItems, completedItems := patchPlan.Progress()
			env.turnInfo["patch_plan_items"] = totalItems
			env.turnInfo["patch_plan_complete_items"] = completedItems
		}
	}
	if env.cfg.verbose {
		fmt.Fprintln(os.Stderr, "[patcher] planner payload (raw):", trimTo(strings.TrimSpace(question), 1200))
	}
	stream := env.display.BeginPrompt("Patcher: create patch", env.patcherPromptOpts)
	patchTurnLabel := "Patcher: create patch"
	shouldFinalizeAfterPatch := false
	turnCounted := false
	markTurnCounted := func() {
		if turnCounted {
			return
		}
		*env.userTurnCounter++
		turnCounted = true
		if *env.userTurnCounter == env.cfg.maxSteps {
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
			env.turnInfo[k] = v
		}
		*env.turnsCompleted = *env.userTurnCounter
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, status, env.turnInfo, err)
	}
	recordPatchError := func(kind string, err error, extra map[string]any) turnExecutionResult {
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
		if shouldFinalizeAfterPatch {
			return turnExecutionResult{action: turnLoopFinalize}
		}
		return turnExecutionResult{action: turnLoopContinue}
	}
	jsonBytes, jerr := parser.ExtractPatchJSONPayload(question)
	if jerr != nil {
		stream.Abort("invalid patch payload")
		_ = env.writeTurn(env.step, "Patcher: invalid input JSON", "", nil, "Error extracting JSON: "+jerr.Error(), "patch-error")
		return recordPatchError("invalid_patch_payload", jerr, nil)
	}
	if env.cfg.verbose {
		fmt.Fprintln(os.Stderr, "[patcher] extracted JSON:", trimTo(string(jsonBytes), 1200))
	}
	if env.trajectoryWriter != nil {
		excerpt := trajectory.MakeTextExcerpt(string(jsonBytes), env.trajectoryWriter.ExcerptLen())
		env.turnInfo = trajectory.MergeExcerptWithPrefix(env.turnInfo, excerpt, "patch_instructions")
	}
	var instr mctpatcher.Instructions
	dec := json.NewDecoder(bytes.NewReader(jsonBytes))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&instr); derr != nil {
		stream.Abort("invalid patch payload")
		_ = env.writeTurn(env.step, "Patcher: invalid instructions", "", nil, "Error decoding JSON: "+derr.Error(), "patch-error")
		return recordPatchError("invalid_patch_instructions", derr, nil)
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
			if norm, err := edit.NormalizedPath(env.repoRoot); err == nil {
				nPath = normalizePlannerPath(norm)
			} else {
				nPath = normalizePlannerPath(edit.Path)
			}
			if nPath == "" {
				skipAllSuccess = false
				continue
			}
			if !env.plannerProgress.hasSuccess(nPath) {
				skipAllSuccess = false
				continue
			}
			if _, exists := seenSkip[nPath]; !exists {
				seenSkip[nPath] = struct{}{}
				skipPaths = append(skipPaths, nPath)
			}
		}
	}
	if skipAllSuccess && len(skipPaths) > 0 && !forceRepatch && false {
		skipMsg := fmt.Sprintf("Skipping patch because all target files were already updated earlier this session: %s. Reload the latest file contents before generating another patch.", strings.Join(skipPaths, ", "))
		stream.Abort("patch skipped (already updated)")
		_ = env.writeTurn(env.step, "Patcher: skip (already updated)", "", nil, skipMsg, "patch-error")
		env.turnInfo["patch_skip_already_updated"] = skipPaths
		env.plannerProgress.noteForceRepatchHint()
		return recordPatchError("patch_skipped_already_success", fmt.Errorf("patch targeted previously updated files"), map[string]any{"skip_files": skipPaths})
	}
	if forceRepatch {
		env.turnInfo["patch_force_repatch"] = true
	}
	if instr.Metadata != nil {
		if desc := strings.TrimSpace(instr.Metadata.Description); desc != "" {
			patchTurnLabel = "Patcher: " + desc
		}
	}
	preparedInstr, modeAdjustments, precheckErr := preprocessPatchInstructions(env.repoRoot, instr)
	if len(modeAdjustments) > 0 {
		env.turnInfo["patch_precheck_adjustments"] = len(modeAdjustments)
		if env.cfg.verbose {
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
		if err := env.writeTurn(env.step, patchTurnLabel, "", nil, summary, "patch-error"); err != nil {
			fmt.Fprintln(os.Stderr, "Transcript write error:", err)
			*env.sessionErr = err
			patchOutcome("error", err, map[string]any{"patch_error_kind": "transcript_write_error", "patch_error_context": "precheck_summary"})
			return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}}
		}
		patchOutcome("error", precheckErr, map[string]any{"patch_error_kind": "patch_instruction_precheck", "patch_precheck_conflicts": precheckErr.Count(), "patch_error": precheckErr.Error(), "is_retry": !turnCounted, "retry_reason": "patch_instruction_precheck"})
		if !turnCounted {
			markTurnCounted()
		}
		*env.turnsCompleted = *env.userTurnCounter
		if shouldFinalizeAfterPatch {
			return turnExecutionResult{action: turnLoopFinalize}
		}
		return turnExecutionResult{action: turnLoopContinue}
	}
	instr = preparedInstr
	if env.pRunner == nil {
		stream.Abort("patch runner unavailable")
		_ = env.writeTurn(env.step, patchTurnLabel, "", nil, "Error: patch runner disabled", "patch-error")
		return recordPatchError("patch_runner_unavailable", errors.New("patch runner disabled"), nil)
	}
	if err := env.pRunner.Resolve(); err != nil {
		stream.Abort("patcher resolve failed")
		_ = env.writeTurn(env.step, patchTurnLabel, "", nil, "Error: "+err.Error(), "patch-error")
		return recordPatchError("patch_runner_resolve", err, nil)
	}
	autoFixApplied := false
	autoFixEdits := []int(nil)
	var result *mctpatcher.PatchResult
	var applyErr error
	var ctxPErr error
	for attempt := 0; attempt < 2; attempt++ {
		ctxP, cancelP := makeTurnContext(env.rootCtx, env.cfg.timeoutPerTurn)
		ctxP = attachTrajectory(ctxP, env.trajectoryWriter, env.parentSpanID)
		result, applyErr = env.pRunner.Apply(ctxP, instr, env.cfg.verbose)
		if ctxP != nil {
			ctxPErr = ctxP.Err()
		}
		if cancelP != nil {
			cancelP()
		}
		if applyErr == nil {
			break
		}
		if env.isContextCancelled(applyErr) || env.isContextCancelled(ctxPErr) {
			stream.Abort("interrupted")
			return turnExecutionResult{action: turnLoopReturn, result: env.interruptedResult(applyErr)}
		}
		var valErrFix *mctpatcher.ValidationError
		if !autoFixApplied && errors.As(applyErr, &valErrFix) {
			rewritten, edits := convertRewriteMissingToCreate(instr, valErrFix, env.cfg.verbose)
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
			if err := env.writeTurn(env.step, patchTurnLabel, "", nil, summary, "patch-error"); err != nil {
				fmt.Fprintln(os.Stderr, "Transcript write error:", err)
				*env.sessionErr = err
				patchOutcome("error", err, map[string]any{"patch_error_kind": "transcript_write_error", "patch_error_context": "validation_summary"})
				return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}}
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
			_ = env.recorder.RecordPatchValidation(env.step, rec)
			patchOutcome("error", applyErr, map[string]any{
				"patch_error_kind":           "patch_validation_failed",
				"patch_error":                applyErr.Error(),
				"is_retry":                   !turnCounted,
				"retry_reason":               "patch_validation_failed",
				"patch_validation_operation": cleanErr.Diagnostics.Operation,
				"patch_validation_status":    "failed",
				"patch_validation_messages":  len(cleanErr.Diagnostics.Messages),
				"conflicted_edits":           len(cleanErr.Diagnostics.ContentConflicts),
			})
		case errors.As(applyErr, &valErr):
			stream.Abort("patch validation error")
			_ = env.writeTurn(env.step, patchTurnLabel, "", nil, valErr.Error(), "patch-error")
			patchOutcome("error", valErr, map[string]any{"patch_error_kind": "patch_validation_error", "patch_error": valErr.Error(), "is_retry": !turnCounted, "retry_reason": "patch_validation_error"})
		case errors.As(applyErr, &genErr):
			stream.Abort("patch generation error")
			_ = env.writeTurn(env.step, patchTurnLabel, "", nil, genErr.Error(), "patch-error")
			patchOutcome("error", genErr, map[string]any{"patch_error_kind": "patch_generation_error", "patch_error": genErr.Error(), "is_retry": !turnCounted, "retry_reason": "patch_generation_error"})
		default:
			stream.Abort("patcher execution error")
			_ = env.writeTurn(env.step, patchTurnLabel, "", nil, applyErr.Error(), "patch-error")
			patchOutcome("error", applyErr, map[string]any{"patch_error_kind": "patch_apply_error", "patch_error": applyErr.Error(), "is_retry": !turnCounted, "retry_reason": "patch_apply_error"})
		}
		if !turnCounted {
			markTurnCounted()
		}
		*env.turnsCompleted = *env.userTurnCounter
		if shouldFinalizeAfterPatch {
			return turnExecutionResult{action: turnLoopFinalize}
		}
		return turnExecutionResult{action: turnLoopContinue}
	}
	if result == nil {
		stream.Abort("patcher returned no result")
		_ = env.writeTurn(env.step, patchTurnLabel, "", nil, "patcher returned empty result", "patch-error")
		return recordPatchError("patch_empty_result", errors.New("patcher returned empty result"), nil)
	}
	if autoFixApplied {
		env.turnInfo["patch_auto_fix_rewrite_missing"] = map[string]any{"count": len(autoFixEdits), "edits": autoFixEdits}
	}
	if desc := strings.TrimSpace(result.Description); desc != "" {
		patchTurnLabel = "Patcher: " + desc
	}
	workspaceStatus := "workspace_applied: no"
	if result.AppliedInWorkspace {
		workspaceStatus = "workspace_applied: yes"
	} else if env.cfg.dryRun {
		workspaceStatus = "workspace_applied: (dry-run)"
	}
	finalizeStatus := "finalize: atomic (done)"
	if env.cfg.dryRun {
		finalizeStatus = "finalize: (dry-run)"
	} else if env.cfg.patchNoApply {
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
	*env.pendingPatchDraft = &patchTranscriptDraft{Step: env.step, Description: descTrimmed, Answer: ans}
	stream.Complete(ans)
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
		recordDiscoveryPending(env.sessionID, result.FilesModified, env.cfg.verbose)
		if err := mctsync.RefreshSyncedWorkspace(env.sessionID, result.FilesModified, env.cfg.verbose); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: discovery workspace refresh failed: %v\n", err)
		}
	}
	env.plannerProgress.beginPendingReview(review)
	autoAcceptNote := ""
	autoAccepted := false
	var autoAcceptedReview *planner.PendingReview
	if patchReviewDisabled {
		autoAcceptNote = "Review disabled – patch auto-accepted."
		if err := env.writePendingPatchTranscript("SUCCESS", "patch", autoAcceptNote, false); err != nil {
			fmt.Fprintln(os.Stderr, "Transcript write error:", err)
			*env.sessionErr = err
			patchOutcome("error", err, map[string]any{"planner_review_action": "auto-accept"})
			*env.turnsCompleted = *env.userTurnCounter
			return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}}
		}
		autoAcceptedReview = env.plannerProgress.commitPendingReview()
		autoAccepted = true
		if autoAcceptedReview != nil {
			review = autoAcceptedReview
		}
	}
	plannerSuccessFiles := env.plannerProgress.successList()
	env.turnInfo["planner_applied_patches"] = env.plannerProgress.appliedCount()
	env.turnInfo["planner_success_file_count"] = len(plannerSuccessFiles)
	reviewPending := env.plannerProgress.hasPendingReview()
	env.turnInfo["planner_pending_review"] = reviewPending
	env.turnInfo["patch_review_pending"] = reviewPending
	if autoAccepted {
		env.turnInfo["planner_review_action"] = "auto-accept"
		env.turnInfo["patch_finalize_pending"] = false
		if autoAcceptNote != "" {
			env.turnInfo["planner_review_note"] = autoAcceptNote
		}
		if autoAcceptedReview != nil {
			env.turnInfo["patch_review_sequence"] = autoAcceptedReview.Sequence
			if autoAcceptedReview.Description != "" {
				env.turnInfo["patch_review_description"] = autoAcceptedReview.Description
			}
			if len(autoAcceptedReview.Files) > 0 {
				env.turnInfo["patch_review_files"] = append([]string(nil), autoAcceptedReview.Files...)
			}
		}
		if review != nil {
			env.display.Notify(fmt.Sprintf("Patch %d auto-accepted (review disabled)%s", review.Sequence, formatOptionalSuffix(review.Description)))
		} else {
			env.display.Notify("Patch auto-accepted (review disabled)")
		}
	} else {
		env.turnInfo["patch_finalize_pending"] = true
		if review != nil && len(review.Files) > 0 {
			env.turnInfo["planner_pending_review_files"] = append([]string(nil), review.Files...)
		}
	}
	extra := map[string]any{}
	if env.trajectoryWriter != nil {
		extra = trajectory.MergeExcerptWithPrefix(extra, trajectory.MakeTextExcerpt(ans, env.trajectoryWriter.ExcerptLen()), "answer")
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
	extra["planner_applied_patches"] = env.plannerProgress.appliedCount()
	extra["planner_success_files_total"] = len(plannerSuccessFiles)
	if len(plannerSuccessFiles) > 0 {
		extra["planner_success_files"] = plannerSuccessFiles
	}
	extra["patch_workspace_applied"] = result.AppliedInWorkspace
	extra["patch_applied"] = result.AppliedInWorkspace
	extra["patch_review_pending"] = env.plannerProgress.hasPendingReview()
	extra["patch_finalize_pending"] = env.plannerProgress.hasPendingReview()
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
	if shouldFinalizeAfterPatch && !env.plannerProgress.hasPendingReview() {
		return turnExecutionResult{action: turnLoopFinalize}
	}
	return turnExecutionResult{action: turnLoopContinue}
}

func syncTurnWorkspace(cfg legacyConfig, repoRoot string, decision planner.Decision, shellAgentUsed bool) error {
	shouldSync := false
	switch decision {
	case planner.DecisionAsk:
		if shellAgentUsed {
			shouldSync = true
		}
	case planner.DecisionPatch, planner.DecisionAccept, planner.DecisionReject:
		shouldSync = false
	}
	if !shouldSync {
		return nil
	}
	if root := strings.TrimSpace(tempdir.SessionRoot()); root != "" {
		snapshotRepoRoot := filepath.Join(root, "repo")
		patchDir := filepath.Join(root, "patches")
		if err := workspace.SyncSnapshotToHost(snapshotRepoRoot, repoRoot, patchDir); err != nil {
			return err
		}
	}
	return nil
}
