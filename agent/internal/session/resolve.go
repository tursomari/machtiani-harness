package session

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
)

// ResolveSessionID resolves a user-supplied session-ID query against the
// stored session directory names.
func ResolveSessionID(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("session id required")
	}

	sessionsRoot, err := artifacts.SessionsRoot()
	if err != nil {
		return "", fmt.Errorf("resolve sessions root: %w", err)
	}
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read sessions directory: %w", err)
	}

	canonicalQuery := canonicalSessionID(query)
	var exactMatches []string
	var prefixMatches []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		canonicalName := canonicalSessionID(name)
		if canonicalName == canonicalQuery {
			exactMatches = append(exactMatches, name)
		}
		if strings.HasPrefix(canonicalName, canonicalQuery) {
			prefixMatches = append(prefixMatches, name)
		}
	}

	if len(exactMatches) == 1 {
		return exactMatches[0], nil
	}
	switch len(prefixMatches) {
	case 0:
		return "", fmt.Errorf("unknown session: %q", query)
	case 1:
		return prefixMatches[0], nil
	default:
		sort.Strings(prefixMatches)
		return "", fmt.Errorf("ambiguous session id %q: %d candidates: %s", query, len(prefixMatches), strings.Join(prefixMatches, ", "))
	}
}

func canonicalSessionID(value string) string {
	return strings.ReplaceAll(strings.ToUpper(value), "-", "")
}
