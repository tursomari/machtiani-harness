package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/tursomari/machtiani/agent/internal/configcatalog"
)

const (
	modelDiscoveryLimit     = 25
	modelDiscoveryBodyLimit = 4 << 20
)

var modelDiscoveryHTTPClient = &http.Client{Timeout: 15 * time.Second}

type discoveredModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	ContextLength int    `json:"context_length"`
}

func discoverProviderModels(client *http.Client, provider configcatalog.Provider, apiKey, search string, ellipsis ...string) ([]discoveredModel, error) {
	if strings.TrimSpace(provider.ModelsURL) == "" {
		return nil, fmt.Errorf("%s does not define model discovery", provider.Name)
	}
	discoveryURL, err := url.Parse(provider.ModelsURL)
	if err != nil {
		return nil, fmt.Errorf("parse %s model discovery URL: %w", provider.Name, err)
	}
	if parameter := strings.TrimSpace(provider.ModelsSearchParam); parameter != "" {
		query := discoveryURL.Query()
		query.Set(parameter, strings.TrimSpace(search))
		discoveryURL.RawQuery = query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, discoveryURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create model discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "mct-agent/model-discovery")
	if value := strings.TrimSpace(apiKey); value != "" {
		req.Header.Set("Authorization", "Bearer "+value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s models: %w", provider.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s models: HTTP %s", provider.Name, resp.Status)
	}
	var payload struct {
		Data []discoveredModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, modelDiscoveryBodyLimit)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode %s models: %w", provider.Name, err)
	}
	needle := strings.ToLower(strings.TrimSpace(search))
	seen := make(map[string]struct{}, len(payload.Data))
	matches := make([]discoveredModel, 0, min(len(payload.Data), modelDiscoveryLimit))
	for _, model := range payload.Data {
		model.ID = strings.TrimSpace(model.ID)
		model.Name = compactDiscoveryText(model.Name, 80, ellipsis...)
		model.Description = compactDiscoveryText(model.Description, 120, ellipsis...)
		if model.ID == "" || strings.IndexFunc(model.ID, unicode.IsControl) >= 0 {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(model.ID), needle) && !strings.Contains(strings.ToLower(model.Name), needle) {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		matches = append(matches, model)
	}
	if len(matches) > modelDiscoveryLimit {
		matches = matches[:modelDiscoveryLimit]
	}
	return matches, nil
}

func compactDiscoveryText(value string, maxRunes int, ellipsis ...string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	characters := []rune(value)
	if len(characters) <= maxRunes {
		return value
	}
	marker := "…"
	if len(ellipsis) > 0 && ellipsis[0] != "" {
		marker = ellipsis[0]
	}
	keep := maxRunes - len([]rune(marker))
	if keep < 0 {
		keep = 0
	}
	return string(characters[:keep]) + marker
}

func suggestedModelAlias(modelID string) string {
	value := strings.TrimSpace(modelID)
	if slash := strings.LastIndex(value, "/"); slash >= 0 {
		value = value[slash+1:]
	}
	var result strings.Builder
	lastDash := false
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '.' || character == '_' || character == '-' {
			result.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash && result.Len() > 0 {
			result.WriteByte('-')
			lastDash = true
		}
	}
	alias := strings.Trim(result.String(), "-._")
	if alias == "" {
		return "model"
	}
	return alias
}

func uniqueModelAliasSuggestion(modelID string, models map[string]any) string {
	base := suggestedModelAlias(modelID)
	if _, exists := models[base]; !exists {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if _, exists := models[candidate]; !exists {
			return candidate
		}
	}
}
