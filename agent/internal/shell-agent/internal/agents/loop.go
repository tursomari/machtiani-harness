package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/run"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/subprocess"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"github.com/tursomari/machtiani/agent/internal/shellbridge"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

// RunLoop executes the core agent loop using the already-configured
// messages (set via SetMessages and AddMessage). It does not reset
// state or prepend system/instance prompts — those must be set by the
// caller before invoking RunLoop.
func (a *DefaultAgent) RunLoop(ctx context.Context, resuming bool) (string, string, error) {
	markerPath := minisweagent.FinalMarkerPath(a.RunConfig.SessionID)
	a.State.ExtraVars = a.loopTemplateVars(markerPath)
	if !resuming {
		a.State.stepCounter = 0
		a.State.lastNonEmptyOutput = ""
		a.RunConfig.SystemPrompt = ""
		a.RunConfig.SystemPromptCached = false
		a.State.finalizeRequested = false
		a.State.consecutiveFormatErrors = 0
	}

	for {
		if a.RunConfig.Verbose {
			log.Printf("RunLoop main loop started: sessionID=%s", a.RunConfig.SessionID)
		}
		select {
		case <-ctx.Done():
			log.Printf("RunLoop ctx.Done fired: sessionID=%s step=%d", a.RunConfig.SessionID, a.State.StepCounter())
			traj := run.FromAgent(a, "Cancelled", "", nil)
			if a.RunConfig.CheckpointDir != "" {
				run.SaveTrajectoryToPath(traj, a.RunConfig.CheckpointDir)
			}
			return "Cancelled", "", ctx.Err()
		default:
		}

		nextStep := a.State.stepCounter + 1
		if a.RunConfig.Verbose {
			log.Printf("starting step %d", nextStep)
		}
		if err := a.maybeForceFinalize(); err != nil {
			return "Error", "", err
		}
		if err := a.Step(ctx); err != nil {
			var agentErr minisweagent.AgentError
			if errors.As(err, &agentErr) {
				a.State.stepCounter++
				var formatErr *minisweagent.FormatError
				if errors.As(err, &formatErr) {
					a.State.consecutiveFormatErrors++
				} else {
					a.State.consecutiveFormatErrors = 0
					if msg := strings.TrimSpace(agentErr.Error()); msg != "" {
						if len(a.State.Messages) == 0 || strings.TrimSpace(a.State.Messages[len(a.State.Messages)-1].Content) != msg {
							a.addMessage("user", msg, nil)
						}
					}
				}
				if a.State.consecutiveFormatErrors >= maxConsecutiveFormatErrors {
					return "FormatErrorLoop", agentErr.Error(), nil
				}
				if a.RunConfig.Verbose {
					log.Printf("step %d recorded recoverable error: %s", a.State.stepCounter, agentErr.Error())
				}
				if a.RunConfig.Verbose {
					if emitErr := a.savePartialTrajectory(a.State.stepCounter); emitErr != nil {
						log.Printf("warning: failed to save partial trajectory after recoverable error: %v", emitErr)
					}
				}
				if agentErr.IsTerminating() {
					return errorTypeName(agentErr), agentErr.Error(), nil
				}
				continue
			}
			return "Error", "", err
		}
		a.State.stepCounter++
		a.State.consecutiveFormatErrors = 0
		if a.State.InterruptStep > 0 && a.State.stepCounter >= a.State.InterruptStep {
			log.Printf("RunLoop interrupt step reached: sessionID=%s step=%d", a.RunConfig.SessionID, a.State.stepCounter)
			traj := run.FromAgent(a, "Cancelled", "", nil)
			if a.RunConfig.CheckpointDir != "" {
				run.SaveTrajectoryToPath(traj, a.RunConfig.CheckpointDir)
			}
			return "Cancelled", "", nil
		}
		if a.RunConfig.Verbose {
			log.Printf("completed step %d", a.State.stepCounter)
		}
		if a.RunConfig.Verbose {
			if emitErr := a.savePartialTrajectory(a.State.stepCounter); emitErr != nil {
				log.Printf("warning: failed to save partial trajectory after successful step: %v", emitErr)
			}
		}
		a.checkpointStep()
	}
}

func (a *DefaultAgent) loopTemplateVars(markerPath string) map[string]interface{} {
	vars := map[string]interface{}{
		"final_marker_path": markerPath,
		"FinalMarkerPath":   markerPath,
		"answer_tag":        a.RunConfig.AnswerTag,
		"AnswerTag":         a.RunConfig.AnswerTag,
		"command_tag":        a.RunConfig.CommandTag,
		"CommandTag":         a.RunConfig.CommandTag,
	}

	task := strings.TrimSpace(a.RunConfig.Task)
	if task == "" && a.State.ExtraVars != nil {
		if v, ok := a.State.ExtraVars["Task"].(string); ok {
			task = strings.TrimSpace(v)
		}
		if task == "" {
			if v, ok := a.State.ExtraVars["task"].(string); ok {
				task = strings.TrimSpace(v)
			}
		}
	}
	if task != "" {
		vars["task"] = task
		vars["Task"] = task
	}

	return vars
}

// RunWithMessages executes the agent loop using the provided pre-built
// message history. This is the dedicated entry point for the in-process
// library path: it replaces the message history, then enters the standard
// tool-calling loop. No system or instance prompt is prepended — the
// caller is responsible for including them in the message array.
func (a *DefaultAgent) RunWithMessages(ctx context.Context, msgs []minisweagent.Message) (string, string, error) {
	if err := a.refreshModel(); err != nil {
		return "Error", "", err
	}
	a.State.Messages = append([]minisweagent.Message(nil), msgs...)
	status, result, err := a.RunLoop(ctx, false)
	return status, result, err
}

// ResumeWithMessages resumes a previously saved agent session using the
// provided pre-built message history. It is identical to RunWithMessages
// except it calls RunLoop with resuming=true, which preserves existing
// state (stepCounter, lastNonEmptyOutput, etc.) instead of resetting it.
func (a *DefaultAgent) ResumeWithMessages(ctx context.Context, msgs []minisweagent.Message) (string, string, error) {
	if err := a.refreshModel(); err != nil {
		return "Error", "", err
	}
	a.State.Messages = append([]minisweagent.Message(nil), msgs...)
	status, result, err := a.RunLoop(ctx, true)
	return status, result, err
}

// checkpointStep saves the current trajectory state to the configured
// checkpoint directory. When CheckpointDir is empty this is a no-op.
// Failures are logged but not returned — this is a best-effort checkpoint.
func (a *DefaultAgent) checkpointStep() {
	if a.RunConfig.CheckpointDir == "" {
		return
	}
	traj := run.FromAgent(a, "Ongoing", "", nil)
	if err := run.SaveTrajectoryToPath(traj, a.RunConfig.CheckpointDir); err != nil {
		log.Printf("checkpoint step: %v", err)
	}
}

func (a *DefaultAgent) savePartialTrajectory(stepNum int, warningPayloads ...map[string]any) error {
	scratchDir := strings.TrimSpace(os.Getenv("MACHTIANI_SESSION_TEMP_ROOT"))
	if scratchDir == "" {
		scratchDir = os.TempDir()
	}
	filename := filepath.Join(scratchDir, fmt.Sprintf("partial-trajectory-%d.json", stepNum))
	extra := map[string]interface{}{"step": stepNum}
	if len(warningPayloads) > 0 {
		extra["cache_warnings"] = warningPayloads
	}
	traj := run.FromAgent(a, "Ongoing", "", extra)
	if err := run.SaveTrajectory(traj, filename); err != nil {
		return err
	}
	log.Printf("partial trajectory saved: %s", filename)
	return nil
}

// refreshModel invokes the newModel factory (if set) and assigns
// the result to a.RunConfig.Model. This is called at the start of Run() and
// RunWithMessages() so each invocation gets a model whose
// nCalls counter starts at 0. Returns the factory error so the
// caller can surface it; the model field is unchanged on error.
func (a *DefaultAgent) refreshModel() error {
	if a.RunConfig.NewModel == nil {
		return nil
	}
	m, err := a.RunConfig.NewModel()
	if err != nil {
		return err
	}
	a.RunConfig.Model = m
	return nil
}

// Run executes the agent loop for the provided task description.
// The method generates a unique sessionID (if not already set) and uses it to ensure
// per-session isolation of marker files, preventing context pollution between sessions.
func (a *DefaultAgent) Run(ctx context.Context, task string, opts ...minisweagent.RunOption) (string, string, error) {
	if err := a.refreshModel(); err != nil {
		return "", "", err
	}
	a.RunConfig.Task = task
	markerPath := minisweagent.FinalMarkerPath(a.RunConfig.SessionID)
	a.State.ExtraVars = map[string]interface{}{
		"task":              task,
		"Task":              task,
		"final_marker_path": markerPath,
		"FinalMarkerPath":   markerPath,
		"answer_tag":        a.RunConfig.AnswerTag,
		"AnswerTag":         a.RunConfig.AnswerTag,
		"command_tag":        a.RunConfig.CommandTag,
		"CommandTag":         a.RunConfig.CommandTag,
	}

	var (
		err         error
		sysContent  string
		instContent string
	)

	a.State.Messages = a.State.Messages[:0]
	a.State.stepCounter = 0
	a.State.lastNonEmptyOutput = ""
	a.RunConfig.SystemPrompt = ""
	a.RunConfig.SystemPromptCached = false
	a.State.finalizeRequested = false

	sysContent, err = a.systemPromptContent()
	if err != nil {
		return "", "", err
	}
	a.addMessage("system", sysContent, nil)

	instContent, err = a.renderTemplate(a.State.Prompts.Planner.InstanceTemplate, nil)
	if err != nil {
		return "", "", err
	}
	a.addMessage("user", instContent, nil)

	for {
		select {
		case <-ctx.Done():
			traj := run.FromAgent(a, "Cancelled", "", nil)
			if a.RunConfig.CheckpointDir != "" {
				run.SaveTrajectoryToPath(traj, a.RunConfig.CheckpointDir)
			}
			return "Cancelled", "", ctx.Err()
		default:
		}

		nextStep := a.State.stepCounter + 1
		if a.RunConfig.Verbose {
			log.Printf("starting step %d", nextStep)
		}
		if err := a.maybeForceFinalize(); err != nil {
			return "Error", "", err
		}
		if err := a.Step(ctx); err != nil {
			var agentErr minisweagent.AgentError
			var timeoutErr *llm.ProviderTimeoutError
			if errors.As(err, &timeoutErr) {
				a.State.stepCounter++
				log.Printf("step %d provider timeout, retrying", a.State.stepCounter)
				if a.RunConfig.Verbose {
					if emitErr := a.savePartialTrajectory(a.State.stepCounter); emitErr != nil {
						log.Printf("warning: failed to save partial trajectory after provider timeout: %v", emitErr)
					}
				}
				continue
			}
			if errors.As(err, &agentErr) {
				a.State.stepCounter++
				var formatErr *minisweagent.FormatError
				if !errors.As(err, &formatErr) {
					if msg := strings.TrimSpace(agentErr.Error()); msg != "" {
						if len(a.State.Messages) == 0 || strings.TrimSpace(a.State.Messages[len(a.State.Messages)-1].Content) != msg {
							a.addMessage("user", msg, nil)
						}
					}
				}
				if a.RunConfig.Verbose {
					log.Printf("step %d recorded recoverable error: %s", a.State.stepCounter, agentErr.Error())
				}
				if a.RunConfig.Verbose {
					if emitErr := a.savePartialTrajectory(a.State.stepCounter); emitErr != nil {
						log.Printf("warning: failed to save partial trajectory after recoverable error: %v", emitErr)
					}
				}
				if agentErr.IsTerminating() {
					return errorTypeName(agentErr), agentErr.Error(), nil
				}
				continue
			}
			return "Error", "", err
		}
		a.State.stepCounter++
		if a.RunConfig.Verbose {
			log.Printf("completed step %d", a.State.stepCounter)
		}
		if a.RunConfig.Verbose {
			if emitErr := a.savePartialTrajectory(a.State.stepCounter); emitErr != nil {
				log.Printf("warning: failed to save partial trajectory after successful step: %v", emitErr)
			}
		}
	}
	return "Ongoing", "", nil
}

// Step performs a single model → environment iteration.
func (a *DefaultAgent) Step(ctx context.Context) error {
	resp, err := a.Query(ctx)
	if err != nil {
		return err
	}
	if a.RunConfig.Verbose {
		trimmed := strings.TrimSpace(resp.Content)
		if trimmed != "" {
			log.Printf("model response:\n%s", trimmed)
		} else {
			log.Printf("model response empty")
		}
	}

	// Display workspace sync progress between model query and execution
	a.displaySyncProgress()

	outcome, err := a.translateAndExecute(ctx, resp)
	if err != nil {
		return err
	}
	if a.RunConfig.Verbose {
		log.Printf("executed command: %s", outcome.Command)
		trimmedOutput := strings.TrimSpace(outcome.Result.Output)
		if trimmedOutput != "" {
			log.Printf("command output:\n%s", trimmedOutput)
		} else {
			log.Printf("command output empty")
		}
		log.Printf("return code: %d", outcome.Result.ReturnCode)
		_ = trajectory.EmitFromContext(ctx, trajectory.Event{Kind: "shell.command", Payload: map[string]any{"command": outcome.Command, "output": outcome.Result.Output, "return_code": outcome.Result.ReturnCode}})
	}
	return nil
}

// Query sends the accumulated messages to the model while enforcing budgets.
func (a *DefaultAgent) Query(ctx context.Context) (minisweagent.QueryResult, error) {
	if a.RunConfig.MaxSteps > 0 && a.RunConfig.Model.NCalls() >= a.RunConfig.MaxSteps {
		return minisweagent.QueryResult{}, &minisweagent.LimitsExceeded{Reason: "step limit exceeded"}
	}

	step := a.State.stepCounter + 1
	modelCalls := a.RunConfig.Model.NCalls() + 1
	a.State.ExtraVars["step"] = step
	a.State.ExtraVars["Step"] = step
	a.State.ExtraVars["model_calls"] = modelCalls
	a.State.ExtraVars["ModelCalls"] = modelCalls
	a.attachQueryMetadata(step, modelCalls)
	resolvedModel, hasResolved := a.resolvedModel()

	sysContent, err := a.systemPromptContent()
	if err != nil {
		return minisweagent.QueryResult{}, err
	}
	tempMessages := a.queryMessagesWithSystem(sysContent)
	if hasResolved {
		tempMessages = a.ensureCacheAnchor(sysContent, tempMessages, step, resolvedModel)
		// Verify cache prefix hash against the stored insertion-time hash.
		// Drift detection fires when prefix content changed without a rotation.
		for i, msg := range tempMessages {
			if isCacheAnchorMessage(msg.Metadata) && !cacheAnchorRetired(msg.Metadata) {
				if storedHash, _ := msg.Metadata[llm.CacheAnchorInsertionPrefixHashMetadataKey].(string); storedHash != "" {
					currentHash := llm.CachePrefixHash(
						llm.FormatMessagesForHashing(toLLMMessages(tempMessages[:i])), i,
					)
					if currentHash != storedHash {
						if a.RunConfig.Verbose {
							log.Printf("CACHE_DRIFT: anchor_index=%d stored_hash=%s current_hash=%s",
								i, storedHash[:16], currentHash[:16])
							_ = a.savePartialTrajectory(a.State.stepCounter, map[string]any{
								"type":         "cache_warning",
								"reason":       "cache_prefix_drift",
								"anchor_index": i,
								"stored_hash":  storedHash,
								"current_hash": currentHash,
							})
						}
					}
				}
				break
			}
		}
	}
	if a.RunConfig.MaxInputTokens > 0 {
		tempMessages, _ = subprocess.ApplyMessageTokenLimit(tempMessages, a.RunConfig.MaxInputTokens)
		a.syncCacheAnchorMetadata(tempMessages)
	}
	tempMessages = a.applyEstimatedTokens(tempMessages)

	queryCtx := ctx
	var usageTracker *cacheUsageTracker
	if hasResolved && cacheControlEnabled(resolvedModel) {
		usageTracker = &cacheUsageTracker{}
		queryCtx = llm.WithCacheUsageObserver(queryCtx, usageTracker.Observe)
	}

	resp, err := a.RunConfig.Model.Query(queryCtx, tempMessages)
	if err != nil {
		return minisweagent.QueryResult{}, err
	}
	if usageTracker != nil {
		usageTracker.UpdateMessages(a.State.Messages)
	}

	a.addMessage("assistant", resp.Content, resp.Extra)
	return resp, nil
}

func (a *DefaultAgent) queryMessagesWithSystem(sysContent string) []minisweagent.Message {
	systemMessage := a.messageWithEstimatedTokens(minisweagent.Message{Role: "system", Content: sysContent})
	if len(a.State.Messages) == 0 || a.State.Messages[0].Role != "system" {
		a.State.Messages = append([]minisweagent.Message{systemMessage}, a.State.Messages...)
	} else {
		a.State.Messages[0] = systemMessage
	}

	temp := make([]minisweagent.Message, 0, len(a.State.Messages)+1)
	temp = append(temp, systemMessage)
	if len(a.State.Messages) > 1 {
		temp = append(temp, a.State.Messages[1:]...)
	}
	return temp
}

func (a *DefaultAgent) translateAndExecute(ctx context.Context, resp minisweagent.QueryResult) (executionOutcome, error) {
	if finalAnswer, ok := a.extractFinalAnswer(resp.Content); ok {
		return executionOutcome{}, &minisweagent.Submitted{Result: finalAnswer}
	}
	command, corrections, err := parseXMLCommand(resp.Content, a.RunConfig.CommandTag)
	if err != nil {
		rawMsg := fmt.Sprintf("Command parsing failed: %v", err)
		a.addMessage("user", a.renderFormatError(err), nil)
		return executionOutcome{}, &minisweagent.FormatError{Message: rawMsg}
	}
	if len(corrections) > 0 && a.RunConfig.Verbose {
		for _, note := range corrections {
			log.Printf("normalized planner response: %s", note)
		}
	}

	// If finalize was requested but model emitted a command, remind it.
	if a.State.finalizeRequested && command != "" {
		if a.State.consecutiveFinalizeReminders < maxFinalizeReminders {
			a.State.consecutiveFinalizeReminders++
			a.State.consecutiveFormatErrors = 0
			reminder, _ := a.renderTemplate(finalizeReminderTemplate, nil)
			a.addMessage("user", reminder, nil)
			return executionOutcome{}, &minisweagent.FormatError{Message: "finalize requested but model emitted command, reminder sent"}
		}
	}

	translatedCmd, err := validateSearchScope(command)
	if err != nil {
		rawMsg := fmt.Sprintf("Command validation failed: %v", err)
		a.addMessage("user", a.renderFormatError(err), nil)
		return executionOutcome{}, &minisweagent.FormatError{Message: rawMsg}
	}
	command = translatedCmd

	// On the first few agent steps (when enforceEarlyCommands is
	// enabled) also reject commands with bash syntax errors so the
	// model can recover on its next attempt instead of wasting a real
	// execution slot on input that cannot possibly run. Relaxed mode
	// (`enforceEarlyCommands=false`) skips this check entirely so
	// existing behaviour is preserved.
	if a.earlyTurnEnforcementEnabled() {
		if err := validateBashSyntax(command); err != nil {
			rawMsg := fmt.Sprintf("Bash syntax validation failed: %v", err)
			a.addMessage("user", a.renderFormatError(err), nil)
			return executionOutcome{}, &minisweagent.FormatError{Message: rawMsg}
		}
	}

	a.State.commandsExecuted++
	emitShellAction(shellbridge.ActionMessage{
		Description:      strings.TrimSpace(resp.Content),
		Command:          command,
		ModelCallsUsed:   a.RunConfig.Model.NCalls(),
		StepLimit:        a.RunConfig.MaxSteps,
		RemainingSteps:   max(a.RunConfig.MaxSteps-a.RunConfig.Model.NCalls(), 0),
		CommandsExecuted: a.State.commandsExecuted,
	})

	timeout := a.execTimeout()
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, execErr := a.RunConfig.Env.Execute(execCtx, command, a.workingDirectory())

	outcome := executionOutcome{Command: command, Result: result}

	if execCtx.Err() == context.DeadlineExceeded {
		feedback := formatCommandFeedback(command, result, fmt.Sprintf("Command timed out after %s", timeout))
		a.addMessage("user", feedback, nil)
		return outcome, &minisweagent.ExecutionTimeoutError{Message: fmt.Sprintf("command timed out after %s", timeout)}
	}
	if execErr != nil {
		feedback := formatCommandFeedback(command, result, fmt.Sprintf("Command execution error: %v", execErr))
		a.addMessage("user", feedback, nil)
		return outcome, fmt.Errorf("execute command: %w", execErr)
	}

	if submitted := a.hasFinished(command, result); submitted != nil {
		return outcome, submitted
	}

	feedbackNote := ""
	if result.ReturnCode != 0 {
		feedbackNote = fmt.Sprintf("Command failed with exit code %d", result.ReturnCode)
	}
	feedback := formatCommandFeedback(command, result, feedbackNote)
	a.addMessage("user", feedback, nil)
	if trimmed := strings.TrimSpace(result.Output); trimmed != "" {
		a.State.lastNonEmptyOutput = trimmed
	}

	return outcome, nil
}

type executionOutcome struct {
	Command string
	Result  minisweagent.ExecuteResult
}

const maxConsecutiveFormatErrors = 3

func (a *DefaultAgent) addMessage(role, content string, extra map[string]interface{}) {
	msg := minisweagent.Message{Role: role, Content: content, Extra: extra}
	msg = a.messageWithEstimatedTokens(msg)
	a.State.Messages = append(a.State.Messages, msg)
}

func (a *DefaultAgent) workingDirectory() string {
	if a.RunConfig.Env != nil {
		if cwd, ok := a.RunConfig.Env.GetTemplateVars()["CWD"].(string); ok {
			return cwd
		}
	}
	return ""
}

func parseXMLCommand(content string, tag string) (string, []string, error) {
	trimmed := strings.TrimSpace(content)
	corrections := make([]string, 0)

	if trimmed == "" {
		return "", corrections, fmt.Errorf("response must include a <%s>...</%s> block", tag, tag)
	}

	openTag := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	start := strings.Index(trimmed, openTag)
	if start == -1 {
		return "", corrections, fmt.Errorf("response must include a <%s>...</%s> block", tag, tag)
	}

	if before := strings.TrimSpace(trimmed[:start]); before != "" {
		corrections = append(corrections, "discarded leading commentary before "+openTag+" tag")
	}

	innerStart := start + len(openTag)
	remaining := trimmed[innerStart:]

	closeStart := strings.Index(remaining, closeTag)
	if closeStart == -1 {
		return "", corrections, fmt.Errorf("response must include a closing %s tag", closeTag)
	}

	inner := strings.TrimSpace(remaining[:closeStart])
	after := strings.TrimSpace(remaining[closeStart+len(closeTag):])
	if after != "" {
		corrections = append(corrections, "discarded trailing commentary after "+closeTag+" tag")
	}

	if inner == "" {
		return "", corrections, fmt.Errorf("command block must not be empty")
	}

	command := strings.TrimSpace(inner)
	if command == "" {
		return "", corrections, fmt.Errorf("command block must not be empty")
	}
	return command, corrections, nil
}

func (a *DefaultAgent) attachQueryMetadata(step, modelCalls int) {
	if len(a.State.Messages) == 0 {
		return
	}
	msg := &a.State.Messages[len(a.State.Messages)-1]
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]any)
	}
	msg.Metadata["step"] = step
	msg.Metadata["model_calls"] = modelCalls
}

func (a *DefaultAgent) execTimeout() time.Duration {
	envCfg, ok := a.RunConfig.Env.Config().(*minisweagent.EnvironmentConfig)
	if !ok || envCfg.CommandTimeout <= 0 {
		return 30 * time.Second
	}
	return time.Duration(envCfg.CommandTimeout) * time.Second
}

func formatCommandFeedback(command string, result minisweagent.ExecuteResult, note string) string {
	builder := &strings.Builder{}
	builder.WriteString(fmt.Sprintf("Command executed: %s\n", command))
	builder.WriteString(fmt.Sprintf("Exit code: %d\n", result.ReturnCode))
	if note = strings.TrimSpace(note); note != "" {
		builder.WriteString(note)
		builder.WriteString("\n")
	}
	if output := strings.TrimSpace(result.Output); output != "" {
		builder.WriteString("Output:\n")
		builder.WriteString(output)
	}
	return strings.TrimRight(builder.String(), "\n")
}

func emitShellAction(msg shellbridge.ActionMessage) {
	msg.Description = strings.TrimSpace(msg.Description)
	msg.Command = strings.TrimSpace(msg.Command)
	if msg.Description == "" && msg.Command == "" {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(os.Stdout, "%s%s\n", shellbridge.ActionPrefix, data); err != nil {
		log.Printf("failed to emit shell action: %v", err)
	}
}

func (a *DefaultAgent) TrajectoryExtras() map[string]interface{} {
	if a == nil || a.RunConfig == nil || a.RunConfig.Model == nil {
		return nil
	}
	remaining := a.RunConfig.MaxSteps - a.RunConfig.Model.NCalls()
	if remaining < 0 {
		remaining = 0
	}
	return map[string]interface{}{
		"max_steps":        a.RunConfig.MaxSteps,
		"model_calls_used":  a.RunConfig.Model.NCalls(),
		"remaining_steps":   remaining,
		"commands_executed": a.State.commandsExecuted,
	}
}

func (a *DefaultAgent) displaySyncProgress() {
	if !a.RunConfig.Verbose {
		return
	}

	progress := a.RunConfig.Env.GetSyncProgress()
	status := a.RunConfig.Env.GetSyncStatus()

	// Only display if progress is less than complete or if there's a status message
	if progress >= 1.0 && status == "" {
		return
	}

	// Format progress percentage
	progressPct := progress * 100.0

	// Log the sync progress
	if status != "" {
		log.Printf("workspace sync (%.1f%% complete): %s", progressPct, status)
	} else {
		log.Printf("workspace sync (%.1f%% complete)", progressPct)
	}
}

func errorTypeName(err minisweagent.AgentError) string {
	t := reflect.TypeOf(err)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}
