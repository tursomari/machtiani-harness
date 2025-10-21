package session

import (
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

func countTurns(md string) int {
	lines := strings.Split(md, "\n")
	count := 0
	hasTurnZero := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "## Turn ") {
			count++
			if strings.HasPrefix(trimmed, "## Turn 0") {
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
