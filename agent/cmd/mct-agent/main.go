package main

import (
    "context"
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
}

func main() {
    os.Exit(run())
}

func run() int {
    var cfg config
    fs := flag.NewFlagSet("mct-agent", flag.ExitOnError)
    fs.IntVar(&cfg.maxSteps, "max-steps", 4, "maximum number of turns before finalizing")
    fs.StringVar(&cfg.model, "model", "", "model for mct calls (optional; mirrors mct default)")
    fs.StringVar(&cfg.agentModel, "agent-model", "", "model for planner/finalizer LLM (optional)")
    fs.IntVar(&cfg.timeoutPerTurn, "timeout-per-turn", 120, "per-turn timeout in seconds")
    fs.StringVar(&cfg.mctBin, "mct-bin", "", "path to mct binary override")
    fs.BoolVar(&cfg.dryRun, "dry-run", false, "print intended mct calls; don’t execute")
    fs.BoolVar(&cfg.verbose, "verbose", false, "verbose agent logging")

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

    // Resolve mct runner
    mctRunner := runner.Runner{
        MCTBin:  cfg.mctBin,
        Verbose: cfg.verbose,
        DryRun:  cfg.dryRun,
    }
    if err := mctRunner.Resolve(); err != nil {
        fmt.Fprintln(os.Stderr, "mct resolution error:", err)
        fmt.Fprintln(os.Stderr, "Hint: install mct into PATH or set MCT_BIN to its location.")
        return 1
    }

    // Planner/Finalizer client
    pl := planner.NewClient(planner.ClientConfig{
        AgentModel:        cfg.agentModel,
        AgentModelAPIKey:  firstNonEmpty(os.Getenv("AGENT_MODEL_API_KEY"), os.Getenv("MCT_MODEL_API_KEY")),
        AgentModelBaseURL: firstNonEmpty(os.Getenv("AGENT_MODEL_BASE_URL"), os.Getenv("MCT_MODEL_BASE_URL")),
        Verbose:           cfg.verbose,
        DryRun:            cfg.dryRun,
    })

    // Running state
    summary := ""
    evidence := make([]planner.Turn, 0, cfg.maxSteps)
    lastAnswer := ""
    retrieved := []string{}

    if err := tr.WriteHeader(goal, sessionID, cfg); err != nil {
        fmt.Fprintln(os.Stderr, "Error writing transcript header:", err)
        return 1
    }

    // Loop
    for step := 1; step <= cfg.maxSteps; step++ {
        // Decide next action
        ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.timeoutPerTurn)*time.Second)
        decision, question, perr := pl.Plan(ctx, goal, summary, lastAnswer, retrieved, step, cfg.maxSteps)
        cancel()
        if perr != nil {
            fmt.Fprintln(os.Stderr, "Planner error:", perr)
            return 1
        }

        if cfg.verbose {
            fmt.Printf("Step %d decision: %s\n", step, decision)
        }

        if decision == planner.DecisionFinalize || step == cfg.maxSteps {
            // Compose final answer
            ctx, cancelF := context.WithTimeout(context.Background(), time.Duration(cfg.timeoutPerTurn)*time.Second)
            answer, ferr := pl.Finalize(ctx, goal, evidence, summary)
            cancelF()
            if ferr != nil {
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
        if cfg.model != "" {
            args = append(args, "--model", cfg.model)
        }
        args = append(args, question)

        ctx2, cancel2 := context.WithTimeout(context.Background(), time.Duration(cfg.timeoutPerTurn)*time.Second)
        savedPath, merr := mctRunner.RunPrompt(ctx2, sessionID, args...)
        cancel2()
        if merr != nil {
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

        // Update summary/evidence (simple, concise excerpt)
        short := trimTo(firstNonEmpty(parser.ExtractAnswerSummary(lastAnswer), lastAnswer), 600)
        evidence = append(evidence, planner.Turn{
            Step:              step,
            Question:          question,
            RetrievedFilePaths: retrieved,
            AnswerExcerpt:     short,
            SavedChatPath:     savedPath,
        })
        summary = parser.UpdateSummary(summary, short, retrieved)

        // Log to transcript
        if err := tr.WriteTurn(step, question, savedPath, retrieved, short, "ask"); err != nil {
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
