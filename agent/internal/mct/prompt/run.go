package prompt

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
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
	"github.com/tursomari/machtiani/agent/internal/templates"
)

var (
	chatStreamWithRuntime  = llm.ChatStreamWithResolvedFallback
	discoveryRunnerRun     = discoveryrunner.Run
	patchPromptLogger      = patchlog.WritePrompt
)

const defaultShellAgentPromptNotice = "I understand that I don't have access to a shell directly. The `shell-agent` will carry out my request and report back with explanation of the results and not necessarily the full output of commands it executes."

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
	assistantOverride := ""

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
	case isAnswerOnly:
		var buildErr error
		combined, included, buildErr = contextbuilder.Build(opts.Prompt, nil, hist, contextbuilder.Options{IncludeHistory: includeHistory, MaxInputTokens: opts.MaxInputTokens, PreludeTemplate: historyTemplate})
		if buildErr != nil {
			return res, buildErr
		}
	case opts.ShellAgent:
		// History is carried in pre-built messages; the combined
		// string is only used for the header template.
		combined = opts.Prompt
	case useBaselineContext:
		var err error
		combined, included, err = buildBaselinePromptContext(opts.Prompt, hist, filtered, includeHistory, opts.MaxInputTokens, opts.SessionID, opts.Verbose, historyTemplate)
		if err != nil {
			return res, err
		}
	default:
		options := contextbuilder.Options{IncludeHistory: includeHistory, MaxInputTokens: opts.MaxInputTokens, PreludeTemplate: historyTemplate}
		var buildErr error
		combined, included, buildErr = contextbuilder.Build(opts.Prompt, filtered, hist, options)
		if buildErr != nil {
			return res, buildErr
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

	shellAgentUsed := false
	shellAgentOutput := ""
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
		if opts.ShellAgentLibrary == nil {
			return res, fmt.Errorf("shell-agent library is required when shell-agent mode is enabled")
		}
		_, verbatimBlock, _, shellErr := runShellAgentLibrary(ctx, opts.Prompt, opts)
		if shellErr != nil {
			return res, shellErr
		}
		shellAgentUsed = true
		shellAgentOutput = verbatimBlock
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
	assistant := assistantOverride
	assistantFromShell := false
	if shellAgentOutput != "" {
		assistant = shellAgentOutput
		assistantFromShell = true
	}
	if assistant == "" {
		var err error
		assistant, err = chatStreamWithRuntime(chatCtx, answerRuntime.Resolved, answerRuntime.FallbackAliases, answerRuntime.FallbackResolved, copyExtrasMap(answerRuntime.Extras), messages, opts.OnToken)
		if err != nil {
			return res, err
		}
	} else if opts.OnToken != nil && !assistantFromShell {
		opts.OnToken(assistant)
	}
	if !assistantFromShell {
		if enriched, injected := maybeInjectTagSnippets(assistant, opts.ResponseDirectives, included, opts.Verbose); injected {
			assistant = enriched
		}
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
	res.FileDiscoveryRan = fileDiscoveryRan
	res.ShellAgentUsed = shellAgentUsed

	// History is now derived from conversation.json on read; the MCT prompt
	// layer no longer maintains a parallel on-disk record.

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
	builder.WriteString("Here are baseline-relative unified diffs for the referenced files:\n\n")
	builder.WriteString(diffBlock)

	return builder.String(), included, nil
}

func loadFullDiffsForSession(sessionID string, verbose bool) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", nil
	}
	conversationPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(conversationPath)
	if err != nil {
		if os.IsNotExist(err) {
			if verbose {
				fmt.Fprintf(os.Stderr, "[full-diff] conversation not found at %s\n", conversationPath)
			}
			return "", nil
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "[full-diff] read conversation failed: %v\n", err)
		}
		return "", err
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[full-diff] parse conversation failed: %v\n", err)
		}
		return "", err
	}
	fullDiffs := conversation.ExtractFullDiffs(conv)
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
	if strings.TrimSpace(cfg.ShellAgentPromptNotice) == "" {
		if embedded, err := templates.GetEmbeddedTemplate("mct.shell_agent_prompt_notice"); err == nil {
			cfg.ShellAgentPromptNotice = embedded
		}
	}
	if strings.TrimSpace(cfg.PatchSuccessNote) == "" {
		if embedded, err := templates.GetEmbeddedTemplate("mct.patch_success_note"); err == nil {
			cfg.PatchSuccessNote = embedded
		}
	}
	if strings.TrimSpace(cfg.FullDiffNote) == "" {
		if embedded, err := templates.GetEmbeddedTemplate("mct.full_diff_note"); err == nil {
			cfg.FullDiffNote = embedded
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

func shellAgentPromptNoticeText(cfg *llm.MCTPromptsConfig) string {
	if cfg != nil {
		if trimmed := strings.TrimSpace(cfg.ShellAgentPromptNotice); trimmed != "" {
			return trimmed
		}
	}
	if embedded, err := templates.GetEmbeddedTemplate("mct.shell_agent_prompt_notice"); err == nil {
		if trimmed := strings.TrimSpace(embedded); trimmed != "" {
			return trimmed
		}
	}
	return defaultShellAgentPromptNotice
}

// AppendShellAgentPromptNotice adds the shell-agent notice to a prompt if missing.
func AppendShellAgentPromptNotice(prompt string, cfg *llm.MCTPromptsConfig) string {
	notice := shellAgentPromptNoticeText(cfg)
	if strings.TrimSpace(notice) == "" {
		return prompt
	}
	if strings.TrimSpace(prompt) == "" {
		return notice
	}
	if strings.Contains(prompt, notice) {
		return prompt
	}
	return strings.TrimRight(prompt, "\n") + "\n\n" + notice
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
