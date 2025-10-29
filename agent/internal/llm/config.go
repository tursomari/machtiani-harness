package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/tursomari/machtiani/agent/internal/git"
)

type Config struct {
	Listen       string                     `toml:"listen"`
	DefaultModel string                     `toml:"default_model"`
	Model        *ModelConfig               `toml:"model"`
	Agent        *AgentConfig               `toml:"agent"`
	Environment  *EnvironmentConfig         `toml:"environment"`
	Providers    map[string]ProviderConfig  `toml:"providers"`
	Models       map[string]ModelDefinition `toml:"models"`
}

// AgentConfig mirrors the shell-agent agent configuration section and is loaded
// from the global TOML configuration under [agent].
type AgentConfig struct {
	SystemTemplate            string  `toml:"system_template"`
	InstanceTemplate          string  `toml:"instance_template"`
	TimeoutTemplate           string  `toml:"timeout_template"`
	FormatErrorTemplate       string  `toml:"format_error_template"`
	ActionObservationTemplate string  `toml:"action_observation_template"`
	LightweightSystemTemplate string  `toml:"lightweight_system_template"`
	LightweightIntentTemplate string  `toml:"lightweight_intent_template"`
	LightweightErrorTemplate  string  `toml:"lightweight_error_template"`
	LightweightMaxAttempts    int     `toml:"lightweight_max_attempts"`
	StepLimit                 int     `toml:"step_limit"`
	CostLimit                 float64 `toml:"cost_limit"`
}

// ModelConfig captures direct model overrides under the top-level [model]
// section. These values are used by shell-agent and can provide fallback
// OpenAI-compatible settings for the orchestrator when aliases are absent.
type ModelConfig struct {
	ModelName   string         `toml:"model_name"`
	APIKey      string         `toml:"api_key"`
	ModelKwargs map[string]any `toml:"model_kwargs"`
}

// EnvironmentConfig describes shell execution settings loaded from the
// [environment] section of the unified configuration.
type EnvironmentConfig struct {
	Type          string            `toml:"type"`
	Timeout       int               `toml:"timeout"`
	CWD           string            `toml:"cwd"`
	EnvVars       map[string]string `toml:"env_vars"`
	Image         string            `toml:"image"`
	Runtime       string            `toml:"runtime"`
	TrajectoryDir string            `toml:"trajectory_dir"`
}

type ProviderConfig struct {
	BaseURL  string            `toml:"base_url"`
	APIKey   string            `toml:"api_key"`
	Headers  map[string]string `toml:"headers"`
	Query    map[string]string `toml:"query"`
	Endpoint string            `toml:"endpoint"`
}

type ModelDefinition struct {
	Provider string         `toml:"provider"`
	Model    string         `toml:"model"`
	Params   map[string]any `toml:"params"`
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
}

type configData struct {
	path   string
	config Config
}

var (
	configOnce sync.Once
	cfgData    *configData
	cfgErr     error
)

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

	resolved := ResolvedModel{
		Alias:        effectiveAlias,
		ProviderName: providerName,
		BaseURL:      baseURL,
		Headers:      copyStringMap(provider.Headers),
		Query:        copyStringMap(provider.Query),
		Endpoint:     strings.TrimSpace(provider.Endpoint),
		Model:        strings.TrimSpace(modelDef.Model),
		Params:       deepCopyMap(modelDef.Params),
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
		cfgData = &configData{path: path, config: cfg}
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
	if v, ok := raw["listen"].(string); ok {
		cfg.Listen = v
	}
	if v, ok := raw["default_model"].(string); ok {
		cfg.DefaultModel = v
	}
	if modelRaw, ok := toMap(raw["model"]); ok {
		modelCfg, err := parseModelSection(path, modelRaw)
		if err != nil {
			return Config{}, err
		}
		cfg.Model = modelCfg
	}
	if agentRaw, ok := toMap(raw["agent"]); ok {
		agentCfg, err := parseAgentSection(path, agentRaw)
		if err != nil {
			return Config{}, err
		}
		cfg.Agent = agentCfg
	}
	if envRaw, ok := toMap(raw["environment"]); ok {
		envCfg, err := parseEnvironmentSection(path, envRaw)
		if err != nil {
			return Config{}, err
		}
		cfg.Environment = envCfg
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

func parseModelSection(path string, data map[string]any) (*ModelConfig, error) {
	model := &ModelConfig{}
	if v, ok := data["model_name"].(string); ok {
		model.ModelName = v
	}
	if v, ok := data["api_key"].(string); ok {
		model.APIKey = v
	}
	if kwargs, ok := toMap(data["model_kwargs"]); ok {
		model.ModelKwargs = deepCopyMap(kwargs)
	}
	return model, nil
}

func parseAgentSection(path string, data map[string]any) (*AgentConfig, error) {
	agent := &AgentConfig{}
	if v, ok := data["system_template"].(string); ok {
		agent.SystemTemplate = v
	}
	if v, ok := data["instance_template"].(string); ok {
		agent.InstanceTemplate = v
	}
	if v, ok := data["timeout_template"].(string); ok {
		agent.TimeoutTemplate = v
	}
	if v, ok := data["format_error_template"].(string); ok {
		agent.FormatErrorTemplate = v
	}
	if v, ok := data["action_observation_template"].(string); ok {
		agent.ActionObservationTemplate = v
	}
	if v, ok := data["lightweight_system_template"].(string); ok {
		agent.LightweightSystemTemplate = v
	}
	if v, ok := data["lightweight_intent_template"].(string); ok {
		agent.LightweightIntentTemplate = v
	}
	if v, ok := data["lightweight_error_template"].(string); ok {
		agent.LightweightErrorTemplate = v
	}
	if v, ok := toInt(data["lightweight_max_attempts"]); ok {
		agent.LightweightMaxAttempts = v
	}
	if v, ok := toInt(data["step_limit"]); ok {
		agent.StepLimit = v
	}
	if v, ok := toFloat(data["cost_limit"]); ok {
		agent.CostLimit = v
	}
	return agent, nil
}

func parseEnvironmentSection(path string, data map[string]any) (*EnvironmentConfig, error) {
	env := &EnvironmentConfig{}
	if v, ok := data["type"].(string); ok {
		env.Type = v
	}
	if v, ok := toInt(data["timeout"]); ok {
		env.Timeout = v
	}
	if v, ok := data["cwd"].(string); ok {
		env.CWD = v
	}
	if vars, ok := toMap(data["env_vars"]); ok {
		stringMap, err := mapStringString(vars)
		if err != nil {
			return nil, fmt.Errorf("parse %s [environment.env_vars]: %w", path, err)
		}
		env.EnvVars = stringMap
	}
	if v, ok := data["image"].(string); ok {
		env.Image = v
	}
	if v, ok := data["runtime"].(string); ok {
		env.Runtime = v
	}
	if v, ok := data["trajectory_dir"].(string); ok {
		env.TrajectoryDir = v
	}
	return env, nil
}

func toMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
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

func cloneConfig(in Config) Config {
	clone := Config{
		Listen:       in.Listen,
		DefaultModel: in.DefaultModel,
		Providers:    make(map[string]ProviderConfig, len(in.Providers)),
		Models:       make(map[string]ModelDefinition, len(in.Models)),
	}
	if in.Agent != nil {
		agent := *in.Agent
		clone.Agent = &agent
	}
	if in.Environment != nil {
		env := *in.Environment
		if len(env.EnvVars) > 0 {
			env.EnvVars = copyStringMap(env.EnvVars)
		}
		clone.Environment = &env
	}
	if in.Model != nil {
		model := *in.Model
		if len(model.ModelKwargs) > 0 {
			model.ModelKwargs = deepCopyMap(model.ModelKwargs)
		}
		clone.Model = &model
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
			Provider: model.Provider,
			Model:    model.Model,
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
		Alias:        in.Alias,
		ProviderName: in.ProviderName,
		BaseURL:      in.BaseURL,
		APIKey:       in.APIKey,
		Headers:      copyStringMap(in.Headers),
		Query:        copyStringMap(in.Query),
		Endpoint:     in.Endpoint,
		Model:        in.Model,
		Params:       deepCopyMap(in.Params),
	}
	return clone
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
