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
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "## Turn ") {
			n++
		}
	}
	return n
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
