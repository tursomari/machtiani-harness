package prompt

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/mct/internal/contextbuilder"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/discoveryrunner"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/git"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/naming"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/readme"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/session"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/utils"
	"github.com/tursomari/machtiani/agent/internal/mct/llm"
)

var chatStreamWithRuntime = llm.ChatStreamWithResolvedFallback

const shellAgentContextPrefix = "Here is possibly relevant information from the shell agent."

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
	fileDiscoveryRan := false

	if isAnswerOnly || opts.ShellAgent {
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
			Extras:             copyExtrasMap(fdRuntime.Extras),
			ParamPairs:         append([]string(nil), fdRuntime.ParamPairs...),
			ParamJSON:          append([]string(nil), fdRuntime.ParamJSON...),
			FallbackAliases:    append([]string(nil), fdRuntime.FallbackAliases...),
			FallbackResolved:   cloneResolvedModels(fdRuntime.FallbackResolved),
			TrajectoryOverride: strings.TrimSpace(opts.FileDiscoveryTrajectory),
		}
		dr, err := discoveryrunner.Run(ctx, opts.Prompt, drModel, opts.SessionID, opts.Verbose)
		if err != nil {
			return res, fmt.Errorf("file discovery: %w", err)
		}
		filtered := filterPaths(dr.Paths, ignoreFiles)
		combined, included = contextbuilder.Build(opts.Prompt, filtered, hist, contextbuilder.Options{IncludeHistory: includeHistory, MaxInputTokens: opts.MaxInputTokens})
		fileDiscoveryRan = true
	}

	shellTrajectory := ""
	shellAgentUsed := false
	if opts.ShellAgent {
		contextBlock, trajectoryPath, err := invokeShellAgent(ctx, combined, opts)
		if err != nil {
			return res, err
		}
		shellTrajectory = trajectoryPath
		shellAgentUsed = true
		if strings.TrimSpace(contextBlock) != "" {
			if strings.TrimSpace(combined) != "" {
				combined = combined + "\n\n" + contextBlock
			} else {
				combined = contextBlock
			}
		}
	}

	header := buildHeader(combined)
	res.Header = header
	if opts.OnHeader != nil {
		opts.OnHeader(header)
	}

	messages := []llm.Message{{Role: "user", Content: combined}}
	answerRuntime := opts.AnswerRuntime
	if runtimeIsZero(answerRuntime) {
		answerRuntime = opts.Runtime
	}
	assistant, err := chatStreamWithRuntime(ctx, answerRuntime.Resolved, answerRuntime.FallbackAliases, answerRuntime.FallbackResolved, copyExtrasMap(answerRuntime.Extras), messages, opts.OnToken)
	if err != nil {
		return res, err
	}
	res.Assistant = assistant
	res.FullText = header + assistant
	if len(included) > 0 {
		res.FullText += formatRetrievedSection(included)
	}
	res.RetrievedFiles = append([]string(nil), included...)
	if shellTrajectory != "" {
		res.TrajectoryPath = shellTrajectory
	}
	res.FileDiscoveryRan = fileDiscoveryRan
	res.ShellAgentUsed = shellAgentUsed

	_ = session.AddMessage("user", opts.Prompt, nil)
	_ = session.AddMessage("assistant", res.Assistant, included)

	if err := runReadmeManager(ctx, opts, isAnswerOnly); err != nil {
		utils.LogErrorIfNotAnswerOnly(isAnswerOnly, err, "internal README management failed")
	}

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

func invokeShellAgent(ctx context.Context, prompt string, opts RunOptions) (string, string, error) {
	args := []string{"-output-format=json"}
	if opts.Verbose {
		args = append(args, "-verbose")
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, "shell-agent", args...)
	cmd.Env = append(os.Environ(), runtimeEnvFrom(opts.Runtime)...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("shell-agent stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", "", fmt.Errorf("shell-agent stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("shell-agent start: %w", err)
	}

	var stdoutBuf bytes.Buffer
	var writer io.Writer = &stdoutBuf
	if opts.OnToken != nil {
		writer = io.MultiWriter(&stdoutBuf, &tokenWriter{onToken: opts.OnToken})
	}

	var stderrBuf bytes.Buffer
	stderrDone := make(chan struct{})
	var stderrErr error
	go func() {
		_, stderrErr = io.Copy(&stderrBuf, stderrPipe)
		close(stderrDone)
	}()

	if _, err := io.Copy(writer, stdoutPipe); err != nil {
		_ = cmd.Process.Kill()
		<-stderrDone
		_ = cmd.Wait()
		return "", "", fmt.Errorf("shell-agent read stdout: %w", err)
	}

	err = cmd.Wait()
	<-stderrDone
	if stderrErr != nil {
		return "", "", fmt.Errorf("shell-agent read stderr: %w", stderrErr)
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg == "" {
				stderrMsg = strings.TrimSpace(stdoutBuf.String())
			}
			if stderrMsg != "" {
				return "", "", fmt.Errorf("shell-agent exited with code %d: %s", exitErr.ExitCode(), stderrMsg)
			}
			return "", "", fmt.Errorf("shell-agent exited with code %d", exitErr.ExitCode())
		}
		return "", "", fmt.Errorf("shell-agent wait: %w", err)
	}

	stdoutText := strings.TrimSpace(stdoutBuf.String())
	stderrText := strings.TrimSpace(stderrBuf.String())
	contextBlock := formatShellAgentContext(stdoutText, stderrText)
	trajectory := extractTrajectoryPath(stdoutText, stderrText)
	return contextBlock, trajectory, nil
}

func formatShellAgentContext(stdoutText, stderrText string) string {
	var b strings.Builder
	b.WriteString(shellAgentContextPrefix)
	b.WriteString("\n\n")
	if stdoutText != "" {
		b.WriteString(stdoutText)
		b.WriteString("\n")
	}
	if stderrText != "" {
		if stdoutText != "" {
			b.WriteString("\n")
		}
		b.WriteString("[stderr]\n")
		b.WriteString(stderrText)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func extractTrajectoryPath(outputs ...string) string {
	trajectoryLine := regexp.MustCompile(`Trajectory:\s*(.+\.json)`) // e.g. "Trajectory: trajectory-20240930-120000.json"
	filePattern := regexp.MustCompile(`trajectory-\d{8}-\d{6}\.json`)
	for _, out := range outputs {
		if strings.TrimSpace(out) == "" {
			continue
		}
		if match := trajectoryLine.FindStringSubmatch(out); len(match) == 2 {
			candidate := strings.TrimSpace(match[1])
			if candidate != "" {
				if abs, err := filepath.Abs(candidate); err == nil {
					return abs
				}
				return candidate
			}
		}
	}
	for _, out := range outputs {
		if strings.TrimSpace(out) == "" {
			continue
		}
		if match := filePattern.FindString(out); match != "" {
			if abs, err := filepath.Abs(match); err == nil {
				return abs
			}
			return match
		}
	}
	return ""
}

func runtimeEnvFrom(rt ModelRuntime) []string {
	env := []string{}
	apiKey := strings.TrimSpace(rt.Resolved.APIKey)
	if apiKey != "" {
		env = append(env, "OPENAI_API_KEY="+apiKey)
	}
	baseURL := strings.TrimSpace(rt.Resolved.BaseURL)
	if baseURL != "" {
		env = append(env, "OPENAI_BASE_URL="+baseURL)
	}
	model := strings.TrimSpace(rt.Resolved.Model)
	if model != "" {
		env = append(env, "OPENAI_MODEL="+model)
	}
	return env
}

type tokenWriter struct {
	onToken func(string)
}

func (w *tokenWriter) Write(p []byte) (int, error) {
	if w.onToken != nil && len(p) > 0 {
		w.onToken(string(p))
	}
	return len(p), nil
}

func runReadmeManager(ctx context.Context, opts RunOptions, isAnswerOnly bool) error {
	if opts.Readme == nil || !opts.Readme.Enabled {
		return nil
	}
	if opts.ShellAgent {
		return nil
	}
	if isAnswerOnly {
		return nil
	}
	if strings.TrimSpace(os.Getenv(readme.SkipReadmeManagerEnv)) != "" {
		return nil
	}
	commit := ""
	if opts.Readme != nil {
		commit = strings.TrimSpace(opts.Readme.ProjectCommitSHA)
	}
	if commit == "" {
		resolved, err := git.GetHeadCommitHash()
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "not a git repository") {
				return nil
			}
			return fmt.Errorf("resolve project commit: %w", err)
		}
		commit = strings.TrimSpace(resolved)
	}
	if commit == "" {
		return fmt.Errorf("readme: unable to determine project commit hash")
	}
	mgr, err := readme.NewManager(isAnswerOnly, opts.Verbose)
	if err != nil {
		return err
	}
	mgr.SetMaxInputTokens(opts.MaxInputTokens)
	mgr.SetPromptExecutor(func(execCtx context.Context, prompt string) (string, error) {
		prev, hadPrev := os.LookupEnv(readme.SkipReadmeManagerEnv)
		if err := os.Setenv(readme.SkipReadmeManagerEnv, "1"); err != nil {
			return "", fmt.Errorf("set %s: %w", readme.SkipReadmeManagerEnv, err)
		}
		defer func() {
			if hadPrev {
				_ = os.Setenv(readme.SkipReadmeManagerEnv, prev)
			} else {
				_ = os.Unsetenv(readme.SkipReadmeManagerEnv)
			}
		}()

		innerSessionID := deriveReadmeSessionID(commit)
		innerOpts := RunOptions{
			Prompt:               prompt,
			Mode:                 "default",
			IncludeHistory:       false,
			SessionID:            innerSessionID,
			ExplicitName:         "internal-readme",
			Runtime:              opts.Runtime,
			AnswerRuntime:        opts.AnswerRuntime,
			FileDiscoveryRuntime: opts.FileDiscoveryRuntime,
			Verbose:              opts.Verbose,
			MaxInputTokens:       opts.MaxInputTokens,
		}
		res, err := Run(execCtx, innerOpts)
		if err != nil {
			return "", err
		}
		assistant := strings.TrimSpace(res.Assistant)
		if assistant != "" {
			return assistant, nil
		}
		return strings.TrimSpace(res.FullText), nil
	})
	return mgr.Run(ctx, commit)
}

func deriveReadmeSessionID(commit string) string {
	trimmed := strings.TrimSpace(commit)
	if trimmed == "" {
		return "readme"
	}
	if len(trimmed) > 12 {
		trimmed = trimmed[:12]
	}
	return fmt.Sprintf("readme-%s", trimmed)
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

func copyExtrasMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneResolvedModels(src []llm.ResolvedModel) []llm.ResolvedModel {
	if len(src) == 0 {
		return nil
	}
	out := make([]llm.ResolvedModel, 0, len(src))
	for _, m := range src {
		out = append(out, llm.CloneResolvedModel(m))
	}
	return out
}

func runtimeIsZero(rt ModelRuntime) bool {
	if strings.TrimSpace(rt.Resolved.Model) != "" {
		return false
	}
	if strings.TrimSpace(rt.Resolved.BaseURL) != "" {
		return false
	}
	if strings.TrimSpace(rt.Alias) != "" {
		return false
	}
	if len(rt.FallbackAliases) > 0 {
		return false
	}
	if len(rt.FallbackResolved) > 0 {
		return false
	}
	if len(rt.ParamPairs) > 0 {
		return false
	}
	if len(rt.ParamJSON) > 0 {
		return false
	}
	if len(rt.Extras) > 0 {
		return false
	}
	return true
}
