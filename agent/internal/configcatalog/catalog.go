package configcatalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

//go:embed catalog.json
var embeddedCatalog []byte

var (
	idPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type Catalog struct {
	SchemaVersion int        `json:"schema_version"`
	Providers     []Provider `json:"providers"`
}

type Provider struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Description       string  `json:"description"`
	Protocol          string  `json:"protocol"`
	BaseURL           string  `json:"base_url"`
	Endpoint          string  `json:"endpoint"`
	APIKeyEnv         string  `json:"api_key_env"`
	DocumentationURL  string  `json:"documentation_url"`
	ModelsURL         string  `json:"models_url,omitempty"`
	ModelsSearchParam string  `json:"models_search_param,omitempty"`
	ReasoningFormat   string  `json:"reasoning_format"`
	DefaultModel      string  `json:"default_model"`
	Models            []Model `json:"models"`
}

type Model struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Alias        string   `json:"alias"`
	Description  string   `json:"description"`
	Reasoning    []string `json:"reasoning"`
	CacheDefault string   `json:"cache_default"`
}

func Load() (Catalog, error) {
	var catalog Catalog
	if err := json.Unmarshal(embeddedCatalog, &catalog); err != nil {
		return Catalog{}, fmt.Errorf("parse embedded provider catalog: %w", err)
	}
	if err := Validate(catalog); err != nil {
		return Catalog{}, fmt.Errorf("validate embedded provider catalog: %w", err)
	}
	return catalog, nil
}

func (c Catalog) Provider(id string) (Provider, bool) {
	for _, provider := range c.Providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

func (p Provider) Model(id string) (Model, bool) {
	for _, model := range p.Models {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}

func (p Provider) Default() (Model, bool) {
	return p.Model(p.DefaultModel)
}

func Validate(catalog Catalog) error {
	if catalog.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version %d", catalog.SchemaVersion)
	}
	if len(catalog.Providers) == 0 {
		return fmt.Errorf("providers must not be empty")
	}
	providerIDs := make(map[string]struct{}, len(catalog.Providers))
	for i, provider := range catalog.Providers {
		path := fmt.Sprintf("providers[%d]", i)
		if !idPattern.MatchString(provider.ID) {
			return fmt.Errorf("%s.id %q is invalid", path, provider.ID)
		}
		if _, exists := providerIDs[provider.ID]; exists {
			return fmt.Errorf("duplicate provider id %q", provider.ID)
		}
		providerIDs[provider.ID] = struct{}{}
		if strings.TrimSpace(provider.Name) == "" {
			return fmt.Errorf("%s.name must not be empty", path)
		}
		if provider.Protocol != "openai-chat" {
			return fmt.Errorf("%s.protocol %q is unsupported", path, provider.Protocol)
		}
		if provider.ReasoningFormat != "reasoning_effort" && provider.ReasoningFormat != "reasoning" {
			return fmt.Errorf("%s.reasoning_format %q is unsupported", path, provider.ReasoningFormat)
		}
		parsedURL, err := url.Parse(provider.BaseURL)
		if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" {
			return fmt.Errorf("%s.base_url must be an HTTPS URL", path)
		}
		if !strings.HasPrefix(provider.Endpoint, "/") {
			return fmt.Errorf("%s.endpoint must start with /", path)
		}
		if !envPattern.MatchString(provider.APIKeyEnv) {
			return fmt.Errorf("%s.api_key_env %q is invalid", path, provider.APIKeyEnv)
		}
		if provider.DocumentationURL != "" {
			docsURL, docsErr := url.Parse(provider.DocumentationURL)
			if docsErr != nil || docsURL.Scheme != "https" || docsURL.Host == "" {
				return fmt.Errorf("%s.documentation_url must be an HTTPS URL", path)
			}
		}
		if provider.ModelsURL != "" {
			modelsURL, modelsErr := url.Parse(provider.ModelsURL)
			if modelsErr != nil || modelsURL.Scheme != "https" || modelsURL.Host == "" {
				return fmt.Errorf("%s.models_url must be an HTTPS URL", path)
			}
		}
		if provider.ModelsSearchParam != "" && provider.ModelsURL == "" {
			return fmt.Errorf("%s.models_search_param requires models_url", path)
		}
		if strings.ContainsAny(provider.ModelsSearchParam, "&=?# ") {
			return fmt.Errorf("%s.models_search_param %q is invalid", path, provider.ModelsSearchParam)
		}
		if len(provider.Models) == 0 {
			return fmt.Errorf("%s.models must not be empty", path)
		}

		modelIDs := make(map[string]struct{}, len(provider.Models))
		aliases := make(map[string]struct{}, len(provider.Models))
		for j, model := range provider.Models {
			modelPath := fmt.Sprintf("%s.models[%d]", path, j)
			if strings.TrimSpace(model.ID) == "" {
				return fmt.Errorf("%s.id must not be empty", modelPath)
			}
			if _, exists := modelIDs[model.ID]; exists {
				return fmt.Errorf("%s has duplicate model id %q", path, model.ID)
			}
			modelIDs[model.ID] = struct{}{}
			if strings.TrimSpace(model.Name) == "" {
				return fmt.Errorf("%s.name must not be empty", modelPath)
			}
			if !idPattern.MatchString(model.Alias) {
				return fmt.Errorf("%s.alias %q is invalid", modelPath, model.Alias)
			}
			if _, exists := aliases[model.Alias]; exists {
				return fmt.Errorf("%s has duplicate model alias %q", path, model.Alias)
			}
			aliases[model.Alias] = struct{}{}
			if model.CacheDefault != "inherit" && model.CacheDefault != "enabled" && model.CacheDefault != "disabled" {
				return fmt.Errorf("%s.cache_default %q is invalid", modelPath, model.CacheDefault)
			}
			reasoning := make(map[string]struct{}, len(model.Reasoning))
			for _, effort := range model.Reasoning {
				if strings.TrimSpace(effort) == "" {
					return fmt.Errorf("%s.reasoning contains an empty value", modelPath)
				}
				if _, exists := reasoning[effort]; exists {
					return fmt.Errorf("%s.reasoning contains duplicate value %q", modelPath, effort)
				}
				reasoning[effort] = struct{}{}
			}
		}
		if _, exists := modelIDs[provider.DefaultModel]; !exists {
			return fmt.Errorf("%s.default_model %q is not in models", path, provider.DefaultModel)
		}
	}
	return nil
}
