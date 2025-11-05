package llm

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

type apiKeyOverrideContextKey struct{}

// ParseAPIKeyOverrides parses provider-specific API key overrides from CLI flag
// values in the format "provider:key". Provider names are normalized to
// lower-case for case-insensitive matching. The returned map is nil if no
// overrides were provided.
func ParseAPIKeyOverrides(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	overrides := make(map[string]string, len(values))
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return nil, fmt.Errorf("api key override: empty value")
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("api key override: expected format provider:key")
		}
		provider := strings.TrimSpace(parts[0])
		apiKey := strings.TrimSpace(parts[1])
		if provider == "" {
			return nil, fmt.Errorf("api key override: provider name required")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("api key override for provider %q requires non-empty api key", provider)
		}
		overrides[normalizeProviderKey(provider)] = apiKey
	}
	if len(overrides) == 0 {
		return nil, nil
	}
	return overrides, nil
}

// WithAPIKeyOverrides attaches provider API key overrides to the supplied
// context. Callers should treat the returned context as immutable.
func WithAPIKeyOverrides(ctx context.Context, overrides map[string]string) context.Context {
	if len(overrides) == 0 {
		return ctx
	}
	cp := copyAPIKeyOverrides(overrides)
	return context.WithValue(ctx, apiKeyOverrideContextKey{}, cp)
}

func apiKeyOverridesFromContext(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(apiKeyOverrideContextKey{})
	if raw == nil {
		return nil
	}
	if overrides, ok := raw.(map[string]string); ok {
		return overrides
	}
	return nil
}

func copyAPIKeyOverrides(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[normalizeProviderKey(k)] = v
	}
	return out
}

func normalizeProviderKey(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func lookupAPIKeyOverride(overrides map[string]string, names ...string) (string, bool) {
	if len(overrides) == 0 {
		return "", false
	}
	for _, name := range names {
		if trimmed := normalizeProviderKey(name); trimmed != "" {
			if key, ok := overrides[trimmed]; ok {
				return key, true
			}
		}
	}
	return "", false
}

// ValidateAPIKeyOverrideProvider checks if any provider-specific overrides were
// provided that don't match the model's configured provider. If a mismatch is
// detected, it returns a clear error message indicating what the user should do.
// This ensures users get immediate feedback if they specify an API key override
// for the wrong provider.
//
// The validation passes if:
// 1. The configured provider has an override key provided, OR
// 2. The model alias has an override key provided, OR
// 3. No overrides were provided
//
// If overrides ARE provided but none match the configured provider or alias,
// the validation fails to prevent accidental use of the wrong provider.
func ValidateAPIKeyOverrideProvider(overrides map[string]string, modelAlias, configuredProvider string) error {
	if len(overrides) == 0 {
		return nil
	}

	normalizedConfigured := normalizeProviderKey(configuredProvider)
	if normalizedConfigured == "" {
		return nil
	}

	aliasKey := normalizeProviderKey(modelAlias)

	// Check if the configured provider exists in overrides
	if _, ok := overrides[normalizedConfigured]; ok {
		return nil
	}

	// Check if the model alias exists in overrides (fallback lookup)
	if aliasKey != "" {
		if _, ok := overrides[aliasKey]; ok {
			return nil
		}
	}

	// If we reach here, no matching override was found.
	// To prevent accidental provider mismatches when multiple overrides are
	// provided, we must report an error.
	//
	// Find any override provider to include in the error message.
	// We iterate through the map to get one provider (order doesn't matter
	// since they're all mismatches).
	var firstMismatchProvider string
	for provider := range overrides {
		firstMismatchProvider = provider
		break
	}

	return fmt.Errorf(
		"API key override provider %q does not match model %q configured provider %q. "+
			"Specify --api-key=%s:<api-key> instead",
		firstMismatchProvider, modelAlias, normalizedConfigured, normalizedConfigured,
	)
}

func providerEnvVarName(provider string) string {
	trimmed := strings.TrimSpace(provider)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case unicode.IsLetter(r):
			b.WriteRune(unicode.ToUpper(r))
		case unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "_API_KEY"
}

// CopyAPIKeyOverridesForRuntime returns a defensive copy of overrides for
// inclusion in runtime structs. It preserves the normalized provider keys.
func CopyAPIKeyOverridesForRuntime(overrides map[string]string) map[string]string {
	if len(overrides) == 0 {
		return nil
	}
	cp := make(map[string]string, len(overrides))
	for k, v := range overrides {
		cp[k] = v
	}
	return cp
}
