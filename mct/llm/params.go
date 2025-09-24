package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseParamOverrides converts repeated --param key=value and --param-json JSON blobs
// into a single map ready to merge with the resolved model defaults.
func ParseParamOverrides(pairs []string, jsonDocs []string) (map[string]any, error) {
	extra := map[string]any{}
	for _, pair := range pairs {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		idx := strings.Index(pair, "=")
		if idx <= 0 {
			return nil, fmt.Errorf("invalid param %q; expected key=value", pair)
		}
		key := strings.TrimSpace(pair[:idx])
		rawVal := strings.TrimSpace(pair[idx+1:])
		if key == "" {
			return nil, fmt.Errorf("invalid param %q; key is empty", pair)
		}
		var val any
		if rawVal == "" {
			val = ""
		} else {
			if err := json.Unmarshal([]byte(rawVal), &val); err != nil {
				val = rawVal
			}
		}
		extra[key] = val
	}

	merged := extra
	for _, doc := range jsonDocs {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(doc), &obj); err != nil {
			return nil, fmt.Errorf("invalid param-json payload: %w", err)
		}
		merged = mergeMaps(merged, obj)
	}
	return merged, nil
}
