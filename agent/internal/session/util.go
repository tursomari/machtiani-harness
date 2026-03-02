package session

import (
	"fmt"
	"os"
	"strings"

	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	"github.com/tursomari/machtiani/agent/internal/transcript"
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

func applySingleAskRoutingPolicy(hasSplitAsk bool, showFileHandled bool, useShellAgent bool) (bool, bool) {
	if hasSplitAsk {
		return useShellAgent, false
	}
	if showFileHandled {
		return false, false
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

func convertPatchMessages(msgs []mctpatcher.PatchValidationMessage) []transcript.PatchValidationMessage {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]transcript.PatchValidationMessage, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, transcript.PatchValidationMessage{
			Severity: strings.TrimSpace(msg.Severity),
			Path:     strings.TrimSpace(msg.Path),
			Line:     msg.Line,
			Message:  strings.TrimSpace(firstNonEmpty(msg.Message, msg.Raw)),
			Raw:      strings.TrimSpace(msg.Raw),
		})
	}
	return out
}

func formatContentConflicts(conflicts []mctpatcher.ContentConflictDiagnostic) []string {
	if len(conflicts) == 0 {
		return nil
	}
	formatted := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		var b strings.Builder
		header := fmt.Sprintf("* Edit[%d] %s: %s\n", conflict.EditIndex, strings.TrimSpace(conflict.Path), strings.TrimSpace(conflict.Reason))
		b.WriteString(header)
		for _, h := range conflict.HunkConflicts {
			b.WriteString(fmt.Sprintf("  Hunk[%d] %s\n", h.HunkIndex, strings.TrimSpace(h.Reason)))
			b.WriteString(fmt.Sprintf("    Expected lines: %d, actual: %d\n", h.ExpectedLines, h.ActualLines))
			expectedHash := truncateHash(h.ExpectedHash)
			actualHash := truncateHash(h.ActualHash)
			if expectedHash != "" || actualHash != "" {
				b.WriteString(fmt.Sprintf("    Expected hash: %s\n", expectedHash))
				b.WriteString(fmt.Sprintf("    Actual hash:   %s\n", actualHash))
			}
			if strings.TrimSpace(h.DiffPreview) != "" {
				b.WriteString(indentMultiline("    ", strings.TrimSpace(h.DiffPreview)))
				b.WriteString("\n")
			}
		}
		formatted = append(formatted, b.String())
	}
	return formatted
}

func truncateHash(hash string) string {
	trimmed := strings.TrimSpace(hash)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 16 {
		return trimmed
	}
	return trimmed[:16] + "..."
}

func indentMultiline(prefix, text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = line
			continue
		}
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func patchDiffForTranscript(path string, limit int) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("patch path empty")
	}
	data, err := os.ReadFile(trimmed)
	if err != nil {
		return "", err
	}
	diff := strings.TrimSpace(string(data))
	if diff == "" {
		return "(empty patch diff)", nil
	}
	truncated := false
	if limit > 0 && len(diff) > limit {
		truncated = true
		if limit <= 3 {
			diff = diff[:limit]
		} else {
			diff = diff[:limit-3] + "..."
		}
	}
	if truncated {
		diff += "\n\n[diff truncated]"
	}
	return diff, nil
}
