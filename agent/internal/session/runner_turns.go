// Package session coordinates the interaction between the planner and discovery runners.
// The orchestrator manages turn sequences, ensuring the conversation.json state reflects
// the results of file discovery and the status of pending patch applications.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	promptsvc "github.com/tursomari/machtiani/agent/internal/mct/prompt"
	"github.com/tursomari/machtiani/agent/internal/runner"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

type transcriptTurnWriter func(step int, question, savedPath string, retrieved []string, summary string, decision string) error

type turnLoopAction string

const (
	turnLoopAskWorker turnLoopAction = "ask_worker"
	turnLoopAnswerUser turnLoopAction = "answer_user"
	turnLoopReturn   turnLoopAction = "return"
	turnLoopAskUser  turnLoopAction = "ask_user"
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
	plannerProgress             *plannerProgressTracker
	display                     ui.SessionDisplay
	diagWriter                  io.Writer
	hasNewInput                 bool // from Options; propagated to TurnContext for ResumeAttempt
	isResumingTurn              bool // set when resumableShellAgent is true; triggers TUI replay
	shellAgentInterruptStep     int
	sessTelemetry               *sessionTelemetry
	turn                        *turnTelemetry
	turnDecision                string
	turnInfo                    map[string]any
	parentSpanID                string
	trajectoryWriter            *trajectory.Writer
	writeTurn                   transcriptTurnWriter
	interruptedResult           func(error) Result
	isContextCancelled          func(error) bool
	mctRunner                   *runner.Runner
	pl                          Planner
	tr                          *transcript.Transcript
	recorder                    *conversationRecorder
	orchPromptOpts              **ui.PromptOptions
	baseOrchMetadata            []string
	mctResponseDirectives       []string
}

func executeAskDecision(env *runTurnEnv, question string) turnExecutionResult {
	if env == nil {
		return turnExecutionResult{}
	}
	if question == "" {
		errEmpty := errors.New("planner returned empty question")
		fmt.Fprintln(env.diagWriter, "Planner returned empty question for 'ask' decision")
		*env.sessionErr = errEmpty
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, errEmpty)
		
		return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: errEmpty}}
	}
	if env.cfg.verbose {
		fmt.Fprintln(env.diagWriter, "Question:", question)
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
			fmt.Fprintln(env.diagWriter, "Preflight routing error:", preflightErr)
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
	if env.isResumingTurn {
		env.display.Notify(fmt.Sprintf("[resume] shell-agent session is resumable, step %d", env.step))
		replayShellAgentActions(env.display, env.diagWriter, env.sessionID, env.step)
	}
	var interruptedMsgs []minisweagent.Message
	if env.isResumingTurn {
		resumePath := filepath.Join(os.TempDir(), env.sessionID+"-resume.json")
		data, err := os.ReadFile(resumePath)
		if err == nil {
			var traj struct {
				Messages []minisweagent.Message `json:"messages"`
			}
			if err := json.Unmarshal(data, &traj); err == nil {
				interruptedMsgs = traj.Messages
			}
		}
	}
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
			env.recorder.SetShellAgentMetadata("", true)
			trajPath := fmt.Sprintf("%s/shell-agent/%d", env.sessionID, env.step)
			if err := env.recorder.PreWriteTurn(env.step, shellAskTrimmed, trajPath); err != nil {
				fmt.Fprintf(env.diagWriter, "Warning: pre-write work_request for shell-agent: %v\n", err)
			}
			ctxShell, cancelShell := makeTurnContext(env.rootCtx, env.cfg.timeoutPerTurn)
			ctxShell = attachTrajectory(ctxShell, env.trajectoryWriter, env.parentSpanID)
			shellCancel = cancelShell
			sasID := fmt.Sprintf("%s/shell-agent/%d", env.sessionID, env.step)
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
				ShellAgentSessionID:  sasID,
				GlobalConfigPath:     env.mctRunner.GlobalConfigPath,
				PersistTmpData:       env.mctRunner.PersistTmpData,
				SessionTempRoot:      env.mctRunner.SessionTempRoot,
				ResponseDirectives:   append([]string(nil), env.mctResponseDirectives...),
			}
			if env.mctRunner.Prompts != nil {
				shellOpts.Prompts = env.mctRunner.Prompts.MCT
			}
			var req shellagent.Request
			if env.mctRunner.ShellAgentLibrary != nil {
				tc := &TurnContext{TurnIndex: env.step, Conversation: env.recorder, ShellAgentLib: env.mctRunner.ShellAgentLibrary, HasNewInput: env.hasNewInput, ShellAgentInterruptStep: env.shellAgentInterruptStep}
				var err error
				req, err = tc.buildShellAgentRequest(shellAskTrimmed, env.sessionID, env.cfg.verbose, env.cfg.maxInputTokens)
				if err == nil {
					shellOpts.ShellAgentRequest = &req
				}
				if err != nil && env.cfg.verbose {
					fmt.Fprintln(env.diagWriter, "shell-agent library: build request:", err)
				}
			}
			go func() {
				defer close(shellDone)
				if shellCancel != nil {
					defer shellCancel()
				}
				shellResult, shellErr = promptsvc.RunShellAgentOnly(ctxShell, shellOpts, req)
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
				OnStreamHeader:     func(s string) { stream.OnChunk(s) },
				OnStreamToken:      func(s string) { stream.OnChunk(s) },
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
					fmt.Fprintf(env.diagWriter, "mct prompt error: %s. Try increasing --turn-timeout or set 0 for unlimited.\n", msg)
				} else {
					fmt.Fprintln(env.diagWriter, "mct prompt error:", merr)
				}
				stream.Abort(msg)
				*env.sessionErr = merr
				finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, merr)
				
				return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: merr}, shellAgentUsed: shellAgentUsedThisTurn}
			}
		}

		if shellDone != nil {
			<-shellDone
		}
		result.ShellAgentTrajectoryMessages = shellResult.TrajectoryMessages
		if shellErr != nil {
			env.turnInfo["shell_agent_error"] = trimTo(shellErr.Error(), 200)
		}
		if shellResult.Cancelled {
			if strings.TrimSpace(shellResult.TrajectoryPath) != "" {
				env.turnInfo["shell_agent_trajectory"] = trimTo(shellResult.TrajectoryPath, 200)
			}
			env.turnInfo["shell_agent_cancelled"] = true
			resumePath := filepath.Join(os.TempDir(), env.sessionID+"-resume.json")
			if _, err := os.Stat(resumePath); err == nil {
				env.recorder.SetShellAgentMetadata(shellResult.TrajectoryPath, true)
			}
			if env.isResumingTurn {
				env.display.Notify("[resume] shell-agent interrupted again; assertion deferred")
			}
		} else {
		if strings.TrimSpace(shellResult.TrajectoryPath) != "" {
			env.turnInfo["shell_agent_trajectory"] = trimTo(shellResult.TrajectoryPath, 200)
		}
		if env.isResumingTurn {
			resumedMsgs := result.ShellAgentTrajectoryMessages
			err := verifyShellAgentResume(interruptedMsgs, resumedMsgs)
			if err != nil {
				msg := fmt.Sprintf("[resume] ASSERTION FAILED: %v", err)
				env.display.Notify(msg)
				panic(msg)
			}
			env.display.Notify(fmt.Sprintf("[resume] assertion passed: %d messages restored, %d new messages", len(interruptedMsgs), len(resumedMsgs)-len(interruptedMsgs)))
		}
	}
		savedPath := ""
		lastAnswer := ""
		var retrieved []string
		if env.cfg.dryRun {
		} else if runNoShell {
			if result.SaveError != nil {
				fmt.Fprintln(env.diagWriter, "Warning: failed to save chat transcript:", result.SaveError)
			}
			savedPath = strings.TrimSpace(result.SavedPath)
			if savedPath == "" {
				chatDir, err := artifacts.SessionChatDirectory(env.sessionID)
				if err != nil {
					fmt.Fprintln(env.diagWriter, "Failed to resolve chat directory:", err)
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
				fmt.Fprintf(env.diagWriter, "[tag-format] %s\n", warn)
			}
		}
		stream.Complete(fullAns)
		transcriptQuestion := question
		if block := strings.TrimSpace(result.DirectiveBlock); block != "" && runNoShell {
			transcriptQuestion = transcriptQuestion + "\n\n" + block
		}
		if err := env.writeTurn(env.step, transcriptQuestion, savedPath, retrieved, fullAns, "ask"); err != nil {
			fmt.Fprintln(env.diagWriter, "Transcript write error:", err)
			*env.sessionErr = err
			finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, err)
			return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: err}, shellAgentUsed: shellAgentUsedThisTurn}
		}
		env.turnInfo["retrieved_count"] = len(retrieved)
		*env.turnsCompleted++
		
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "success", env.turnInfo, nil)
		if *env.turnsCompleted == env.cfg.maxTurns {
			return turnExecutionResult{action: turnLoopAnswerUser, shellAgentUsed: shellAgentUsedThisTurn}
		}
		return turnExecutionResult{action: turnLoopAskWorker, shellAgentUsed: shellAgentUsedThisTurn}
	}
	sasID := fmt.Sprintf("%s/shell-agent/%d", env.sessionID, env.step)
	input := runner.PromptInput{
		Prompt:             question,
		Mode:               "default",
		IncludeHistory:     true,
		OnStreamHeader:     func(s string) { stream.OnChunk(s) },
		OnStreamToken:      func(s string) { stream.OnChunk(s) },
		MaxInputTokens:     env.cfg.maxInputTokens,
		ShellAgentSessionID: sasID,
		ResponseDirectives: append([]string(nil), env.mctResponseDirectives...),
	}
	if useShellAgent && env.mctRunner.ShellAgentLibrary != nil {
		tc := &TurnContext{TurnIndex: env.step, Conversation: env.recorder, ShellAgentLib: env.mctRunner.ShellAgentLibrary, HasNewInput: env.hasNewInput, ShellAgentInterruptStep: env.shellAgentInterruptStep}
		req, err := tc.buildShellAgentRequest(question, env.sessionID, env.cfg.verbose, env.cfg.maxInputTokens)
		if err == nil {
			input.ShellAgentRequest = &req
		}
		if err != nil && env.cfg.verbose {
			fmt.Fprintln(env.diagWriter, "shell-agent library: build request:", err)
		}
	}
	env.recorder.SetShellAgentMetadata("", true)
	trajPath := fmt.Sprintf("%s/shell-agent/%d", env.sessionID, env.step)
	if err := env.recorder.PreWriteTurn(env.step, question, trajPath); err != nil {
		fmt.Fprintf(env.diagWriter, "Warning: pre-write work_request for shell-agent: %v\n", err)
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
			fmt.Fprintf(env.diagWriter, "mct prompt error: %s. Try increasing --turn-timeout or set 0 for unlimited.\n", msg)
		} else {
			fmt.Fprintln(env.diagWriter, "mct prompt error:", merr)
		}
		stream.Abort(msg)
		*env.sessionErr = merr
		finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "error", env.turnInfo, merr)
		
		return turnExecutionResult{action: turnLoopReturn, result: Result{ExitCode: 1, Err: merr}, shellAgentUsed: shellAgentUsedThisTurn}
	}

	if strings.TrimSpace(result.ShellAgentTrajectoryPath) != "" {
		env.turnInfo["shell_agent_trajectory"] = trimTo(result.ShellAgentTrajectoryPath, 200)
	}
	if result.ShellAgentCancelled {
		env.recorder.SetShellAgentMetadata(result.ShellAgentTrajectoryPath, true)
		env.turnInfo["shell_agent_cancelled"] = true
	}

	savedPath := strings.TrimSpace(result.SavedPath)
	lastAnswer := ""
	var retrieved []string
	if env.cfg.dryRun {
		lastAnswer = "[dry-run] mct would have produced a chat response here."
		retrieved = nil
	} else {
		if result.SaveError != nil {
			fmt.Fprintln(env.diagWriter, "Warning: failed to save chat transcript:", result.SaveError)
		}
		if savedPath == "" {
			chatDir, err := artifacts.SessionChatDirectory(env.sessionID)
			if err != nil {
				fmt.Fprintln(env.diagWriter, "Failed to resolve chat directory:", err)
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
			fmt.Fprintf(env.diagWriter, "[tag-format] %s\n", warn)
		}
	}
	stream.Complete(fullAns)
	transcriptQuestion := question
	if block := strings.TrimSpace(result.DirectiveBlock); block != "" {
		transcriptQuestion = transcriptQuestion + "\n\n" + block
	}
	if err := env.writeTurn(env.step, transcriptQuestion, savedPath, retrieved, fullAns, "ask"); err != nil {
		fmt.Fprintln(env.diagWriter, "Transcript write error:", err)
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
	*env.turnsCompleted++
	if strings.TrimSpace(result.ShellAgentTrajectoryPath) != "" {
		env.recorder.SetShellAgentMetadata(result.ShellAgentTrajectoryPath, false)
	}
	
	finishTurn(env.sessTelemetry, env.turn, env.turnDecision, "success", env.turnInfo, nil)
	if *env.turnsCompleted == env.cfg.maxTurns {
		return turnExecutionResult{action: turnLoopAnswerUser, shellAgentUsed: shellAgentUsedThisTurn}
	}
	return turnExecutionResult{action: turnLoopAskWorker, shellAgentUsed: shellAgentUsedThisTurn}
}


// TurnContext bundles turn-scoped data for shell-agent request construction.
type TurnContext struct {
	HasNewInput  bool // from Options; determines ResumeAttempt on shellagent.Request
	TurnIndex    int
	Conversation *conversationRecorder
	ShellAgentLib *shellagent.ShellAgentLibrary
	ShellAgentInterruptStep int
}

// buildShellAgentRequest constructs a shellagent.Request from the TurnContext
// and explicit turn parameters, encoding the ExtraInstructions / FewShot /
// BuildShellAgentMessages / RenderInstancePrompt pipeline.
func (tc *TurnContext) buildShellAgentRequest(task string, sessionID string, verbose bool, maxInputTokens int) (shellagent.Request, error) {
	extraInstr := tc.ShellAgentLib.ExtraInstructions
	if tc.ShellAgentLib.FewShotVariant == "system" && tc.TurnIndex < 3 {
		extraInstr += "\n" + shellagent.FewShotShellAgentExamples(tc.ShellAgentLib.CommandTag)
	}

	conv := tc.Conversation.Conversation()
	prebuilt, err := shellagent.BuildShellAgentMessages(
		conv.ToLLMMessages(),
		tc.ShellAgentLib.Prompts,
		extraInstr,
		tc.ShellAgentLib.AnswerTag,
		tc.ShellAgentLib.CommandTag,
	)
	if err != nil {
		return shellagent.Request{}, err
	}

	messages := make([]llm.Message, len(prebuilt))
	copy(messages, prebuilt)

	extraVars := map[string]interface{}{}
	if tc.ShellAgentLib.FewShotVariant == "instance" && tc.TurnIndex < 3 {
		extraVars["ShowFewShot"] = true
	}

	instPrompt, err := shellagent.RenderInstancePrompt(
		tc.ShellAgentLib.Prompts,
		task,
		tc.ShellAgentLib.Config,
		tc.ShellAgentLib.Env,
		extraVars,
		tc.ShellAgentLib.AnswerTag,
		tc.ShellAgentLib.CommandTag,
	)
	if err != nil {
		return shellagent.Request{}, err
	}

	messages = append(messages, llm.Message{
		Role: "user",
		Content: instPrompt,
	})

	req := shellagent.Request{
		PreconstructedMessages: messages,
		Task:                   task,
		Config:                 tc.ShellAgentLib.Config,
		Prompts:                tc.ShellAgentLib.Prompts,
		Model:                  tc.ShellAgentLib.Model,
		Env:                    tc.ShellAgentLib.Env,
		Verbose:                verbose,
		MaxInputTokens:         maxInputTokens,
		SessionID:              sessionID,
		PlannerTurn:            tc.TurnIndex,
		EnforceEarlyCommands:   tc.ShellAgentLib.EnforceEarlyCommands,
		AnswerTag:              tc.ShellAgentLib.AnswerTag,
		CommandTag:             tc.ShellAgentLib.CommandTag,
		// ResumeAttempt is true when no new CLI input was provided, false when -t or -f were given.
		ResumeAttempt: !tc.HasNewInput,
		InterruptStep: tc.ShellAgentInterruptStep,
	}

	return req, nil
}

// replayShellAgentActions replays shell-agent command steps from a saved
// resume file into the TUI display so the user sees what happened before
// the interrupt when resuming.
func replayShellAgentActions(display ui.SessionDisplay, diagWriter io.Writer, sessionID string, step int) error {
	resumePath := filepath.Join(os.TempDir(), sessionID+"-resume.json")

	// Load the resume file as a raw trajectory (avoid importing internal/run).
	data, err := os.ReadFile(resumePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		fmt.Fprintf(diagWriter, "Warning: unable to load shell-agent resume state for replay: %v\n", err)
		return nil
	}

	var traj struct {
		Messages []minisweagent.Message `json:"messages"`
	}
	if err := json.Unmarshal(data, &traj); err != nil {
		fmt.Fprintf(diagWriter, "Warning: unable to parse shell-agent resume state for replay: %v\n", err)
		return nil
	}

	cmdSteps := 0
	for _, msg := range traj.Messages {
		if msg.Role == "assistant" && strings.Contains(msg.Content, "<command") {
			cmdSteps++
		}
	}

	if cmdSteps == 0 {
		return nil
	}

	cmdIndex := 0
	for _, msg := range traj.Messages {
		switch msg.Role {
		case "assistant":
			if strings.Contains(msg.Content, "<command") {
				cmdIndex++
				line := fmt.Sprintf("[shell step %d/%d cmd %d] %s", cmdIndex, cmdSteps, cmdIndex, strings.TrimSpace(msg.Content))
				display.StreamAction(line)
			}
		case "tool", "user":
			if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
				display.StreamAction(trimmed)
			}
		}
	}

	return nil
}

func verifyShellAgentResume(interruptedMsgs, resumedMsgs []minisweagent.Message) error {
	if len(interruptedMsgs) == 0 {
		return fmt.Errorf("interrupted message history is empty")
	}
	if len(resumedMsgs) <= len(interruptedMsgs) {
		return fmt.Errorf("no new messages added after resume: interrupted=%d resumed=%d", len(interruptedMsgs), len(resumedMsgs))
	}
	for i := range interruptedMsgs {
		if interruptedMsgs[i].Role != resumedMsgs[i].Role {
			return fmt.Errorf("message prefix mismatch at index %d: expected role %q, got %q", i, interruptedMsgs[i].Role, resumedMsgs[i].Role)
		}
		if interruptedMsgs[i].Content != resumedMsgs[i].Content {
			return fmt.Errorf("message prefix mismatch at index %d: content differs", i)
		}
	}
	return nil
}
