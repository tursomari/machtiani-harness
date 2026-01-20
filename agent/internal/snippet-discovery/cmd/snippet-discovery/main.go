package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	cfgpkg "github.com/tursomari/machtiani/agent/internal/snippet-discovery/internal/config"
	"github.com/tursomari/machtiani/agent/internal/snippet-discovery/internal/discovery"
)

// version metadata is injected at build time via ldflags
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

type csvString []string

func (c *csvString) String() string {
	return strings.Join(*c, ",")
}

func (c *csvString) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		*c = append(*c, trimmed)
	}
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
	fmt.Fprintf(os.Stderr, "snippet-discovery %s\n", version)
	fmt.Fprintln(os.Stderr, "Usage: snippet-discovery -r <reason> -f <path> [-f <path> ...] [flags]")
	fmt.Fprintln(os.Stderr, "\nAuth & model (env or flags):")
	fmt.Fprintln(os.Stderr, "  -api-key, --openai-api-key    direct API key or provider override (provider:key); OR OPENAI_API_KEY env")
	fmt.Fprintln(os.Stderr, "  -base-url, --openai-base-url  or OPENAI_BASE_URL   API base URL (required)")
	fmt.Fprintln(os.Stderr, "  -model, --openai-model        or OPENAI_MODEL      Model name (required)")
	fmt.Fprintln(os.Stderr, "\nCore flags:")
	fmt.Fprintln(os.Stderr, "  -r, -reason <text>       Reason/query describing desired snippets")
	fmt.Fprintln(os.Stderr, "  -f <path>                File path to search (repeatable or comma-separated)")
	fmt.Fprintln(os.Stderr, "  -max-rounds <n>          Maximum LLM rounds (default 10)")
	fmt.Fprintln(os.Stderr, "  -timeout <sec>           Per-round timeout seconds (default 60)")
	fmt.Fprintln(os.Stderr, "  -max-lines <n>           Max lines per file for <show> output (default 500)")
	fmt.Fprintln(os.Stderr, "  -max-transcript <bytes>  Global transcript cap bytes (default 300000)")
	fmt.Fprintln(os.Stderr, "  -log-json                Log JSON to stderr (default false)")
	fmt.Fprintln(os.Stderr, "  -v                       Verbose logging (default false)")
	fmt.Fprintln(os.Stderr, "  -error-stream <path>     Stream structured errors to a file or pipe")
	fmt.Fprintln(os.Stderr, "\nTrajectory:")
	fmt.Fprintln(os.Stderr, "  -trajectory <path>       Path to trajectory JSONL file (default auto-named)")
	fmt.Fprintln(os.Stderr, "  -no-trajectory           Disable trajectory recording")
	fmt.Fprintln(os.Stderr, "  env: SNIPPET_DISCOVERY_TRAJECTORY used if -trajectory not set")
	fmt.Fprintln(os.Stderr, "\nOther:")
	fmt.Fprintln(os.Stderr, "  -version                 Print version and exit")
}

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)

	var cfg cfgpkg.Config
	var showVersion bool
	flag.Usage = usage
	flag.StringVar(&cfg.Reason, "r", "", "Reason/query describing desired snippets")
	flag.StringVar(&cfg.Reason, "reason", "", "Alias for -r")
	var fileFlags csvString
	flag.Var(&fileFlags, "f", "File path to search (repeatable or comma-separated)")
	flag.IntVar(&cfg.MaxRounds, "max-rounds", 10, "Maximum LLM rounds")
	flag.IntVar(&cfg.TimeoutSec, "timeout", 60, "Per-round timeout seconds")
	flag.IntVar(&cfg.MaxLinesPerFile, "max-lines", 500, "Max lines per file for <show> output")
	flag.IntVar(&cfg.MaxTranscript, "max-transcript", 300000, "Global transcript cap bytes")
	flag.BoolVar(&cfg.LogJSON, "log-json", false, "Log JSON to stderr")
	flag.BoolVar(&cfg.Verbose, "v", false, "Verbose logging")
	flag.StringVar(&cfg.ErrorStreamPath, "error-stream", "", "Path to stream structured errors (optional)")
	flag.StringVar(&cfg.TrajectoryPath, "trajectory", "", "Path to trajectory JSONL file; defaults to auto-named in cwd")
	flag.BoolVar(&cfg.NoTrajectory, "no-trajectory", false, "Disable trajectory recording")
	var apiKeyOverrideFlags multiString
	apiCollector := &apiKeyFlag{direct: &cfg.APIKey, overrides: &apiKeyOverrideFlags}
	flag.Var(apiCollector, "api-key", "API key (direct) or provider override in provider:key format (repeatable)")
	flag.Var(apiCollector, "openai-api-key", "Alias for --api-key")
	flag.StringVar(&cfg.BaseURL, "base-url", "", "OpenAI-compatible API base URL (overrides OPENAI_BASE_URL)")
	flag.StringVar(&cfg.BaseURL, "openai-base-url", "", "Alias for --base-url")
	var modelAlias string
	var snippetAlias string
	var openAIModel string
	flag.StringVar(&modelAlias, "model", "", "Model alias defined in .machtiani/config.toml")
	flag.StringVar(&snippetAlias, "snippet-discovery-model", "", "Model alias override (defaults to --model or config)")
	flag.StringVar(&openAIModel, "openai-model", "", "Direct upstream model name (deprecated; prefer --model)")
	var paramFlags multiString
	var paramJSON multiString
	flag.Var(&paramFlags, "param", "Additional request parameter key=value (repeatable)")
	flag.Var(&paramJSON, "param-json", "Merge JSON object of additional parameters (repeatable)")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("snippet-discovery %s\ncommit: %s\nbuilt: %s\ndirty: %s\n", version, commit, builtAt, dirty)
		os.Exit(0)
	}

	errorStream, err := cfgpkg.OpenErrorStream(cfg.ErrorStreamPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to open error stream:", err)
		os.Exit(2)
	}
	cfg.ErrorStream = errorStream
	lg := cfgpkg.Logger{JSON: cfg.LogJSON, V: cfg.Verbose, ErrorStream: errorStream}

	cfg.FilePaths = append(cfg.FilePaths, fileFlags...)
	paramPairs := append([]string(nil), paramFlags...)
	paramJSONVals := append([]string(nil), paramJSON...)
	apiOverrides, err := llm.ParseAPIKeyOverrides(apiKeyOverrideFlags)
	if err != nil {
		lg.ErrorWithFields("invalid api key overrides", map[string]any{"category": "api_key_override", "error": err.Error()})
		os.Exit(2)
	}
	cfg.APIKeyOverrides = apiOverrides

	effectiveAlias := firstNonEmpty(strings.TrimSpace(snippetAlias), strings.TrimSpace(modelAlias))
	runtime, err := resolveModelRuntime(&cfg, effectiveAlias, openAIModel, paramPairs, paramJSONVals, apiOverrides)
	if err != nil {
		if miss, ok := err.(*missingConfigError); ok {
			lg.ErrorWithFields("missing model config", map[string]any{"category": "missing_model_config", "missing": miss.items})
			os.Exit(2)
		}
		lg.ErrorWithFields("model resolution error", map[string]any{"category": "model_resolution_error", "error": err.Error()})
		os.Exit(1)
	}

	cfg.APIKey = runtime.resolved.APIKey
	cfg.BaseURL = runtime.resolved.BaseURL
	cfg.Model = runtime.resolved.Model

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

	alias := firstNonEmpty(strings.TrimSpace(aliasFlag), strings.TrimSpace(os.Getenv("MCT_SNIPPET_DISCOVERY_MODEL")))
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
