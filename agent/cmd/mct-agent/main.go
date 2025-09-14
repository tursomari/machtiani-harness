package main

import (
    "context"
    "errors"
    "flag"
    "fmt"
    "os"
    "strings"
    "time"

    "github.com/tursomari/machtiani/agent/internal/planner"
    "github.com/tursomari/machtiani/agent/internal/runner"
    "github.com/tursomari/machtiani/agent/internal/parser"
    "github.com/tursomari/machtiani/agent/internal/transcript"
)

type config struct {
    maxSteps       int
    model          string
    agentModel     string
    timeoutPerTurn int
    mctBin         string
    dryRun         bool
    verbose        bool
    // Normalized OpenAI flags
    openAIAPIKey   string
    openAIBaseURL  string
    openAIModel    string
}

func main() {
    os.Exit(run())
}

func run() int {
    var cfg config
    fs := flag.NewFlagSet("mct-agent", flag.ExitOnError)
    fs.IntVar(&cfg.maxSteps, "max-steps", 4, "maximum number of turns before finalizing")
    fs.StringVar(&cfg.model, "model", "", "alias of --openai-model (deprecated)")
    fs.StringVar(&cfg.agentModel, "agent-model", "", "legacy planner model (deprecated; use --openai-model)")
    fs.IntVar(&cfg.timeoutPerTurn, "timeout-per-turn", 120, "per-turn timeout in seconds (set 0 for no timeout)")
    fs.StringVar(&cfg.mctBin, "mct-bin", "", "path to mct binary override")
    fs.BoolVar(&cfg.dryRun, "dry-run", false, "print intended mct calls; don’t execute")
    fs.BoolVar(&cfg.verbose, "verbose", false, "verbose agent logging")
    // Normalized OpenAI flags
    fs.StringVar(&cfg.openAIAPIKey, "openai-api-key", "", "API key for OpenAI-compatible endpoint")
    fs.StringVar(&cfg.openAIBaseURL, "openai-base-url", "", "Base URL for OpenAI-compatible endpoint")
    fs.StringVar(&cfg.openAIModel, "openai-model", "", "Model name for planner and mct")

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

    // Prepare transcript file
    tr, err := transcript.New()
    if err != nil {
        fmt.Fprintln(os.Stderr, "Error preparing transcript:", err)
        return 1
    }
    defer tr.Close()

    if cfg.verbose {
        fmt.Println("mct-agent starting; transcript:", tr.Path())
    }

    // Setup session ID for correlation
    sessionID := runner.GenerateSessionID()
    if cfg.verbose {
        fmt.Println("Session:", sessionID)
    }

    // Resolve effective OPENAI_* to use throughout
    effAPIKey, effBaseURL, effModel := resolveOpenAI(cfg)
    // Fail fast on missing planner/model configuration
    missing := []string{}
    if strings.TrimSpace(effAPIKey) == "" { missing = append(missing, "--openai-api-key or OPENAI_API_KEY") }
    if strings.TrimSpace(effBaseURL) == "" { missing = append(missing, "--openai-base-url or OPENAI_BASE_URL") }
    if strings.TrimSpace(effModel) == "" { missing = append(missing, "--openai-model/--model or OPENAI_MODEL") }
    if len(missing) > 0 {
        fmt.Fprintln(os.Stderr, "Missing model config: set:")
        for _, m := range missing { fmt.Fprintln(os.Stderr, " - ", m) }
        return 2
    }

    // Resolve mct runner
    mctRunner := runner.Runner{
        MCTBin:        cfg.mctBin,
        Verbose:       cfg.verbose,
        DryRun:        cfg.dryRun,
        OpenAIAPIKey:  effAPIKey,
        OpenAIBaseURL: effBaseURL,
        OpenAIModel:   effModel,
    }
    if err := mctRunner.Resolve(); err != nil {
        fmt.Fprintln(os.Stderr, "mct resolution error:", err)
        fmt.Fprintln(os.Stderr, "Hint: install mct into PATH or set MCT_BIN to its location.")
        return 1
    }

    // Planner/Finalizer client uses normalized OPENAI_* values
    pl := planner.NewClient(planner.ClientConfig{
        AgentModel:        effModel,
        AgentModelAPIKey:  effAPIKey,
        AgentModelBaseURL: effBaseURL,
        Verbose:           cfg.verbose,
        DryRun:            cfg.dryRun,
    })

    // Running state (kept only for transcript writing)
    lastAnswer := ""
    retrieved := []string{}

    if err := tr.WriteHeader(goal, sessionID, cfg); err != nil {
        fmt.Fprintln(os.Stderr, "Error writing transcript header:", err)
        return 1
    }

    // First turn: use the original prompt directly (no planner)
    {
        step := 1
        // Build mct args
        args := []string{"prompt", "--mode=default"}
        effPromptModel := firstNonEmpty(cfg.openAIModel, cfg.model, cfg.agentModel, effModel)
        if effPromptModel != "" { args = append(args, "--model", effPromptModel) }
        args = append(args, goal)

        ctx2, cancel2 := makeTurnContext(cfg.timeoutPerTurn)
        savedPath, merr := mctRunner.RunPrompt(ctx2, sessionID, args...)
        // ensure we release timers even on success
        cancel2()
        if merr != nil {
            // Improve diagnostics for timeouts (common cause of "signal: killed")
            if errors.Is(ctx2.Err(), context.DeadlineExceeded) || strings.Contains(strings.ToLower(merr.Error()), "signal: killed") {
                fmt.Fprintf(os.Stderr, "mct prompt error: timed out after %ds. Try increasing --timeout-per-turn or set 0 for unlimited.\n", cfg.timeoutPerTurn)
                return 1
            }
            fmt.Fprintln(os.Stderr, "mct prompt error:", merr)
            return 1
        }

        // Read the authoritative response file
        content, rerr := os.ReadFile(".machtiani/chat/machtiani-response.md")
        if rerr != nil {
            fmt.Fprintln(os.Stderr, "Failed to read saved chat (.machtiani/chat/machtiani-response.md):", rerr)
            return 1
        }
        lastAnswer = string(content)
        retrieved = parser.ExtractRetrievedFilePaths(lastAnswer)

        // Log to transcript with full Assistant answer
        fullAns := parser.ExtractAssistantAnswer(lastAnswer)
        if strings.TrimSpace(fullAns) == "" { fullAns = lastAnswer }
        if err := tr.WriteTurn(step, goal, savedPath, retrieved, fullAns, "initial"); err != nil {
            fmt.Fprintln(os.Stderr, "Transcript write error:", err)
            return 1
        }
    }

    // If only one step is allowed, finalize immediately using transcript only
    if cfg.maxSteps == 1 {
        ctx, cancelF := makeTurnContext(cfg.timeoutPerTurn)
        trFull := tr.Content()
        answer, ferr := pl.Finalize(ctx, goal, trFull)
        cancelF()
        if ferr != nil {
            if errors.Is(ctx.Err(), context.DeadlineExceeded) {
                fmt.Fprintf(os.Stderr, "Finalizer error: timed out after %ds. Increase --timeout-per-turn or set 0 for unlimited.\n", cfg.timeoutPerTurn)
                return 1
            }
            fmt.Fprintln(os.Stderr, "Finalizer error:", ferr)
            return 1
        }
        if err := tr.WriteFinal(answer, 1, false); err != nil {
            fmt.Fprintln(os.Stderr, "Transcript write error:", err)
            return 1
        }
        fmt.Println("Conclusion:")
        fmt.Println(answer)
        return 0
    }

    // Subsequent turns loop
    for step := 2; step <= cfg.maxSteps; step++ {
        // Decide next action using transcript only
        ctx, cancel := makeTurnContext(cfg.timeoutPerTurn)
        trFull := tr.Content()
        decision, question, perr := pl.Plan(ctx, goal, trFull, step, cfg.maxSteps)
        cancel()
        if perr != nil {
            if errors.Is(ctx.Err(), context.DeadlineExceeded) {
                fmt.Fprintf(os.Stderr, "Planner error: timed out after %ds. Increase --timeout-per-turn or set 0 for unlimited.\n", cfg.timeoutPerTurn)
                return 1
            }
            fmt.Fprintln(os.Stderr, "Planner error:", perr)
            return 1
        }

        if cfg.verbose {
            fmt.Printf("Step %d decision: %s\n", step, decision)
        }

        if decision == planner.DecisionFinalize || step == cfg.maxSteps {
            // Compose final answer using transcript only
            ctx, cancelF := makeTurnContext(cfg.timeoutPerTurn)
            trFull := tr.Content()
            answer, ferr := pl.Finalize(ctx, goal, trFull)
            cancelF()
            if ferr != nil {
                if errors.Is(ctx.Err(), context.DeadlineExceeded) {
                    fmt.Fprintf(os.Stderr, "Finalizer error: timed out after %ds. Increase --timeout-per-turn or set 0 for unlimited.\n", cfg.timeoutPerTurn)
                    return 1
                }
                fmt.Fprintln(os.Stderr, "Finalizer error:", ferr)
                return 1
            }
            if err := tr.WriteFinal(answer, step, step == cfg.maxSteps && decision != planner.DecisionFinalize); err != nil {
                fmt.Fprintln(os.Stderr, "Transcript write error:", err)
                return 1
            }
            fmt.Println("Conclusion:")
            fmt.Println(answer)
            return 0
        }

        // Ask one question via mct
        if question == "" {
            fmt.Fprintln(os.Stderr, "Planner returned empty question for 'ask' decision")
            return 1
        }

        if cfg.verbose {
            fmt.Println("Question:", question)
        }

        // Build mct args
        args := []string{"prompt", "--mode=default"}
        effPromptModel := firstNonEmpty(cfg.openAIModel, cfg.model, cfg.agentModel, effModel)
        if effPromptModel != "" { args = append(args, "--model", effPromptModel) }
        args = append(args, question)

        ctx2, cancel2 := makeTurnContext(cfg.timeoutPerTurn)
        savedPath, merr := mctRunner.RunPrompt(ctx2, sessionID, args...)
        cancel2()
        if merr != nil {
            if errors.Is(ctx2.Err(), context.DeadlineExceeded) || strings.Contains(strings.ToLower(merr.Error()), "signal: killed") {
                fmt.Fprintf(os.Stderr, "mct prompt error: timed out after %ds. Try increasing --timeout-per-turn or set 0 for unlimited.\n", cfg.timeoutPerTurn)
                return 1
            }
            fmt.Fprintln(os.Stderr, "mct prompt error:", merr)
            return 1
        }

        // Read the authoritative response file
        content, rerr := os.ReadFile(".machtiani/chat/machtiani-response.md")
        if rerr != nil {
            fmt.Fprintln(os.Stderr, "Failed to read saved chat (.machtiani/chat/machtiani-response.md):", rerr)
            return 1
        }
        lastAnswer = string(content)
        retrieved = parser.ExtractRetrievedFilePaths(lastAnswer)

        // Log to transcript with full Assistant answer
        fullAns := parser.ExtractAssistantAnswer(lastAnswer)
        if strings.TrimSpace(fullAns) == "" { fullAns = lastAnswer }
        if err := tr.WriteTurn(step, question, savedPath, retrieved, fullAns, "ask"); err != nil {
            fmt.Fprintln(os.Stderr, "Transcript write error:", err)
            return 1
        }
    }

    return 0
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

// makeTurnContext returns a context for a single turn.
// If timeoutSec <= 0, returns a cancellable context without a deadline.
func makeTurnContext(timeoutSec int) (context.Context, context.CancelFunc) {
    if timeoutSec <= 0 {
        return context.WithCancel(context.Background())
    }
    return context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
}

// resolveOpenAI resolves OPENAI_* from flags first, then env, then legacy envs with a one-time warning.
func resolveOpenAI(cfg config) (apiKey, baseURL, model string) {
    // Flags first
    apiKey = strings.TrimSpace(cfg.openAIAPIKey)
    baseURL = strings.TrimSpace(cfg.openAIBaseURL)
    model = strings.TrimSpace(cfg.openAIModel)
    // Also accept deprecated flags as last-resort model alias
    if model == "" {
        model = firstNonEmpty(strings.TrimSpace(cfg.model), strings.TrimSpace(cfg.agentModel))
    }
    // Then OPENAI_* envs
    if apiKey == "" { apiKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) }
    if baseURL == "" { baseURL = strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")) }
    if model == "" { model = strings.TrimSpace(os.Getenv("OPENAI_MODEL")) }
    // Legacy fallbacks with warning
    usedLegacy := false
    if apiKey == "" {
        apiKey = firstNonEmpty(strings.TrimSpace(os.Getenv("AGENT_MODEL_API_KEY")), strings.TrimSpace(os.Getenv("MCT_MODEL_API_KEY")))
        if apiKey != "" { usedLegacy = true }
    }
    if baseURL == "" {
        baseURL = firstNonEmpty(strings.TrimSpace(os.Getenv("AGENT_MODEL_BASE_URL")), strings.TrimSpace(os.Getenv("MCT_MODEL_BASE_URL")))
        if baseURL != "" { usedLegacy = true }
    }
    if model == "" {
        model = firstNonEmpty(strings.TrimSpace(os.Getenv("AGENT_MODEL")), strings.TrimSpace(os.Getenv("MCT_MODEL")))
        if model != "" { usedLegacy = true }
    }
    if usedLegacy {
        fmt.Fprintln(os.Stderr, "[deprecation] Using legacy AGENT_MODEL_* or MCT_MODEL_* envs. Please migrate to OPENAI_*.")
    }
    return
}
