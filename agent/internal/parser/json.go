package parser

import (
	"encoding/json"
	"errors"
	"strings"
)

// ExtractJSONObjectPayload scans arbitrary text and returns the outermost JSON
// object payload. It tolerates leading/trailing text and fenced blocks.
func ExtractJSONObjectPayload(body string) ([]byte, error) {
	s := strings.TrimSpace(body)
	if s == "" {
		return nil, errors.New("empty body")
	}

	runes := []rune(s)
	start := -1
	depth := 0
	inString := false
	escaped := false
	end := -1

	for idx, r := range runes {
		if start < 0 {
			if r == '{' {
				start = idx
				depth = 1
				inString = false
				escaped = false
			}
			continue
		}

		if inString {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}

		switch r {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = idx
				break
			}
		}
	}

	if start < 0 || end <= start {
		return nil, errors.New("no json object found")
	}

	payload := string(runes[start : end+1])
	var parsed any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return nil, err
	}
	if _, ok := parsed.(map[string]any); !ok {
		return nil, errors.New("expected JSON object")
	}
	return []byte(payload), nil
}
