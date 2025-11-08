package patcher

import (
	"strconv"
	"strings"
)

// PatchValidationMessage captures a single diagnostic emitted by git apply.
type PatchValidationMessage struct {
	Severity string
	Path     string
	Line     int
	Message  string
	Raw      string
}

// HunkConflictDiagnostic describes why an individual hunk failed to match.
type HunkConflictDiagnostic struct {
	HunkIndex     int    `json:"hunk_index"`
	Reason        string `json:"reason"`
	ExpectedHash  string `json:"expected_hash"`
	ActualHash    string `json:"actual_hash"`
	ExpectedLines int    `json:"expected_lines"`
	ActualLines   int    `json:"actual_lines"`
	DiffPreview   string `json:"diff_preview"`
}

// ContentConflictDiagnostic captures diagnostics for a single edit failure.
type ContentConflictDiagnostic struct {
	EditIndex     int                      `json:"edit_index"`
	Path          string                   `json:"path"`
	Reason        string                   `json:"reason"`
	HunkConflicts []HunkConflictDiagnostic `json:"hunk_conflicts"`
}

// PatchValidationDiagnostics encapsulates structured diagnostics for a
// validation failure when checking patch applicability.
type PatchValidationDiagnostics struct {
	Operation        string
	Stdout           string
	Stderr           string
	Messages         []PatchValidationMessage
	ContentConflicts []ContentConflictDiagnostic
	Raw              string
}

// ParseGitApplyMessages converts stderr emitted by git apply into structured
// diagnostics suitable for presentation to end users.
func ParseGitApplyMessages(raw string) []PatchValidationMessage {
	lines := strings.Split(raw, "\n")
	var msgs []PatchValidationMessage
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "patch does not apply cleanly:") {
			line = strings.TrimSpace(line[len("patch does not apply cleanly:"):])
			if line == "" {
				continue
			}
		}
		msg := parseGitApplyLine(line)
		msgs = append(msgs, msg)
	}
	return msgs
}

func parseGitApplyLine(line string) PatchValidationMessage {
	msg := PatchValidationMessage{Severity: "error", Raw: line, Message: line}
	text := strings.TrimSpace(line)
	severity, remainder, matched := stripSeverityPrefix(text)
	if matched {
		msg.Severity = severity
		text = remainder
		msg.Message = remainder
	} else {
		msg.Message = text
	}
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "patch failed:") {
		rest := strings.TrimSpace(text[len("patch failed:"):])
		path, lineNo, extra := parsePathLine(rest)
		msg.Path = path
		msg.Line = lineNo
		msg.Message = "patch failed"
		if extra != "" {
			msg.Message = msg.Message + ": " + extra
		}
		return msg
	}
	path, lineNo, extra := parsePathLine(text)
	if path != "" {
		msg.Path = path
		msg.Line = lineNo
		if extra != "" {
			msg.Message = extra
		}
	}
	return msg
}

func stripSeverityPrefix(line string) (string, string, bool) {
	lower := strings.ToLower(line)
	prefixes := []struct {
		Prefix   string
		Severity string
	}{
		{"error:", "error"},
		{"fatal:", "error"},
		{"warning:", "warning"},
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p.Prefix) {
			return p.Severity, strings.TrimSpace(line[len(p.Prefix):]), true
		}
	}
	return "error", strings.TrimSpace(line), false
}

func parsePathLine(text string) (string, int, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", 0, ""
	}
	last := strings.LastIndex(text, ":")
	if last == -1 {
		return "", 0, text
	}
	path := strings.TrimSpace(text[:last])
	if path == "" {
		return "", 0, strings.TrimSpace(text[last+1:])
	}
	tail := strings.TrimSpace(text[last+1:])
	if tail == "" {
		return path, 0, ""
	}
	if lineNo, err := strconv.Atoi(tail); err == nil {
		return path, lineNo, ""
	}
	return path, 0, tail
}
