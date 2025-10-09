package parser

import (
	"encoding/json"
	"errors"
	"strings"
)

// ExtractPatchJSONPayload scans a planner response body and extracts the outermost
// JSON object. It tolerates leading/trailing text or code fences and returns the
// raw bytes of the JSON object. Returns error if none found or invalid JSON.
func ExtractPatchJSONPayload(body string) ([]byte, error) {
	s := strings.TrimSpace(body)
	if s == "" {
		return nil, errors.New("empty body")
	}
	// Strip common markdown fences if present to be defensive, but do not rely on them.
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")

	// Find the first '{' and parse until the matching '}' tracking string state.
	runes := []rune(s)
	start := -1
	depth := 0
	inStr := false
	escape := false
	var end int
	for i, r := range runes {
		if start < 0 {
			if r == '{' {
				start = i
				depth = 1
				inStr = false
				escape = false
			}
			continue
		}
		// We are inside a JSON object; honor string escapes
		if inStr {
			if escape {
				escape = false
				continue
			}
			if r == '\\' {
				escape = true
				continue
			}
			if r == '"' {
				inStr = false
			}
			continue
		}
		if r == '"' {
			inStr = true
			continue
		}
		if r == '{' {
			depth++
			continue
		}
		if r == '}' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}
	if start < 0 || end <= start {
		return nil, errors.New("no json object found")
	}
	snippet := string(runes[start : end+1])
	// Validate JSON and ensure it's an object
	var parsed any
	if err := json.Unmarshal([]byte(snippet), &parsed); err != nil {
		return nil, err
	}
	if _, ok := parsed.(map[string]any); !ok {
		return nil, errors.New("expected JSON object")
	}
	return []byte(snippet), nil
}
