package prompt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/git"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/contextbuilder"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/discoveryrunner"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/naming"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/readme"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/session"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/utils"
	"github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/patchlog"
	"github.com/tursomari/machtiani/agent/internal/prompts"
	"github.com/tursomari/machtiani/agent/internal/shellbridge"
	"github.com/tursomari/machtiani/agent/internal/templates"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/transcript"
)

var (
	chatStreamWithRuntime    = llm.ChatStreamWithResolvedFallback
	discoveryRunnerRun       = discoveryrunner.Run
	shellAgentCommandContext = exec.CommandContext
	patchPromptLogger        = patchlog.WritePrompt
)

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
	useBaselineContext := isPatcherPromptMode(mode)

	if err := validateMCTPromptsConfig(opts.Prompts); err != nil {
		return res, err
	}

	var historyTemplate string
	if opts.Prompts != nil {
		historyTemplate = opts.Prompts.ConversationHistoryTemplate
	}

	// Transform raw prompt into decision-based instruction for session resumption
	if opts.SessionID != "" {
		historyNote := ""
		if len(hist) > 0 {
			historyNote = "Review the session history above and "
		}
		opts.Prompt = fmt.Sprintf(
			"Continue this session. %sAnalyze the conversation history and decide what action to take or how to respond: %s",
			historyNote, opts.Prompt)
	}
	combined := opts.Prompt
	included := []string(nil)
	fileDiscoveryRan := false
	var filtered []string

	if !isAnswerOnly && !opts.ShellAgent {
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
			APIKeyOverrides:    llm.CopyAPIKeyOverridesForRuntime(fdRuntime.APIKeyOverrides),
		}
		discoCtx := llm.WithStage(llm.WithAPIKeyOverrides(ctx, fdRuntime.APIKeyOverrides), "file-discovery")
		dr, err := discoveryRunnerRun(discoCtx, opts.Prompt, drModel, opts.SessionID, opts.Verbose)
		if err != nil {
			return res, fmt.Errorf("file discovery: %w", err)
		}
		filtered = filterPaths(dr.Paths, ignoreFiles)
		fileDiscoveryRan = true
	}

	switch {
	case isAnswerOnly || opts.ShellAgent:
		var buildErr error
		combined, included, buildErr = contextbuilder.Build(opts.Prompt, nil, hist, contextbuilder.Options{IncludeHistory: includeHistory, MaxInputTokens: opts.MaxInputTokens, PreludeTemplate: historyTemplate})
		if buildErr != nil {
			return res, buildErr
		}
	case useBaselineContext:
		var err error
		combined, included, err = buildBaselinePromptContext(opts.Prompt, hist, filtered, includeHistory, opts.MaxInputTokens, opts.SessionID, opts.Verbose, historyTemplate)
		if err != nil {
			return res, err
		}
	default:
		options := contextbuilder.Options{IncludeHistory: includeHistory, MaxInputTokens: opts.MaxInputTokens, PreludeTemplate: historyTemplate}
		if len(filtered) == 0 {
			var buildErr error
			combined, included, buildErr = contextbuilder.Build(opts.Prompt, filtered, hist, options)
			if buildErr != nil {
				return res, buildErr
			}
			break
		}
		prelude, _, buildErr := contextbuilder.Build(opts.Prompt, nil, hist, options)
		if buildErr != nil {
			return res, buildErr
		}
		repoRoot := ""
		if cwd, err := os.Getwd(); err == nil {
			if root, err := git.RepoRoot(cwd); err == nil {
				repoRoot = root
			}
		}
		detection := ShowFileDetection{IsShowFileRequest: true, Filepaths: filtered, Reason: opts.Prompt}
		snippetRuntime := opts.FileDiscoveryRuntime
		if strings.TrimSpace(snippetRuntime.Resolved.Model) == "" {
			snippetRuntime = opts.Runtime
		}
		snippetModelAlias := strings.TrimSpace(snippetRuntime.Alias)
		snippets, snippetErr := FetchFileSnippets(ctx, detection, repoRoot, snippetModelAlias, snippetRuntime.APIKeyOverrides, opts.Verbose)
		fallbackSet := map[string]struct{}{}
		fallbackFiles := []string{}
		addFallback := func(path string) {
			normalized := normalizeShowFilePath(path)
			if normalized == "" {
				return
			}
			if _, ok := fallbackSet[normalized]; ok {
				return
			}
			fallbackSet[normalized] = struct{}{}
			fallbackFiles = append(fallbackFiles, normalized)
		}
		snippetFiles := map[string][]LineRange{}
		emptySnippetFiles := []string{}
		warnings := []string{}
		reasons := []string{}
		var partialErr *SnippetDiscoveryPartialError
		if snippetErr != nil {
			if errors.As(snippetErr, &partialErr) {
				for _, path := range partialErr.Missing {
					addFallback(path)
				}
				invalidPaths := make([]string, 0, len(partialErr.Invalid))
				for path := range partialErr.Invalid {
					invalidPaths = append(invalidPaths, path)
				}
				sort.Strings(invalidPaths)
				for _, path := range invalidPaths {
					addFallback(path)
				}
				if len(partialErr.Missing) > 0 {
					warnings = append(warnings, fmt.Sprintf("snippet-discovery returned no snippets for: %s", strings.Join(partialErr.Missing, ", ")))
				}
				if len(partialErr.Invalid) > 0 {
					details := make([]string, 0, len(invalidPaths))
					for _, path := range invalidPaths {
						details = append(details, fmt.Sprintf("%s (%s)", path, partialErr.Invalid[path]))
					}
					warnings = append(warnings, fmt.Sprintf("snippet-discovery returned invalid snippets for: %s", strings.Join(details, ", ")))
				}
				reasons = append(reasons, "partial_results")
			} else {
				for _, path := range filtered {
					addFallback(path)
				}
				warnings = append(warnings, fmt.Sprintf("snippet-discovery failed (%s)", strings.TrimSpace(snippetErr.Error())))
				reasons = append(reasons, "snippet_error")
			}
		}
		for path, ranges := range snippets {
			if len(ranges) == 0 {
				emptySnippetFiles = append(emptySnippetFiles, path)
				continue
			}
			snippetFiles[path] = ranges
		}
		if len(emptySnippetFiles) > 0 {
			sort.Strings(emptySnippetFiles)
			for _, path := range emptySnippetFiles {
				addFallback(path)
			}
			warnings = append(warnings, fmt.Sprintf("snippet-discovery returned empty snippets for: %s", strings.Join(emptySnippetFiles, ", ")))
			reasons = append(reasons, "empty_snippets")
		}
		var snippetText string
		var snippetPaths []string
		if len(snippetFiles) > 0 {
			var snippetWarnings []string
			snippetText, snippetPaths, snippetWarnings = FormatSnippetsResponse(snippetFiles, "", repoRoot)
			warnings = append(warnings, snippetWarnings...)
		}
		var fullText string
		var fullPaths []string
		if len(fallbackFiles) > 0 {
			var fallbackWarnings []string
			fullText, fullPaths, fallbackWarnings = FormatFullFileFallback(fallbackFiles, repoRoot, snippetErr)
			warnings = append(warnings, fallbackWarnings...)
		}
		sections := []string{}
		if strings.TrimSpace(prelude) != "" {
			sections = append(sections, strings.TrimRight(prelude, "\n"))
		}
		if strings.TrimSpace(snippetText) != "" {
			sections = append(sections, snippetText)
		}
		if strings.TrimSpace(fullText) != "" {
			sections = append(sections, fullText)
		}
		combined = strings.Join(sections, "\n\n")
		included = append(included, snippetPaths...)
		included = append(included, fullPaths...)
		if len(fallbackFiles) > 0 {
			payload := map[string]any{"files": append([]string(nil), fallbackFiles...)}
			if len(reasons) > 0 {
				payload["reasons"] = append([]string(nil), reasons...)
			}
			evt := trajectory.Event{Kind: "snippet_fallback", Payload: payload}
			if parentID, ok := trajectory.ParentSpanID(ctx); ok {
				evt.ParentSpanID = parentID
			}
			_ = trajectory.EmitFromContext(ctx, evt)
		}
		if opts.Verbose {
			for _, warning := range warnings {
				trimmed := strings.TrimSpace(warning)
				if trimmed == "" {
					continue
				}
				fmt.Fprintf(os.Stderr, "[snippet-fallback] %s\n", trimmed)
			}
		}
	}

	directiveBlock := formatResponseDirectives(opts.ResponseDirectives)
	if directiveBlock != "" {
		if strings.TrimSpace(combined) != "" {
			combined = combined + "\n\n" + directiveBlock
		} else {
			combined = directiveBlock
		}
	}
	res.DirectiveBlock = directiveBlock

	shellTrajectory := ""
	shellAgentUsed := false
	if opts.ShellAgent {
		if strings.TrimSpace(opts.ShellAgentModel) == "" {
			candidate := strings.TrimSpace(opts.Runtime.Alias)
			if candidate == "" {
				candidate = strings.TrimSpace(opts.Runtime.Resolved.Alias)
			}
			if candidate != "" {
				opts.ShellAgentModel = candidate
			}
		}
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

	header, err := buildHeader(combined, opts.Prompts)
	if err != nil {
		return res, err
	}
	res.Header = header
	if opts.OnHeader != nil {
		opts.OnHeader(header)
	}

	messages := []llm.Message{{Role: "user", Content: combined}}
	answerRuntime := opts.AnswerRuntime
	if runtimeIsZero(answerRuntime) {
		answerRuntime = opts.Runtime
	}
	if shouldLogPatchPrompt(opts) {
		meta := patchlog.Metadata{
			Source: "prompt.run",
			Model:  strings.TrimSpace(answerRuntime.Resolved.Model),
			Alias:  strings.TrimSpace(answerRuntime.Alias),
			Note:   fmt.Sprintf("mode=%s", strings.TrimSpace(opts.Mode)),
		}
		if path, err := patchPromptLogger(combined, meta); err != nil {
			fmt.Fprintf(os.Stderr, "[patch-log] failed to write prompt log: %v\n", err)
		} else if opts.Verbose {
			fmt.Fprintf(os.Stderr, "[patch-log] wrote prompt log to %s\n", path)
		}
	}
	chatCtx := llm.WithAPIKeyOverrides(ctx, answerRuntime.APIKeyOverrides)
	assistant, err := chatStreamWithRuntime(chatCtx, answerRuntime.Resolved, answerRuntime.FallbackAliases, answerRuntime.FallbackResolved, copyExtrasMap(answerRuntime.Extras), messages, opts.OnToken)
	if err != nil {
		return res, err
	}
	if enriched, injected := maybeInjectTagSnippets(assistant, opts.ResponseDirectives, included, opts.Verbose); injected {
		assistant = enriched
	}
	res.Assistant = assistant
	res.FullText = header + assistant
	if len(included) > 0 {
		res.FullText += formatRetrievedSection(included)
	}
	if fullDiffs, err := loadFullDiffsForSession(opts.SessionID, opts.Verbose); err == nil && strings.TrimSpace(fullDiffs) != "" {
		res.FullText = appendFullDiffSection(res.FullText, fullDiffs)
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

func buildBaselinePromptContext(prompt string, history []contextbuilder.Message, filePaths []string, includeHistory bool, maxInputTokens int, sessionID string, verbose bool, historyTemplate string) (string, []string, error) {
	options := contextbuilder.Options{IncludeHistory: includeHistory, MaxInputTokens: maxInputTokens, PreludeTemplate: historyTemplate}
	base, _, err := contextbuilder.Build(prompt, nil, history, options)
	if err != nil {
		return "", nil, err
	}

	if len(filePaths) == 0 {
		return base, nil, nil
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		if verbose {
			fmt.Fprintf(os.Stderr, "[baseline-context] session id missing; falling back to raw file contents\n")
		}
		combined, included, err := contextbuilder.Build(prompt, filePaths, history, options)
		if err != nil {
			return "", nil, err
		}
		return combined, included, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, fmt.Errorf("resolve working directory: %w", err)
	}
	repoRoot, err := git.RepoRoot(cwd)
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[baseline-context] not inside a git repository; falling back to raw file contents: %v\n", err)
		}
		combined, included, buildErr := contextbuilder.Build(prompt, filePaths, history, options)
		if buildErr != nil {
			return "", nil, buildErr
		}
		return combined, included, nil
	}

	baseline, err := patcher.EnsureBaseline(sessionID, repoRoot, time.Now())
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[baseline-context] ensure baseline failed; falling back to raw file contents: %v\n", err)
		}
		combined, included, buildErr := contextbuilder.Build(prompt, filePaths, history, options)
		if buildErr != nil {
			return "", nil, buildErr
		}
		return combined, included, nil
	}

	sections := make([]string, 0, len(filePaths))
	included := make([]string, 0, len(filePaths))
	for _, rel := range filePaths {
		section, ok, err := patcher.BuildBaselineDiffSection(baseline, repoRoot, rel)
		if err != nil {
			if verbose {
				fmt.Fprintf(os.Stderr, "[baseline-context] failed to render %s: %v\n", rel, err)
			}
			continue
		}
		if ok {
			sections = append(sections, section)
			included = append(included, rel)
		}
	}

	if len(sections) == 0 {
		if verbose {
			fmt.Fprintf(os.Stderr, "[baseline-context] no diff sections generated; falling back to raw file contents\n")
		}
		combined, fallbackIncluded, buildErr := contextbuilder.Build(prompt, filePaths, history, options)
		if buildErr != nil {
			return "", nil, buildErr
		}
		return combined, fallbackIncluded, nil
	}

	diffBlock := strings.Join(sections, "\n\n")
	var builder strings.Builder
	base = strings.TrimRight(base, "\n")
	if strings.TrimSpace(base) != "" {
		builder.WriteString(base)
		builder.WriteString("\n\n")
	}
	builder.WriteString("Here are baseline-relative diffs with line numbers for the referenced files:\n\n")
	builder.WriteString(diffBlock)

	return builder.String(), included, nil
}

func loadFullDiffsForSession(sessionID string, verbose bool) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", nil
	}
	chatDir, err := artifacts.SessionChatDirectory(sessionID)
	if err != nil {
		return "", err
	}
	transcriptPath := filepath.Join(chatDir, "agent-transcript.adoc")
	data, err := os.ReadFile(transcriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			if verbose {
				fmt.Fprintf(os.Stderr, "[full-diff] transcript not found at %s\n", transcriptPath)
			}
			return "", nil
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "[full-diff] read transcript failed: %v\n", err)
		}
		return "", err
	}
	fullDiffs, err := transcript.ExtractFullDiffsFromContent(string(data))
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[full-diff] extract failed: %v\n", err)
		}
		return "", err
	}
	if verbose && strings.TrimSpace(fullDiffs) == "" {
		fmt.Fprintln(os.Stderr, "[full-diff] no full diffs found")
	}
	return fullDiffs, nil
}

func appendFullDiffSection(base, fullDiffs string) string {
	trimmedDiffs := strings.TrimRight(fullDiffs, "\n")
	if strings.TrimSpace(trimmedDiffs) == "" {
		return base
	}
	const sectionHeader = "# Full Diffs of Patched Files"
	marker := "\n\n---\n\n" + sectionHeader
	if idx := strings.Index(base, marker); idx != -1 {
		base = strings.TrimRight(base[:idx], "\n")
	}
	section := marker + "\n\n" + trimmedDiffs
	if strings.TrimSpace(base) == "" {
		return strings.TrimLeft(section, "\n")
	}
	return strings.TrimRight(base, "\n") + section
}

func isPatcherPromptMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "patch", "patcher", "patch-json", "strict-patch":
		return true
	default:
		return false
	}
}

func invokeShellAgent(ctx context.Context, prompt string, opts RunOptions) (string, string, error) {
	args := make([]string, 0, 3)
	if opts.Verbose {
		args = append(args, "-verbose")
	}
	if model := strings.TrimSpace(opts.ShellAgentModel); model != "" {
		args = append(args, "--shell-agent-model", model)
	}
	if opts.MaxInputTokens > 0 {
		args = append(args, "--max-input-tokens", fmt.Sprintf("%d", opts.MaxInputTokens))
	}
	if overrides := firstNonEmptyOverrides(opts.Runtime, opts.FileDiscoveryRuntime, opts.AnswerRuntime); len(overrides) > 0 {
		providers := make([]string, 0, len(overrides))
		trimmed := make(map[string]string, len(overrides))
		for provider, key := range overrides {
			p := strings.TrimSpace(provider)
			k := strings.TrimSpace(key)
			if p == "" || k == "" {
				continue
			}
			if _, exists := trimmed[p]; exists {
				continue
			}
			providers = append(providers, p)
			trimmed[p] = k
		}
		sort.Strings(providers)
		for _, provider := range providers {
			args = append(args, "--api-key", fmt.Sprintf("%s:%s", provider, trimmed[provider]))
		}
	}
	args = append(args, prompt)

	cmd := shellAgentCommandContext(ctx, "shell-agent", args...)
	env := append([]string(nil), os.Environ()...)
	env = append(env, runtimeEnvFrom(opts.Runtime)...)
	if opts.PersistTmpData {
		env = append(env, "MACHTIANI_PERSIST_TMP_DATA=1")
	}
	if tempRoot := strings.TrimSpace(opts.SessionTempRoot); tempRoot != "" {
		env = append(env, "MACHTIANI_SESSION_TEMP_ROOT="+tempRoot)
	}
	if configPath := strings.TrimSpace(opts.GlobalConfigPath); configPath != "" {
		env = append(env, "MACHTIANI_CONFIG="+configPath)
	}
	cmd.Env = env

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

	var stderrBuf bytes.Buffer
	stderrDone := make(chan struct{})
	var stderrErr error
	go func() {
		_, stderrErr = io.Copy(&stderrBuf, stderrPipe)
		close(stderrDone)
	}()

	trajWriter, _ := trajectory.FromContext(ctx)
	parentSpan, _ := trajectory.ParentSpanID(ctx)
	emitAction := func(desc, cmd string) {
		emitShellActionEvent(ctx, trajWriter, parentSpan, desc, cmd)
	}
	if err := interceptShellAgentStdout(stdoutPipe, &stdoutBuf, opts.OnToken, emitAction); err != nil {
		_ = cmd.Process.Kill()
		<-stderrDone
		_ = cmd.Wait()
		return "", "", fmt.Errorf("shell-agent read stdout: %w", err)
	}

	err = cmd.Wait()
	<-stderrDone
	if stderrErr != nil && !isClosedPipeError(stderrErr) {
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
	contextBlock, formatErr := formatShellAgentContext(stdoutText, stderrText, opts.Prompts)
	if formatErr != nil {
		return "", "", formatErr
	}
	trajectory := extractTrajectoryPath(stdoutText, stderrText)
	return contextBlock, trajectory, nil
}

func formatShellAgentContext(stdoutText, stderrText string, cfg *llm.MCTPromptsConfig) (string, error) {
	stdout := strings.TrimSpace(stdoutText)
	stderr := strings.TrimSpace(stderrText)
	prefix := shellAgentContextPrefix
	if cfg != nil {
		if configured := strings.TrimSpace(cfg.ShellAgentContextPrefix); configured != "" {
			prefix = configured
		} else if embedded, err := templates.GetEmbeddedTemplate("mct.shell_agent_context_prefix"); err == nil && strings.TrimSpace(embedded) != "" {
			prefix = embedded
		}
	} else if embedded, err := templates.GetEmbeddedTemplate("mct.shell_agent_context_prefix"); err == nil && strings.TrimSpace(embedded) != "" {
		prefix = embedded
	}

	var tmpl string
	if cfg != nil {
		tmpl = strings.TrimSpace(cfg.ShellAgentContextTemplate)
	}
	if tmpl == "" {
		if embedded, err := templates.GetEmbeddedTemplate("mct.shell_agent_context_template"); err == nil {
			tmpl = embedded
		}
	}
	if strings.TrimSpace(tmpl) == "" {
		return "", fmt.Errorf("shell context template not configured")
	}
	data := map[string]any{
		"Prefix":    prefix,
		"Stdout":    stdout,
		"Stderr":    stderr,
		"HasStdout": stdout != "",
		"HasStderr": stderr != "",
	}
	rendered, err := prompts.Render("mct_shell_context", tmpl, data, nil)
	if err != nil {
		return "", fmt.Errorf("render shell context template: %w", err)
	}
	return strings.TrimSpace(rendered), nil
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

func interceptShellAgentStdout(r io.Reader, buf *bytes.Buffer, onToken func(string), emitAction func(string, string)) error {
	if r == nil {
		return nil
	}
	tmp := make([]byte, 4096)
	pending := make([]byte, 0, 4096)
	flush := func(data []byte) {
		if len(data) == 0 {
			return
		}
		buf.Write(data)
		if onToken != nil {
			onToken(string(data))
		}
	}
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			pending = append(pending, tmp[:n]...)
			for {
				idx := bytes.IndexByte(pending, '\n')
				if idx == -1 {
					break
				}
				line := pending[:idx+1]
				pending = pending[idx+1:]
				if handleShellActionLine(line, emitAction) {
					continue
				}
				flush(line)
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}
	if len(pending) > 0 {
		if handleShellActionLine(pending, emitAction) {
			return nil
		}
		flush(pending)
	}
	return nil
}

func handleShellActionLine(line []byte, emitAction func(string, string)) bool {
	trimmed := bytes.TrimRight(line, "\r\n")
	if !bytes.HasPrefix(trimmed, []byte(shellbridge.ActionPrefix)) {
		return false
	}
	payload := bytes.TrimSpace(trimmed[len(shellbridge.ActionPrefix):])
	if len(payload) == 0 {
		return true
	}
	var msg shellbridge.ActionMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return false
	}
	desc := strings.TrimSpace(msg.Description)
	cmd := strings.TrimSpace(msg.Command)
	if desc == "" && cmd == "" {
		return true
	}
	if emitAction != nil {
		emitAction(desc, cmd)
	}
	return true
}

func emitShellActionEvent(ctx context.Context, writer *trajectory.Writer, parentSpan string, desc, cmd string) {
	if writer == nil {
		return
	}
	if desc == "" && cmd == "" {
		return
	}
	payload := map[string]any{
		"event_version": 1,
	}
	if desc != "" {
		payload["description"] = desc
	}
	if cmd != "" {
		payload["command"] = cmd
	}
	evt := trajectory.Event{Kind: "shell-agent.action", ParentSpanID: strings.TrimSpace(parentSpan), Payload: payload}
	if err := writer.Emit(ctx, evt); err != nil {
		fmt.Fprintf(os.Stderr, "[trajectory] shell action emit error: %v\n", err)
	}
}

func firstNonEmptyOverrides(runtimes ...ModelRuntime) map[string]string {
	for _, rt := range runtimes {
		if len(rt.APIKeyOverrides) > 0 {
			return rt.APIKeyOverrides
		}
	}
	return nil
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
	mgr.SetPrompts(opts.Prompts)
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
			GlobalConfigPath:     opts.GlobalConfigPath,
			Prompts:              opts.Prompts,
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

func buildHeader(combined string, cfg *llm.MCTPromptsConfig) (string, error) {
	trimmed := strings.TrimSpace(combined)
	data := map[string]string{"Combined": combined}
	var tmpl string
	var name string
	var embeddedKey string
	if strings.HasPrefix(trimmed, "# User") {
		name = "mct_header_existing"
		embeddedKey = "mct.header_existing"
		if cfg != nil {
			tmpl = strings.TrimSpace(cfg.HeaderExistingTemplate)
		}
	} else {
		name = "mct_header_user"
		embeddedKey = "mct.header_user"
		if cfg != nil {
			tmpl = strings.TrimSpace(cfg.HeaderUserTemplate)
		}
	}
	if tmpl == "" {
		if embedded, err := templates.GetEmbeddedTemplate(embeddedKey); err == nil {
			tmpl = embedded
		}
	}
	if strings.TrimSpace(tmpl) == "" {
		return "", fmt.Errorf("%s template not configured", name)
	}
	rendered, err := prompts.Render(name, tmpl, data, nil)
	if err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return rendered, nil
}

func validateMCTPromptsConfig(cfg *llm.MCTPromptsConfig) error {
	if cfg == nil {
		cfg = &llm.MCTPromptsConfig{}
	}
	if strings.TrimSpace(cfg.ConversationHistoryTemplate) == "" {
		if embedded, err := templates.GetEmbeddedTemplate("mct.conversation_history_template"); err == nil {
			cfg.ConversationHistoryTemplate = embedded
		}
	}
	if strings.TrimSpace(cfg.HeaderUserTemplate) == "" {
		if embedded, err := templates.GetEmbeddedTemplate("mct.header_user"); err == nil {
			cfg.HeaderUserTemplate = embedded
		}
	}
	if strings.TrimSpace(cfg.HeaderExistingTemplate) == "" {
		if embedded, err := templates.GetEmbeddedTemplate("mct.header_existing"); err == nil {
			cfg.HeaderExistingTemplate = embedded
		}
	}
	if strings.TrimSpace(cfg.ShellAgentContextTemplate) == "" {
		if embedded, err := templates.GetEmbeddedTemplate("mct.shell_agent_context_template"); err == nil {
			cfg.ShellAgentContextTemplate = embedded
		}
	}
	if strings.TrimSpace(cfg.ReadmeSystemTemplate) == "" {
		if embedded, err := templates.GetEmbeddedTemplate("mct.readme_system_template"); err == nil {
			cfg.ReadmeSystemTemplate = embedded
		}
	}
	if strings.TrimSpace(cfg.ShellAgentContextPrefix) == "" {
		if embedded, err := templates.GetEmbeddedTemplate("mct.shell_agent_context_prefix"); err == nil {
			cfg.ShellAgentContextPrefix = embedded
		}
	}
	if strings.TrimSpace(cfg.ConversationHistoryTemplate) == "" {
		return fmt.Errorf("conversation history template is required")
	}
	if strings.TrimSpace(cfg.HeaderUserTemplate) == "" {
		return fmt.Errorf("header user template is required")
	}
	if strings.TrimSpace(cfg.HeaderExistingTemplate) == "" {
		return fmt.Errorf("header existing template is required")
	}
	if strings.TrimSpace(cfg.ShellAgentContextTemplate) == "" {
		return fmt.Errorf("shell agent context template is required")
	}
	if strings.TrimSpace(cfg.ReadmeSystemTemplate) == "" {
		return fmt.Errorf("readme system template is required")
	}
	return nil
}

func isClosedPipeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrClosed) {
		return true
	}
	if pe, ok := err.(*os.PathError); ok {
		return isClosedPipeError(pe.Err)
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "file already closed") || strings.Contains(msg, "use of closed file")
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

var tagFormatSnippetPattern = regexp.MustCompile(`\[(?P<path>[^\[\]|]+?)\s*\|\s*(?P<start>[^:\]]+)\s*:\s*(?P<end>[^\]]+)\]`)

type tagReference struct {
	Path      string
	StartLine int
	EndLine   int
}

func maybeInjectTagSnippets(answer string, directives []string, retrieved []string, verbose bool) (string, bool) {
	if !containsUseTagFormatDirective(directives) {
		return answer, false
	}
	if strings.TrimSpace(answer) == "" {
		return answer, false
	}
	indexes := tagFormatSnippetPattern.FindAllStringSubmatchIndex(answer, -1)
	if len(indexes) == 0 {
		return answer, false
	}

	retrievedSet := make(map[string]struct{}, len(retrieved))
	for _, path := range retrieved {
		norm := normalizeTagPath(path)
		if norm == "" {
			continue
		}
		retrievedSet[norm] = struct{}{}
	}
	if len(retrievedSet) == 0 {
		return answer, false
	}

	repoRoot := ""
	if cwd, err := os.Getwd(); err == nil {
		if root, err := git.RepoRoot(cwd); err == nil {
			repoRoot = root
		}
	}

	fileCache := make(map[string][]string)
	var b strings.Builder
	last := 0
	injected := 0

	for _, idxs := range indexes {
		if len(idxs) < 8 {
			continue
		}
		b.WriteString(answer[last:idxs[1]])
		ref, ok := buildTagReference(answer, idxs)
		if ok {
			if snippet, ok := renderTagSnippet(ref, repoRoot, fileCache, retrievedSet, verbose); ok {
				b.WriteString("\n\n")
				b.WriteString(snippet)
				if !strings.HasSuffix(snippet, "\n") {
					b.WriteString("\n")
				}
				injected++
			}
		}
		last = idxs[1]
	}
	b.WriteString(answer[last:])
	if injected == 0 {
		return answer, false
	}
	return b.String(), true
}

func containsUseTagFormatDirective(directives []string) bool {
	for _, d := range directives {
		if strings.TrimSpace(d) == "use_tag_format" {
			return true
		}
	}
	return false
}

func buildTagReference(answer string, idxs []int) (tagReference, bool) {
	pathRaw := strings.TrimSpace(answer[idxs[2]:idxs[3]])
	startRaw := strings.TrimSpace(answer[idxs[4]:idxs[5]])
	endRaw := strings.TrimSpace(answer[idxs[6]:idxs[7]])
	startLine, serr := strconv.Atoi(startRaw)
	endLine, eerr := strconv.Atoi(endRaw)
	if serr != nil || eerr != nil {
		return tagReference{}, false
	}
	if startLine <= 0 || endLine <= 0 || endLine < startLine {
		return tagReference{}, false
	}
	resolved := normalizeTagPath(pathRaw)
	if resolved == "" {
		return tagReference{}, false
	}
	return tagReference{
		Path:      resolved,
		StartLine: startLine,
		EndLine:   endLine,
	}, true
}

func renderTagSnippet(ref tagReference, repoRoot string, cache map[string][]string, retrieved map[string]struct{}, verbose bool) (string, bool) {
	if _, ok := retrieved[ref.Path]; !ok {
		return "", false
	}
	lines, ok := cache[ref.Path]
	if !ok {
		resolvedPath, err := resolveReferencePath(ref.Path, repoRoot)
		if err != nil {
			logTagVerbose(verbose, "snippet resolve %s: %v", ref.Path, err)
			return "", false
		}
		loaded, err := loadFileLinesForTag(resolvedPath)
		if err != nil {
			logTagVerbose(verbose, "snippet read %s: %v", resolvedPath, err)
			return "", false
		}
		cache[ref.Path] = loaded
		lines = loaded
	}
	if ref.StartLine > len(lines) {
		logTagVerbose(verbose, "snippet range %s %d:%d exceeds file (%d lines)", ref.Path, ref.StartLine, ref.EndLine, len(lines))
		return "", false
	}
	end := ref.EndLine
	if end > len(lines) {
		end = len(lines)
	}
	if end < ref.StartLine {
		return "", false
	}
	lang := contextbuilder.DetectFenceLanguage(ref.Path)
	var snippet strings.Builder
	snippet.WriteString("```")
	if lang != "" {
		snippet.WriteString(lang)
	}
	snippet.WriteString("\n")
	for line := ref.StartLine; line <= end; line++ {
		snippet.WriteString(lines[line-1])
		snippet.WriteString("\n")
	}
	snippet.WriteString("```\n")
	return snippet.String(), true
}

func resolveReferencePath(relPath, repoRoot string) (string, error) {
	resolved := filepath.FromSlash(relPath)
	if repoRoot == "" {
		return resolved, nil
	}
	candidate := filepath.Clean(filepath.Join(repoRoot, resolved))
	relCandidate, err := filepath.Rel(repoRoot, candidate)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(relCandidate, "..") {
		return "", fmt.Errorf("path escapes repo root")
	}
	return candidate, nil
}

func loadFileLinesForTag(resolvedPath string) ([]string, error) {
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, err
	}
	raw := strings.Split(string(data), "\n")
	if len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	for i, line := range raw {
		raw[i] = strings.TrimSuffix(line, "\r")
	}
	return raw, nil
}

func normalizeTagPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimPrefix(trimmed, "./")
	return filepath.ToSlash(trimmed)
}

func logTagVerbose(verbose bool, format string, args ...any) {
	if !verbose {
		return
	}
	fmt.Fprintf(os.Stderr, "[tag-format] "+format+"\n", args...)
}

func formatResponseDirectives(directives []string) string {
	if len(directives) == 0 {
		return ""
	}

	rules := make([]string, 0, len(directives))
	for _, raw := range directives {
		directive := strings.TrimSpace(raw)
		if directive == "" {
			continue
		}
		switch directive {
		case "use_tag_format":
			rules = append(rules, "Reference code or documents using `[path/to/file | start:end]` tags in lieu of reproducing file contents. Provide explicit start and end line numbers, avoid bare paths or Markdown links.")
		default:
			rules = append(rules, directive)
		}
	}
	if len(rules) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Response Rules:\n")
	for idx, rule := range rules {
		fmt.Fprintf(&b, "%d. %s\n", idx+1, rule)
	}

	return strings.TrimSpace(b.String())
}

func shouldLogPatchPrompt(opts RunOptions) bool {
	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	switch mode {
	case "patch", "patcher", "patch-json", "strict-patch":
		return true
	}
	for _, directive := range opts.ResponseDirectives {
		clean := strings.ToLower(strings.TrimSpace(directive))
		switch {
		case strings.Contains(clean, "patch"),
			strings.Contains(clean, "instruction"),
			strings.Contains(clean, "edits"),
			strings.Contains(clean, "hunk"):
			return true
		}
	}
	return false
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
	if len(rt.APIKeyOverrides) > 0 {
		return false
	}
	return true
}
