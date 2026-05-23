package session

import (
	"strings"
)

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func splitAskLines(question string) (string, string, bool) {
	var noShell string
	var shell string
	for _, raw := range strings.Split(question, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if noShell == "" {
			if val, ok := splitAskLabel(line, "no-shell:", "no shell:", "noshell:", "content:"); ok {
				noShell = strings.TrimSpace(val)
				continue
			}
		}
		if shell == "" {
			if val, ok := splitAskLabel(line, "shell:", "command:", "commands:"); ok {
				shell = strings.TrimSpace(val)
				continue
			}
		}
	}
	if noShell == "" || shell == "" {
		return "", "", false
	}
	return noShell, shell, true
}

func collapseSplitAskLines(noShell, shell string) string {
	parts := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(noShell); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if trimmed := strings.TrimSpace(shell); trimmed != "" {
		parts = append(parts, trimmed)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func applySingleAskRoutingPolicy(hasSplitAsk bool, useShellAgent bool) (bool, bool) {
	if hasSplitAsk {
		return useShellAgent, false
	}
	if useShellAgent {
		return true, false
	}
	return true, true
}

func splitAskLabel(line string, labels ...string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	for _, label := range labels {
		if strings.HasPrefix(lower, label) {
			return strings.TrimSpace(trimmed[len(label):]), true
		}
	}
	return "", false
}

func countTurns(doc string) int {
	lines := strings.Split(doc, "\n")
	count := 0
	hasTurnZero := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "== Turn ") {
			count++
			if strings.HasPrefix(trimmed, "== Turn 0") {
				hasTurnZero = true
			}
		}
	}
	if hasTurnZero && count > 0 {
		count--
	}
	return count
}
