package runner

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	promptsvc "github.com/tursomari/machtiani/agent/internal/mct/prompt"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

type Runner struct {
	Verbose                 bool
	DryRun                  bool
	EnableReadme            bool
	Runtime                 promptsvc.ModelRuntime
	AnswerRuntime           promptsvc.ModelRuntime
	FileDiscoveryRuntime    promptsvc.ModelRuntime
	FileDiscoveryTrajectory string
	ShellAgent              bool
	ShellAgentModel         string
	GlobalConfigPath        string
	PersistTmpData          bool
	SessionTempRoot         string
	Prompts                 *llm.PromptsConfig
}

type PromptInput struct {
	Prompt             string
	Mode               string
	IncludeHistory     bool
	SourceFile         string
	OnStreamHeader     func(string)
	OnStreamToken      func(string)
	MaxInputTokens     int
	ResponseDirectives []string
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
	w, hasWriter := trajectory.FromContext(ctx)
	parentSpan, _ := trajectory.ParentSpanID(ctx)
	var span trajectory.Span
	if hasWriter {
		directives := append([]string(nil), in.ResponseDirectives...)
		span = w.StartSpan(parentSpan)
		payload := map[string]any{
			"event_version":    1,
			"mode":             safeMode,
			"include_history":  in.IncludeHistory,
			"source_file":      strings.TrimSpace(in.SourceFile),
			"max_input_tokens": in.MaxInputTokens,
		}
		if in.MaxInputTokens == 0 {
			delete(payload, "max_input_tokens")
		}
		payload["shell_agent_enabled"] = r.ShellAgent
		payload["file_discovery_enabled"] = !r.ShellAgent && safeMode != "answer-only"
		if len(directives) > 0 {
			payload["response_directives"] = directives
		}
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(in.Prompt, w.ExcerptLen()), "prompt")
		evt := trajectory.Event{Kind: "mct.prompt.start", SpanID: span.ID, ParentSpanID: parentSpan, Payload: payload}
		if err := w.Emit(ctx, evt); err != nil {
			reportRunnerTrajectoryError(err)
		}
	}
	start := time.Now()
	childCtx := trajectory.ContextWithParentSpan(ctx, span.ID)
	var readmeOpts *promptsvc.ReadmeOptions
	if r.EnableReadme && !r.DryRun && safeMode != "answer-only" {
		readmeOpts = &promptsvc.ReadmeOptions{Enabled: true}
	}
	var mctPrompts *llm.MCTPromptsConfig
	if r.Prompts != nil {
		mctPrompts = r.Prompts.MCT
	}
	res, err := promptsvc.Run(childCtx, promptsvc.RunOptions{
		Prompt:                  in.Prompt,
		Mode:                    safeMode,
		IncludeHistory:          in.IncludeHistory,
		SessionID:               sessionID,
		SourceFile:              in.SourceFile,
		Runtime:                 r.Runtime,
		AnswerRuntime:           r.AnswerRuntime,
		FileDiscoveryRuntime:    fdRuntime,
		OnHeader:                onHeader,
		OnToken:                 onToken,
		Verbose:                 r.Verbose,
		FileDiscoveryTrajectory: r.FileDiscoveryTrajectory,
		MaxInputTokens:          in.MaxInputTokens,
		Readme:                  readmeOpts,
		ShellAgent:              r.ShellAgent,
		ShellAgentModel:         strings.TrimSpace(r.ShellAgentModel),
		GlobalConfigPath:        r.GlobalConfigPath,
		PersistTmpData:          r.PersistTmpData,
		SessionTempRoot:         r.SessionTempRoot,
		ResponseDirectives:      append([]string(nil), in.ResponseDirectives...),
		Prompts:                 mctPrompts,
	})
	if useMarkdown && ms != nil {
		_ = ms.Flush()
	}
	duration := time.Since(start)
	if err != nil {
		if hasWriter {
			directives := append([]string(nil), in.ResponseDirectives...)
			payload := map[string]any{
				"event_version":          1,
				"mode":                   safeMode,
				"include_history":        in.IncludeHistory,
				"source_file":            strings.TrimSpace(in.SourceFile),
				"duration_ms":            duration.Milliseconds(),
				"retrieved_count":        0,
				"discovered_paths_count": 0,
				"max_input_tokens":       in.MaxInputTokens,
			}
			if in.MaxInputTokens == 0 {
				delete(payload, "max_input_tokens")
			}
			if len(directives) > 0 {
				payload["response_directives"] = directives
			}
			category, code := llm.ClassifyError(err)
			evt := trajectory.Event{
				Level:        "error",
				Kind:         "mct.prompt.result",
				SpanID:       span.ID,
				ParentSpanID: parentSpan,
				Payload:      payload,
				Err: &trajectory.ErrorInfo{
					Message:  err.Error(),
					Category: category,
					Code:     code,
				},
			}
			if emitErr := w.Emit(ctx, evt); emitErr != nil {
				reportRunnerTrajectoryError(emitErr)
			}
		}
		return promptsvc.Result{}, err
	}
	if hasWriter {
		directives := append([]string(nil), in.ResponseDirectives...)
		retrieved := len(res.RetrievedFiles)
		payload := map[string]any{
			"event_version":          1,
			"mode":                   safeMode,
			"include_history":        in.IncludeHistory,
			"source_file":            strings.TrimSpace(in.SourceFile),
			"duration_ms":            duration.Milliseconds(),
			"retrieved_count":        retrieved,
			"discovered_paths_count": retrieved,
			"saved_chat_path":        strings.TrimSpace(res.SavedPath),
			"max_input_tokens":       in.MaxInputTokens,
			"dry_run":                r.DryRun,
		}
		if in.MaxInputTokens == 0 {
			delete(payload, "max_input_tokens")
		}
		if len(directives) > 0 {
			payload["response_directives"] = directives
		}
		if res.SaveError != nil {
			payload["save_error"] = res.SaveError.Error()
		}
		if strings.TrimSpace(res.TrajectoryPath) != "" {
			payload["shell_agent_trajectory_path"] = strings.TrimSpace(res.TrajectoryPath)
		}
		payload["file_discovery_used"] = res.FileDiscoveryRan
		payload["shell_agent_used"] = res.ShellAgentUsed
		if retrieved > 0 && retrieved <= 10 {
			payload["retrieved_paths"] = append([]string(nil), res.RetrievedFiles...)
		}
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(in.Prompt, w.ExcerptLen()), "prompt")
		// Also include an excerpt of the composed prompt (with history, files, and response rules)
		// so it’s clear when directives were injected.
		composed := strings.TrimSpace(res.Header)
		if composed != "" {
			// res.Header is built as: "# User\n\n<combined>\n\n# Assistant\n\n"
			// Extract the <combined> portion between the markers if present.
			userMarker := "# User\n\n"
			assistantMarker := "\n\n# Assistant"
			if strings.HasPrefix(composed, userMarker) {
				composed = composed[len(userMarker):]
			}
			if idx := strings.Index(composed, assistantMarker); idx >= 0 {
				composed = composed[:idx]
			}
			payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(composed, w.ExcerptLen()), "composed_prompt")
		}
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(res.Assistant, w.ExcerptLen()), "answer")
		evt := trajectory.Event{Kind: "mct.prompt.result", SpanID: span.ID, ParentSpanID: parentSpan, Payload: payload}
		if emitErr := w.Emit(ctx, evt); emitErr != nil {
			reportRunnerTrajectoryError(emitErr)
		}
	}
	return res, nil
}

func GenerateSessionID() string {
	// Simple timestamp+rand; good enough for correlation
	now := time.Now().UTC().Format("20060102T150405")
	return fmt.Sprintf("agent-%s-%04d", now, rand.Intn(10000))
}

func reportRunnerTrajectoryError(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[trajectory] runner emit error: %v\n", err)
}
