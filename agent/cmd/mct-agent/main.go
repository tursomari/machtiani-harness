package main

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "flag"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
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
    patcherBin     string
    dryRun         bool
    verbose        bool
    finalFile      string
    // Patch application behavior
    noApply        bool
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
    fs.StringVar(&cfg.patcherBin, "patcher-bin", "", "path to patcher binary override (default: PATCHER_BIN or PATH)")
    fs.BoolVar(&cfg.dryRun, "dry-run", false, "print intended mct calls; don’t execute")
    fs.BoolVar(&cfg.verbose, "verbose", false, "verbose agent logging")
    fs.StringVar(&cfg.finalFile, "final-file", "", "path to write final answer-only artifact (default: .machtiani/chat/agent-final-<sessionID>.txt)")
    fs.BoolVar(&cfg.noApply, "no-apply", false, "do not auto-apply generated patches (default: apply)\n")
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

    // Resolve patcher runner
    pRunner := runner.PatcherRunner{Verbose: cfg.verbose, DryRun: cfg.dryRun, SessionID: sessionID}
    if err := pRunner.Resolve(cfg.patcherBin); err != nil {
        if cfg.verbose {
            fmt.Fprintln(os.Stderr, "[patcher] resolve warning:", err)
        }
        // Non-fatal until we actually need to patch; we will re-resolve errors then.
    }

    // Planner/Finalizer client uses normalized OPENAI_* values
    pl := planner.NewClient(planner.ClientConfig{
        AgentModel:        effModel,
        AgentModelAPIKey:  effAPIKey,
        AgentModelBaseURL: effBaseURL,
        Verbose:           cfg.verbose,
        DryRun:            cfg.dryRun,
        RequestTimeoutSec: cfg.timeoutPerTurn,
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
        if err := writeFinalAnswer(sessionID, answer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
            fmt.Fprintln(os.Stderr, "Final file write error:", err)
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
            // Graceful fallback: if the planner hit a transient network error, finalize instead of aborting.
            perrStr := strings.ToLower(perr.Error())
            if strings.Contains(perrStr, "deadline exceeded") || strings.Contains(perrStr, "timeout") || strings.Contains(perrStr, "temporary") {
                fmt.Fprintln(os.Stderr, "Planner warning:", perr)
                fmt.Fprintln(os.Stderr, "Falling back to finalizing with current transcript.")
                ctxF, cancelF := makeTurnContext(cfg.timeoutPerTurn)
                trFull := tr.Content()
                answer, ferr := pl.Finalize(ctxF, goal, trFull)
                cancelF()
                if ferr != nil {
                    if errors.Is(ctxF.Err(), context.DeadlineExceeded) {
                        fmt.Fprintf(os.Stderr, "Finalizer error: timed out after %ds. Increase --timeout-per-turn or set 0 for unlimited.\n", cfg.timeoutPerTurn)
                        return 1
                    }
                    fmt.Fprintln(os.Stderr, "Finalizer error:", ferr)
                    return 1
                }
                if err := tr.WriteFinal(answer, step, true); err != nil {
                    fmt.Fprintln(os.Stderr, "Transcript write error:", err)
                    return 1
                }
                if err := writeFinalAnswer(sessionID, answer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
                    fmt.Fprintln(os.Stderr, "Final file write error:", err)
                    return 1
                }
                fmt.Println("Conclusion:")
                fmt.Println(answer)
                return 0
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
            if err := writeFinalAnswer(sessionID, answer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
                fmt.Fprintln(os.Stderr, "Final file write error:", err)
                return 1
            }
            fmt.Println("Conclusion:")
            fmt.Println(answer)
            return 0
        }

        switch decision {
        case planner.DecisionAsk:
            if question == "" {
                fmt.Fprintln(os.Stderr, "Planner returned empty question for 'ask' decision")
                return 1
            }
            if cfg.verbose { fmt.Println("Question:", question) }
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
            // Continue to next turn unless we've hit cap, in which case break to finalize path.
            if step == cfg.maxSteps { goto FINALIZE }

        case planner.DecisionPatch:
            // Extract JSON and invoke patcher
            payload := question // for patch decisions, parseDecision returns raw body here
            if cfg.verbose {
                fmt.Fprintln(os.Stderr, "[patcher] planner payload (raw):", trimTo(strings.TrimSpace(payload), 1200))
            }
            jsonBytes, jerr := parser.ExtractPatchJSONPayload(payload)
            if jerr != nil {
                // Record a patch-error turn then exit 1
                _ = tr.WriteTurn(step, "Patcher: invalid input JSON", "", nil, "Error extracting JSON: "+jerr.Error(), "patch-error")
                return 1
            }
            if cfg.verbose {
                fmt.Fprintln(os.Stderr, "[patcher] extracted JSON:", trimTo(string(jsonBytes), 1200))
            }
            // Ensure patcher is resolved (may have failed earlier lazily)
            if err := pRunner.Resolve(cfg.patcherBin); err != nil {
                _ = tr.WriteTurn(step, "Patcher: resolve failed", "", nil, "Error: "+err.Error(), "patch-error")
                return 1
            }
            ctxP, cancelP := makeTurnContext(cfg.timeoutPerTurn)
            stdout, stderr, perr := pRunner.RunJSON(ctxP, jsonBytes, cfg.verbose)
            cancelP()
            if perr != nil {
                _ = tr.WriteTurn(step, "Patcher: execution error", "", nil, trimTo("stderr: "+string(stderr), 800), "patch-error")
                return 1
            }
            if cfg.verbose {
                if len(stdout) > 0 { fmt.Fprintln(os.Stderr, "[patcher] stdout:", trimTo(string(stdout), 1200)) }
                if len(stderr) > 0 { fmt.Fprintln(os.Stderr, "[patcher] stderr:", trimTo(string(stderr), 1200)) }
            }
            // Parse stdout JSON
            var pout struct {
                PatchPath     string   `json:"patch_path"`
                Applies       bool     `json:"applies"`
                FilesModified []string `json:"files_modified"`
                Insertions    int      `json:"insertions"`
                Deletions     int      `json:"deletions"`
                Description   string   `json:"description"`
            }
            if err := json.Unmarshal(stdout, &pout); err != nil {
                _ = tr.WriteTurn(step, "Patcher: invalid stdout JSON", "", nil, "stdout: "+trimTo(string(stdout), 600)+"\nstderr: "+trimTo(string(stderr), 200), "patch-error")
                return 1
            }
            if !pout.Applies {
                _ = tr.WriteTurn(step, "Patcher: apply-check failed", "", nil, "stdout: "+trimTo(string(stdout), 600)+"\nstderr: "+trimTo(string(stderr), 200), "patch-error")
                return 1
            }
            // Build concise answer block
            qline := "Patcher: create patch"
            if strings.TrimSpace(pout.Description) != "" { qline = "Patcher: "+pout.Description }
            ans := fmt.Sprintf("Patch created: %s\nfiles_modified: %v\ninsertions: %d\ndeletions: %d\n\ninput:\n%s\n\noutput:\n%s\n",
                pout.PatchPath,
                strings.Join(pout.FilesModified, ", "),
                pout.Insertions,
                pout.Deletions,
                trimTo(string(jsonBytes), 1000),
                trimTo(string(stdout), 1000),
            )
            // Optionally apply the patch to the working tree
            if !cfg.noApply && !cfg.dryRun {
                if aerr := gitApply(pout.PatchPath, cfg.verbose); aerr != nil {
                    if cfg.verbose { fmt.Fprintln(os.Stderr, "[git] apply error:", aerr) }
                    _ = tr.WriteTurn(step, "Patcher: apply failed", "", nil, trimTo(aerr.Error()+"\n"+trimTo(string(stderr), 400), 800), "patch-error")
                    return 1
                }
                ans = ans + "applied: yes\n"
            } else if cfg.dryRun {
                if cfg.verbose { fmt.Fprintln(os.Stderr, "[git] apply (dry-run) skipping") }
                ans = ans + "applied: (dry-run)\n"
            } else {
                ans = ans + "applied: skipped (use without --no-apply)\n"
            }
            if err := tr.WriteTurn(step, qline, "", nil, ans, "patch"); err != nil {
                fmt.Fprintln(os.Stderr, "Transcript write error:", err)
                return 1
            }
            if step == cfg.maxSteps { goto FINALIZE }

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
        ctx, cancel := makeTurnContext(cfg.timeoutPerTurn)
        trFull := tr.Content()
        lastDec, lastBody, err := pl.Plan(ctx, goal, trFull, step, cfg.maxSteps)
        cancel()
        if err == nil && lastDec == planner.DecisionPatch {
            if cfg.verbose {
                fmt.Fprintln(os.Stderr, "[patcher] pre-finalize planner payload (raw):", trimTo(strings.TrimSpace(lastBody), 1200))
            }
            jsonBytes, jerr := parser.ExtractPatchJSONPayload(lastBody)
            if jerr != nil {
                _ = tr.WriteTurn(step, "Patcher: pre-finalize (invalid JSON)", "", nil, jerr.Error(), "patch-error")
            } else {
                if cfg.verbose {
                    fmt.Fprintln(os.Stderr, "[patcher] pre-finalize extracted JSON:", trimTo(string(jsonBytes), 1200))
                }
                _ = pRunner.Resolve(cfg.patcherBin)
                ctxP, cancelP := makeTurnContext(cfg.timeoutPerTurn)
                stdout, stderr, perr := pRunner.RunJSON(ctxP, jsonBytes, cfg.verbose)
                cancelP()
                if perr != nil {
                    _ = tr.WriteTurn(step, "Patcher: pre-finalize (error)", "", nil, trimTo(string(stderr), 800), "patch-error")
                } else {
                    if cfg.verbose {
                        if len(stdout) > 0 { fmt.Fprintln(os.Stderr, "[patcher] pre-finalize stdout:", trimTo(string(stdout), 1200)) }
                        if len(stderr) > 0 { fmt.Fprintln(os.Stderr, "[patcher] pre-finalize stderr:", trimTo(string(stderr), 1200)) }
                    }
                    var pout struct {
                        PatchPath     string   `json:"patch_path"`
                        Applies       bool     `json:"applies"`
                        FilesModified []string `json:"files_modified"`
                        Insertions    int      `json:"insertions"`
                        Deletions     int      `json:"deletions"`
                        Description   string   `json:"description"`
                    }
                    if json.Unmarshal(stdout, &pout) == nil && pout.Applies {
                        qline := "Patcher: pre-finalize"
                        if strings.TrimSpace(pout.Description) != "" { qline = "Patcher: pre-finalize - "+pout.Description }
                        ans := fmt.Sprintf("Patch created: %s\nfiles_modified: %v\ninsertions: %d\ndeletions: %d\n\ninput:\n%s\n\noutput:\n%s\n",
                            pout.PatchPath,
                            strings.Join(pout.FilesModified, ", "),
                            pout.Insertions,
                            pout.Deletions,
                            trimTo(string(jsonBytes), 1000),
                            trimTo(string(stdout), 1000),
                        )
                        // Optionally apply at pre-finalize too
                        if !cfg.noApply && !cfg.dryRun {
                            if aerr := gitApply(pout.PatchPath, cfg.verbose); aerr != nil {
                                if cfg.verbose { fmt.Fprintln(os.Stderr, "[git] apply error (pre-finalize):", aerr) }
                                _ = tr.WriteTurn(step, "Patcher: pre-finalize apply failed", "", nil, trimTo(aerr.Error()+"\n"+trimTo(string(stderr), 400), 800), "patch-error")
                            } else {
                                ans = ans + "applied: yes\n"
                            }
                        } else if cfg.dryRun {
                            if cfg.verbose { fmt.Fprintln(os.Stderr, "[git] apply (pre-finalize dry-run) skipping") }
                            ans = ans + "applied: (dry-run)\n"
                        } else {
                            ans = ans + "applied: skipped (use without --no-apply)\n"
                        }
                        _ = tr.WriteTurn(step, qline, "", nil, ans, "patch")
                    } else {
                        _ = tr.WriteTurn(step, "Patcher: pre-finalize (apply-check failed)", "", nil, trimTo(string(stdout), 800)+"\n"+trimTo(string(stderr), 200), "patch-error")
                    }
                }
            }
        }
    }

    // Compose final answer using transcript only
    {
        // Determine how many turns occurred by counting headings
        turns := countTurns(tr.Content())
        ctx, cancelF := makeTurnContext(cfg.timeoutPerTurn)
        trFull := tr.Content()
        answer, ferr := pl.Finalize(ctx, goal, trFull)
        cancelF()
        if ferr != nil {
            fmt.Fprintln(os.Stderr, "Finalizer error:", ferr)
            return 1
        }
        if err := tr.WriteFinal(answer, turns, turns >= cfg.maxSteps); err != nil {
            fmt.Fprintln(os.Stderr, "Transcript write error:", err)
            return 1
        }
        if err := writeFinalAnswer(sessionID, answer, cfg.finalFile, cfg.verbose, cfg.dryRun); err != nil {
            fmt.Fprintln(os.Stderr, "Final file write error:", err)
            return 1
        }
        fmt.Println("Conclusion:")
        fmt.Println(answer)
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
            if out.Len() > 0 { fmt.Fprintln(os.Stderr, "[git] stdout:", trimTo(out.String(), 800)) }
            if errb.Len() > 0 { fmt.Fprintln(os.Stderr, "[git] stderr:", trimTo(errb.String(), 800)) }
        }
        return fmt.Errorf("git apply failed: %v\n%s", err, trimTo(errb.String(), 600))
    }
    if verbose {
        fmt.Fprintln(os.Stderr, "[git] apply: success")
        if out.Len() > 0 { fmt.Fprintln(os.Stderr, "[git] stdout:", trimTo(out.String(), 800)) }
        // Show a concise git status diff summary to confirm changes landed
        st := exec.Command("git", "status", "--porcelain")
        var sb bytes.Buffer
        st.Stdout = &sb
        _ = st.Run()
        s := strings.TrimSpace(sb.String())
        if s != "" { fmt.Fprintln(os.Stderr, "[git] status:", trimTo(strings.ReplaceAll(s, "\n", "; "), 800)) }
    }
    return nil
}

// countTurns counts how many Turn headings exist in the transcript content.
func countTurns(md string) int {
    lines := strings.Split(md, "\n")
    n := 0
    for _, l := range lines {
        if strings.HasPrefix(strings.TrimSpace(l), "## Turn ") { n++ }
    }
    return n
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

// writeFinalAnswer persists the final answer to a plain text file.
// It writes nothing in dry-run mode.
// If finalFileFlag is empty, it writes to .machtiani/chat/agent-final-<sessionID>.txt
func writeFinalAnswer(sessionID, answer, finalFileFlag string, verbose bool, dryRun bool) error {
    if dryRun {
        return nil
    }
    path := strings.TrimSpace(finalFileFlag)
    if path == "" {
        path = filepath.Join(".machtiani", "chat", fmt.Sprintf("agent-final-%s.txt", sessionID))
    }
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return err
    }
    // Always overwrite
    if err := os.WriteFile(path, []byte(answer+"\n"), 0o644); err != nil {
        return err
    }
    if verbose {
        fmt.Println("Final answer saved:", path)
    }
    return nil
}
