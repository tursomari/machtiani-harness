package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/tursomari/machtiani/mct/internal/git"
)

type Config struct {
	Listen       string                     `toml:"listen"`
	DefaultModel string                     `toml:"default_model"`
	Providers    map[string]ProviderConfig  `toml:"providers"`
	Models       map[string]ModelDefinition `toml:"models"`
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
	if strings.TrimSpace(provider.BaseURL) == "" {
		return ResolvedModel{}, fmt.Errorf("provider %q missing base_url in %s", providerName, cfg.path)
	}
	if strings.TrimSpace(provider.APIKey) == "" {
		return ResolvedModel{}, fmt.Errorf("provider %q missing api_key in %s", providerName, cfg.path)
	}
	resolved := ResolvedModel{
		Alias:        effectiveAlias,
		ProviderName: providerName,
		BaseURL:      strings.TrimSpace(provider.BaseURL),
		APIKey:       strings.TrimSpace(provider.APIKey),
		Headers:      copyStringMap(provider.Headers),
		Query:        copyStringMap(provider.Query),
		Endpoint:     strings.TrimSpace(provider.Endpoint),
		Model:        strings.TrimSpace(modelDef.Model),
		Params:       deepCopyMap(modelDef.Params),
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
