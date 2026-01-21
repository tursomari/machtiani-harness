package session

import (
	"fmt"
	"sort"
	"strings"

	promptsvc "github.com/tursomari/machtiani/agent/internal/mct/prompt"
)

func appendShowFileWarnings(body string, warnings []string) string {
	trimmed := strings.TrimRight(body, "\n")
	var filtered []string
	for _, warn := range warnings {
		trimmedWarn := strings.TrimSpace(warn)
		if trimmedWarn == "" {
			continue
		}
		filtered = append(filtered, trimmedWarn)
	}
	if len(filtered) == 0 {
		return trimmed
	}
	var b strings.Builder
	if trimmed != "" {
		b.WriteString(trimmed)
		b.WriteString("\n\n")
	}
	b.WriteString("Warnings:\n")
	for _, warn := range filtered {
		b.WriteString(fmt.Sprintf("- %s\n", warn))
	}
	return strings.TrimRight(b.String(), "\n")
}

func showFilePartialWarnings(partial *promptsvc.SnippetDiscoveryPartialError) []string {
	if partial == nil {
		return nil
	}
	warnings := []string{}
	if len(partial.Missing) > 0 {
		warnings = append(warnings, fmt.Sprintf("snippet-discovery returned no snippets for: %s", strings.Join(partial.Missing, ", ")))
	}
	if len(partial.Invalid) > 0 {
		paths := make([]string, 0, len(partial.Invalid))
		for path := range partial.Invalid {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		details := make([]string, 0, len(paths))
		for _, path := range paths {
			details = append(details, fmt.Sprintf("%s (%s)", path, partial.Invalid[path]))
		}
		warnings = append(warnings, fmt.Sprintf("snippet-discovery returned invalid snippets for: %s", strings.Join(details, ", ")))
	}
	return warnings
}

func countSnippetRanges(snippets map[string][]promptsvc.LineRange) int {
	count := 0
	for _, ranges := range snippets {
		count += len(ranges)
	}
	return count
}
