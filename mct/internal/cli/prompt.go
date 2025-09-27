package cli

import (
	"context"
	"fmt"
	"io/ioutil"

	"log"
	"os"
	//"os/exec" // No longer needed here for git apply
	"path"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/mct/internal/contextbuilder"
	"github.com/tursomari/machtiani/mct/internal/discoveryrunner"
	"github.com/tursomari/machtiani/mct/internal/naming"
	"github.com/tursomari/machtiani/mct/internal/session"
	"github.com/tursomari/machtiani/mct/internal/utils"
	"github.com/tursomari/machtiani/mct/llm"
)

const (
	defaultMatchStrength = "mid"
	defaultMode          = "default"
)

const (
	CONTENT_TYPE_KEY     = "Content-Type"
	CONTENT_TYPE_VALUE   = "application/json"
	API_GATEWAY_HOST_KEY = "X-RapidAPI-Key"
)

// Function to create a visual separator
func createSeparator(message string) string {
	separator := strings.Repeat("=", 60)
	if message == "" { // Handle empty message for just a line break separator
		return fmt.Sprintf("\n%s\n", separator)
	}
	return fmt.Sprintf("\n%s\n%s\n%s\n", separator, message, separator)
}

func handlePrompt(args []string, config *utils.Config, apiKey *string, headCommitHash string) {
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
	// flags retained for compatibility in other subcommands; not used in local prompt path
	verboseFlag := fs.Bool("verbose", false, "Enable verbose output")
	// remote not needed for local prompt path

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

	// New local pipeline: discovery -> context -> direct LLM
	var rawResponse string
	var retrievedFilePaths []string

	cfg := config // already loaded by caller

	paramPairs := append([]string(nil), (*paramFlag)...)
	paramJSON := append([]string(nil), (*paramJSONFlag)...)

	runtime, err := resolveModelRuntime(cfg, strings.TrimSpace(*modelFlag), strings.TrimSpace(*openAIBaseURLFlag), strings.TrimSpace(*openAIAPIKeyFlag), strings.TrimSpace(*openAIModelFlag), paramPairs, paramJSON)
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

	if isAnswerOnlyMode {
		// No discovery; include conversation history for continuity.
		hist, _ := session.LoadHistory()
		combined, included := contextbuilder.Build(prompt, nil, hist, contextbuilder.Options{})

		ms, _ := llm.NewMarkdownStreamer()
		header := combined
		if strings.HasPrefix(strings.TrimSpace(combined), "# User") {
			header = fmt.Sprintf("%s\n# Assistant\n\n", combined)
		} else {
			header = fmt.Sprintf("# User\n\n%s\n\n# Assistant\n\n", combined)
		}
		// Print header rendered
		if ms != nil {
			_ = ms.Feed(header)
		}
		messages := []llm.Message{{Role: "user", Content: combined}}
		full, err := llm.ChatStreamWithResolved(ctx, runtime.resolved, runtime.extras, messages, func(tok string) {
			if ms != nil {
				_ = ms.Feed(tok)
			} else {
				fmt.Print(tok)
			}
		})
		if ms != nil {
			_ = ms.Flush()
		}
		if err != nil {
			log.Fatalf("Error calling LLM: %v", err)
		}
		rawResponse = header + full

		// Persist history even in answer-only mode
		_ = session.AddMessage("user", prompt, nil)
		_ = session.AddMessage("assistant", full, included)
	} else {
		// Run discovery
		drModel := discoveryrunner.ModelSettings{
			UsingAlias: runtime.usingAlias,
			Alias:      runtime.alias,
			Resolved:   runtime.resolved,
			ParamPairs: runtime.paramPairs,
			ParamJSON:  runtime.paramJSON,
		}
		dr, err := discoveryrunner.Run(ctx, prompt, drModel, sessionID, *verboseFlag)
		if err != nil {
			log.Fatalf("Error running local file discovery: %v", err)
		}
		// Filter via .machtiani.ignore
		_, ignoreFiles, _ := utils.LoadConfigAndIgnoreFiles()
		filtered := filterPaths(dr.Paths, ignoreFiles)
		retrievedFilePaths = filtered

		// Load conversation history and build combined prompt
		hist, _ := session.LoadHistory()
		combined, included := contextbuilder.Build(prompt, filtered, hist, contextbuilder.Options{})

		// Stream chat
		ms, _ := llm.NewMarkdownStreamer()
		header := combined
		if strings.HasPrefix(strings.TrimSpace(combined), "# User") {
			header = fmt.Sprintf("%s\n# Assistant\n\n", combined)
		} else {
			header = fmt.Sprintf("# User\n\n%s\n\n# Assistant\n\n", combined)
		}
		if ms != nil {
			_ = ms.Feed(header)
		}
		messages := []llm.Message{{Role: "user", Content: combined}}
		full, err := llm.ChatStreamWithResolved(ctx, runtime.resolved, runtime.extras, messages, func(tok string) {
			if ms != nil {
				_ = ms.Feed(tok)
			} else {
				fmt.Print(tok)
			}
		})
		if ms != nil {
			_ = ms.Flush()
		}
		if err != nil {
			log.Fatalf("Error calling LLM: %v", err)
		}
		rawResponse = header + full

		// Persist updated history
		_ = session.AddMessage("user", prompt, nil)
		_ = session.AddMessage("assistant", full, included)

		// Append Retrieved File Paths section to rawResponse
		retrievedFilePaths = included
		if len(retrievedFilePaths) > 0 {
			var b strings.Builder
			b.WriteString("\n\n---\n\n# Retrieved File Paths\n\n")
			for _, p := range retrievedFilePaths {
				b.WriteString(fmt.Sprintf("- %s\n", p))
			}
			rawResponse += b.String()
		}
	}

	// In answer-only mode, we don't need to generate any filename
	var filename string
	if !isAnswerOnlyMode {
		// Determine the filename to save the response
		filename = path.Base(*fileFlag)

		// Strip all extensions from the filename
		for ext := path.Ext(filename); ext != ""; ext = path.Ext(filename) {
			filename = strings.TrimSuffix(filename, ext)
		}

		// Generate a filename if necessary
		if filename == "" || filename == "." {
			filename = naming.Generate(ctx, prompt, runtime.resolved)
		}
	}

	utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "%s", createSeparator("Saving Chat Response"))

	// Handle the final response with structured data, passing the isAnswerOnlyMode flag
	handleAPIResponse(prompt, rawResponse, retrievedFilePaths, filename, *fileFlag, isAnswerOnlyMode)
}

func handleAPIResponse(prompt, openaiResponse string, retrievedFilePaths []string, filename, fileFlag string, isAnswerOnlyMode bool) {
	// In answer-only mode, just print the raw response without any file operations
	if isAnswerOnlyMode {
		return
	}

	// For other modes, continue with file creation and structured output
	var finalContent string
	finalContent = openaiResponse

	tempFile, err := utils.CreateTempMarkdownFile(finalContent, filename)
	if err != nil {
		log.Printf("Error creating markdown file '%s': %v", filename+".md", err)
		fmt.Println("\n--- Start Fallback Response Output ---")
		fmt.Println(finalContent)
		fmt.Println("--- End Fallback Response Output ---")
		return
	}

	fmt.Printf("Response saved to %s\n", tempFile)
}

// Remote generateFilename removed; replaced by naming.Generate()

// createMarkdownContent - unchanged
func createMarkdownContent(prompt, openAIResponse string, retrievedFilePaths []string, fileFlag string) string {
	var markdownContent string
	if fileFlag != "" {
		// Ensure reading the file doesn't cause a fatal error if it fails here
		// It should have been read successfully earlier in handlePrompt
		content, err := ioutil.ReadFile(fileFlag)
		if err != nil {
			log.Printf("Warning: could not re-read markdown file %s for content creation: %v", fileFlag, err)
			// Fallback to just using the prompt string if file read fails here
			markdownContent = fmt.Sprintf("# User\n\n%s\n\n# Assistant\n\n%s", prompt, openAIResponse)
		} else {
			markdownContent = fmt.Sprintf("%s\n\n# Assistant\n\n%s", string(content), openAIResponse)
		}
	} else {
		markdownContent = fmt.Sprintf("# User\n\n%s\n\n# Assistant\n\n%s", prompt, openAIResponse)
	}

	if len(retrievedFilePaths) > 0 {
		markdownContent += "\n\n# Retrieved File Paths\n\n"
		for _, path := range retrievedFilePaths {
			markdownContent += fmt.Sprintf("- `%s`\n", path) // Added backticks for code formatting
		}
	}

	return markdownContent
}

// renderMarkdown - unchanged
func renderMarkdown(content string) {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(120), // Adjust wrap width as needed
	)
	if err != nil {
		// Log error but perhaps fallback to plain print
		log.Printf("Error creating glamour renderer: %v. Printing raw content.", err)
		fmt.Println(content)
		return
	}

	out, err := renderer.Render(content)
	if err != nil {
		// Log error but perhaps fallback to plain print
		log.Printf("Error rendering Markdown with glamour: %v. Printing raw content.", err)
		fmt.Println(content)
		return
	}

	fmt.Println(out)
}

// readMarkdownFile - unchanged (though maybe make it return error instead of fatal)
func readMarkdownFile(path string) string {
	content, err := ioutil.ReadFile(path)
	if err != nil {
		// This is called from createMarkdownContent which now handles potential errors
		log.Fatalf("Error reading markdown file: %v", err) // Keep fatal here if initial read must succeed
	}
	return string(content)
}

// printVerboseInfo - unchanged

func printVerboseInfo(markdown, model, matchStrength, mode, prompt string) {
	_, ignoreFiles, err := utils.LoadConfigAndIgnoreFiles()
	if err != nil {
		log.Printf("Warning: Error loading config/ignore files for verbose info: %v", err)
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

// filterPaths applies ignore rules: exact match, directory prefix (trailing '/'), and glob patterns
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
				// directory prefix rule
				prefix := strings.TrimSuffix(rule, "/") + "/"
				if strings.HasPrefix(p, prefix) {
					drop = true
					break
				}
			}
			// glob pattern support
			if strings.ContainsAny(rule, "*?") {
				if ok, _ := filepath.Match(rule, p); ok {
					drop = true
					break
				}
			}
			// exact match
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

func resolveModelRuntime(cfg *utils.Config, aliasFlag, baseURLFlag, apiKeyFlag, directModelFlag string, paramPairs, paramJSON []string) (modelRuntime, error) {
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

	directBaseURL := utilsFirstNonEmpty(strings.TrimSpace(baseURLFlag), strings.TrimSpace(cfg.Environment.ModelBaseURL), strings.TrimSpace(cfg.Environment.ModelBaseURLOther), strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")))
	directAPIKey := utilsFirstNonEmpty(strings.TrimSpace(apiKeyFlag), strings.TrimSpace(cfg.Environment.ModelAPIKey), strings.TrimSpace(cfg.Environment.ModelAPIKeyOther), strings.TrimSpace(os.Getenv("OPENAI_API_KEY")))
	directModel := utilsFirstNonEmpty(strings.TrimSpace(directModelFlag), strings.TrimSpace(os.Getenv("OPENAI_MODEL")), strings.TrimSpace(os.Getenv("MCT_MODEL")))

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
