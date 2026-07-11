package llm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/tursomari/machtiani/agent/internal/git"
	"github.com/tursomari/machtiani/agent/internal/presentation"
)

type Config struct {
	DefaultModel       string                     `toml:"default_model"`
	DefaultModelSource FieldSource                `toml:"-"`
	Planner            *PlannerConfig             `toml:"planner"`
	ShellAgent         *ShellAgentConfig          `toml:"shell-agent"`
	Prompts            *PromptsConfig             `toml:"prompts"`
	Environment        *EnvironmentConfig         `toml:"environment"`
	Trajectory         *TrajectoryConfig          `toml:"trajectory"`
	Providers          map[string]ProviderConfig  `toml:"providers"`
	Models             map[string]ModelDefinition `toml:"models"`
	ProviderSources    map[string]FieldSource     `toml:"-"`
	ModelSources       map[string]FieldSource     `toml:"-"`
	Mode               *ModeConfig                `toml:"mode"`
	UI                 *UIConfig                  `toml:"ui"`

	// Top-level behavioral settings
	Verbose                       bool        `toml:"verbose"`
	VerboseSource                 FieldSource `toml:"-"` // verbose logging
	PersistTmpData                bool        `toml:"persist_tmp_data"`
	PersistTmpDataSource          FieldSource `toml:"-"` // persist temp directories
	DryRun                        bool        `toml:"dry_run"`
	DryRunSource                  FieldSource `toml:"-"` // dry-run mode
	ShellAgentEnabled             bool        `toml:"shell_agent_enabled"`
	ShellAgentEnabledSource       FieldSource `toml:"-"`                 // whether to use shell-agent by default
	ShellAgentModel               string      `toml:"shell_agent_model"` // default model for shell-agent
	ShellAgentModelSource         FieldSource `toml:"-"`
	AnswerModel                   string      `toml:"answer_model"` // model for final answer
	AnswerModelSource             FieldSource `toml:"-"`
	FileDiscoveryModel            string      `toml:"file_discovery_model"` // model for file discovery
	FileDiscoveryModelSource      FieldSource `toml:"-"`
	AnswerTag                     string      `toml:"answer_tag"` // output tag for answers
	AnswerTagSource               FieldSource `toml:"-"`
	Tag                           string      `toml:"tag"` // default command tag
	TagSource                     FieldSource `toml:"-"`
	EnableTagFormat               bool        `toml:"enable_tag_format"`
	EnableTagFormatSource         FieldSource `toml:"-"`          // enable tag format output
	FinalFile                     string      `toml:"final_file"` // path for final answer file
	FinalFileSource               FieldSource `toml:"-"`
	TranscriptFile                string      `toml:"transcript_file"` // path for transcript file
	TranscriptFileSource          FieldSource `toml:"-"`
	FileDiscoveryTrajectory       string      `toml:"file_discovery_trajectory"` // file-discovery trajectory path
	FileDiscoveryTrajectorySource FieldSource `toml:"-"`
	FileDiscoveryOutputDir        string      `toml:"file_discovery_output_dir"` // file-discovery output directory
	FileDiscoveryOutputDirSource  FieldSource `toml:"-"`
}

type UIConfig struct {
	Theme       string      `toml:"theme"`
	ThemeSource FieldSource `toml:"-"`
}

// ShellAgentConfig mirrors the shell-agent configuration section and is loaded
// from the global TOML configuration under [shell-agent] (or legacy [agent]).
type ShellAgentConfig struct {
	MaxSteps               int `toml:"max_steps"`
	FinalizeRemainingSteps int `toml:"finalize_remaining_steps"`

	maxStepsSet                  bool        `toml:"-"`
	finalizeRemainingStepsSet    bool        `toml:"-"`
	MaxStepsSource               FieldSource `toml:"-"`
	FinalizeRemainingStepsSource FieldSource `toml:"-"`
}

// PlannerConfig captures configuration intended for the orchestration planner.
type PlannerConfig struct {
	MaxTurns       int `toml:"max_turns"`
	TurnTimeout    int `toml:"turn_timeout"`     // per-turn timeout in seconds, 0 = unlimited
	MaxInputTokens int `toml:"max_input_tokens"` // max tokens per LLM call, 0 = disabled

	maxTurnsSet          bool        `toml:"-"`
	turnTimeoutSet       bool        `toml:"-"`
	maxInputTokensSet    bool        `toml:"-"`
	MaxTurnsSource       FieldSource `toml:"-"`
	TurnTimeoutSource    FieldSource `toml:"-"`
	MaxInputTokensSource FieldSource `toml:"-"`
}

// PromptsConfig captures the prompt templates referenced by planner and shell-agent.
type PromptsConfig struct {
	Planner       *PlannerPromptsConfig       `toml:"planner"`
	ShellAgent    *ShellAgentPromptsConfig    `toml:"shell-agent"`
	FileDiscovery *FileDiscoveryPromptsConfig `toml:"file_discovery"`
	MCT           *MCTPromptsConfig           `toml:"mct"`
}

// PlannerPromptsConfig contains planner prompt templates.
type PlannerPromptsConfig struct {
	SystemTemplate      string `toml:"system_template"`
	InstanceTemplate    string `toml:"instance_template"`
	TimeoutTemplate     string `toml:"timeout_template"`
	FormatErrorTemplate string `toml:"format_error_template"`
	AskPrompt           string `toml:"ask_prompt"`
	PlanSystemPrompt    string `toml:"plan_system_prompt"`
	PlanPrompt          string `toml:"plan_prompt"`
	FinalizePrompt      string `toml:"finalize_prompt"`
	ReviewPrompt        string `toml:"review_prompt"`

	systemTemplateSet      bool `toml:"-"`
	instanceTemplateSet    bool `toml:"-"`
	timeoutTemplateSet     bool `toml:"-"`
	formatErrorTemplateSet bool `toml:"-"`
	askPromptSet           bool `toml:"-"`
	planSystemPromptSet    bool `toml:"-"`
	planPromptSet          bool `toml:"-"`
	finalizePromptSet      bool `toml:"-"`
	reviewPromptSet        bool `toml:"-"`
}

// ShellAgentPromptsConfig contains shell-agent prompt templates.
type ShellAgentPromptsConfig struct {
	SystemTemplate            string `toml:"system_template"`
	InstanceTemplate          string `toml:"instance_template"`
	TimeoutTemplate           string `toml:"timeout_template"`
	FormatErrorTemplate       string `toml:"format_error_template"`
	ActionObservationTemplate string `toml:"action_observation_template"`
	LightweightSystemTemplate string `toml:"lightweight_system_template"`
	LightweightIntentTemplate string `toml:"lightweight_intent_template"`
	LightweightErrorTemplate  string `toml:"lightweight_error_template"`

	systemTemplateSet            bool `toml:"-"`
	instanceTemplateSet          bool `toml:"-"`
	timeoutTemplateSet           bool `toml:"-"`
	formatErrorTemplateSet       bool `toml:"-"`
	actionObservationTemplateSet bool `toml:"-"`
	lightweightSystemTemplateSet bool `toml:"-"`
	lightweightIntentTemplateSet bool `toml:"-"`
	lightweightErrorTemplateSet  bool `toml:"-"`
}

// FileDiscoveryPromptsConfig contains prompt templates for the file discovery agent.
type FileDiscoveryPromptsConfig struct {
	SystemPromptTemplate string `toml:"system_prompt_template"`

	systemPromptTemplateSet bool `toml:"-"`
}

// MCTPromptsConfig contains prompt templates for the mct agent wrapper.
type MCTPromptsConfig struct {
	ShellAgentContextPrefix     string `toml:"shell_agent_context_prefix"`
	ShellAgentPromptNotice      string `toml:"shell_agent_prompt_notice"`
	HeaderUserTemplate          string `toml:"header_user_template"`
	HeaderExistingTemplate      string `toml:"header_existing_template"`
	ConversationHistoryTemplate string `toml:"conversation_history_template"`
	ReadmeSystemTemplate        string `toml:"readme_system_template"`
	ShellAgentContextTemplate   string `toml:"shell_agent_context_template"`
	FullDiffNote                string `toml:"full_diff_note"`

	shellAgentContextPrefixSet     bool `toml:"-"`
	shellAgentPromptNoticeSet      bool `toml:"-"`
	headerUserTemplateSet          bool `toml:"-"`
	headerExistingTemplateSet      bool `toml:"-"`
	conversationHistoryTemplateSet bool `toml:"-"`
	readmeSystemTemplateSet        bool `toml:"-"`
	shellAgentContextTemplateSet   bool `toml:"-"`
	fullDiffNoteSet                bool `toml:"-"`
}

// EnvironmentConfig describes shell execution settings loaded from the
// [environment] section of the unified configuration.
type EnvironmentConfig struct {
	Type                        string      `toml:"type"`
	CommandTimeout              int         `toml:"command_timeout"`
	MaxCommandOutputBytes       int         `toml:"max_command_output_bytes"`
	CWD                         string      `toml:"cwd"`
	ComputedImageTag            string      `toml:"-"`
	commandTimeoutSet           bool        `toml:"-"`
	TypeSource                  FieldSource `toml:"-"`
	CommandTimeoutSource        FieldSource `toml:"-"`
	MaxCommandOutputBytesSource FieldSource `toml:"-"`
	CWDSource                   FieldSource `toml:"-"`
}

// TrajectoryConfig controls trajectory recording settings.
type TrajectoryConfig struct {
	Enabled            bool        `toml:"enabled"`        // whether to record trajectory
	File               string      `toml:"file"`           // trajectory file path
	VerboseLLM         bool        `toml:"verbose_llm"`    // verbose LLM logging in trajectory
	StreamTokens       bool        `toml:"stream_tokens"`  // stream token output
	Excerpt            int         `toml:"excerpt"`        // excerpt length for trajectory
	OmitRepoRoot       bool        `toml:"omit_repo_root"` // omit repo root from paths
	EnabledSource      FieldSource `toml:"-"`
	FileSource         FieldSource `toml:"-"`
	VerboseLLMSource   FieldSource `toml:"-"`
	StreamTokensSource FieldSource `toml:"-"`
	ExcerptSource      FieldSource `toml:"-"`
	OmitRepoRootSource FieldSource `toml:"-"`
}

type ModeConfig struct {
	InstructionDir  string                  `toml:"instruction_dir"`
	InstructionFile string                  `toml:"instruction_file"`
	ShellPrompt     string                  `toml:"shell_prompt"`
	Modes           map[string]ModeOverride `toml:"modes"`
}

type ModeOverride struct {
	InstructionFile string `toml:"instruction_file"`
	ShellPrompt     string `toml:"shell_prompt"`
}

// ModeInstructionsFormat enumerates the supported custom instruction formats.
type ModeInstructionsFormat string

const (
	ModeInstructionsFormatText ModeInstructionsFormat = "text"
	ModeInstructionsFormatTOML ModeInstructionsFormat = "toml"
)

// ModeTask represents a single task entry parsed from a TOML-based
// mode instruction file.
type ModeTask struct {
	Title        string `toml:"title"`
	Description  string `toml:"description"`
	Instruction  string `toml:"instruction"`
	SystemPrompt string `toml:"system_prompt"`
	ShellPrompt  string `toml:"shell_prompt"`
}

// ModeInstructions captures the resolved instruction payload, preserving both
// the raw source content and a single structured task.
type ModeInstructions struct {
	Format           ModeInstructionsFormat
	Path             string
	Raw              string
	Task             ModeTask
	ShellInstruction string
}

type ProviderConfig struct {
	BaseURL        string            `toml:"base_url"`
	APIKey         string            `toml:"api_key"`
	Headers        map[string]string `toml:"headers"`
	Query          map[string]string `toml:"query"`
	Endpoint       string            `toml:"endpoint"`
	BaseURLSource  FieldSource       `toml:"-"`
	APIKeySource   FieldSource       `toml:"-"`
	EndpointSource FieldSource       `toml:"-"`
	HeadersSource  FieldSource       `toml:"-"`
	QuerySource    FieldSource       `toml:"-"`
}

type ModelDefinition struct {
	Provider                           string         `toml:"provider"`
	Model                              string         `toml:"model"`
	Params                             map[string]any `toml:"params"`
	ProviderSource                     FieldSource    `toml:"-"`
	ModelSource                        FieldSource    `toml:"-"`
	ParamsSource                       FieldSource    `toml:"-"`
	CacheKeyNameSource                 FieldSource    `toml:"-"`
	CacheControlSource                 FieldSource    `toml:"-"`
	CacheTriggerThresholdSource        FieldSource    `toml:"-"`
	CacheLookbackOffsetSource          FieldSource    `toml:"-"`
	CacheReanchorTokensSource          FieldSource    `toml:"-"`
	CacheReanchorMessagesSource        FieldSource    `toml:"-"`
	CacheReanchorMinCachedTokensSource FieldSource    `toml:"-"`

	CacheKeyName                 string         `toml:"cache_key_name"`
	CacheControl                 map[string]any `toml:"cache_control"`
	CacheTriggerThreshold        int            `toml:"cache_trigger_threshold"`
	CacheLookbackOffset          int            `toml:"cache_lookback_offset"`
	CacheReanchorTokens          int            `toml:"cache_reanchor_tokens"`
	CacheReanchorMessages        int            `toml:"cache_reanchor_messages"`
	CacheReanchorMinCachedTokens int            `toml:"cache_reanchor_min_cached_tokens"`
}

type ResolvedModel struct {
	Alias        string
	ProviderName string
	BaseURL      string
	APIKey       string
	Headers      map[string]string
	Query        map[string]string
	Endpoint     string
	Model        string
	Params       map[string]any

	CacheKeyName                 string
	CacheControl                 map[string]any
	CacheTriggerThreshold        int
	CacheLookbackOffset          int
	CacheReanchorTokens          int
	CacheReanchorMessages        int
	CacheReanchorMinCachedTokens int
}

// FieldSource tracks where a configuration value originated.
type FieldSource int

const (
	SourceDefault FieldSource = iota
	SourceFile
	SourceFlag
)

type configData struct {
	path   string
	config Config
}

var (
	configOnce sync.Once
	cfgData    *configData
	cfgErr     error
)

const defaultModeInstructionDir = ".machtiani/modes"

func ResolveModel(alias string) (ResolvedModel, error) {
	return ResolveModelWithOverrides(alias, nil)
}

// ResolveModelWithOverrides resolves the model alias using the unified
// configuration, applying API key overrides provided via CLI flags. The lookup
// order for API keys is: overrides map > config file > environment variables.
// Environment variables are derived from the provider and alias names (e.g.
// OPENAI_API_KEY).
func ResolveModelWithOverrides(alias string, overrides map[string]string) (ResolvedModel, error) {
	cfg, err := loadConfig()
	if err != nil {
		return ResolvedModel{}, err
	}

	effectiveAlias := strings.TrimSpace(alias)
	if effectiveAlias == "" {
		effectiveAlias = strings.TrimSpace(cfg.config.DefaultModel)
		if effectiveAlias == "" {
			return ResolvedModel{}, fmt.Errorf("no model alias provided and default_model not set in %s", cfg.path)
		}
	}

	modelDef, ok := cfg.config.Models[effectiveAlias]
	if !ok {
		return ResolvedModel{}, fmt.Errorf("model alias %q not found in %s", effectiveAlias, cfg.path)
	}

	providerName := strings.TrimSpace(modelDef.Provider)
	if providerName == "" {
		providerName = effectiveAlias
	}

	provider, ok := cfg.config.Providers[providerName]
	if !ok {
		return ResolvedModel{}, fmt.Errorf("provider %q for model %q not defined in %s", providerName, effectiveAlias, cfg.path)
	}

	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		return ResolvedModel{}, fmt.Errorf("provider %q missing base_url in %s", providerName, cfg.path)
	}

	// Validate that any API key overrides match the model's configured provider
	if err := ValidateAPIKeyOverrideProvider(overrides, effectiveAlias, providerName); err != nil {
		return ResolvedModel{}, err
	}

	resolved := ResolvedModel{
		Alias:                        effectiveAlias,
		ProviderName:                 providerName,
		BaseURL:                      baseURL,
		Headers:                      copyStringMap(provider.Headers),
		Query:                        copyStringMap(provider.Query),
		Endpoint:                     strings.TrimSpace(provider.Endpoint),
		Model:                        strings.TrimSpace(modelDef.Model),
		Params:                       deepCopyMap(modelDef.Params),
		CacheKeyName:                 strings.TrimSpace(modelDef.CacheKeyName),
		CacheControl:                 deepCopyMap(modelDef.CacheControl),
		CacheTriggerThreshold:        modelDef.CacheTriggerThreshold,
		CacheLookbackOffset:          modelDef.CacheLookbackOffset,
		CacheReanchorTokens:          modelDef.CacheReanchorTokens,
		CacheReanchorMessages:        modelDef.CacheReanchorMessages,
		CacheReanchorMinCachedTokens: modelDef.CacheReanchorMinCachedTokens,
	}

	configKey := strings.TrimSpace(provider.APIKey)
	envCandidates := []string{providerEnvVarName(providerName)}
	if aliasEnv := providerEnvVarName(effectiveAlias); aliasEnv != "" && aliasEnv != envCandidates[0] {
		envCandidates = append(envCandidates, aliasEnv)
	}

	if overrideKey, ok := lookupAPIKeyOverride(overrides, providerName, effectiveAlias); ok {
		resolved.APIKey = overrideKey
	} else if configKey != "" {
		resolved.APIKey = configKey
	} else {
		for _, envName := range envCandidates {
			if strings.TrimSpace(envName) == "" {
				continue
			}
			if val := strings.TrimSpace(os.Getenv(envName)); val != "" {
				resolved.APIKey = val
				break
			}
		}
	}

	resolved.APIKey = strings.TrimSpace(resolved.APIKey)

	if resolved.APIKey == "" {
		envHint := envCandidates[0]
		if envHint == "" {
			envHint = "<PROVIDER>_API_KEY"
		}
		return ResolvedModel{}, fmt.Errorf("provider %q missing api_key in %s (set %s or use --api-key)", providerName, cfg.path, envHint)
	}

	if resolved.Model == "" {
		resolved.Model = effectiveAlias
	}
	if resolved.Endpoint == "" {
		resolved.Endpoint = "/chat/completions"
	}

	return resolved, nil
}

func DefaultModelAlias() (string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	alias := strings.TrimSpace(cfg.config.DefaultModel)
	if alias == "" {
		return "", fmt.Errorf("default_model not set in %s", cfg.path)
	}
	if _, ok := cfg.config.Models[alias]; !ok {
		return "", fmt.Errorf("default_model %q not defined under [models] in %s", alias, cfg.path)
	}
	return alias, nil
}

func ConfigPath() (string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	return cfg.path, nil
}

func loadConfig() (*configData, error) {
	configOnce.Do(func() {
		path, err := locateConfig()
		if err != nil {
			cfgErr = err
			return
		}
		cfg, err := parseConfig(path)
		if err != nil {
			cfgErr = err
			return
		}
		merged := MergeConfig(DefaultConfig(), cfg, Config{})
		cfgData = &configData{path: path, config: merged}
	})
	if cfgErr != nil {
		return nil, cfgErr
	}
	return cfgData, nil
}

func locateConfig() (string, error) {
	if env := strings.TrimSpace(os.Getenv("MACHTIANI_CONFIG")); env != "" {
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("MACHTIANI_CONFIG %s: %w", env, err)
		}
		return env, nil
	}
	if wd, err := os.Getwd(); err == nil {
		local, err := findLocalConfig(wd)
		if err != nil {
			return "", err
		}
		if local != "" {
			return local, nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".machtiani", "config.toml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("machtiani config not found; set MACHTIANI_CONFIG or create .machtiani/config.toml")
}

func findLocalConfig(start string) (string, error) {
	rel := filepath.Join(".machtiani", "config.toml")
	if git.IsGitRepo(start) {
		root, err := git.RepoRoot(start)
		if err != nil {
			return "", fmt.Errorf("resolve git root: %w", err)
		}
		return searchParents(start, rel, root)
	}
	return searchParents(start, rel, "")
}

func searchParents(start, rel, limit string) (string, error) {
	dir := filepath.Clean(start)
	if limit != "" {
		limit = filepath.Clean(limit)
	}
	for {
		candidate := filepath.Join(dir, rel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("stat %s: %w", candidate, err)
		}
		if limit != "" && filepath.Clean(dir) == limit {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", nil
}

func parseConfig(path string) (Config, error) {
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return Config{}, err
	}
	cfg := Config{
		Providers: make(map[string]ProviderConfig),
		Models:    make(map[string]ModelDefinition),
	}
	if v, ok := raw["default_model"].(string); ok {
		cfg.DefaultModel = v
	}
	var (
		legacyPlannerPrompts    *PlannerPromptsConfig
		legacyShellAgentPrompts *ShellAgentPromptsConfig
	)

	// Top-level behavioral settings
	if v, ok := raw["verbose"].(bool); ok {
		cfg.Verbose = v
	}
	if v, ok := raw["persist_tmp_data"].(bool); ok {
		cfg.PersistTmpData = v
	}
	if v, ok := raw["dry_run"].(bool); ok {
		cfg.DryRun = v
	}
	if v, ok := raw["shell_agent_enabled"].(bool); ok {
		cfg.ShellAgentEnabled = v
	}
	if v, ok := raw["shell_agent_model"].(string); ok {
		cfg.ShellAgentModel = v
	}
	if v, ok := raw["answer_model"].(string); ok {
		cfg.AnswerModel = v
	}
	if v, ok := raw["file_discovery_model"].(string); ok {
		cfg.FileDiscoveryModel = v
	}
	if v, ok := raw["answer_tag"].(string); ok {
		cfg.AnswerTag = v
	}
	if v, ok := raw["tag"].(string); ok {
		cfg.Tag = v
	}
	if v, ok := raw["enable_tag_format"].(bool); ok {
		cfg.EnableTagFormat = v
	}
	if v, ok := raw["final_file"].(string); ok {
		cfg.FinalFile = v
	}
	if v, ok := raw["transcript_file"].(string); ok {
		cfg.TranscriptFile = v
	}
	if v, ok := raw["file_discovery_trajectory"].(string); ok {
		cfg.FileDiscoveryTrajectory = v
	}
	if v, ok := raw["file_discovery_output_dir"].(string); ok {
		cfg.FileDiscoveryOutputDir = v
	}
	if shellAgentRaw, ok := toMap(raw["shell-agent"]); ok {
		agentCfg, shellPrompts, plannerOverrides, err := parseShellAgentSection(path, "shell-agent", shellAgentRaw)
		if err != nil {
			return Config{}, err
		}
		cfg.ShellAgent = agentCfg
		legacyShellAgentPrompts = mergeShellPromptSources(legacyShellAgentPrompts, shellPrompts)
		legacyPlannerPrompts = mergePlannerPromptSources(legacyPlannerPrompts, plannerOverrides)
	} else if legacyAgentRaw, ok := toMap(raw["agent"]); ok {
		agentCfg, shellPrompts, plannerOverrides, err := parseShellAgentSection(path, "agent", legacyAgentRaw)
		if err != nil {
			return Config{}, err
		}
		cfg.ShellAgent = agentCfg
		legacyShellAgentPrompts = mergeShellPromptSources(legacyShellAgentPrompts, shellPrompts)
		legacyPlannerPrompts = mergePlannerPromptSources(legacyPlannerPrompts, plannerOverrides)
	}
	if plannerRaw, ok := toMap(raw["planner"]); ok {
		plannerCfg, promptsCfg, err := parsePlannerSection(path, "planner", plannerRaw)
		if err != nil {
			return Config{}, err
		}
		cfg.Planner = plannerCfg
		legacyPlannerPrompts = mergePlannerPromptSources(legacyPlannerPrompts, promptsCfg)
	}
	if promptsRaw, ok := toMap(raw["prompts"]); ok {
		promptsCfg, err := parsePromptsSection(path, promptsRaw)
		if err != nil {
			return Config{}, err
		}
		cfg.Prompts = promptsCfg
	}
	cfg.Prompts = mergePromptsSources(cfg.Prompts, legacyPlannerPrompts, legacyShellAgentPrompts)
	if envRaw, ok := toMap(raw["environment"]); ok {
		envCfg, err := parseEnvironmentSection(path, envRaw)
		if err != nil {
			return Config{}, err
		}
		cfg.Environment = envCfg
	}
	if modeRaw, ok := toMap(raw["mode"]); ok {
		modeCfg, err := parseModeSection(path, modeRaw)
		if err != nil {
			return Config{}, err
		}
		cfg.Mode = modeCfg
	}
	if uiRaw, ok := toMap(raw["ui"]); ok {
		uiCfg := &UIConfig{}
		if rawTheme, exists := uiRaw["theme"]; exists {
			v, ok := rawTheme.(string)
			if !ok {
				return Config{}, fmt.Errorf("parse %s [ui.theme]: expected a string", path)
			}
			profile, err := presentation.NormalizeProfile(v)
			if err != nil {
				return Config{}, fmt.Errorf("parse %s [ui.theme]: %w", path, err)
			}
			uiCfg.Theme = string(profile)
		}
		cfg.UI = uiCfg
	}
	if trajRaw, ok := toMap(raw["trajectory"]); ok {
		trajCfg := &TrajectoryConfig{}
		if v, ok := trajRaw["enabled"].(bool); ok {
			trajCfg.Enabled = v
		}
		if v, ok := trajRaw["file"].(string); ok {
			trajCfg.File = v
		}
		if v, ok := trajRaw["verbose_llm"].(bool); ok {
			trajCfg.VerboseLLM = v
		}
		if v, ok := trajRaw["stream_tokens"].(bool); ok {
			trajCfg.StreamTokens = v
		}
		if v, ok := toInt(trajRaw["excerpt"]); ok {
			trajCfg.Excerpt = v
		}
		if v, ok := trajRaw["omit_repo_root"].(bool); ok {
			trajCfg.OmitRepoRoot = v
		}
		cfg.Trajectory = trajCfg
	}
	if provRaw, ok := toMap(raw["providers"]); ok {
		for name, entry := range provRaw {
			entryMap, ok := toMap(entry)
			if !ok {
				return Config{}, fmt.Errorf("parse %s [providers.%s]: expected table", path, name)
			}
			prov := ProviderConfig{}
			if v, ok := entryMap["base_url"].(string); ok {
				prov.BaseURL = v
			}
			if v, ok := entryMap["api_key"].(string); ok {
				prov.APIKey = v
			}
			if v, ok := entryMap["endpoint"].(string); ok {
				prov.Endpoint = v
			}
			if v, ok := toMap(entryMap["headers"]); ok {
				headers, err := mapStringString(v)
				if err != nil {
					return Config{}, fmt.Errorf("parse %s [providers.%s.headers]: %w", path, name, err)
				}
				prov.Headers = headers
			}
			if v, ok := toMap(entryMap["query"]); ok {
				query, err := mapStringString(v)
				if err != nil {
					return Config{}, fmt.Errorf("parse %s [providers.%s.query]: %w", path, name, err)
				}
				prov.Query = query
			}
			cfg.Providers[name] = prov
		}
	}
	if modelsRaw, ok := toMap(raw["models"]); ok {
		for name, entry := range modelsRaw {
			entryMap, ok := toMap(entry)
			if !ok {
				return Config{}, fmt.Errorf("parse %s [models.%s]: expected table", path, name)
			}
			model := ModelDefinition{}
			params := map[string]any{}
			for k, v := range entryMap {
				switch k {
				case "provider":
					str, ok := v.(string)
					if !ok {
						return Config{}, fmt.Errorf("parse %s [models.%s]: provider must be string", path, name)
					}
					model.Provider = str
				case "model":
					str, ok := v.(string)
					if !ok {
						return Config{}, fmt.Errorf("parse %s [models.%s]: model must be string", path, name)
					}
					model.Model = str
				case "cache_key_name":
					str, ok := v.(string)
					if !ok {
						return Config{}, fmt.Errorf("parse %s [models.%s]: cache_key_name must be string", path, name)
					}
					model.CacheKeyName = str
				case "cache_control":
					m, ok := toMap(v)
					if !ok {
						return Config{}, fmt.Errorf("parse %s [models.%s.cache_control]: expected table", path, name)
					}
					model.CacheControl = deepCopyMap(m)
				case "cache_trigger_threshold":
					val, ok := toInt(v)
					if !ok {
						return Config{}, fmt.Errorf("parse %s [models.%s]: cache_trigger_threshold must be number", path, name)
					}
					model.CacheTriggerThreshold = val
				case "cache_lookback_offset":
					val, ok := toInt(v)
					if !ok {
						return Config{}, fmt.Errorf("parse %s [models.%s]: cache_lookback_offset must be number", path, name)
					}
					model.CacheLookbackOffset = val
				case "cache_reanchor_tokens":
					val, ok := toInt(v)
					if !ok {
						return Config{}, fmt.Errorf("parse %s [models.%s]: cache_reanchor_tokens must be number", path, name)
					}
					model.CacheReanchorTokens = val
				case "cache_reanchor_messages":
					val, ok := toInt(v)
					if !ok {
						return Config{}, fmt.Errorf("parse %s [models.%s]: cache_reanchor_messages must be number", path, name)
					}
					model.CacheReanchorMessages = val
				case "cache_reanchor_min_cached_tokens":
					val, ok := toInt(v)
					if !ok {
						return Config{}, fmt.Errorf("parse %s [models.%s]: cache_reanchor_min_cached_tokens must be number", path, name)
					}
					model.CacheReanchorMinCachedTokens = val
				case "params":
					m, ok := toMap(v)
					if !ok {
						return Config{}, fmt.Errorf("parse %s [models.%s.params]: expected table", path, name)
					}
					params = mergeMaps(params, m)
				default:
					if len(params) == 0 {
						params = map[string]any{}
					}
					params[k] = deepCopyValue(v)
				}
			}
			if len(params) > 0 {
				model.Params = params
			}
			cfg.Models[name] = model
		}
	}
	return cfg, nil
}

func parseShellAgentSection(path, section string, data map[string]any) (*ShellAgentConfig, *ShellAgentPromptsConfig, *PlannerPromptsConfig, error) {
	agent := &ShellAgentConfig{}
	var (
		shellPrompts   *ShellAgentPromptsConfig
		plannerPrompts *PlannerPromptsConfig
	)
	allowShellPrompts := section != "agent"
	ensureShellPrompts := func() *ShellAgentPromptsConfig {
		if shellPrompts == nil {
			shellPrompts = &ShellAgentPromptsConfig{}
		}
		return shellPrompts
	}
	ensurePlannerPrompts := func() *PlannerPromptsConfig {
		if plannerPrompts == nil {
			plannerPrompts = &PlannerPromptsConfig{}
		}
		return plannerPrompts
	}

	if raw, ok := data["system_template"]; ok {
		val, err := templateStringFromRaw(path, section, "system_template", raw)
		if err != nil {
			return nil, nil, nil, err
		}
		planner := ensurePlannerPrompts()
		planner.SystemTemplate = val
		planner.systemTemplateSet = true
		if allowShellPrompts {
			shell := ensureShellPrompts()
			shell.SystemTemplate = val
			shell.systemTemplateSet = true
		}
	}
	if raw, ok := data["instance_template"]; ok {
		val, err := templateStringFromRaw(path, section, "instance_template", raw)
		if err != nil {
			return nil, nil, nil, err
		}
		planner := ensurePlannerPrompts()
		planner.InstanceTemplate = val
		planner.instanceTemplateSet = true
		if allowShellPrompts {
			shell := ensureShellPrompts()
			shell.InstanceTemplate = val
			shell.instanceTemplateSet = true
		}
	}
	if allowShellPrompts {
		if raw, ok := data["timeout_template"]; ok {
			val, err := templateStringFromRaw(path, section, "timeout_template", raw)
			if err != nil {
				return nil, nil, nil, err
			}
			p := ensureShellPrompts()
			p.TimeoutTemplate = val
			p.timeoutTemplateSet = true
		}
		if raw, ok := data["format_error_template"]; ok {
			val, err := templateStringFromRaw(path, section, "format_error_template", raw)
			if err != nil {
				return nil, nil, nil, err
			}
			p := ensureShellPrompts()
			p.FormatErrorTemplate = val
			p.formatErrorTemplateSet = true
		}
		if raw, ok := data["action_observation_template"]; ok {
			val, err := templateStringFromRaw(path, section, "action_observation_template", raw)
			if err != nil {
				return nil, nil, nil, err
			}
			p := ensureShellPrompts()
			p.ActionObservationTemplate = val
			p.actionObservationTemplateSet = true
		}
		if raw, ok := data["lightweight_system_template"]; ok {
			val, err := templateStringFromRaw(path, section, "lightweight_system_template", raw)
			if err != nil {
				return nil, nil, nil, err
			}
			p := ensureShellPrompts()
			p.LightweightSystemTemplate = val
			p.lightweightSystemTemplateSet = true
		}
		if raw, ok := data["lightweight_intent_template"]; ok {
			val, err := templateStringFromRaw(path, section, "lightweight_intent_template", raw)
			if err != nil {
				return nil, nil, nil, err
			}
			p := ensureShellPrompts()
			p.LightweightIntentTemplate = val
			p.lightweightIntentTemplateSet = true
		}
		if raw, ok := data["lightweight_error_template"]; ok {
			val, err := templateStringFromRaw(path, section, "lightweight_error_template", raw)
			if err != nil {
				return nil, nil, nil, err
			}
			p := ensureShellPrompts()
			p.LightweightErrorTemplate = val
			p.lightweightErrorTemplateSet = true
		}
	}
	if val, ok := toInt(data["max_steps"]); ok {
		agent.MaxSteps = val
		agent.maxStepsSet = true
	}
	if val, ok := toInt(data["finalize_remaining_steps"]); ok {
		agent.FinalizeRemainingSteps = val
		agent.finalizeRemainingStepsSet = true
	}
	return agent, shellPrompts, plannerPrompts, nil
}

func parsePlannerSection(path, section string, data map[string]any) (*PlannerConfig, *PlannerPromptsConfig, error) {
	planner := &PlannerConfig{}
	var prompts *PlannerPromptsConfig
	ensurePrompts := func() *PlannerPromptsConfig {
		if prompts == nil {
			prompts = &PlannerPromptsConfig{}
		}
		return prompts
	}
	if raw, ok := data["system_template"]; ok {
		val, err := templateStringFromRaw(path, section, "system_template", raw)
		if err != nil {
			return nil, nil, err
		}
		p := ensurePrompts()
		p.SystemTemplate = val
		p.systemTemplateSet = true
	}
	if raw, ok := data["instance_template"]; ok {
		val, err := templateStringFromRaw(path, section, "instance_template", raw)
		if err != nil {
			return nil, nil, err
		}
		p := ensurePrompts()
		p.InstanceTemplate = val
		p.instanceTemplateSet = true
	}
	if raw, ok := data["timeout_template"]; ok {
		val, err := templateStringFromRaw(path, section, "timeout_template", raw)
		if err != nil {
			return nil, nil, err
		}
		p := ensurePrompts()
		p.TimeoutTemplate = val
		p.timeoutTemplateSet = true
	}
	if raw, ok := data["format_error_template"]; ok {
		val, err := templateStringFromRaw(path, section, "format_error_template", raw)
		if err != nil {
			return nil, nil, err
		}
		p := ensurePrompts()
		p.FormatErrorTemplate = val
		p.formatErrorTemplateSet = true
	}
	if raw, ok := data["ask_prompt"]; ok {
		val, err := templateStringFromRaw(path, section, "ask_prompt", raw)
		if err != nil {
			return nil, nil, err
		}
		p := ensurePrompts()
		p.AskPrompt = val
		p.askPromptSet = true
	}
	if raw, ok := data["plan_system_prompt"]; ok {
		val, err := templateStringFromRaw(path, section, "plan_system_prompt", raw)
		if err != nil {
			return nil, nil, err
		}
		p := ensurePrompts()
		p.PlanSystemPrompt = val
		p.planSystemPromptSet = true
	}
	if raw, ok := data["plan_prompt"]; ok {
		val, err := templateStringFromRaw(path, section, "plan_prompt", raw)
		if err != nil {
			return nil, nil, err
		}
		p := ensurePrompts()
		p.PlanPrompt = val
		p.planPromptSet = true
	}
	if raw, ok := data["finalize_prompt"]; ok {
		val, err := templateStringFromRaw(path, section, "finalize_prompt", raw)
		if err != nil {
			return nil, nil, err
		}
		p := ensurePrompts()
		p.FinalizePrompt = val
		p.finalizePromptSet = true
	}
	if raw, ok := data["review_prompt"]; ok {
		val, err := templateStringFromRaw(path, section, "review_prompt", raw)
		if err != nil {
			return nil, nil, err
		}
		p := ensurePrompts()
		p.ReviewPrompt = val
		p.reviewPromptSet = true
	}
	if val, ok := toInt(data["max_turns"]); ok {
		planner.MaxTurns = val
		planner.maxTurnsSet = true
	}
	if val, ok := toInt(data["turn_timeout"]); ok {
		planner.TurnTimeout = val
		planner.turnTimeoutSet = true
	}
	if val, ok := toInt(data["max_input_tokens"]); ok {
		planner.MaxInputTokens = val
		planner.maxInputTokensSet = true
	}
	return planner, prompts, nil
}

func parsePromptsSection(path string, data map[string]any) (*PromptsConfig, error) {
	prompts := &PromptsConfig{}
	if raw, exists := data["planner"]; exists {
		plannerRaw, ok := toMap(raw)
		if !ok {
			return nil, fmt.Errorf("parse %s [prompts.planner]: expected table", path)
		}
		if _, plannerPrompts, err := parsePlannerSection(path, "prompts.planner", plannerRaw); err != nil {
			return nil, err
		} else {
			prompts.Planner = plannerPrompts
		}
	}
	if raw, exists := data["shell-agent"]; exists {
		shellRaw, ok := toMap(raw)
		if !ok {
			return nil, fmt.Errorf("parse %s [prompts.shell-agent]: expected table", path)
		}
		if _, shellPrompts, plannerOverrides, err := parseShellAgentSection(path, "prompts.shell-agent", shellRaw); err != nil {
			return nil, err
		} else {
			prompts.ShellAgent = shellPrompts
			prompts.Planner = mergePlannerPromptSources(prompts.Planner, plannerOverrides)
		}
	}
	if raw, exists := data["file_discovery"]; exists {
		fdRaw, ok := toMap(raw)
		if !ok {
			return nil, fmt.Errorf("parse %s [prompts.file_discovery]: expected table", path)
		}
		cfg := &FileDiscoveryPromptsConfig{}
		if tplRaw, ok := fdRaw["system_prompt_template"]; ok {
			val, err := templateStringFromRaw(path, "prompts.file_discovery", "system_prompt_template", tplRaw)
			if err != nil {
				return nil, err
			}
			cfg.SystemPromptTemplate = val
			cfg.systemPromptTemplateSet = true
		}
		if cfg.systemPromptTemplateSet {
			prompts.FileDiscovery = cfg
		}
	}
	if raw, exists := data["mct"]; exists {
		mctRaw, ok := toMap(raw)
		if !ok {
			return nil, fmt.Errorf("parse %s [prompts.mct]: expected table", path)
		}
		cfg := &MCTPromptsConfig{}
		if tplRaw, ok := mctRaw["shell_agent_context_prefix"]; ok {
			val, err := templateStringFromRaw(path, "prompts.mct", "shell_agent_context_prefix", tplRaw)
			if err != nil {
				return nil, err
			}
			cfg.ShellAgentContextPrefix = val
			cfg.shellAgentContextPrefixSet = true
		}
		if tplRaw, ok := mctRaw["shell_agent_prompt_notice"]; ok {
			val, err := templateStringFromRaw(path, "prompts.mct", "shell_agent_prompt_notice", tplRaw)
			if err != nil {
				return nil, err
			}
			cfg.ShellAgentPromptNotice = val
			cfg.shellAgentPromptNoticeSet = true
		}
		if tplRaw, ok := mctRaw["header_user_template"]; ok {
			val, err := templateStringFromRaw(path, "prompts.mct", "header_user_template", tplRaw)
			if err != nil {
				return nil, err
			}
			cfg.HeaderUserTemplate = val
			cfg.headerUserTemplateSet = true
		}
		if tplRaw, ok := mctRaw["header_existing_template"]; ok {
			val, err := templateStringFromRaw(path, "prompts.mct", "header_existing_template", tplRaw)
			if err != nil {
				return nil, err
			}
			cfg.HeaderExistingTemplate = val
			cfg.headerExistingTemplateSet = true
		}
		if tplRaw, ok := mctRaw["conversation_history_template"]; ok {
			val, err := templateStringFromRaw(path, "prompts.mct", "conversation_history_template", tplRaw)
			if err != nil {
				return nil, err
			}
			cfg.ConversationHistoryTemplate = val
			cfg.conversationHistoryTemplateSet = true
		}
		if tplRaw, ok := mctRaw["readme_system_template"]; ok {
			val, err := templateStringFromRaw(path, "prompts.mct", "readme_system_template", tplRaw)
			if err != nil {
				return nil, err
			}
			cfg.ReadmeSystemTemplate = val
			cfg.readmeSystemTemplateSet = true
		}
		if tplRaw, ok := mctRaw["shell_agent_context_template"]; ok {
			val, err := templateStringFromRaw(path, "prompts.mct", "shell_agent_context_template", tplRaw)
			if err != nil {
				return nil, err
			}
			cfg.ShellAgentContextTemplate = val
			cfg.shellAgentContextTemplateSet = true
		}
		if tplRaw, ok := mctRaw["full_diff_note"]; ok {
			val, err := templateStringFromRaw(path, "prompts.mct", "full_diff_note", tplRaw)
			if err != nil {
				return nil, err
			}
			cfg.FullDiffNote = val
			cfg.fullDiffNoteSet = true
		}
		if cfg.shellAgentContextPrefixSet || cfg.shellAgentPromptNoticeSet || cfg.headerUserTemplateSet || cfg.headerExistingTemplateSet || cfg.conversationHistoryTemplateSet || cfg.readmeSystemTemplateSet || cfg.shellAgentContextTemplateSet || cfg.fullDiffNoteSet {
			prompts.MCT = cfg
		}
	}
	if prompts.Planner == nil && prompts.ShellAgent == nil && prompts.FileDiscovery == nil && prompts.MCT == nil {
		return nil, nil
	}
	return prompts, nil
}

func mergePromptsSources(base *PromptsConfig, plannerLegacy *PlannerPromptsConfig, shellLegacy *ShellAgentPromptsConfig) *PromptsConfig {
	if plannerLegacy == nil && shellLegacy == nil {
		return base
	}
	if base == nil {
		base = &PromptsConfig{}
	}
	base.Planner = mergePlannerPromptSources(base.Planner, plannerLegacy)
	base.ShellAgent = mergeShellPromptSources(base.ShellAgent, shellLegacy)
	if base.Planner == nil && base.ShellAgent == nil && base.FileDiscovery == nil && base.MCT == nil {
		return nil
	}
	return base
}

func mergePlannerPromptSources(base, override *PlannerPromptsConfig) *PlannerPromptsConfig {
	if override == nil {
		return base
	}
	if base == nil {
		copy := *override
		return &copy
	}
	if override.systemTemplateSet {
		base.SystemTemplate = override.SystemTemplate
		base.systemTemplateSet = true
	}
	if override.instanceTemplateSet {
		base.InstanceTemplate = override.InstanceTemplate
		base.instanceTemplateSet = true
	}
	if override.timeoutTemplateSet {
		base.TimeoutTemplate = override.TimeoutTemplate
		base.timeoutTemplateSet = true
	}
	if override.formatErrorTemplateSet {
		base.FormatErrorTemplate = override.FormatErrorTemplate
		base.formatErrorTemplateSet = true
	}
	if override.askPromptSet {
		base.AskPrompt = override.AskPrompt
		base.askPromptSet = true
	}
	if override.planSystemPromptSet {
		base.PlanSystemPrompt = override.PlanSystemPrompt
		base.planSystemPromptSet = true
	}
	if override.planPromptSet {
		base.PlanPrompt = override.PlanPrompt
		base.planPromptSet = true
	}
	if override.finalizePromptSet {
		base.FinalizePrompt = override.FinalizePrompt
		base.finalizePromptSet = true
	}
	if override.reviewPromptSet {
		base.ReviewPrompt = override.ReviewPrompt
		base.reviewPromptSet = true
	}
	return base
}

func mergeShellPromptSources(base, override *ShellAgentPromptsConfig) *ShellAgentPromptsConfig {
	if override == nil {
		return base
	}
	if base == nil {
		copy := *override
		return &copy
	}
	if override.systemTemplateSet {
		base.SystemTemplate = override.SystemTemplate
		base.systemTemplateSet = true
	}
	if override.instanceTemplateSet {
		base.InstanceTemplate = override.InstanceTemplate
		base.instanceTemplateSet = true
	}
	if override.timeoutTemplateSet {
		base.TimeoutTemplate = override.TimeoutTemplate
		base.timeoutTemplateSet = true
	}
	if override.formatErrorTemplateSet {
		base.FormatErrorTemplate = override.FormatErrorTemplate
		base.formatErrorTemplateSet = true
	}
	if override.actionObservationTemplateSet {
		base.ActionObservationTemplate = override.ActionObservationTemplate
		base.actionObservationTemplateSet = true
	}
	if override.lightweightSystemTemplateSet {
		base.LightweightSystemTemplate = override.LightweightSystemTemplate
		base.lightweightSystemTemplateSet = true
	}
	if override.lightweightIntentTemplateSet {
		base.LightweightIntentTemplate = override.LightweightIntentTemplate
		base.lightweightIntentTemplateSet = true
	}
	if override.lightweightErrorTemplateSet {
		base.LightweightErrorTemplate = override.LightweightErrorTemplate
		base.lightweightErrorTemplateSet = true
	}
	return base
}

func parseEnvironmentSection(path string, data map[string]any) (*EnvironmentConfig, error) {
	env := &EnvironmentConfig{}
	if v, ok := data["type"].(string); ok {
		env.Type = v
	}
	if v, ok := toInt(data["command_timeout"]); ok {
		env.CommandTimeout = v
		env.commandTimeoutSet = true
	}
	if v, ok := toInt(data["max_command_output_bytes"]); ok {
		env.MaxCommandOutputBytes = v
	}
	if v, ok := data["cwd"].(string); ok {
		env.CWD = v
	}
	if _, ok := data["image"]; ok {
		return nil, fmt.Errorf("%s [environment.image] is no longer supported; use dockerfile_path", path)
	}
	return env, nil
}

func parseModeSection(path string, data map[string]any) (*ModeConfig, error) {
	mode := &ModeConfig{}
	if v, ok := data["instruction_dir"].(string); ok {
		mode.InstructionDir = v
	}
	if rawModes, exists := data["modes"]; exists {
		modesMap, ok := toMap(rawModes)
		if !ok {
			return nil, fmt.Errorf("parse %s [mode.modes]: expected table", path)
		}
		if len(modesMap) > 0 {
			mode.Modes = make(map[string]ModeOverride, len(modesMap))
			for modeName, inner := range modesMap {
				entry, ok := toMap(inner)
				if !ok {
					return nil, fmt.Errorf("parse %s [mode.modes.%s]: expected table", path, modeName)
				}
				cfg := ModeOverride{}
				if v, ok := entry["instruction_file"].(string); ok {
					cfg.InstructionFile = v
				}
				trimmed := strings.TrimSpace(modeName)
				if trimmed == "" {
					continue
				}
				mode.Modes[trimmed] = cfg
				lower := strings.ToLower(trimmed)
				if lower != trimmed {
					if _, exists := mode.Modes[lower]; !exists {
						mode.Modes[lower] = cfg
					}
				}
			}
		}
	}
	return mode, nil
}

func toMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func toSlice(v any) ([]any, bool) {
	slice, ok := v.([]any)
	return slice, ok
}

func mapStringString(in map[string]any) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected string value for %s, got %T", k, v)
		}
		out[k] = s
	}
	return out, nil
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyModeMap(in map[string]ModeOverride) map[string]ModeOverride {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ModeOverride, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func deepCopyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return deepCopyMap(val)
	case []any:
		cp := make([]any, len(val))
		for i := range val {
			cp[i] = deepCopyValue(val[i])
		}
		return cp
	default:
		return val
	}
}

func mergeMaps(base, override map[string]any) map[string]any {
	out := deepCopyMap(base)
	for k, v := range override {
		if existing, ok := out[k]; ok {
			if existingMap, ok1 := existing.(map[string]any); ok1 {
				if overrideMap, ok2 := toStringMap(v); ok2 {
					out[k] = mergeMaps(existingMap, overrideMap)
					continue
				}
			}
		}
		out[k] = deepCopyValue(v)
	}
	return out
}

func toInt(v any) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case float64:
		return int(val), true
	default:
		return 0, false
	}
}

func toFloat(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	default:
		return 0, false
	}
}

func templateStringFromRaw(path, section, key string, raw any) (string, error) {
	if raw == nil {
		return "", fmt.Errorf("parse %s [%s]: %s must not be nil", path, section, key)
	}
	if str, ok := raw.(string); ok {
		return str, nil
	}
	if table, ok := toMap(raw); ok {
		src, exists := table["file"]
		if !exists {
			return "", fmt.Errorf("parse %s [%s]: %s table must contain file", path, section, key)
		}
		filePath, ok := src.(string)
		if !ok {
			return "", fmt.Errorf("parse %s [%s]: %s file must be a string", path, section, key)
		}
		content, err := readTemplateFile(path, filePath)
		if err != nil {
			return "", fmt.Errorf("parse %s [%s]: %w", path, section, err)
		}
		return content, nil
	}
	return "", fmt.Errorf("parse %s [%s]: %s must be a string or table", path, section, key)
}

func readTemplateFile(configPath, templatePath string) (string, error) {
	trimmed := strings.TrimSpace(templatePath)
	if trimmed == "" {
		return "", fmt.Errorf("template file path is empty")
	}
	expanded, err := expandUserPath(trimmed)
	if err != nil {
		return "", err
	}
	cleaned := filepath.Clean(expanded)
	if filepath.IsAbs(cleaned) {
		data, err := os.ReadFile(cleaned)
		if err != nil {
			return "", fmt.Errorf("read template file %s: %w", cleaned, err)
		}
		return string(data), nil
	}

	configDir := filepath.Dir(configPath)
	candidates := []string{filepath.Join(configDir, cleaned)}
	triedRepo := false
	if repoRoot, err := git.RepoRoot(configDir); err == nil {
		repoCandidate := filepath.Join(repoRoot, cleaned)
		if repoCandidate != candidates[0] {
			candidates = append(candidates, repoCandidate)
			triedRepo = true
		}
		machtianiCandidate := filepath.Join(repoRoot, ".machtiani", cleaned)
		if machtianiCandidate != candidates[0] && machtianiCandidate != repoCandidate {
			candidates = append(candidates, machtianiCandidate)
			triedRepo = true
		}
	}
	var lastErr error
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err == nil {
			return string(data), nil
		}
		lastErr = err
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read template file %s: %w", candidate, err)
		}
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	if triedRepo {
		return "", fmt.Errorf("read template file %s relative to %s or repository root: %w", cleaned, configDir, lastErr)
	}
	return "", fmt.Errorf("read template file %s relative to %s: %w", cleaned, configDir, lastErr)
}

func expandUserPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path[0] != '~' {
		return path, nil
	}
	if len(path) > 1 && path[1] != '/' && path[1] != '\\' {
		return "", fmt.Errorf("expand home in %q: user prefixes not supported", path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand home in %q: %w", path, err)
	}
	if len(path) == 1 {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func cloneConfig(in Config) Config {
	clone := Config{
		DefaultModel: in.DefaultModel,
		Providers:    make(map[string]ProviderConfig, len(in.Providers)),
		Models:       make(map[string]ModelDefinition, len(in.Models)),
	}
	if in.ShellAgent != nil {
		agent := *in.ShellAgent
		clone.ShellAgent = &agent
	}
	if in.Planner != nil {
		planner := *in.Planner
		clone.Planner = &planner
	}
	if in.Prompts != nil {
		copyPrompts := &PromptsConfig{}
		if in.Prompts.Planner != nil {
			planner := *in.Prompts.Planner
			copyPrompts.Planner = &planner
		}
		if in.Prompts.ShellAgent != nil {
			shell := *in.Prompts.ShellAgent
			copyPrompts.ShellAgent = &shell
		}
		if in.Prompts.FileDiscovery != nil {
			fd := *in.Prompts.FileDiscovery
			copyPrompts.FileDiscovery = &fd
		}
		if in.Prompts.MCT != nil {
			mct := *in.Prompts.MCT
			copyPrompts.MCT = &mct
		}
		if copyPrompts.Planner != nil || copyPrompts.ShellAgent != nil || copyPrompts.FileDiscovery != nil || copyPrompts.MCT != nil {
			clone.Prompts = copyPrompts
		}
	}
	if in.Environment != nil {
		env := *in.Environment
		clone.Environment = &env
	}
	if in.Mode != nil {
		m := *in.Mode
		if len(m.Modes) > 0 {
			m.Modes = copyModeMap(m.Modes)
		}
		clone.Mode = &m
	}
	if in.UI != nil {
		uiConfig := *in.UI
		clone.UI = &uiConfig
	}
	if in.Trajectory != nil {
		t := *in.Trajectory
		clone.Trajectory = &t
	}
	for name, prov := range in.Providers {
		copyProv := ProviderConfig{
			BaseURL:  prov.BaseURL,
			APIKey:   prov.APIKey,
			Endpoint: prov.Endpoint,
		}
		if len(prov.Headers) > 0 {
			copyProv.Headers = copyStringMap(prov.Headers)
		}
		if len(prov.Query) > 0 {
			copyProv.Query = copyStringMap(prov.Query)
		}
		clone.Providers[name] = copyProv
	}
	for name, model := range in.Models {
		copyModel := ModelDefinition{
			Provider:                     model.Provider,
			Model:                        model.Model,
			CacheKeyName:                 model.CacheKeyName,
			CacheControl:                 deepCopyMap(model.CacheControl),
			CacheTriggerThreshold:        model.CacheTriggerThreshold,
			CacheLookbackOffset:          model.CacheLookbackOffset,
			CacheReanchorTokens:          model.CacheReanchorTokens,
			CacheReanchorMessages:        model.CacheReanchorMessages,
			CacheReanchorMinCachedTokens: model.CacheReanchorMinCachedTokens,
		}
		if len(model.Params) > 0 {
			copyModel.Params = deepCopyMap(model.Params)
		}
		clone.Models[name] = copyModel
	}
	return clone
}

// LoadGlobalConfig returns a deep copy of the parsed configuration and the
// resolved file path. Callers may safely mutate the returned config.
func LoadGlobalConfig() (Config, string, error) {
	data, err := loadConfig()
	if err != nil {
		return Config{}, "", err
	}
	return cloneConfig(data.config), data.path, nil
}

// ResetConfigForTesting clears cached configuration state. It is intended for
// use in tests that need to swap configuration fixtures.
func ResetConfigForTesting() {
	configOnce = sync.Once{}
	cfgData = nil
	cfgErr = nil
}

// CloneResolvedModel returns a deep copy of the provided resolved model so callers
// can safely modify headers, query params, or request payload overrides without
// mutating shared state.
func CloneResolvedModel(in ResolvedModel) ResolvedModel {
	clone := ResolvedModel{
		Alias:                        in.Alias,
		ProviderName:                 in.ProviderName,
		BaseURL:                      in.BaseURL,
		APIKey:                       in.APIKey,
		Headers:                      copyStringMap(in.Headers),
		Query:                        copyStringMap(in.Query),
		Endpoint:                     in.Endpoint,
		Model:                        in.Model,
		Params:                       deepCopyMap(in.Params),
		CacheKeyName:                 in.CacheKeyName,
		CacheControl:                 deepCopyMap(in.CacheControl),
		CacheTriggerThreshold:        in.CacheTriggerThreshold,
		CacheLookbackOffset:          in.CacheLookbackOffset,
		CacheReanchorTokens:          in.CacheReanchorTokens,
		CacheReanchorMessages:        in.CacheReanchorMessages,
		CacheReanchorMinCachedTokens: in.CacheReanchorMinCachedTokens,
	}
	return clone
}

// LoadModeInstructions resolves the mode instructions for the
// given mode. The lookup order is:
//  1. --mode-instruction-dir (overrideDir)
//  2. Mode-specific config entry [mode.modes.<mode>]
//  3. [mode] instruction_dir
//  4. Default .machtiani/modes relative to repo/config
//
// The function returns the resolved instruction payload including structured
// tasks when a TOML file is used.
func LoadModeInstructions(mode, overrideDir string, cfg Config, configPath string) (ModeInstructions, error) {
	trimmedMode := strings.TrimSpace(mode)
	if trimmedMode == "" {
		return ModeInstructions{}, fmt.Errorf("mode name is required")
	}
	tasksFile := filepath.Join(trimmedMode, "tasks.toml")

	var candidates []string
	appendCandidate := func(path string) {
		clean := filepath.Clean(path)
		for _, existing := range candidates {
			if existing == clean {
				return
			}
		}
		candidates = append(candidates, clean)
	}

	addDirCandidates := func(dir string) {
		appendCandidate(filepath.Join(dir, tasksFile))
	}

	addFileCandidates := func(path string) {
		appendCandidate(path)
	}

	configDir := effectiveConfigDir(configPath)
	modeCfg := cfg.Mode

	if strings.TrimSpace(overrideDir) != "" {
		dirs, err := resolveModeInstructionDir(overrideDir, configDir)
		if err != nil {
			return ModeInstructions{}, err
		}
		for _, dir := range dirs {
			addDirCandidates(dir)
		}
	}

	if modeCfg != nil {
		modeKey := trimmedMode
		modeOverride, ok := modeCfg.Modes[modeKey]
		if !ok {
			modeOverride, ok = modeCfg.Modes[strings.ToLower(modeKey)]
		}
		if ok && strings.TrimSpace(modeOverride.InstructionFile) != "" {
			files, err := resolveModeInstructionFile(modeOverride.InstructionFile, configDir)
			if err != nil {
				return ModeInstructions{}, err
			}
			for _, file := range files {
				addFileCandidates(file)
			}
		}
		if strings.TrimSpace(modeCfg.InstructionDir) != "" {
			dirs, err := resolveModeInstructionDir(modeCfg.InstructionDir, configDir)
			if err != nil {
				return ModeInstructions{}, err
			}
			for _, dir := range dirs {
				addDirCandidates(dir)
			}
		}
	}

	defaultDirs, err := resolveModeInstructionDir(defaultModeInstructionDir, configDir)
	if err == nil {
		for _, dir := range defaultDirs {
			addDirCandidates(dir)
		}
	}

	if len(candidates) == 0 {
		return ModeInstructions{}, fmt.Errorf("no search paths available for mode instructions (%s mode)", trimmedMode)
	}

	var notFound []string
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				notFound = append(notFound, candidate)
				continue
			}
			return ModeInstructions{}, fmt.Errorf("read mode instructions %s: %w", candidate, err)
		}
		format := detectModeInstructionFormat(candidate)
		raw := string(data)
		switch format {
		case ModeInstructionsFormatTOML:
			task, perr := parseModeInstructionTOML(data, candidate)
			if perr != nil {
				return ModeInstructions{}, fmt.Errorf("parse mode instructions %s: %w", candidate, perr)
			}
			result := ModeInstructions{Format: format, Path: candidate, Raw: raw, Task: task}
			if strings.TrimSpace(task.ShellPrompt) != "" {
				baseDir := filepath.Dir(candidate)
				shellPath := filepath.Join(baseDir, task.ShellPrompt)
				shellContent, err := os.ReadFile(shellPath)
				if err != nil {
					return ModeInstructions{}, fmt.Errorf("read shell-agent instructions %s: %w", shellPath, err)
				}
				result.ShellInstruction = strings.TrimSpace(string(shellContent))
			}
			return result, nil
		default:
			return ModeInstructions{Format: format, Path: candidate, Raw: raw}, nil
		}
	}

	return ModeInstructions{}, fmt.Errorf("mode instructions for mode %q not found (searched %s)", trimmedMode, strings.Join(notFound, ", "))
}

func detectModeInstructionFormat(path string) ModeInstructionsFormat {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return ModeInstructionsFormatTOML
	default:
		return ModeInstructionsFormatText
	}
}

func parseModeInstructionTOML(data []byte, tomlFilePath string) (ModeTask, error) {
	var task ModeTask
	if err := toml.Unmarshal(data, &task); err != nil {
		return ModeTask{}, err
	}

	// Detect legacy multi-step [[tasks]] arrays
	var legacy struct {
		Tasks []ModeTask `toml:"tasks"`
	}
	if err := toml.Unmarshal(data, &legacy); err == nil && len(legacy.Tasks) > 0 {
		return ModeTask{}, fmt.Errorf("unsupported TOML: multi-step [[tasks]] arrays are removed; use flat single-task schema")
	}

	baseDir := filepath.Dir(tomlFilePath)
	task.Title = strings.TrimSpace(task.Title)
	description, err := resolveTaskTextField(baseDir, task.Description)
	if err != nil {
		return ModeTask{}, fmt.Errorf("resolve description: %w", err)
	}
	task.Description = description
	instruction, err := resolveTaskTextField(baseDir, task.Instruction)
	if err != nil {
		return ModeTask{}, fmt.Errorf("resolve instruction: %w", err)
	}
	task.Instruction = instruction
	systemPrompt, err := resolveTaskTextField(baseDir, task.SystemPrompt)
	if err != nil {
		return ModeTask{}, fmt.Errorf("resolve system_prompt: %w", err)
	}
	task.SystemPrompt = systemPrompt
	if task.Title == "" {
		return ModeTask{}, fmt.Errorf("title is required")
	}
	return task, nil
}

func resolveTaskTextField(baseDir, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if !isFileReference(trimmed) {
		return trimmed, nil
	}
	filePath := filepath.Join(baseDir, trimmed)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("load %q: %w", filePath, err)
	}
	return strings.TrimSpace(string(content)), nil
}

func isFileReference(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, "\r\n") {
		return false
	}
	if strings.Contains(s, " ") {
		return false
	}
	if strings.Contains(s, "\\") {
		return false
	}
	knownExts := map[string]bool{".txt": true, ".md": true, ".sh": true}
	ext := strings.ToLower(filepath.Ext(s))
	if knownExts[ext] {
		return true
	}
	if strings.Contains(s, "/") {
		return true
	}
	return false
}

func effectiveConfigDir(configPath string) string {
	trimmed := strings.TrimSpace(configPath)
	if trimmed == "" {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return ""
	}
	abs := trimmed
	if !filepath.IsAbs(abs) {
		if resolved, err := filepath.Abs(abs); err == nil {
			abs = resolved
		}
	}
	return filepath.Dir(abs)
}

func resolveModeInstructionDir(base, configDir string) ([]string, error) {
	expanded, err := expandUserPath(base)
	if err != nil {
		return nil, fmt.Errorf("resolve mode instruction dir %q: %w", base, err)
	}
	cleaned := filepath.Clean(expanded)
	if filepath.IsAbs(cleaned) {
		return []string{cleaned}, nil
	}
	var dirs []string
	if configDir != "" {
		if shouldJoinConfigDir(configDir, cleaned) {
			dirs = append(dirs, filepath.Join(configDir, cleaned))
		}
		parent := filepath.Dir(configDir)
		if parent != "" && parent != configDir {
			dirs = append(dirs, filepath.Join(parent, cleaned))
		}
		if repoRoot, err := git.RepoRoot(configDir); err == nil {
			repoCandidate := filepath.Join(repoRoot, cleaned)
			if !containsString(dirs, repoCandidate) {
				dirs = append(dirs, repoCandidate)
			}
			machtianiCandidate := filepath.Join(repoRoot, ".machtiani", cleaned)
			if !containsString(dirs, machtianiCandidate) {
				dirs = append(dirs, machtianiCandidate)
			}
		}
	}
	if len(dirs) == 0 {
		if wd, err := os.Getwd(); err == nil {
			dirs = append(dirs, filepath.Join(wd, cleaned))
		}
	}
	return uniqueStrings(dirs), nil
}

func resolveModeInstructionFile(path, configDir string) ([]string, error) {
	expanded, err := expandUserPath(path)
	if err != nil {
		return nil, fmt.Errorf("resolve mode instruction file %q: %w", path, err)
	}
	cleaned := filepath.Clean(expanded)
	if filepath.IsAbs(cleaned) {
		return []string{cleaned}, nil
	}
	var files []string
	if configDir != "" {
		if shouldJoinConfigDir(configDir, cleaned) {
			files = append(files, filepath.Join(configDir, cleaned))
		}
		parent := filepath.Dir(configDir)
		if parent != "" && parent != configDir {
			files = append(files, filepath.Join(parent, cleaned))
		}
		if repoRoot, err := git.RepoRoot(configDir); err == nil {
			repoCandidate := filepath.Join(repoRoot, cleaned)
			if !containsString(files, repoCandidate) {
				files = append(files, repoCandidate)
			}
			machtianiCandidate := filepath.Join(repoRoot, ".machtiani", cleaned)
			if !containsString(files, machtianiCandidate) {
				files = append(files, machtianiCandidate)
			}
		}
	}
	if len(files) == 0 {
		if wd, err := os.Getwd(); err == nil {
			files = append(files, filepath.Join(wd, cleaned))
		}
	}
	return uniqueStrings(files), nil
}

func containsString(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func uniqueStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	var out []string
	for _, item := range items {
		clean := filepath.Clean(item)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func shouldJoinConfigDir(configDir, cleaned string) bool {
	trimmed := strings.Trim(cleaned, string(filepath.Separator))
	if trimmed == "" {
		return true
	}
	first := trimmed
	if idx := strings.IndexRune(trimmed, filepath.Separator); idx != -1 {
		first = trimmed[:idx]
	}
	if first != ".machtiani" {
		return true
	}
	return filepath.Base(configDir) != ".machtiani"
}

func toStringMap(v any) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	switch val := v.(type) {
	case map[string]any:
		return val, true
	default:
		return nil, false
	}
}

func DefaultMinimalConfig() Config {
	return Config{
		DefaultModel: "",
		Planner:      &PlannerConfig{MaxTurns: 150},
		ShellAgent:   &ShellAgentConfig{FinalizeRemainingSteps: 10, MaxSteps: 110},
		Environment: &EnvironmentConfig{
			Type:           "local",
			CommandTimeout: 9999,
			CWD:            ".",
		},
		Prompts:   nil,
		Mode:      nil,
		Providers: nil,
		Models:    nil,
	}
}

func NewDirectModel(baseURL, apiKey, model string) (ResolvedModel, error) {
	base := strings.TrimSpace(baseURL)
	key := strings.TrimSpace(apiKey)
	m := strings.TrimSpace(model)
	if base == "" || key == "" || m == "" {
		return ResolvedModel{}, fmt.Errorf("baseURL, apiKey, and model must be provided for direct model")
	}
	return ResolvedModel{
		BaseURL:  base,
		APIKey:   key,
		Model:    m,
		Endpoint: "/chat/completions",
		Params:   map[string]any{},
	}, nil
}
