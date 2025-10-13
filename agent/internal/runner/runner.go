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
	FileDiscoveryRuntime    promptsvc.ModelRuntime
	FileDiscoveryTrajectory string
}

type PromptInput struct {
	Prompt         string
	Mode           string
	IncludeHistory bool
	SourceFile     string
	OnStreamHeader func(string)
	OnStreamToken  func(string)
	MaxInputTokens int
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

	var (
		ms          *llm.MarkdownStreamer
		onHeader    func(string)
		onToken     func(string)
		useMarkdown bool
	)

	if in.OnStreamHeader != nil {
		onHeader = in.OnStreamHeader
	}
	if in.OnStreamToken != nil {
		onToken = in.OnStreamToken
	}

	if onHeader == nil || onToken == nil {
		ms, _ = llm.NewMarkdownStreamer()
		useMarkdown = ms != nil
	}

	if onHeader == nil {
		onHeader = func(chunk string) {
			if useMarkdown {
				_ = ms.Feed(chunk)
			} else {
				fmt.Print(chunk)
			}
		}
	}
	if onToken == nil {
		onToken = func(tok string) {
			if useMarkdown {
				_ = ms.Feed(tok)
			} else {
				fmt.Print(tok)
			}
		}
	}

	fdRuntime := r.FileDiscoveryRuntime
	if strings.TrimSpace(fdRuntime.Resolved.Model) == "" {
		fdRuntime = r.Runtime
	}
	res, err := promptsvc.Run(ctx, promptsvc.RunOptions{
		Prompt:                  in.Prompt,
		Mode:                    safeMode,
		IncludeHistory:          in.IncludeHistory,
		SessionID:               sessionID,
		SourceFile:              in.SourceFile,
		Runtime:                 r.Runtime,
		FileDiscoveryRuntime:    fdRuntime,
		OnHeader:                onHeader,
		OnToken:                 onToken,
		Verbose:                 r.Verbose,
		FileDiscoveryTrajectory: r.FileDiscoveryTrajectory,
		MaxInputTokens:          in.MaxInputTokens,
	})
	if useMarkdown && ms != nil {
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
