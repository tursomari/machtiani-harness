package prompt

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/mct/internal/contextbuilder"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/discoveryrunner"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/naming"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/session"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/utils"
	"github.com/tursomari/machtiani/agent/internal/mct/llm"
)

// Run executes the core prompt flow used by the mct CLI and mct-agent.
// It handles context building, file discovery, streaming, transcript
// persistence, and chat file saving based on the provided options.
func Run(ctx context.Context, opts RunOptions) (Result, error) {
	var res Result

	mode := strings.TrimSpace(opts.Mode)
	if mode == "" {
		mode = "default"
	}
	isAnswerOnly := mode == "answer-only"

	origSession := os.Getenv("MACHTIANI_SESSION_ID")
	restoreSession := false
	if opts.SessionID != "" && origSession != opts.SessionID {
		if err := os.Setenv("MACHTIANI_SESSION_ID", opts.SessionID); err != nil {
			return res, fmt.Errorf("set session id: %w", err)
		}
		restoreSession = true
	}
	if restoreSession {
		defer func() {
			if origSession == "" {
				_ = os.Unsetenv("MACHTIANI_SESSION_ID")
				return
			}
			_ = os.Setenv("MACHTIANI_SESSION_ID", origSession)
		}()
	}

	hist, err := session.LoadHistory()
	if err != nil {
		hist = []contextbuilder.Message{}
	}

	includeHistory := opts.IncludeHistory

	combined := opts.Prompt
	included := []string(nil)

	if isAnswerOnly {
		combined, included = contextbuilder.Build(opts.Prompt, nil, hist, contextbuilder.Options{IncludeHistory: includeHistory, MaxInputTokens: opts.MaxInputTokens})
	} else {
		ignoreFiles, err := utils.ReadIgnoreFile(".machtiani.ignore")
		if err != nil {
			return res, fmt.Errorf("load ignore rules: %w", err)
		}

		fdRuntime := opts.FileDiscoveryRuntime
		if strings.TrimSpace(fdRuntime.Resolved.Model) == "" {
			fdRuntime = opts.Runtime
		}
		drModel := discoveryrunner.ModelSettings{
			UsingAlias:         fdRuntime.UsingAlias,
			Alias:              fdRuntime.Alias,
			Resolved:           fdRuntime.Resolved,
			ParamPairs:         append([]string(nil), fdRuntime.ParamPairs...),
			ParamJSON:          append([]string(nil), fdRuntime.ParamJSON...),
			TrajectoryOverride: strings.TrimSpace(opts.FileDiscoveryTrajectory),
		}
		dr, err := discoveryrunner.Run(ctx, opts.Prompt, drModel, opts.SessionID, opts.Verbose)
		if err != nil {
			return res, fmt.Errorf("file discovery: %w", err)
		}
		filtered := filterPaths(dr.Paths, ignoreFiles)
		combined, included = contextbuilder.Build(opts.Prompt, filtered, hist, contextbuilder.Options{IncludeHistory: includeHistory, MaxInputTokens: opts.MaxInputTokens})
	}

	header := buildHeader(combined)
	res.Header = header
	if opts.OnHeader != nil {
		opts.OnHeader(header)
	}

	messages := []llm.Message{{Role: "user", Content: combined}}
	assistant, err := llm.ChatStreamWithResolvedFallback(ctx, opts.Runtime.Resolved, opts.Runtime.FallbackAliases, opts.Runtime.FallbackResolved, opts.Runtime.Extras, messages, opts.OnToken)
	if err != nil {
		return res, err
	}
	res.Assistant = assistant
	res.FullText = header + assistant

	if len(included) > 0 {
		res.FullText += formatRetrievedSection(included)
	}
	res.RetrievedFiles = append([]string(nil), included...)

	_ = session.AddMessage("user", opts.Prompt, nil)
	_ = session.AddMessage("assistant", assistant, included)

	if isAnswerOnly {
		return res, nil
	}

	filename := strings.TrimSpace(opts.ExplicitName)
	if filename == "" {
		filename = deriveFilename(opts.SourceFile)
	}
	if filename == "" || filename == "." {
		filename = naming.Generate(ctx, opts.Prompt, opts.Runtime.Resolved)
	}
	res.Filename = filename

	savedPath, saveErr := utils.CreateTempMarkdownFile(res.FullText, filename, opts.SessionID)
	if saveErr != nil {
		res.SaveError = fmt.Errorf("write chat file: %w", saveErr)
		return res, nil
	}
	res.SavedPath = savedPath
	return res, nil
}

func buildHeader(combined string) string {
	trimmed := strings.TrimSpace(combined)
	if strings.HasPrefix(trimmed, "# User") {
		return combined + "\n# Assistant\n\n"
	}
	return fmt.Sprintf("# User\n\n%s\n\n# Assistant\n\n", combined)
}

func deriveFilename(source string) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	name := path.Base(source)
	for ext := path.Ext(name); ext != ""; ext = path.Ext(name) {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}

func filterPaths(paths []string, ignores []string) []string {
	if len(ignores) == 0 {
		return paths
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		drop := false
		for _, rule := range ignores {
			rule = strings.TrimSpace(rule)
			if rule == "" || strings.HasPrefix(rule, "#") {
				continue
			}
			if strings.HasSuffix(rule, "/") {
				prefix := strings.TrimSuffix(rule, "/") + "/"
				if strings.HasPrefix(p, prefix) {
					drop = true
					break
				}
			}
			if strings.ContainsAny(rule, "*?") {
				if ok, _ := filepath.Match(rule, p); ok {
					drop = true
					break
				}
			}
			if p == rule {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, p)
		}
	}
	return out
}

func formatRetrievedSection(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n---\n\n# Retrieved File Paths\n\n")
	for _, p := range paths {
		b.WriteString("- ")
		b.WriteString(p)
		b.WriteString("\n")
	}
	return b.String()
}
