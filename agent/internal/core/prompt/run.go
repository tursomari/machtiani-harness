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

	"github.com/tursomari/machtiani/agent/internal/core/contextbuilder"
	"github.com/tursomari/machtiani/agent/internal/core/discoveryrunner"
	"github.com/tursomari/machtiani/agent/internal/core/naming"
	"github.com/tursomari/machtiani/agent/internal/core/readme"
	"github.com/tursomari/machtiani/agent/internal/core/session"
	"github.com/tursomari/machtiani/agent/internal/core/utils"
	"github.com/tursomari/machtiani/agent/internal/git"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/prompts"
	"github.com/tursomari/machtiani/agent/internal/templates"
)

var (
	chatStreamWithRuntime = llm.ChatStreamWithResolvedFallback
	discoveryRunnerRun    = discoveryrunner.Run
	shellAgentRun         = runShellAgentWithInterception
)

const defaultShellAgentPromptNotice = "I understand that I don't have access to a shell directly. The `shell-agent` will carry out my request and report back with explanation of the results and not necessarily the full output of commands it executes."

// Run executes the core prompt flow used by Machtiani.
// It handles context building, file discovery, streaming, transcript
// persistence, and chat file saving based on the provided options.
func Run(ctx context.Context, opts RunOptions) (Result, error) {
	var res Result

	mode := strings.TrimSpace(opts.Mode)
	if mode == "" {
		mode = "default"
	}
	isAnswerOnly := mode == "answer-only"
	answerRuntime := opts.AnswerRuntime
	if runtimeIsZero(answerRuntime) {
		answerRuntime = opts.Runtime
	}
	budget, err := llm.ResolveInputBudgetForChain(answerRuntime.Resolved, answerRuntime.FallbackResolved, opts.ContextLength)
	if err != nil {
		return res, err
	}
	maxInputTokens := budget.MaxInputTokens

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

	if err := validateMCTPromptsConfig(opts.Prompts); err != nil {
		return res, err
	}

	var historyTemplate string
	if opts.Prompts != nil {
		historyTemplate = opts.Prompts.ConversationHistoryTemplate
	}

	// Transform raw prompt into decision-based instruction for session resumption
	if opts.PromptMaterial == nil && opts.SessionID != "" {
		historyNote := ""
		if len(hist) > 0 {
			historyNote = "Review the session history above and "
		}
		opts.Prompt = fmt.Sprintf(
			"Resume this session. %sAnalyze the conversation history and decide what action to take or how to respond: %s",
			historyNote, opts.Prompt)
	}
	answerMaterialTruncated := false
	if opts.PromptMaterial != nil {
		fitted, fitErr := opts.PromptMaterial.Render(maxInputTokens)
		if fitErr != nil {
			return res, fmt.Errorf("answer context budget: %w", fitErr)
		}
		opts.Prompt = fitted.Text
		answerMaterialTruncated = fitted.Truncated
	}
	combined := opts.Prompt
	included := []string(nil)
	filtered := []string(nil)
	fileDiscoveryRan := false

	if !isAnswerOnly && !opts.ShellAgent {
		ignoreFiles, err := utils.ReadIgnoreFile(".machtiani.ignore")
		if err != nil {
			return res, fmt.Errorf("load ignore rules: %w", err)
		}

		fdRuntime := opts.FileDiscoveryRuntime
		if strings.TrimSpace(fdRuntime.Resolved.Model) == "" {
			fdRuntime = opts.Runtime
		}
		discoveryBudget, err := llm.ResolveInputBudgetForChain(fdRuntime.Resolved, fdRuntime.FallbackResolved, opts.ContextLength)
		if err != nil {
			return res, fmt.Errorf("discovery context budget: %w", err)
		}
		discoveryCue := opts.Prompt
		discoveryTruncated := false
		if opts.PromptMaterial != nil {
			cueLimit, limitErr := discoveryrunner.InitialPromptTokenLimit(discoveryBudget)
			if limitErr != nil {
				return res, limitErr
			}
			fitted, fitErr := opts.PromptMaterial.Render(cueLimit)
			if fitErr != nil {
				return res, fmt.Errorf("discovery context budget: %w", fitErr)
			}
			discoveryCue = fitted.Text
			discoveryTruncated = fitted.Truncated
		} else {
			discoveryCue, discoveryTruncated, err = discoveryrunner.FitInitialPrompt(discoveryCue, discoveryBudget)
			if err != nil {
				return res, err
			}
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
			TurnTimeout:        opts.TurnTimeout,
			InputBudget:        discoveryBudget,
			PromptTruncated:    discoveryTruncated,
		}
		discoCtx := llm.WithStage(llm.WithAPIKeyOverrides(ctx, fdRuntime.APIKeyOverrides), "file-discovery")
		llm.EmitContextBudgetDetails(discoCtx, fdRuntime.Resolved, discoveryBudget, map[string]any{
			"applied_timeout_sec": opts.TurnTimeout,
			"truncated":           discoveryTruncated,
		})
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "llm.context_budget.resolved context_length=%d source=%s max_input_tokens=%d stage=file-discovery model_alias=%s applied_timeout_sec=%d truncated=%t\n", discoveryBudget.ContextLength, discoveryBudget.Source, discoveryBudget.MaxInputTokens, fdRuntime.Resolved.Alias, opts.TurnTimeout, discoveryTruncated)
		}
		dr, err := discoveryRunnerRun(discoCtx, discoveryCue, drModel, opts.SessionID, opts.Verbose)
		if err != nil {
			return res, fmt.Errorf("file discovery: %w", err)
		}
		filtered = filterPaths(dr.Paths, ignoreFiles)
		fileDiscoveryRan = true
	}

	directiveBlock := formatResponseDirectives(opts.ResponseDirectives)
	buildCombined := func(inputLimit int) (string, []string, error) {
		contentLimit := inputLimit
		if !opts.ShellAgent && inputLimit > 0 && directiveBlock != "" {
			contentLimit -= llm.EstimateTokens("\n\n" + directiveBlock)
			if contentLimit <= 0 {
				return "", nil, fmt.Errorf("answer context budget %d cannot fit required response directives", inputLimit)
			}
		}
		var value string
		var files []string
		var buildErr error
		switch {
		case isAnswerOnly:
			value, files, buildErr = contextbuilder.Build(opts.Prompt, nil, hist, contextbuilder.Options{IncludeHistory: includeHistory, MaxInputTokens: contentLimit, PreludeTemplate: historyTemplate})
		case opts.ShellAgent:
			value = opts.Prompt
		default:
			value, files, buildErr = contextbuilder.Build(opts.Prompt, filtered, hist, contextbuilder.Options{IncludeHistory: includeHistory, MaxInputTokens: contentLimit, PreludeTemplate: historyTemplate})
		}
		if buildErr != nil {
			return "", nil, buildErr
		}
		if directiveBlock != "" {
			if strings.TrimSpace(value) != "" {
				value += "\n\n" + directiveBlock
			} else {
				value = directiveBlock
			}
		}
		if !opts.ShellAgent {
			if err := llm.RequireWithinTokenBudget("answer request", value, inputLimit); err != nil {
				return "", nil, err
			}
		}
		return value, files, nil
	}
	combined, included, err = buildCombined(maxInputTokens)
	if err != nil {
		return res, err
	}
	res.DirectiveBlock = directiveBlock
	answerTruncated := answerMaterialTruncated || strings.Contains(combined, "TRUNCATED")
	answerCtx := llm.WithStage(ctx, "answer")
	llm.EmitContextBudgetDetails(answerCtx, answerRuntime.Resolved, budget, map[string]any{"truncated": answerTruncated})
	if opts.Verbose {
		fmt.Fprintf(os.Stderr, "llm.context_budget.resolved context_length=%d source=%s max_input_tokens=%d stage=answer model_alias=%s truncated=%t\n", budget.ContextLength, budget.Source, budget.MaxInputTokens, answerRuntime.Resolved.Alias, answerTruncated)
	}

	shellAgentUsed := false
	shellAgentOutput := ""
	shellAgentTrajectoryPath := ""
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
		if opts.ShellAgentRequest == nil {
			return res, fmt.Errorf("shell-agent request is required when shell-agent mode is enabled")
		}
		req := *opts.ShellAgentRequest
		req.Task = opts.Prompt
		result, shellErr := shellAgentRun(ctx, req)
		if shellErr != nil {
			return res, shellErr
		}
		shellAgentTrajectoryPath = result.TrajectoryPath
		res.ShellAgentTrajectoryMessages = result.Trajectory.Messages
		if result.Error != nil {
			res.ShellAgentTrajectoryPath = shellAgentTrajectoryPath
			return res, fmt.Errorf("shell-agent failed: %w", result.Error)
		}
		if result.ExitStatus != "Submitted" {
			res.ShellAgentCancelled = true
			res.ShellAgentTrajectoryPath = shellAgentTrajectoryPath
			res.ShellAgentTrajectoryMessages = result.Trajectory.Messages
			if result.ExitStatus == "" {
				return res, nil
			}
			shellAgentUsed = true
			shellAgentOutput = result.Answer
			return res, nil
		}
		verbatimBlock := strings.TrimSpace(result.Answer)
		if verbatimBlock == "" {
			res.ShellAgentTrajectoryPath = shellAgentTrajectoryPath
			res.ShellAgentTrajectoryMessages = result.Trajectory.Messages
			return res, fmt.Errorf("shell-agent submitted an empty final answer")
		}
		shellAgentUsed = true
		shellAgentOutput = verbatimBlock
		res.ShellAgentCancelled = false
		if shellAgentTrajectoryPath != "" {
			res.ShellAgentTrajectoryPath = shellAgentTrajectoryPath
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
	chatCtx := llm.WithStage(llm.WithAPIKeyOverrides(ctx, answerRuntime.APIKeyOverrides), "answer")
	assistant := ""
	assistantFromShell := false
	if shellAgentOutput != "" {
		assistant = shellAgentOutput
		assistantFromShell = true
	}
	if assistant == "" {
		currentBudget := budget
		for reduction := 0; ; reduction++ {
			emitted := false
			onToken := func(token string) {
				if token != "" {
					emitted = true
				}
				if opts.OnToken != nil {
					opts.OnToken(token)
				}
			}
			assistant, err = chatStreamWithRuntime(chatCtx, answerRuntime.Resolved, answerRuntime.FallbackAliases, answerRuntime.FallbackResolved, copyExtrasMap(answerRuntime.Extras), messages, onToken)
			if err == nil {
				if reduction > 0 {
					persisted, persistErr := llm.PersistLearnedContext(answerRuntime.Resolved, budget.ContextLength, currentBudget.ContextLength)
					llm.EmitContextAdjustment(chatCtx, answerRuntime.Resolved, budget.ContextLength, currentBudget.ContextLength, persisted, persistErr)
					fmt.Fprintf(os.Stderr, "Warning: provider rejected context length %d; retry succeeded at %d tokens", budget.ContextLength, currentBudget.ContextLength)
					if persistErr != nil {
						fmt.Fprintf(os.Stderr, "; configuration update failed: %v\n", persistErr)
					} else if persisted {
						fmt.Fprintln(os.Stderr, "; configuration updated.")
					} else {
						fmt.Fprintln(os.Stderr, "; configuration was not changed.")
					}
				}
				break
			}
			if emitted || !llm.IsContextOverflow(err) || reduction >= 4 {
				return res, err
			}
			nextInput := currentBudget.MaxInputTokens / 2
			nextLength := llm.ContextLengthForInputCap(nextInput)
			currentBudget, err = llm.BudgetForContextLength(nextLength, llm.ContextSourceRuntimeLearned)
			if err != nil {
				return res, err
			}
			combined, included, err = buildCombined(currentBudget.MaxInputTokens)
			if err != nil {
				return res, err
			}
			messages = []llm.Message{{Role: "user", Content: combined}}
			llm.EmitContextBudget(chatCtx, answerRuntime.Resolved, currentBudget)
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
	mgr.SetPromptExecutor(func(execCtx context.Context, material llm.PromptMaterial) (string, error) {
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
			Mode:                 "default",
			IncludeHistory:       false,
			SessionID:            innerSessionID,
			ExplicitName:         "internal-readme",
			Runtime:              opts.Runtime,
			AnswerRuntime:        opts.AnswerRuntime,
			FileDiscoveryRuntime: opts.FileDiscoveryRuntime,
			Verbose:              opts.Verbose,
			ContextLength:        opts.ContextLength,
			TurnTimeout:          opts.TurnTimeout,
			GlobalConfigPath:     opts.GlobalConfigPath,
			Prompts:              opts.Prompts,
			PromptMaterial:       &material,
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
