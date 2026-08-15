package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	cfgpkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/config"
	"github.com/tursomari/machtiani/agent/internal/file-discovery/internal/discovery"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

// version metadata is injected at build time via ldflags in mct/build.sh
var (
	version = "dev"
	commit  = "unknown"
	builtAt = "unknown"
	dirty   = "unknown"
)

type multiString []string

func (m *multiString) String() string {
	return strings.Join(*m, ",")
}

func (m *multiString) Set(value string) error {
	*m = append(*m, value)
	return nil
}

type apiKeyFlag struct {
	direct    *string
	overrides *multiString
}

func (f *apiKeyFlag) String() string {
	if f == nil || f.direct == nil {
		return ""
	}
	return *f.direct
}

func (f *apiKeyFlag) Set(value string) error {
	if f == nil {
		return nil
	}
	trimmed := strings.TrimSpace(value)
	if strings.Contains(trimmed, ":") {
		if f.overrides != nil {
			*f.overrides = append(*f.overrides, trimmed)
		}
		return nil
	}
	if f.direct != nil {
		*f.direct = trimmed
	}
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, "file-discovery %s\n", version)
	fmt.Fprintln(os.Stderr, "Usage: cat issue.txt | file-discovery [flags]")
	fmt.Fprintln(os.Stderr, "\nAuth & model (env or flags):")
	fmt.Fprintln(os.Stderr, "  -api-key, --openai-api-key    direct API key or provider override (provider:key); OR OPENAI_API_KEY env")
	fmt.Fprintln(os.Stderr, "  -base-url, --openai-base-url  or OPENAI_BASE_URL   API base URL (required)")
	fmt.Fprintln(os.Stderr, "  -model, --openai-model        or OPENAI_MODEL      Model name (required)")
	fmt.Fprintln(os.Stderr, "\nCore flags:")
	fmt.Fprintln(os.Stderr, "  -max-rounds <n>         Maximum LLM rounds (default 20)")
	fmt.Fprintln(os.Stderr, "  -cmd-timeout <sec>      Per-command timeout seconds (default 30)")
	fmt.Fprintln(os.Stderr, "  -max-stdout <bytes>     Per-command RG_OUT cap bytes (default 20480)")
	fmt.Fprintln(os.Stderr, "  -max-initial-input <bytes> Initial stdin cap bytes (default 300000)")
	fmt.Fprintln(os.Stderr, "  -max-transcript <bytes>    Deprecated alias for -max-initial-input")
	fmt.Fprintln(os.Stderr, "  -log-json               Log JSON to stderr (default false)")
	fmt.Fprintln(os.Stderr, "  -v                      Verbose logging (default false)")
	fmt.Fprintln(os.Stderr, "  -no-json               Use bracket tool-call syntax instead of JSON function calls")
	fmt.Fprintln(os.Stderr, "\nDry-run (no network):")
	fmt.Fprintln(os.Stderr, "  -dry-run-rg             Enumerate native workspace files and print RG_OUT; skip API")
	fmt.Fprintln(os.Stderr, "  -pattern <regex>        Go regex used to filter native workspace paths")
	fmt.Fprintln(os.Stderr, "\nTrajectory:")
	fmt.Fprintln(os.Stderr, "  -trajectory <path>      Path to trajectory JSONL file (default auto-named)")
	fmt.Fprintln(os.Stderr, "  -no-trajectory          Disable trajectory recording")
	fmt.Fprintln(os.Stderr, "  env: FILE_DISCOVERY_TRAJECTORY used if -trajectory not set")
	fmt.Fprintln(os.Stderr, "\nOther:")
	fmt.Fprintln(os.Stderr, "  -session-id, -s        Optional session ID; first 5 characters tag BEGIN/END markers")
	fmt.Fprintln(os.Stderr, "  -version               Print version and exit")
}

func main() {
	// Configure std logger to write cleanly to stderr with no timestamps/prefixes
	log.SetOutput(os.Stderr)
	log.SetFlags(0)

	var cfg cfgpkg.Config
	cfg.LLMTimeoutSec = 60
	var showVersion bool
	var noJSON bool

	flag.Usage = usage
	flag.IntVar(&cfg.MaxRounds, "max-rounds", 20, "Maximum LLM rounds")
	flag.IntVar(&cfg.CmdTimeoutSec, "cmd-timeout", 30, "Per-command timeout seconds")
	flag.IntVar(&cfg.MaxStdoutBytes, "max-stdout", 20480, "Per-command RG_OUT cap bytes")
	flag.IntVar(&cfg.MaxInitialInputBytes, "max-initial-input", 300000, "Initial stdin cap bytes")
	flag.IntVar(&cfg.MaxInitialInputBytes, "max-transcript", 300000, "Deprecated alias for --max-initial-input")
	flag.BoolVar(&cfg.LogJSON, "log-json", false, "Log JSON to stderr")
	flag.BoolVar(&cfg.Verbose, "v", false, "Verbose logging")
	flag.BoolVar(&noJSON, "no-json", false, "Use bracket tool-call syntax instead of JSON function calls")
	flag.BoolVar(&cfg.DryRunRG, "dry-run-rg", false, "Enumerate native workspace files and print RG_OUT; skip API")
	flag.StringVar(&cfg.DryPattern, "pattern", "", "Go regex for dry-run-rg to filter native workspace paths")
	flag.StringVar(&cfg.TrajectoryPath, "trajectory", "", "Path to trajectory JSONL file; defaults to auto-named in cwd")
	flag.BoolVar(&cfg.NoTrajectory, "no-trajectory", false, "Disable trajectory recording")
	var apiKeyOverrideFlags multiString
	apiCollector := &apiKeyFlag{direct: &cfg.APIKey, overrides: &apiKeyOverrideFlags}
	flag.Var(apiCollector, "api-key", "API key (direct) or provider override in provider:key format (repeatable)")
	flag.Var(apiCollector, "openai-api-key", "Alias for --api-key")
	flag.StringVar(&cfg.BaseURL, "base-url", "", "OpenAI-compatible API base URL (overrides OPENAI_BASE_URL)")
	flag.StringVar(&cfg.BaseURL, "openai-base-url", "", "Alias for --base-url")
	var modelAlias string
	var fileDiscoveryAlias string
	var openAIModel string
	flag.StringVar(&modelAlias, "model", "", "Model alias defined in .machtiani/config.toml")
	flag.StringVar(&fileDiscoveryAlias, "file-discovery-model", "", "Model alias override (defaults to --model or config)")
	flag.StringVar(&openAIModel, "openai-model", "", "Direct upstream model name (deprecated; prefer --model)")
	var paramFlags multiString
	var paramJSON multiString
	flag.Var(&paramFlags, "param", "Additional request parameter key=value (repeatable)")
	flag.Var(&paramJSON, "param-json", "Merge JSON object of additional parameters (repeatable)")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	// Session-scoped markers
	flag.StringVar(&cfg.SessionID, "session-id", "", "Optional session ID; first 5 characters tag BEGIN/END markers")
	flag.StringVar(&cfg.SessionID, "s", "", "Alias for -session-id")
	cfg.ToolCallMode = cfgpkg.ToolCallModeJSON
	flag.Parse()
	if noJSON {
		cfg.ToolCallMode = cfgpkg.ToolCallModeSimple
	}

	if showVersion {
		fmt.Printf("file-discovery %s\ncommit: %s\nbuilt: %s\ndirty: %s\n", version, commit, builtAt, dirty)
		os.Exit(0)
	}

	paramPairs := append([]string(nil), paramFlags...)
	paramJSONVals := append([]string(nil), paramJSON...)
	apiOverrides, err := llm.ParseAPIKeyOverrides(apiKeyOverrideFlags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cfg.APIKeyOverrides = apiOverrides

	effectiveAlias := firstNonEmpty(strings.TrimSpace(fileDiscoveryAlias), strings.TrimSpace(modelAlias))
	runtime, err := resolveModelRuntime(&cfg, effectiveAlias, openAIModel, paramPairs, paramJSONVals, apiOverrides)
	if err != nil {
		if cfg.DryRunRG {
			runtime = modelRuntime{}
		} else if miss, ok := err.(*missingConfigError); ok {
			fmt.Fprintln(os.Stderr, "Missing model config: set:")
			for _, item := range miss.items {
				fmt.Fprintln(os.Stderr, " - ", item)
			}
			os.Exit(2)
		} else {
			fmt.Fprintln(os.Stderr, "Model resolution error:", err)
			os.Exit(1)
		}
	}

	if !cfg.DryRunRG {
		cfg.APIKey = runtime.resolved.APIKey
		cfg.BaseURL = runtime.resolved.BaseURL
		cfg.Model = runtime.resolved.Model
	}

	llmSettings := discovery.LLMSettings{
		Model:            runtime.resolved,
		Extras:           runtime.extras,
		FallbackAliases:  runtime.fallbackAliases,
		FallbackResolved: runtime.fallbackResolved,
		APIKeyOverrides:  llm.CopyAPIKeyOverridesForRuntime(runtime.apiKeyOverrides),
	}
	os.Exit(discovery.Run(context.Background(), cfg, llmSettings))
}

type modelRuntime struct {
	resolved         llm.ResolvedModel
	alias            string
	usingAlias       bool
	extras           map[string]any
	fallbackAliases  []string
	fallbackResolved []llm.ResolvedModel
	apiKeyOverrides  map[string]string
}

func resolveModelRuntime(cfg *cfgpkg.Config, aliasFlag, directModelFlag string, paramPairs, paramJSON []string, apiKeyOverrides map[string]string) (modelRuntime, error) {
	extras, err := llm.ParseParamOverrides(paramPairs, paramJSON)
	if err != nil {
		return modelRuntime{}, err
	}
	runtime := modelRuntime{extras: extras, apiKeyOverrides: llm.CopyAPIKeyOverridesForRuntime(apiKeyOverrides)}

	alias := firstNonEmpty(strings.TrimSpace(aliasFlag), strings.TrimSpace(os.Getenv("MACHTIANI_FILE_DISCOVERY_MODEL")))
	hasDirectFlags := strings.TrimSpace(cfg.APIKey) != "" || strings.TrimSpace(cfg.BaseURL) != "" || strings.TrimSpace(directModelFlag) != ""

	directAPIKey := firstNonEmpty(strings.TrimSpace(cfg.APIKey), strings.TrimSpace(os.Getenv("OPENAI_API_KEY")))
	directBaseURL := firstNonEmpty(strings.TrimSpace(cfg.BaseURL), strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")))
	directModel := firstNonEmpty(strings.TrimSpace(directModelFlag), strings.TrimSpace(os.Getenv("OPENAI_MODEL")))

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
		resolved, err := llm.ResolveModelWithOverrides(alias, apiKeyOverrides)
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
