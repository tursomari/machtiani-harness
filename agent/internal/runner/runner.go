package runner

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/tursomari/machtiani/mct/llm"
	promptsvc "github.com/tursomari/machtiani/mct/prompt"
)

type Runner struct {
	Verbose                 bool
	DryRun                  bool
	Runtime                 promptsvc.ModelRuntime
	FileDiscoveryTrajectory string
}

type PromptInput struct {
	Prompt         string
	Mode           string
	IncludeHistory bool
	SourceFile     string
}

func (r *Runner) Resolve() error {
	return nil
}

func (r *Runner) RunPrompt(ctx context.Context, sessionID string, in PromptInput) (promptsvc.Result, error) {
	safeMode := strings.TrimSpace(in.Mode)
	if safeMode == "" {
		safeMode = "default"
	}
	if r.Verbose {
		snippet := strings.TrimSpace(in.Prompt)
		if len(snippet) > 120 {
			snippet = snippet[:120] + "…"
		}
		fmt.Printf("[mct] prompt mode=%s include-history=%t prompt=%q\n", safeMode, in.IncludeHistory, snippet)
	}
	if r.DryRun {
		return promptsvc.Result{}, nil
	}

	ms, _ := llm.NewMarkdownStreamer()
	onHeader := func(chunk string) {
		if ms != nil {
			_ = ms.Feed(chunk)
		} else {
			fmt.Print(chunk)
		}
	}
	onToken := func(tok string) {
		if ms != nil {
			_ = ms.Feed(tok)
		} else {
			fmt.Print(tok)
		}
	}

	res, err := promptsvc.Run(ctx, promptsvc.RunOptions{
		Prompt:                  in.Prompt,
		Mode:                    safeMode,
		IncludeHistory:          in.IncludeHistory,
		SessionID:               sessionID,
		SourceFile:              in.SourceFile,
		Runtime:                 r.Runtime,
		OnHeader:                onHeader,
		OnToken:                 onToken,
		Verbose:                 r.Verbose,
		FileDiscoveryTrajectory: r.FileDiscoveryTrajectory,
	})
	if ms != nil {
		_ = ms.Flush()
	}
	if err != nil {
		return promptsvc.Result{}, err
	}
	return res, nil
}

func GenerateSessionID() string {
	// Simple timestamp+rand; good enough for correlation
	now := time.Now().UTC().Format("20060102T150405")
	return fmt.Sprintf("agent-%s-%04d", now, rand.Intn(10000))
}
