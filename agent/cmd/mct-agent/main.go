package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/mct/llm"
	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	promptsvc "github.com/tursomari/machtiani/agent/internal/mct/prompt"
	"github.com/tursomari/machtiani/agent/internal/parser"
	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/runner"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
	Dirty   = "unknown"
)

type config struct {
	maxSteps                int
	orchModel               string
	patcherModel            string
	fileDiscoveryModel      string
	agentModel              string
	timeoutPerTurn          int
	dryRun                  bool
	verbose                 bool
	finalFile               string
	transcriptFile          string
	fileDiscoveryTrajectory string
	fileDiscoveryOutputDir  string
	maxInputTokens          int
	trajectoryFile          string
	noTrajectory            bool
	trajectoryVerboseLLM    bool
	trajectoryStreamTokens  bool
	trajectoryExcerpt       int
	trajectoryOmitRepoRoot  bool
	// Patch application behavior
	patchNoApply bool
	patch        bool
	// Normalized OpenAI flags
	openAIAPIKey  string
	openAIBaseURL string
	openAIModel   string
}

type multiString []string

func (m *multiString) String() string {
	return strings.Join(*m, ",")
}

func (m *multiString) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func printVersion() {
	fmt.Printf("mct-agent %s\ncommit: %s\nbuilt: %s\ndirty: %s\n", Version, Commit, BuiltAt, Dirty)
}

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "--version", "-version":
			printVersion()
			return 0
		}
	}

	var cfg config
	fs := flag.NewFlagSet("mct-agent", flag.ExitOnError)
	fs.IntVar(&cfg.maxSteps, "max-steps", 4, "maximum number of turns before finalizing")
	fs.StringVar(&cfg.orchModel, "model", "", "Model alias defined in .machtiani/config.toml (alias for --orch-model)")
	fs.StringVar(&cfg.orchModel, "orch-model", "", "Model alias for orchestration/planner steps (default: config or env)")
	fs.StringVar(&cfg.patcherModel, "patcher-model", "", "Model alias for patch planning/execution (default: orchestration model)")
	fs.StringVar(&cfg.fileDiscoveryModel, "file-discovery-model", "", "Model alias for file discovery runs (default: orchestration model)")
	fs.StringVar(&cfg.agentModel, "agent-model", "", "Legacy planner model alias (deprecated; use --orch-model)")
	fs.IntVar(&cfg.timeoutPerTurn, "timeout-per-turn", 120, "per-turn timeout in seconds (set 0 for no timeout)")
	fs.BoolVar(&cfg.dryRun, "dry-run", false, "print intended mct calls; don’t execute")
	fs.BoolVar(&cfg.verbose, "verbose", false, "verbose agent logging")
	fs.StringVar(&cfg.finalFile, "final-file", "", "path to write final answer-only artifact (default: .machtiani/sessions/<sessionID>/chat/agent-final.txt)")
	fs.StringVar(&cfg.transcriptFile, "transcript-file", "", "path to write transcript file (default: .machtiani/sessions/<sessionID>/chat/agent.md)")
	fs.StringVar(&cfg.fileDiscoveryTrajectory, "file-discovery-trajectory", "", "path to write file-discovery trajectory JSONL (default: auto-named under session artifacts)")
	fs.StringVar(&cfg.fileDiscoveryOutputDir, "file-discovery-output-dir", "", "directory for file-discovery artifacts (default: .machtiani/sessions/<sessionID>/artifacts)")
	fs.IntVar(&cfg.maxInputTokens, "max-input-tokens", 0, "maximum number of tokens allowed in constructed prompts (0 disables truncation)")
	fs.StringVar(&cfg.trajectoryFile, "trajectory-file", "", "override path for unified trajectory JSONL (default: session-scoped path)")
	fs.BoolVar(&cfg.noTrajectory, "no-trajectory", false, "disable unified trajectory JSONL emission")
	fs.BoolVar(&cfg.trajectoryVerboseLLM, "trajectory-verbose-llm", false, "include expanded LLM details in the trajectory stream")
	fs.BoolVar(&cfg.trajectoryStreamTokens, "trajectory-stream-tokens", false, "record LLM token streaming events in the trajectory (disabled by default)")
	fs.IntVar(&cfg.trajectoryExcerpt, "trajectory-excerpt", 512, "excerpt length (in characters) for prompts/responses captured in the trajectory")
	fs.BoolVar(&cfg.trajectoryOmitRepoRoot, "trajectory-omit-repo-root", false, "omit repo_root from trajectory events")
	fs.BoolVar(&cfg.patchNoApply, "patch-no-apply", false, "skip applying generated patches to the worktree (default: apply)\n")
	fs.BoolVar(&cfg.patch, "patch", false, "enable patch planning (disabled by default)")
	// Normalized OpenAI flags
	fs.StringVar(&cfg.openAIAPIKey, "openai-api-key", "", "OpenAI-compatible API key (overrides env, deprecated)")
	fs.StringVar(&cfg.openAIBaseURL, "openai-base-url", "", "OpenAI-compatible base URL (overrides env, deprecated)")
	fs.StringVar(&cfg.openAIModel, "openai-model", "", "Direct upstream model name (deprecated; prefer --model)")
	var paramFlags multiString
	var paramJSON multiString
	fs.Var(&paramFlags, "param", "Additional request parameter key=value (repeatable)")
	fs.Var(&paramJSON, "param-json", "Merge JSON object of additional parameters (repeatable)")

	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent run \"<issue or question>\" [flags]")
		fs.Usage = func() {}
		return 2
	}

	// Parse flags after the subcommand
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	args := fs.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Error: missing issue/question. Example: mct-agent run \"Explain X...\"")
		return 2
	}
	goal := strings.TrimSpace(args[0])
	if goal == "" {
		fmt.Fprintln(os.Stderr, "Error: empty issue/question provided")
		return 2
	}
	applyTrajectoryEnvOverrides(&cfg)

	sessionStatus := "error"
	var sessionErr error
	turnsCompleted := 0

	// Setup session ID for correlation
	sessionID := runner.GenerateSessionID()
	trajectoryWriter, repoRoot, trajErr := newTrajectoryWriter(cfg, sessionID)
	if trajErr != nil {
		fmt.Fprintln(os.Stderr, "Trajectory setup error:", trajErr)
		return 1
	}
	if trajectoryWriter != nil {
		fmt.Fprintln(os.Stderr, "[trajectory] unified stream:", trajectoryWriter.Config().Path)
	}
	sessTelemetry := newSessionTelemetry(trajectoryWriter, sessionID, goal, cfg, repoRoot)
	defer func() {
		if sessTelemetry != nil {
			sessTelemetry.Finish(sessionStatus, turnsCompleted, sessionErr)
		} else if trajectoryWriter != nil {
			_ = trajectoryWriter.Close()
		}
	}()

	// Prepare transcript file scoped to this session
	tr, err := transcript.NewWithPath(cfg.transcriptFile, sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error preparing transcript:", err)
		return 1
	}
	defer tr.Close()
	tr.SetTrajectory(trajectoryWriter)

	if cfg.verbose {
		fmt.Fprintln(os.Stderr, "mct-agent starting; transcript:", tr.Path())
	}
	if cfg.verbose {
		fmt.Fprintln(os.Stderr, "Session:", sessionID)
	}

	trajectoryPath, err := resolveFileDiscoveryTrajectory(cfg, sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "File-discovery setup error:", err)
		return 1
	}
	if cfg.verbose && strings.TrimSpace(trajectoryPath) != "" {
		fmt.Fprintln(os.Stderr, "File discovery trajectory:", trajectoryPath)
	}

	paramPairs := append([]string(nil), paramFlags...)
	paramJSONVals := append([]string(nil), paramJSON...)

	models, err := resolveModelRuntimes(cfg, paramPairs, paramJSONVals)
	if err != nil {
		if miss, ok := err.(*missingConfigError); ok {
			fmt.Fprintln(os.Stderr, "Missing model config: set:")
			for _, item := range miss.items {
				fmt.Fprintln(os.Stderr, " - ", item)
			}
			return 2
		}
		fmt.Fprintln(os.Stderr, "Model resolution error:", err)
		return 1
	}
	orchPromptOpts := promptOptions(
		describeModel("orchestrator", models.orchestrator),
		describeModel("file discovery", models.fileDiscovery),
	)
	patcherPromptOpts := promptOptions(
		describeModel("patcher", models.patcher),
	)

	// Resolve mct runner
	mctRunner := runner.Runner{
		Verbose:                 cfg.verbose,
		DryRun:                  cfg.dryRun,
		Runtime:                 models.orchestrator.toPromptRuntime(),
		FileDiscoveryRuntime:    models.fileDiscovery.toPromptRuntime(),
		FileDiscoveryTrajectory: trajectoryPath,
	}
	if err := mctRunner.Resolve(); err != nil {
		fmt.Fprintln(os.Stderr, "mct resolution error:", err)
		fmt.Fprintln(os.Stderr, "Hint: install 'mct' into PATH (see mct/README.md).")
		return 1
	}

	// Resolve patcher runner when patching is enabled
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
			// Non-fatal until we actually need to patch; we will re-resolve errors then.
		}
		pRunner = pr
	}

	// Planner/Finalizer client uses normalized OPENAI_* values
	pl := planner.NewClient(planner.ClientConfig{
		Model:             models.orchestrator.resolved,
		Extras:            models.orchestrator.extras,
		Alias:             models.orchestrator.alias,
		Verbose:           cfg.verbose,
		DryRun:            cfg.dryRun,
		RequestTimeoutSec: cfg.timeoutPerTurn,
		PatchEnabled:      cfg.patch,
	})

	// Running state (kept only for transcript writing)
	lastAnswer := ""
	retrieved := []string{}

	if err := tr.WriteHeader(goal, sessionID, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "Error writing transcript header:", err)
		return 1
	}

	display := ui.NewTerminalDisplay(os.Stdout)
	display.StartSession(goal)
	sessionClosed := false
	defer func() {
		if !sessionClosed {
			display.EndSession()
		}
	}()

	// First turn: use the original prompt directly (no planner)
	{
		step := 1
		stream := display.BeginPrompt(goal, orchPromptOpts)
		input := runner.PromptInput{
			Prompt:         goal,
			Mode:           "default",
			IncludeHistory: true,
			OnStreamHeader: stream.OnChunk,
			OnStreamToken:  stream.OnChunk,
			MaxInputTokens: cfg.maxInputTokens,
		}

		var turn *turnTelemetry
		if sessTelemetry != nil {
			turn = sessTelemetry.StartTurn(step, cfg.maxSteps)
		}
		ctx2, cancel2 := makeTurnContext(cfg.timeoutPerTurn)
		parentSpanID := ""
		if turn != nil {
			parentSpanID = turn.span.ID
		} else if sessTelemetry != nil {
			parentSpanID = sessTelemetry.span.ID
		}
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
			if sessTelemetry != nil {
				info := map[string]any{"prompt_mode": input.Mode}
				sessTelemetry.EndTurn(turn, "initial", "error", info, merr)
			}
			turnsCompleted = step
			return 1
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
					sessionErr = err
					turnsCompleted = step
					return 1
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
		if err := tr.WriteTurn(step, goal, savedPath, retrieved, fullAns, "initial"); err != nil {
			fmt.Fprintln(os.Stderr, "Transcript write error:", err)
			sessionErr = err
			if sessTelemetry != nil {
				info := map[string]any{"prompt_mode": input.Mode}
				sessTelemetry.EndTurn(turn, "initial", "error", info, err)
			}
			turnsCompleted = step
			return 1
		}
		if sessTelemetry != nil {
			info := map[string]any{
				"prompt_mode":     input.Mode,
				"retrieved_count": len(retrieved),
			}
			if savedPath != "" {
				info["saved_chat_path"] = savedPath
			}
			if trajectoryWriter != nil && strings.TrimSpace(fullAns) != "" {
				excerpt := trajectory.MakeTextExcerpt(fullAns, trajectoryWriter.ExcerptLen())
				info = trajectory.MergeExcerptWithPrefix(info, excerpt, "answer")
			}
			sessTelemetry.EndTurn(turn, "initial", "success", info, nil)
		}
		turnsCompleted = 1
	}

	// If only one step is allowed, finalize immediately using transcript only
	if cfg.maxSteps == 1 {
		ctx, cancelF := makeTurnContext(cfg.timeoutPerTurn)
		trFull := tr.Content()
		parentSpanID := ""
		if sessTelemetry != nil {
			parentSpanID = sessTelemetry.span.ID
		}
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
			return 1
		}
		if err := tr.WriteFinal(answer, 1, false); err != nil {
			fmt.Fprintln(os.Stderr, "Transcript write error:", err)
			sessionErr = err
			return 1
		}
		if err := writeFinalAnswer(sessionID, answer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
			fmt.Fprintln(os.Stderr, "Final file write error:", err)
			sessionErr = err
			return 1
		}
		presentFinalAnswer(display, answer)
		display.EndSession()
		sessionClosed = true
		sessionStatus = "success"
		return 0
	}

	// Subsequent turns loop
	for step := 2; step <= cfg.maxSteps; step++ {
		var turn *turnTelemetry
		if sessTelemetry != nil {
			turn = sessTelemetry.StartTurn(step, cfg.maxSteps)
		}
		turnInfo := map[string]any{}
		turnDecision := "pending"
		turnEnded := false
		finishTurn := func(decision, status string, info map[string]any, err error) {
			if turnEnded {
				return
			}
			if info == nil {
				info = map[string]any{}
			}
			if sessTelemetry != nil {
				sessTelemetry.EndTurn(turn, decision, status, info, err)
			}
			turnEnded = true
		}
		parentSpanID := ""
		if turn != nil {
			parentSpanID = turn.span.ID
		} else if sessTelemetry != nil {
			parentSpanID = sessTelemetry.span.ID
		}

		// Decide next action using transcript only
		ctx, cancel := makeTurnContext(cfg.timeoutPerTurn)
		trFull := tr.Content()
		ctx = attachTrajectory(ctx, trajectoryWriter, parentSpanID)
		decision, question, perr := pl.Plan(ctx, goal, trFull, step, cfg.maxSteps)
		cancel()
		if perr != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				fmt.Fprintf(os.Stderr, "Planner error: timed out after %ds. Increase --timeout-per-turn or set 0 for unlimited.\n", cfg.timeoutPerTurn)
				finishTurn("planner", "error", turnInfo, perr)
				return 1
			}
			// Graceful fallback: if the planner hit a transient network error, finalize instead of aborting.
			perrStr := strings.ToLower(perr.Error())

			// Continue on parsing errors to give the model a chance to recover
			if strings.Contains(perrStr, "unable to parse decision from model output") {
				fmt.Fprintln(os.Stderr, "Planner error:", perr)
				turnDecision = "planner"
				finishTurn(turnDecision, "error", turnInfo, perr)
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
					finishTurn("finalize", "error", turnInfo, ferr)
					turnsCompleted = step
					return 1
				}
				if err := tr.WriteFinal(answer, step, true); err != nil {
					fmt.Fprintln(os.Stderr, "Transcript write error:", err)
					sessionErr = err
					finishTurn("finalize", "error", turnInfo, err)
					turnsCompleted = step
					return 1
				}
				if err := writeFinalAnswer(sessionID, answer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
					fmt.Fprintln(os.Stderr, "Final file write error:", err)
					sessionErr = err
					finishTurn("finalize", "error", turnInfo, err)
					turnsCompleted = step
					return 1
				}
				presentFinalAnswer(display, answer)
				display.EndSession()
				sessionClosed = true
				turnDecision = "finalize"
				turnInfo["finalized"] = true
				finishTurn(turnDecision, "success", turnInfo, nil)
				turnsCompleted = step
				sessionStatus = "success"
				return 0
			}
			fmt.Fprintln(os.Stderr, "Planner error:", perr)
			sessionErr = perr
			finishTurn("planner", "error", turnInfo, perr)
			turnsCompleted = step
			return 1
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

		if decision == planner.DecisionFinalize || step == cfg.maxSteps {
			// Compose final answer using transcript only
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
				finishTurn("finalize", "error", turnInfo, ferr)
				turnsCompleted = step
				return 1
			}
			if err := tr.WriteFinal(answer, step, step == cfg.maxSteps && decision != planner.DecisionFinalize); err != nil {
				fmt.Fprintln(os.Stderr, "Transcript write error:", err)
				sessionErr = err
				finishTurn("finalize", "error", turnInfo, err)
				turnsCompleted = step
				return 1
			}
			if err := writeFinalAnswer(sessionID, answer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
				fmt.Fprintln(os.Stderr, "Final file write error:", err)
				sessionErr = err
				finishTurn("finalize", "error", turnInfo, err)
				turnsCompleted = step
				return 1
			}
			presentFinalAnswer(display, answer)
			display.EndSession()
			sessionClosed = true
			turnDecision = "finalize"
			turnInfo["finalized"] = true
			finishTurn(turnDecision, "success", turnInfo, nil)
			turnsCompleted = step
			sessionStatus = "success"
			return 0
		}

		switch decision {
		case planner.DecisionAsk:
			if question == "" {
				errEmpty := errors.New("planner returned empty question")
				fmt.Fprintln(os.Stderr, "Planner returned empty question for 'ask' decision")
				sessionErr = errEmpty
				finishTurn(turnDecision, "error", turnInfo, errEmpty)
				turnsCompleted = step
				return 1
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
				finishTurn(turnDecision, "error", turnInfo, merr)
				turnsCompleted = step
				return 1
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
						return 1
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
				finishTurn(turnDecision, "error", turnInfo, err)
				return 1
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
			if step == cfg.maxSteps {
				finishTurn(turnDecision, "success", turnInfo, nil)
				turnsCompleted = step
				goto FINALIZE
			}
			turnsCompleted = step
			finishTurn(turnDecision, "success", turnInfo, nil)
			continue

		case planner.DecisionPatch:
			payload := question
			stream := display.BeginPrompt("Patcher: create patch", patcherPromptOpts)
			if cfg.verbose {
				fmt.Fprintln(os.Stderr, "[patcher] planner payload (raw):", trimTo(strings.TrimSpace(payload), 1200))
			}
			patchTurnLabel := "Patcher: create patch"
			patchOutcome := func(status string, err error, extra map[string]any) {
				if extra == nil {
					extra = map[string]any{}
				}
				if patchTurnLabel != "" {
					extra["patch_turn_label"] = patchTurnLabel
				}
				for k, v := range extra {
					turnInfo[k] = v
				}
				turnsCompleted = step
				finishTurn(turnDecision, status, turnInfo, err)
			}
			recordPatchError := func(kind string, err error, extra map[string]any) {
				if extra == nil {
					extra = map[string]any{}
				}
				extra["patch_error_kind"] = kind
				if err != nil {
					extra["patch_error"] = err.Error()
				}
				patchOutcome("error", err, extra)
			}
			jsonBytes, jerr := parser.ExtractPatchJSONPayload(payload)
			if jerr != nil {
				stream.Abort("invalid patch payload")
				_ = tr.WriteTurn(step, "Patcher: invalid input JSON", "", nil, "Error extracting JSON: "+jerr.Error(), "patch-error")
				recordPatchError("invalid_patch_payload", jerr, nil)
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
				continue
			}
			if err := pRunner.Resolve(); err != nil {
				stream.Abort("patcher resolve failed")
				_ = tr.WriteTurn(step, patchTurnLabel, "", nil, "Error: "+err.Error(), "patch-error")
				recordPatchError("patch_runner_resolve", err, nil)
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
						return 1
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
					if step == cfg.maxSteps {
						goto FINALIZE
					}
					continue
				case errors.As(applyErr, &valErr):
					stream.Abort("patch validation error")
					_ = tr.WriteTurn(step, patchTurnLabel, "", nil, valErr.Error(), "patch-error")
					recordPatchError("patch_validation_error", valErr, nil)
					continue
				case errors.As(applyErr, &genErr):
					stream.Abort("patch generation error")
					_ = tr.WriteTurn(step, patchTurnLabel, "", nil, genErr.Error(), "patch-error")
					recordPatchError("patch_generation_error", genErr, nil)
					continue
				default:
					stream.Abort("patcher execution error")
					_ = tr.WriteTurn(step, patchTurnLabel, "", nil, applyErr.Error(), "patch-error")
					recordPatchError("patch_apply_error", applyErr, nil)
					continue
				}
			}
			if result == nil {
				errEmpty := errors.New("patcher returned empty result")
				stream.Abort("patcher returned no result")
				_ = tr.WriteTurn(step, patchTurnLabel, "", nil, errEmpty.Error(), "patch-error")
				recordPatchError("patch_empty_result", errEmpty, nil)
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
						fmt.Fprintln(os.Stderr, "[git] apply error:", aerr)
					}
					stream.Abort("git apply failed")
					if err := tr.WriteTurn(step, patchTurnLabel, "", nil, "git apply failed. See diagnostics below.", "patch-error"); err != nil {
						fmt.Fprintln(os.Stderr, "Transcript write error:", err)
						sessionErr = err
						recordPatchError("transcript_write_error", err, map[string]any{"patch_error_context": "git_apply_failure"})
						return 1
					}
					applyStderr := extractApplyStderr(aerr.Error())
					rec := transcript.PatchValidationRecord{
						Operation:    "git apply",
						Status:       "failed",
						PatchPath:    result.PatchPath,
						PatchPreview: readPatchPreview(result.PatchPath, 1000),
						PatchInput:   trimTo(string(jsonBytes), 1000),
						Stdout:       trimTo(string(resJSON), 800),
						Stderr:       trimTo(applyStderr, 800),
						Error:        aerr.Error(),
						Messages:     convertPatchMessages(mctpatcher.ParseGitApplyMessages(applyStderr)),
					}
					_ = tr.WritePatchValidation(step, rec)
					extra := map[string]any{
						"patch_apply_operation":  "git apply",
						"patch_apply_stderr_len": len(applyStderr),
					}
					recordPatchError("git_apply_failed", aerr, extra)
					if step == cfg.maxSteps {
						goto FINALIZE
					}
					continue
				}
				ans = ans + "applied: yes\n"
			} else if cfg.dryRun {
				if cfg.verbose {
					fmt.Fprintln(os.Stderr, "[git] apply (dry-run) skipping")
				}
				ans = ans + "applied: (dry-run)\n"
			} else {
				ans = ans + "applied: skipped (use without --patch-no-apply)\n"
			}
			stream.Complete(ans)
			if err := tr.WriteTurn(step, patchTurnLabel, "", nil, ans, "patch"); err != nil {
				fmt.Fprintln(os.Stderr, "Transcript write error:", err)
				sessionErr = err
				recordPatchError("transcript_write_error", err, map[string]any{"patch_error_context": "patch_success"})
				return 1
			}
			if step == cfg.maxSteps {
				extra := map[string]any{}
				if trajectoryWriter != nil && strings.TrimSpace(ans) != "" {
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
				goto FINALIZE
			}
			extra := map[string]any{}
			if trajectoryWriter != nil && strings.TrimSpace(ans) != "" {
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

		case planner.DecisionFinalize:
			// Break to finalize path immediately.
			goto FINALIZE
		default:
			// Unknown decision: treat as finalize to be safe
			goto FINALIZE
		}
	}

FINALIZE:
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

		// Compose final answer using transcript only
		{
			// Determine how many turns occurred by counting headings
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
				return 1
			}
			if err := tr.WriteFinal(answer, turns, turns >= cfg.maxSteps); err != nil {
				fmt.Fprintln(os.Stderr, "Transcript write error:", err)
				sessionErr = err
				turnsCompleted = turns
				return 1
			}
			if err := writeFinalAnswer(sessionID, answer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
				fmt.Fprintln(os.Stderr, "Final file write error:", err)
				sessionErr = err
				turnsCompleted = turns
				return 1
			}
			presentFinalAnswer(display, answer)
			display.EndSession()
			sessionClosed = true
			sessionStatus = "success"
			turnsCompleted = turns
		}
		return 0
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func trimTo(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

func convertPatchMessages(msgs []mctpatcher.PatchValidationMessage) []transcript.PatchValidationMessage {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]transcript.PatchValidationMessage, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, transcript.PatchValidationMessage{
			Severity: msg.Severity,
			Path:     msg.Path,
			Line:     msg.Line,
			Message:  msg.Message,
			Raw:      msg.Raw,
		})
	}
	return out
}

func extractApplyStderr(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	parts := strings.SplitN(msg, "\n", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func readPatchPreview(path string, max int) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return trimTo(string(data), max)
}

type sessionTelemetry struct {
	writer    *trajectory.Writer
	sessionID string
	span      trajectory.Span
	started   time.Time
	repoRoot  string
	goal      string
	cfg       config
}

type turnTelemetry struct {
	span     trajectory.Span
	step     int
	maxSteps int
	started  time.Time
}

func newSessionTelemetry(writer *trajectory.Writer, sessionID, goal string, cfg config, repoRoot string) *sessionTelemetry {
	if writer == nil {
		return nil
	}
	st := &sessionTelemetry{
		writer:    writer,
		sessionID: sessionID,
		span:      writer.StartSpan(""),
		started:   time.Now(),
		repoRoot:  repoRoot,
		goal:      goal,
		cfg:       cfg,
	}
	payload := map[string]any{
		"event_version": 1,
		"config_summary": map[string]any{
			"max_steps":        cfg.maxSteps,
			"timeout_per_turn": cfg.timeoutPerTurn,
			"dry_run":          cfg.dryRun,
			"patch_enabled":    cfg.patch,
			"patch_no_apply":   cfg.patchNoApply,
		},
		"versions": map[string]any{
			"agent":    Version,
			"commit":   Commit,
			"built_at": BuiltAt,
			"dirty":    Dirty,
		},
	}
	if repoRoot != "" && !cfg.trajectoryOmitRepoRoot {
		payload["repo_root"] = repoRoot
	}
	payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(goal, writer.ExcerptLen()), "goal")
	evt := trajectory.Event{Kind: "agent.session.start", SpanID: st.span.ID, Payload: payload}
	if err := writer.Emit(context.Background(), evt); err != nil {
		fmt.Fprintf(os.Stderr, "[trajectory] session start emit error: %v\n", err)
	}
	return st
}

func (st *sessionTelemetry) StartTurn(step, maxSteps int) *turnTelemetry {
	if st == nil || st.writer == nil {
		return nil
	}
	tt := &turnTelemetry{
		span:     st.writer.StartSpan(st.span.ID),
		step:     step,
		maxSteps: maxSteps,
		started:  time.Now(),
	}
	payload := map[string]any{
		"event_version": 1,
		"step":          step,
		"max_steps":     maxSteps,
	}
	if step == 1 {
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(st.goal, st.writer.ExcerptLen()), "goal")
	}
	evt := trajectory.Event{Kind: "agent.turn.start", SpanID: tt.span.ID, ParentSpanID: st.span.ID, Payload: payload}
	if err := st.writer.Emit(context.Background(), evt); err != nil {
		fmt.Fprintf(os.Stderr, "[trajectory] turn start emit error: %v\n", err)
	}
	return tt
}

func (st *sessionTelemetry) EndTurn(tt *turnTelemetry, decision string, status string, info map[string]any, err error) {
	if st == nil || st.writer == nil || tt == nil {
		return
	}
	payload := map[string]any{
		"event_version": 1,
		"step":          tt.step,
		"max_steps":     tt.maxSteps,
		"decision":      decision,
		"status":        status,
		"duration_ms":   time.Since(tt.started).Milliseconds(),
	}
	for k, v := range info {
		payload[k] = v
	}
	event := trajectory.Event{
		Kind:         "agent.turn.end",
		SpanID:       tt.span.ID,
		ParentSpanID: st.span.ID,
		Payload:      payload,
	}
	if err != nil {
		event.Level = "error"
		event.Err = &trajectory.ErrorInfo{Message: err.Error(), Category: "unknown"}
	}
	if emitErr := st.writer.Emit(context.Background(), event); emitErr != nil {
		fmt.Fprintf(os.Stderr, "[trajectory] turn end emit error: %v\n", emitErr)
	}
}

func (st *sessionTelemetry) Finish(status string, turns int, err error) {
	if st == nil || st.writer == nil {
		return
	}
	payload := map[string]any{
		"event_version": 1,
		"status":        status,
		"turns":         turns,
		"duration_ms":   time.Since(st.started).Milliseconds(),
	}
	event := trajectory.Event{
		Kind:    "agent.session.end",
		SpanID:  st.span.ID,
		Payload: payload,
	}
	if err != nil {
		event.Level = "error"
		event.Err = &trajectory.ErrorInfo{Message: err.Error(), Category: "unknown"}
	}
	if emitErr := st.writer.Emit(context.Background(), event); emitErr != nil {
		fmt.Fprintf(os.Stderr, "[trajectory] session end emit error: %v\n", emitErr)
	}
	_ = st.writer.Close()
}

func applyTrajectoryEnvOverrides(cfg *config) {
	if cfg == nil {
		return
	}
	if v := strings.TrimSpace(os.Getenv("MACHTIANI_TRAJECTORY_FILE")); v != "" {
		cfg.trajectoryFile = v
	}
	if b, ok := envBool("MACHTIANI_NO_TRAJECTORY"); ok && b {
		cfg.noTrajectory = true
	}
	if b, ok := envBool("MACHTIANI_TRAJECTORY_VERBOSE_LLM"); ok && b {
		cfg.trajectoryVerboseLLM = true
	}
	if b, ok := envBool("MACHTIANI_TRAJECTORY_STREAM_TOKENS"); ok && b {
		cfg.trajectoryStreamTokens = true
	}
	if n, ok := envInt("MACHTIANI_TRAJECTORY_EXCERPT"); ok {
		cfg.trajectoryExcerpt = n
	}
	if b, ok := envBool("MACHTIANI_TRAJECTORY_OMIT_REPO_ROOT"); ok && b {
		cfg.trajectoryOmitRepoRoot = true
	}
}

func envBool(name string) (bool, bool) {
	val, ok := os.LookupEnv(name)
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, true
	}
}

func envInt(name string) (int, bool) {
	val, ok := os.LookupEnv(name)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil {
		return 0, false
	}
	return n, true
}

func newTrajectoryWriter(cfg config, sessionID string) (*trajectory.Writer, string, error) {
	if cfg.noTrajectory {
		return nil, "", nil
	}
	path := strings.TrimSpace(cfg.trajectoryFile)
	if path == "" {
		p, err := artifacts.SessionTrajectoryFile(sessionID, "agent")
		if err != nil {
			return nil, "", err
		}
		path = p
	}
	w, err := trajectory.New(trajectory.Config{
		SessionID:    sessionID,
		Path:         path,
		Component:    "agent",
		ExcerptLen:   cfg.trajectoryExcerpt,
		StreamTokens: cfg.trajectoryStreamTokens,
		VerboseLLM:   cfg.trajectoryVerboseLLM,
		OmitRepoRoot: cfg.trajectoryOmitRepoRoot,
	})
	if err != nil {
		return nil, "", fmt.Errorf("create trajectory writer at %s: %w", path, err)
	}
	repoRoot := ""
	if !cfg.trajectoryOmitRepoRoot {
		if isLocal, _ := artifacts.IsLocalContext(); isLocal {
			sessionDir, err := artifacts.SessionDirectory(sessionID)
			if err == nil {
				repoRoot = filepath.Dir(filepath.Dir(filepath.Dir(sessionDir)))
			}
		}
	}
	return w, repoRoot, nil
}

func attachTrajectory(ctx context.Context, w *trajectory.Writer, parent string) context.Context {
	if w == nil {
		return ctx
	}
	ctx = trajectory.ContextWithWriter(ctx, w)
	if parent != "" {
		ctx = trajectory.ContextWithParentSpan(ctx, parent)
	}
	return ctx
}

// makeTurnContext returns a context for a single turn.
// If timeoutSec <= 0, returns a cancellable context without a deadline.
func makeTurnContext(timeoutSec int) (context.Context, context.CancelFunc) {
	if timeoutSec <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
}

// gitApply applies a patch file to the current repository working tree using git apply.
func gitApply(patchPath string, verbose bool) error {
	if strings.TrimSpace(patchPath) == "" {
		return errors.New("empty patch path")
	}
	args := []string{"apply", "--unsafe-paths", patchPath}
	cmd := exec.Command("git", args...)
	var out bytes.Buffer
	var errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if verbose {
		fmt.Fprintln(os.Stderr, "[git]", "git "+strings.Join(args, " "))
	}
	if err := cmd.Run(); err != nil {
		if verbose {
			if out.Len() > 0 {
				fmt.Fprintln(os.Stderr, "[git] stdout:", trimTo(out.String(), 800))
			}
			if errb.Len() > 0 {
				fmt.Fprintln(os.Stderr, "[git] stderr:", trimTo(errb.String(), 800))
			}
		}
		return fmt.Errorf("git apply failed: %v\n%s", err, trimTo(errb.String(), 600))
	}
	if verbose {
		fmt.Fprintln(os.Stderr, "[git] apply: success")
		if out.Len() > 0 {
			fmt.Fprintln(os.Stderr, "[git] stdout:", trimTo(out.String(), 800))
		}
		// Show a concise git status diff summary to confirm changes landed
		st := exec.Command("git", "status", "--porcelain")
		var sb bytes.Buffer
		st.Stdout = &sb
		_ = st.Run()
		s := strings.TrimSpace(sb.String())
		if s != "" {
			fmt.Fprintln(os.Stderr, "[git] status:", trimTo(strings.ReplaceAll(s, "\n", "; "), 800))
		}
	}
	return nil
}

// countTurns counts how many Turn headings exist in the transcript content.
func countTurns(md string) int {
	lines := strings.Split(md, "\n")
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "## Turn ") {
			n++
		}
	}
	return n
}

type modelRuntime struct {
	resolved         llm.ResolvedModel
	alias            string
	usingAlias       bool
	extras           map[string]any
	paramPairs       []string
	paramJSON        []string
	fallbackAliases  []string
	fallbackResolved []llm.ResolvedModel
}

func (m modelRuntime) displayLabel() string {
	alias := strings.TrimSpace(m.alias)
	resolvedModel := strings.TrimSpace(m.resolved.Model)
	provider := strings.TrimSpace(m.resolved.ProviderName)
	var providerModel string
	switch {
	case provider != "" && resolvedModel != "":
		providerModel = fmt.Sprintf("%s:%s", provider, resolvedModel)
	case resolvedModel != "":
		providerModel = resolvedModel
	case provider != "":
		providerModel = provider
	}
	switch {
	case alias != "" && providerModel != "" && !strings.EqualFold(alias, providerModel):
		return fmt.Sprintf("%s (%s)", alias, providerModel)
	case alias != "":
		return alias
	case providerModel != "":
		return providerModel
	}
	baseURL := strings.TrimSpace(m.resolved.BaseURL)
	if baseURL != "" {
		return baseURL
	}
	return ""
}

type componentModelRuntimes struct {
	orchestrator  modelRuntime
	patcher       modelRuntime
	fileDiscovery modelRuntime
}

func (m modelRuntime) toPromptRuntime() promptsvc.ModelRuntime {
	return promptsvc.ModelRuntime{
		Resolved:         llm.CloneResolvedModel(m.resolved),
		Alias:            m.alias,
		UsingAlias:       m.usingAlias,
		Extras:           copyExtras(m.extras),
		ParamPairs:       append([]string(nil), m.paramPairs...),
		ParamJSON:        append([]string(nil), m.paramJSON...),
		FallbackAliases:  append([]string(nil), m.fallbackAliases...),
		FallbackResolved: cloneResolvedModels(m.fallbackResolved),
	}
}

func cloneModelRuntime(m modelRuntime) modelRuntime {
	return modelRuntime{
		resolved:         llm.CloneResolvedModel(m.resolved),
		alias:            m.alias,
		usingAlias:       m.usingAlias,
		extras:           copyExtras(m.extras),
		paramPairs:       append([]string(nil), m.paramPairs...),
		paramJSON:        append([]string(nil), m.paramJSON...),
		fallbackAliases:  append([]string(nil), m.fallbackAliases...),
		fallbackResolved: cloneResolvedModels(m.fallbackResolved),
	}
}

func ensureFallbackToPrimary(target *modelRuntime, primary modelRuntime) {
	if target == nil {
		return
	}
	target.fallbackAliases = prependAliasIfMissing(target.fallbackAliases, primary.alias)
	target.fallbackResolved = prependResolvedModel(target.fallbackResolved, primary.resolved)
}

func copyExtras(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneResolvedModels(in []llm.ResolvedModel) []llm.ResolvedModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]llm.ResolvedModel, 0, len(in))
	for _, m := range in {
		out = append(out, llm.CloneResolvedModel(m))
	}
	return out
}

func prependAliasIfMissing(list []string, alias string) []string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return list
	}
	for _, existing := range list {
		if strings.EqualFold(strings.TrimSpace(existing), alias) {
			return list
		}
	}
	return append([]string{alias}, list...)
}

func prependResolvedModel(list []llm.ResolvedModel, model llm.ResolvedModel) []llm.ResolvedModel {
	if strings.TrimSpace(model.Model) == "" {
		return list
	}
	for _, existing := range list {
		if resolvedModelsEqual(existing, model) {
			return list
		}
	}
	return append([]llm.ResolvedModel{llm.CloneResolvedModel(model)}, list...)
}

func resolvedModelsEqual(a, b llm.ResolvedModel) bool {
	return strings.EqualFold(strings.TrimSpace(a.BaseURL), strings.TrimSpace(b.BaseURL)) &&
		strings.EqualFold(strings.TrimSpace(a.Endpoint), strings.TrimSpace(b.Endpoint)) &&
		strings.EqualFold(strings.TrimSpace(a.Model), strings.TrimSpace(b.Model))
}

func describeModel(system string, runtime modelRuntime) string {
	label := strings.TrimSpace(runtime.displayLabel())
	if label == "" {
		return ""
	}
	return fmt.Sprintf("%s model: %s", system, label)
}

func promptOptions(lines ...string) *ui.PromptOptions {
	var meta []string
	for _, line := range lines {
		clean := strings.TrimSpace(line)
		if clean == "" {
			continue
		}
		meta = append(meta, clean)
	}
	if len(meta) == 0 {
		return nil
	}
	return &ui.PromptOptions{Metadata: meta}
}

func resolveModelRuntimes(cfg config, paramPairs, paramJSON []string) (componentModelRuntimes, error) {
	extras, err := llm.ParseParamOverrides(paramPairs, paramJSON)
	if err != nil {
		return componentModelRuntimes{}, err
	}
	primary := modelRuntime{
		extras:     extras,
		paramPairs: append([]string(nil), paramPairs...),
		paramJSON:  append([]string(nil), paramJSON...),
	}

	orchAlias := firstNonEmpty(
		strings.TrimSpace(cfg.orchModel),
		strings.TrimSpace(os.Getenv("MCT_ORCH_MODEL")),
		strings.TrimSpace(os.Getenv("MCT_MODEL")),
		strings.TrimSpace(cfg.agentModel),
	)

	hasDirectFlags := strings.TrimSpace(cfg.openAIAPIKey) != "" || strings.TrimSpace(cfg.openAIBaseURL) != "" || strings.TrimSpace(cfg.openAIModel) != ""

	directAPIKey := firstNonEmpty(strings.TrimSpace(cfg.openAIAPIKey), strings.TrimSpace(os.Getenv("OPENAI_API_KEY")), strings.TrimSpace(os.Getenv("AGENT_MODEL_API_KEY")))
	directBaseURL := firstNonEmpty(strings.TrimSpace(cfg.openAIBaseURL), strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), strings.TrimSpace(os.Getenv("AGENT_MODEL_BASE_URL")))
	directModel := firstNonEmpty(strings.TrimSpace(cfg.openAIModel), strings.TrimSpace(os.Getenv("OPENAI_MODEL")), strings.TrimSpace(os.Getenv("AGENT_MODEL")))

	switch {
	case hasDirectFlags:
		missing := missingDirect(directAPIKey, directBaseURL, directModel)
		if len(missing) > 0 {
			return componentModelRuntimes{}, &missingConfigError{items: missing}
		}
		resolved, err := llm.NewDirectModel(directBaseURL, directAPIKey, directModel)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		primary.resolved = resolved
		primary.usingAlias = false
	case strings.TrimSpace(orchAlias) != "":
		resolved, err := llm.ResolveModel(orchAlias)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		primary.resolved = resolved
		primary.alias = orchAlias
		primary.usingAlias = true
	default:
		if defaultAlias, err := llm.DefaultModelAlias(); err == nil {
			if resolved, err2 := llm.ResolveModel(defaultAlias); err2 == nil {
				primary.resolved = resolved
				primary.alias = defaultAlias
				primary.usingAlias = true
			}
		}
	}

	if strings.TrimSpace(primary.resolved.Model) == "" {
		missing := missingDirect(directAPIKey, directBaseURL, directModel)
		if len(missing) > 0 {
			return componentModelRuntimes{}, &missingConfigError{items: missing}
		}
		resolved, err := llm.NewDirectModel(directBaseURL, directAPIKey, directModel)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		primary.resolved = resolved
		primary.usingAlias = false
	}

	applyFallbacks(&primary, []string{
		cfg.agentModel,
		cfg.patcherModel,
		cfg.fileDiscoveryModel,
		os.Getenv("MCT_MODEL"),
		os.Getenv("MCT_ORCH_MODEL"),
	}, directBaseURL, directAPIKey, directModel)

	patcher := cloneModelRuntime(primary)
	patcherAlias := firstNonEmpty(strings.TrimSpace(cfg.patcherModel), strings.TrimSpace(os.Getenv("MCT_PATCHER_MODEL")))
	if strings.TrimSpace(patcherAlias) != "" {
		resolved, err := llm.ResolveModel(patcherAlias)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		patcher.resolved = resolved
		patcher.alias = patcherAlias
		patcher.usingAlias = true
	}

	fileDiscovery := cloneModelRuntime(primary)
	fdAlias := firstNonEmpty(strings.TrimSpace(cfg.fileDiscoveryModel), strings.TrimSpace(os.Getenv("MCT_FILE_DISCOVERY_MODEL")))
	if strings.TrimSpace(fdAlias) != "" {
		resolved, err := llm.ResolveModel(fdAlias)
		if err != nil {
			return componentModelRuntimes{}, err
		}
		fileDiscovery.resolved = resolved
		fileDiscovery.alias = fdAlias
		fileDiscovery.usingAlias = true
	}
	ensureFallbackToPrimary(&patcher, primary)
	ensureFallbackToPrimary(&fileDiscovery, primary)

	return componentModelRuntimes{
		orchestrator:  primary,
		patcher:       patcher,
		fileDiscovery: fileDiscovery,
	}, nil
}

func missingDirect(apiKey, baseURL, model string) []string {
	var missing []string
	if strings.TrimSpace(apiKey) == "" {
		missing = append(missing, "--openai-api-key or OPENAI_API_KEY")
	}
	if strings.TrimSpace(baseURL) == "" {
		missing = append(missing, "--openai-base-url or OPENAI_BASE_URL")
	}
	if strings.TrimSpace(model) == "" {
		missing = append(missing, "--openai-model or OPENAI_MODEL")
	}
	return missing
}

type missingConfigError struct {
	items []string
}

func (e *missingConfigError) Error() string {
	return "missing model configuration"
}

func applyFallbacks(rt *modelRuntime, candidates []string, directBase, directKey, directModel string) {
	if rt == nil {
		return
	}
	primaryAlias := strings.TrimSpace(rt.alias)
	seen := make(map[string]struct{}, len(candidates))
	rt.fallbackAliases = nil
	for _, cand := range candidates {
		trimmed := strings.TrimSpace(cand)
		if trimmed == "" {
			continue
		}
		if primaryAlias != "" && strings.EqualFold(primaryAlias, trimmed) {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		rt.fallbackAliases = append(rt.fallbackAliases, trimmed)
	}
	trimmedModel := strings.TrimSpace(directModel)
	trimmedBase := strings.TrimSpace(directBase)
	trimmedKey := strings.TrimSpace(directKey)
	if trimmedModel != "" && trimmedBase != "" && trimmedKey != "" {
		if resolved, err := llm.NewDirectModel(trimmedBase, trimmedKey, trimmedModel); err == nil {
			rt.fallbackResolved = append(rt.fallbackResolved, resolved)
		}
	}
}

func resolveFileDiscoveryTrajectory(cfg config, sessionID string) (string, error) {
	override := strings.TrimSpace(cfg.fileDiscoveryTrajectory)
	outDir := strings.TrimSpace(cfg.fileDiscoveryOutputDir)
	if override != "" && outDir != "" {
		return "", errors.New("cannot combine --file-discovery-trajectory with --file-discovery-output-dir")
	}
	if override != "" {
		path := override
		if !filepath.IsAbs(path) {
			abs, err := filepath.Abs(path)
			if err != nil {
				return "", fmt.Errorf("resolve file-discovery trajectory path: %w", err)
			}
			path = abs
		}
		if !cfg.dryRun {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", fmt.Errorf("create file-discovery trajectory directory: %w", err)
			}
		}
		return path, nil
	}

	dir := outDir
	if dir == "" {
		path, err := artifacts.FileDiscoveryTrajectoryPath(sessionID)
		if err != nil {
			return "", err
		}
		if cfg.dryRun {
			return path, nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", fmt.Errorf("create file-discovery output dir: %w", err)
		}
		return path, nil
	}
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("resolve file-discovery output dir: %w", err)
		}
		dir = abs
	}
	if !cfg.dryRun {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create file-discovery output dir: %w", err)
		}
	}
	name := "file-discovery.jsonl"
	return filepath.Join(dir, name), nil
}

// writeFinalAnswer persists the final answer to a plain text file.
// It writes nothing in dry-run mode.
// If finalFileFlag is empty, it writes to .machtiani/sessions/<sessionID>/chat/agent-final.txt
func writeFinalAnswer(sessionID, answer, finalFileFlag string, verbose bool, dryRun bool) error {
	if dryRun {
		return nil
	}
	path := strings.TrimSpace(finalFileFlag)
	if path == "" {
		dir, err := artifacts.SessionChatDirectory(sessionID)
		if err != nil {
			return err
		}
		path = filepath.Join(dir, "agent-final.txt")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Always overwrite
	if err := os.WriteFile(path, []byte(answer+"\n"), 0o644); err != nil {
		return err
	}
	if verbose {
		fmt.Fprintln(os.Stderr, "Final answer saved:", path)
	}
	return nil
}

func presentFinalAnswer(display *ui.TerminalDisplay, answer string) {
	rendered, fallback, err := renderWithGlow(answer)
	if fallback {
		if err != nil {
			fmt.Fprintln(os.Stderr, "[warning] markdown render failed; showing plain text:", err)
		}
	} else if err != nil {
		fmt.Fprintln(os.Stderr, "[warning] markdown render warning:", err)
	}
	if strings.TrimSpace(rendered) == "" {
		rendered = strings.TrimSpace(answer)
	}
	display.ShowFinal(rendered)
}

func renderWithGlow(content string) (string, bool, error) {
	// Render using glamour (the library used by glow) so we avoid external binaries.
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		// Use default wrapping; TerminalDisplay adds a leading newline before output.
	)
	if err != nil {
		return strings.TrimSpace(content), true, err
	}
	rendered, err := r.Render(content)
	if err != nil {
		return strings.TrimSpace(content), true, err
	}
	rendered = strings.TrimRight(rendered, "\n")
	if strings.TrimSpace(rendered) == "" {
		return strings.TrimSpace(content), false, nil
	}
	return rendered, false, nil
}
