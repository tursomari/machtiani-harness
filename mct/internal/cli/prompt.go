package cli

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/mct/internal/utils"
	"github.com/tursomari/machtiani/mct/llm"
	promptsvc "github.com/tursomari/machtiani/mct/prompt"
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

func handlePrompt(args []string) {
	fs := pflag.NewFlagSet("prompt", pflag.ContinueOnError)
	// Input source (exactly one required)
	fileFlag := fs.StringP("file", "f", "", "Path to the markdown file (required if no positional message provided)")
	// Supported flags
	modelFlag := fs.String("model", "", "Model alias defined in .machtiani/config.toml")
	openAIModelFlag := fs.String("openai-model", "", "Direct upstream model name (deprecated; prefer --model)")
	openAIAPIKeyFlag := fs.String("openai-api-key", "", "OpenAI-compatible API key (overrides env, deprecated)")
	openAIBaseURLFlag := fs.String("openai-base-url", "", "OpenAI-compatible base URL (overrides env, deprecated)")
	paramFlag := fs.StringArray("param", nil, "Additional request parameter key=value (repeatable)")
	paramJSONFlag := fs.StringArray("param-json", nil, "Merge JSON object of additional parameters (repeatable)")
	agentModelFlag := fs.String("agent-model", "", "Agent model for applying patches (defaults to --model)")
	sessionFlag := fs.String("session", "", "Session identifier used to scope conversation history")
	matchStrengthFlag := fs.String("match-strength", defaultMatchStrength, "Match strength: high | mid | low")
	modeFlag := fs.String("mode", defaultMode, "Mode: chat | pure-chat | answer-only | default")
	includeHistoryFlag := fs.Bool("include-history", false, "Include conversation history in the LLM prompt (internal use)")
	maxInputTokensFlag := fs.Int("max-input-tokens", 0, "Maximum number of tokens allowed in the constructed prompt (0 disables truncation)")
	// flags retained for compatibility in other subcommands; not used in local prompt path
	verboseFlag := fs.Bool("verbose", false, "Enable verbose output")
	// remote not needed for local prompt path

	_ = fs.MarkHidden("include-history")

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
	if session := strings.TrimSpace(*sessionFlag); session != "" {
		os.Setenv("MACHTIANI_SESSION_ID", session)
	}

	// Accept a single positional message if --file is not provided
	positionalMessage := ""
	if len(fs.Args()) == 1 && *fileFlag == "" {
		positionalMessage = fs.Args()[0]
	} else if len(fs.Args()) > 0 {
		fs.Usage()
		fmt.Fprintln(os.Stderr, "Error: unexpected positional arguments for 'prompt'. Provide a single message or use --file/-f.")
		os.Exit(2)
	}

	// Enforce exactly one of --file (or positional message)
	sources := 0
	if *fileFlag != "" {
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
	agentModelVal := *agentModelFlag
	if agentModelVal == "" {
		agentModelVal = *modelFlag
	}
	_ = agentModelVal
	_ = *matchStrengthFlag

	// Check if we're in answer-only mode early
	isAnswerOnlyMode := *modeFlag == "answer-only"

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
	if *fileFlag != "" {
		content, err := ioutil.ReadFile(*fileFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading markdown file: %v\n", err)
			os.Exit(1)
		}
		prompt = string(content)
	} else {
		// positional message case
		prompt = positionalMessage
	}

	paramPairs := append([]string(nil), (*paramFlag)...)
	paramJSON := append([]string(nil), (*paramJSONFlag)...)

	runtime, err := resolveModelRuntime(strings.TrimSpace(*modelFlag), strings.TrimSpace(*openAIBaseURLFlag), strings.TrimSpace(*openAIAPIKeyFlag), strings.TrimSpace(*openAIModelFlag), paramPairs, paramJSON)
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

	if *verboseFlag && *modeFlag != "answer-only" {
		printVerboseInfo(*fileFlag, runtime.displayName(), *matchStrengthFlag, *modeFlag, prompt)
	}

	ctx := context.Background()

	ms, _ := llm.NewMarkdownStreamer()
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

	modelRuntime := promptsvc.ModelRuntime{
		Resolved:   runtime.resolved,
		Alias:      runtime.alias,
		UsingAlias: runtime.usingAlias,
		Extras:     runtime.extras,
		ParamPairs: runtime.paramPairs,
		ParamJSON:  runtime.paramJSON,
	}
	result, err := promptsvc.Run(ctx, promptsvc.RunOptions{
		Prompt:               prompt,
		Mode:                 *modeFlag,
		IncludeHistory:       *includeHistoryFlag,
		SessionID:            sessionID,
		SourceFile:           *fileFlag,
		Runtime:              modelRuntime,
		FileDiscoveryRuntime: modelRuntime,
		OnHeader:             streamHeader,
		OnToken:              streamToken,
		Verbose:              *verboseFlag,
		MaxInputTokens:       *maxInputTokensFlag,
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

func printVerboseInfo(markdown, model, matchStrength, mode, prompt string) {
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
	fmt.Printf("  Model: %s\n", model)
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

type modelRuntime struct {
	resolved   llm.ResolvedModel
	alias      string
	usingAlias bool
	extras     map[string]any
	paramPairs []string
	paramJSON  []string
}

func (m modelRuntime) displayName() string {
	if m.usingAlias && strings.TrimSpace(m.alias) != "" {
		return m.alias
	}
	return m.resolved.Model
}

func resolveModelRuntime(aliasFlag, baseURLFlag, apiKeyFlag, directModelFlag string, paramPairs, paramJSON []string) (modelRuntime, error) {
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
		return runtime, nil
	}

	if defaultAlias, err := llm.DefaultModelAlias(); err == nil {
		if resolved, err2 := llm.ResolveModel(defaultAlias); err2 == nil {
			runtime.resolved = resolved
			runtime.alias = defaultAlias
			runtime.usingAlias = true
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
	return runtime, nil
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
