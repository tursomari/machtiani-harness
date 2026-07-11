package cli

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/git"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/readme"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/utils"
	promptsvc "github.com/tursomari/machtiani/agent/internal/mct/prompt"
	"github.com/tursomari/machtiani/agent/internal/presentation"
)

const (
	defaultMatchStrength = "mid"
	defaultMode          = "default"
)

// Function to create a visual separator
func createSeparator(message string) string {
	separator := strings.Repeat("=", 60)
	if message == "" { // Handle empty message for just a line break separator
		return fmt.Sprintf("\n%s\n", separator)
	}
	return fmt.Sprintf("\n%s\n%s\n%s\n", separator, message, separator)
}

type promptFlagValues struct {
	fileFlag           *string
	modelFlag          *string
	orchModelFlag      *string
	answerModelFlag    *string
	openAIModelFlag    *string
	openAIAPIKeyFlag   *string
	openAIBaseURLFlag  *string
	paramFlag          *[]string
	paramJSONFlag      *[]string
	agentModelFlag     *string
	sessionFlag        *string
	matchStrengthFlag  *string
	modeFlag           *string
	includeHistoryFlag *bool
	maxInputTokensFlag *int
	verboseFlag        *bool
	shellAgentFlag     *bool
}

func registerPromptFlags(fs *pflag.FlagSet) *promptFlagValues {
	f := &promptFlagValues{}
	f.fileFlag = fs.StringP("file", "f", "", "Path to the markdown file (required if no positional message provided)")
	f.modelFlag = fs.String("model", "", "Model alias defined in .machtiani/config.toml")
	f.orchModelFlag = fs.String("orch-model", "", "Fallback model alias to try if the primary model fails")
	f.answerModelFlag = fs.String("answer-model", "", "Model alias for answer generation (defaults to --model)")
	f.openAIModelFlag = fs.String("openai-model", "", "Direct upstream model name (deprecated; prefer --model)")
	f.openAIAPIKeyFlag = fs.String("openai-api-key", "", "OpenAI-compatible API key (overrides env, deprecated)")
	f.openAIBaseURLFlag = fs.String("openai-base-url", "", "OpenAI-compatible base URL (overrides env, deprecated)")
	f.paramFlag = fs.StringArray("param", nil, "Additional request parameter key=value (repeatable)")
	f.paramJSONFlag = fs.StringArray("param-json", nil, "Merge JSON object of additional parameters (repeatable)")
	f.agentModelFlag = fs.String("agent-model", "", "Agent model for applying patches (defaults to --model)")
	f.sessionFlag = fs.String("session", "", "Session identifier used to scope conversation history")
	f.matchStrengthFlag = fs.String("match-strength", defaultMatchStrength, "Match strength: high | mid | low")
	f.modeFlag = fs.String("mode", defaultMode, "Mode: chat | pure-chat | answer-only | default")
	f.includeHistoryFlag = fs.Bool("include-history", false, "Include conversation history in the LLM prompt (internal use)")
	f.maxInputTokensFlag = fs.Int("max-input-tokens", 0, "Maximum number of tokens allowed in the constructed prompt (0 disables truncation)")
	f.verboseFlag = fs.BoolP("verbose", "v", false, "Enable verbose output")
	f.shellAgentFlag = fs.Bool("shell-agent", false, "Enable shell-agent mode: invoke shell-agent subprocess binary for task execution")

	_ = fs.MarkHidden("include-history")
	_ = fs.MarkHidden("openai-model")
	_ = fs.MarkHidden("openai-api-key")
	_ = fs.MarkHidden("openai-base-url")
	return f
}

func handlePrompt(args []string) {
	fs := pflag.NewFlagSet("prompt", pflag.ContinueOnError)
	f := registerPromptFlags(fs)

	// Parse the flags from args (unknown flags should error)
	// Ensure Usage is non-nil and goes to stderr
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mct prompt TEXT | --file PATH [options]")
		fmt.Fprintln(os.Stderr, "\nOptions:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		fs.Usage()
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(2)
	}
	if session := strings.TrimSpace(*f.sessionFlag); session != "" {
		os.Setenv("MACHTIANI_SESSION_ID", session)
	}

	// Accept a single positional message if --file is not provided
	positionalMessage := ""
	if len(fs.Args()) == 1 && *f.fileFlag == "" {
		positionalMessage = fs.Args()[0]
	} else if len(fs.Args()) > 0 {
		fs.Usage()
		fmt.Fprintln(os.Stderr, "Error: unexpected positional arguments for 'prompt'. Provide a single message or use --file/-f.")
		os.Exit(2)
	}

	// Enforce exactly one of --file (or positional message)
	sources := 0
	if *f.fileFlag != "" {
		sources++
	}
	if positionalMessage != "" {
		sources++
	}
	if sources != 1 {
		fs.Usage()
		fmt.Fprintln(os.Stderr, "Error: exactly one of --file/-f or a single positional message is required for 'prompt'.")
		os.Exit(2)
	}

	// Use agent-model for patches if specified, otherwise fall back to model (unused in local prompt path)
	agentModelVal := *f.agentModelFlag
	if agentModelVal == "" {
		agentModelVal = *f.modelFlag
	}
	_ = agentModelVal
	_ = *f.matchStrengthFlag

	// Check if we're in answer-only mode early
	isAnswerOnlyMode := *f.modeFlag == "answer-only"

	// Suppress all logging output if mode is answer-only
	if isAnswerOnlyMode {
		log.SetOutput(ioutil.Discard)
	}

	// Print session id (if available)
	sessionID := os.Getenv("MACHTIANI_SESSION_ID")
	if sessionID != "" {
		utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Session ID: %s\n", sessionID)
	}

	// Derive prompt content from exactly one source
	var prompt string
	if *f.fileFlag != "" {
		content, err := ioutil.ReadFile(*f.fileFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading markdown file: %v\n", err)
			os.Exit(1)
		}
		prompt = string(content)
	} else {
		// positional message case
		prompt = positionalMessage
	}

	paramPairs := append([]string(nil), (*f.paramFlag)...)
	paramJSON := append([]string(nil), (*f.paramJSONFlag)...)

	runtime, err := resolveModelRuntime(strings.TrimSpace(*f.modelFlag), strings.TrimSpace(*f.orchModelFlag), strings.TrimSpace(*f.openAIBaseURLFlag), strings.TrimSpace(*f.openAIAPIKeyFlag), strings.TrimSpace(*f.openAIModelFlag), paramPairs, paramJSON)
	if err != nil {
		if miss, ok := err.(*missingConfigError); ok {
			fmt.Fprintln(os.Stderr, "Missing model config: set:")
			for _, item := range miss.items {
				fmt.Fprintln(os.Stderr, " - ", item)
			}
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "Error resolving model: %v\n", err)
		os.Exit(2)
	}

	answerAlias := strings.TrimSpace(*f.answerModelFlag)
	answerDisplay := runtime.displayName()
	var answerPromptRuntime promptsvc.ModelRuntime
	if answerAlias != "" {
		answerRuntime := runtime
		answerRuntime.alias = answerAlias
		answerRuntime.usingAlias = true
		answerRuntime.fallbackAliases = nil
		answerRuntime.fallbackResolved = nil
		if strings.TrimSpace(os.Getenv("MCT_LLM_TEST_STUB")) == "" {
			resolved, err := llm.ResolveModel(answerAlias)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving answer model: %v\n", err)
				os.Exit(2)
			}
			answerRuntime.resolved = resolved
		}
		answerDisplay = answerRuntime.displayName()
		answerPromptRuntime = toPromptModelRuntime(answerRuntime)
	}

	if *f.verboseFlag && *f.modeFlag != "answer-only" {
		printVerboseInfo(*f.fileFlag, runtime.displayName(), answerDisplay, *f.matchStrengthFlag, *f.modeFlag, prompt)
	}

	ctx := context.Background()

	globalConfig, _, configErr := llm.LoadGlobalConfig()
	if configErr != nil {
		fmt.Fprintf(os.Stderr, "Error loading UI theme: %v\n", configErr)
		os.Exit(2)
	}
	themeName := string(presentation.ProfileTerminal)
	if globalConfig.UI != nil {
		themeName = globalConfig.UI.Theme
	}
	uiTheme, themeErr := presentation.Resolve(themeName, os.Stdout)
	if themeErr != nil {
		fmt.Fprintf(os.Stderr, "Error resolving UI theme: %v\n", themeErr)
		os.Exit(2)
	}
	ms, _ := llm.NewMarkdownStreamer(uiTheme)
	streamHeader := func(chunk string) {
		if ms != nil {
			_ = ms.Feed(chunk)
		} else {
			fmt.Print(chunk)
		}
	}
	streamToken := func(tok string) {
		if ms != nil {
			_ = ms.Feed(tok)
		} else {
			fmt.Print(tok)
		}
	}

	primaryRuntime := toPromptModelRuntime(runtime)
	fileDiscoveryRuntime := primaryRuntime

	shellAgent := *f.shellAgentFlag
	shellAgentFlagChanged := fs.Changed("shell-agent")
	preflightReply := ""
	var preflightErr error
	if !shellAgentFlagChanged && !isAnswerOnlyMode {
		useShellAgent, reply, err := promptsvc.PreflightShellRouting(ctx, primaryRuntime, prompt)
		preflightReply = strings.TrimSpace(reply)
		preflightErr = err
		if preflightErr != nil {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, preflightErr, "preflight routing failed")
		}
		shellAgent = useShellAgent
		route := "shell-agent"
		if !shellAgent {
			route = "default"
		}
		switch {
		case preflightReply != "":
			utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "Preflight routing: %s (reply: %s)", route, preflightReply)
		case preflightErr != nil:
			utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "Preflight routing: %s (error fallback)", route)
		default:
			utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "Preflight routing: %s (empty reply)", route)
		}
	}
	if !isAnswerOnlyMode {
		modeIndicator := "mct:shell"
		reason := " (preflight: shell-agent – LLM replied 'shell', run commands)"
		if !shellAgent {
			modeIndicator = "mct:file"
			reason = " (preflight: retrieving relevant files and context)"
		}
		if shellAgentFlagChanged {
			modeIndicator = "mct:shell"
			reason = " (explicit shell-agent flag)"
			if !shellAgent {
				modeIndicator = "mct:file"
				reason = " (explicit default-mode flag)"
			}
		}
		utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "[%s]%s\n", modeIndicator, reason)
	}

	var readmeOpts *promptsvc.ReadmeOptions
	skipReadme := strings.TrimSpace(os.Getenv(readme.SkipReadmeManagerEnv)) != ""
	if !skipReadme && !isAnswerOnlyMode {
		if headCommit, err := git.GetHeadCommitHash(); err == nil {
			readmeOpts = &promptsvc.ReadmeOptions{Enabled: true, ProjectCommitSHA: headCommit}
		} else if err != nil && !strings.Contains(strings.ToLower(err.Error()), "not a git repository") {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Failed to determine project commit for README management")
		}
	}
	result, err := promptsvc.Run(ctx, promptsvc.RunOptions{
		Prompt:               prompt,
		Mode:                 *f.modeFlag,
		IncludeHistory:       *f.includeHistoryFlag,
		SessionID:            sessionID,
		SourceFile:           *f.fileFlag,
		Runtime:              primaryRuntime,
		AnswerRuntime:        answerPromptRuntime,
		FileDiscoveryRuntime: fileDiscoveryRuntime,
		OnHeader:             streamHeader,
		OnToken:              streamToken,
		Verbose:              *f.verboseFlag,
		MaxInputTokens:       *f.maxInputTokensFlag,
		Readme:               readmeOpts,
		ShellAgent:           shellAgent,
	})
	if ms != nil {
		_ = ms.Flush()
	}
	if err != nil {
		log.Fatalf("Error executing prompt: %v", err)
	}

	if isAnswerOnlyMode {
		return
	}

	utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "%s", createSeparator("Saving Chat Response"))
	if result.SaveError != nil {
		log.Printf("Error creating markdown file '%s': %v", result.Filename+".md", result.SaveError)
		fmt.Println("\n--- Start Fallback Response Output ---")
		fmt.Println(result.FullText)
		fmt.Println("--- End Fallback Response Output ---")
		return
	}
	fmt.Printf("Response saved to %s\n", result.SavedPath)
}

// printVerboseInfo - unchanged

func printVerboseInfo(markdown, plannerModel, answerModel, matchStrength, mode, prompt string) {
	ignoreFiles, err := utils.ReadIgnoreFile(".machtiani.ignore")
	if err != nil {
		log.Printf("Warning: Error loading ignore file for verbose info: %v", err)
	} else {
		// Print the file paths
		fmt.Println("Parsed file paths from machtiani.ignore:")
		if len(ignoreFiles) > 0 {
			for _, path := range ignoreFiles {
				fmt.Printf("  %s\n", path)
			}
		} else {
			fmt.Println("  (No ignore rules found or file doesn't exist)")
		}
	}

	fmt.Println("Arguments passed:")
	fmt.Printf("  Markdown file: %s\n", markdown)
	plannerDisplay := strings.TrimSpace(plannerModel)
	if plannerDisplay == "" {
		plannerDisplay = "(unresolved)"
	}
	answerDisplay := strings.TrimSpace(answerModel)
	if answerDisplay == "" {
		answerDisplay = "(uses planner model)"
	}
	fmt.Printf("  Planner model: %s\n", plannerDisplay)
	fmt.Printf("  Answer model: %s\n", answerDisplay)
	fmt.Printf("  Match strength: %s\n", matchStrength)
	fmt.Printf("  Mode: %s\n", mode)
	// Truncate long prompts in verbose output?
	maxPromptLen := 200
	truncatedPrompt := prompt
	if len(prompt) > maxPromptLen {
		truncatedPrompt = prompt[:maxPromptLen] + "..."
	}
	fmt.Printf("  Prompt: %s\n", truncatedPrompt)
	// If you want to print token counts here, use:
	// fmt.Printf("  Embedding tokens: %s\n", utils.FormatIntWithCommas(embeddingTokens))
	// fmt.Printf("  Inference tokens: %s\n", utils.FormatIntWithCommas(inferenceTokens))
}

func utilsFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func cloneResolvedModels(in []llm.ResolvedModel) []llm.ResolvedModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]llm.ResolvedModel, 0, len(in))
	for _, m := range in {
		out = append(out, llm.CloneResolvedModel(m))
	}
	return out
}

func toPromptModelRuntime(rt modelRuntime) promptsvc.ModelRuntime {
	return promptsvc.ModelRuntime{
		Resolved:         llm.CloneResolvedModel(rt.resolved),
		Alias:            rt.alias,
		UsingAlias:       rt.usingAlias,
		Extras:           cloneExtrasMap(rt.extras),
		ParamPairs:       append([]string(nil), rt.paramPairs...),
		ParamJSON:        append([]string(nil), rt.paramJSON...),
		FallbackAliases:  append([]string(nil), rt.fallbackAliases...),
		FallbackResolved: cloneResolvedModels(rt.fallbackResolved),
	}
}

func cloneExtrasMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

type modelRuntime struct {
	resolved         llm.ResolvedModel
	alias            string
	usingAlias       bool
	extras           map[string]any
	paramPairs       []string
	paramJSON        []string
	fallbackAliases  []string
	fallbackResolved []llm.ResolvedModel
}

func (m modelRuntime) displayName() string {
	if m.usingAlias && strings.TrimSpace(m.alias) != "" {
		return m.alias
	}
	return m.resolved.Model
}

func resolveModelRuntime(aliasFlag, orchAliasFlag, baseURLFlag, apiKeyFlag, directModelFlag string, paramPairs, paramJSON []string) (modelRuntime, error) {
	extra, err := llm.ParseParamOverrides(paramPairs, paramJSON)
	if err != nil {
		return modelRuntime{}, err
	}
	runtime := modelRuntime{
		extras:     extra,
		paramPairs: append([]string(nil), paramPairs...),
		paramJSON:  append([]string(nil), paramJSON...),
	}

	if strings.TrimSpace(os.Getenv("MCT_LLM_TEST_STUB")) != "" {
		return runtime, nil
	}

	hasDirectFlags := strings.TrimSpace(baseURLFlag) != "" || strings.TrimSpace(apiKeyFlag) != "" || strings.TrimSpace(directModelFlag) != ""
	alias := strings.TrimSpace(aliasFlag)
	orchAlias := strings.TrimSpace(orchAliasFlag)

	directBaseURL := utilsFirstNonEmpty(
		strings.TrimSpace(baseURLFlag),
		strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
	)
	directAPIKey := utilsFirstNonEmpty(
		strings.TrimSpace(apiKeyFlag),
		strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
	)
	directModel := utilsFirstNonEmpty(
		strings.TrimSpace(directModelFlag),
		strings.TrimSpace(os.Getenv("OPENAI_MODEL")),
	)

	if hasDirectFlags {
		missing := missingDirect(directAPIKey, directBaseURL, directModel)
		if len(missing) > 0 {
			return runtime, &missingConfigError{items: missing}
		}
		resolved, err := llm.NewDirectModel(directBaseURL, directAPIKey, directModel)
		if err != nil {
			return runtime, err
		}
		runtime.resolved = resolved
		runtime.usingAlias = false
		runtime = enrichFallbacks(runtime, alias, orchAlias, directBaseURL, directAPIKey, directModel)
		return runtime, nil
	}

	if alias != "" {
		resolved, err := llm.ResolveModel(alias)
		if err != nil {
			return runtime, err
		}
		runtime.resolved = resolved
		runtime.alias = alias
		runtime.usingAlias = true
		runtime = enrichFallbacks(runtime, alias, orchAlias, directBaseURL, directAPIKey, directModel)
		return runtime, nil
	}

	if defaultAlias, err := llm.DefaultModelAlias(); err == nil {
		if resolved, err2 := llm.ResolveModel(defaultAlias); err2 == nil {
			runtime.resolved = resolved
			runtime.alias = defaultAlias
			runtime.usingAlias = true
			runtime = enrichFallbacks(runtime, alias, orchAlias, directBaseURL, directAPIKey, directModel)
			return runtime, nil
		}
	}

	missing := missingDirect(directAPIKey, directBaseURL, directModel)
	if len(missing) > 0 {
		return runtime, &missingConfigError{items: missing}
	}
	resolved, err := llm.NewDirectModel(directBaseURL, directAPIKey, directModel)
	if err != nil {
		return runtime, err
	}
	runtime.resolved = resolved
	runtime.usingAlias = false
	runtime = enrichFallbacks(runtime, alias, orchAlias, directBaseURL, directAPIKey, directModel)
	return runtime, nil
}

func enrichFallbacks(rt modelRuntime, aliasFlag, orchAliasFlag, directBaseURL, directAPIKey, directModel string) modelRuntime {
	primaryAlias := strings.TrimSpace(rt.alias)
	candidates := []string{strings.TrimSpace(aliasFlag), strings.TrimSpace(orchAliasFlag)}
	fallbackAliases := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if primaryAlias != "" && strings.EqualFold(candidate, primaryAlias) {
			continue
		}
		if containsFold(fallbackAliases, candidate) {
			continue
		}
		fallbackAliases = append(fallbackAliases, candidate)
	}
	rt.fallbackAliases = fallbackAliases

	trimmedModel := strings.TrimSpace(directModel)
	trimmedBase := strings.TrimSpace(directBaseURL)
	trimmedKey := strings.TrimSpace(directAPIKey)
	if trimmedModel != "" && trimmedBase != "" && trimmedKey != "" {
		if resolved, err := llm.NewDirectModel(trimmedBase, trimmedKey, trimmedModel); err == nil {
			rt.fallbackResolved = append(rt.fallbackResolved, resolved)
		}
	}
	return rt
}

func containsFold(list []string, val string) bool {
	for _, item := range list {
		if strings.EqualFold(item, val) {
			return true
		}
	}
	return false
}

func missingDirect(apiKey, baseURL, model string) []string {
	var missing []string
	if strings.TrimSpace(apiKey) == "" {
		missing = append(missing, "--openai-api-key or OPENAI_API_KEY")
	}
	if strings.TrimSpace(baseURL) == "" {
		missing = append(missing, "--openai-base-url or OPENAI_BASE_URL")
	}
	if strings.TrimSpace(model) == "" {
		missing = append(missing, "--openai-model or OPENAI_MODEL")
	}
	return missing
}

type missingConfigError struct {
	items []string
}

func (e *missingConfigError) Error() string {
	return "missing model configuration"
}
