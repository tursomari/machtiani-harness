package run

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/tursomari/machtiani/agent/internal/templates"
)

func TemplateWithFallback(configValue string, embeddedKey string) (string, error) {
	if trimmed := strings.TrimSpace(configValue); trimmed != "" {
		return trimmed, nil
	}
	if trimmedKey := strings.TrimSpace(embeddedKey); trimmedKey != "" {
		if embedded, err := templates.GetEmbeddedTemplate(trimmedKey); err == nil {
			if embeddedTrim := strings.TrimSpace(embedded); embeddedTrim != "" {
				return embeddedTrim, nil
			}
		}
	}
	return "", fmt.Errorf("template not configured")
}

// MergeVars merges maps from lowest to highest precedence, returning a new map.
func MergeVars(parts ...map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{})
	for _, part := range parts {
		for k, v := range part {
			merged[k] = v
		}
	}
	return merged
}

// FlattenConfig converts configuration structs into a plain string-keyed map.
func FlattenConfig(values ...interface{}) map[string]interface{} {
	flat := make(map[string]interface{})
	for _, v := range values {
		if v == nil {
			continue
		}
		m, err := toMap(v)
		if err != nil {
			continue
		}
		for k, val := range m {
			flat[k] = val
		}
	}
	return flat
}

func toMap(v interface{}) (map[string]interface{}, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RenderTemplate renders a Go text/template using the provided variables with strict missing key checking.
func RenderTemplate(tmplStr string, vars map[string]interface{}) (string, error) {
	tmpl, err := template.New("").Option("missingkey=error").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("template parse: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("template render: %w", err)
	}

	return buf.String(), nil
}
