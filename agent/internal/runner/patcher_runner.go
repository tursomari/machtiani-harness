package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type PatcherRunner struct {
	Verbose   bool
	DryRun    bool
	SessionID string
	exePath   string
}

type PatchErrorKind string

const (
	PatchErrorValidation PatchErrorKind = "validation"
)

type PatchValidationMessage struct {
	Severity string
	Path     string
	Line     int
	Message  string
	Raw      string
}

type PatchValidationDiagnostics struct {
	Operation string
	Stdout    string
	Stderr    string
	Messages  []PatchValidationMessage
	Raw       string
}

type PatchRunnerError struct {
	Kind         PatchErrorKind
	Diagnostics  PatchValidationDiagnostics
	WrappedError error
}

func (e *PatchRunnerError) Error() string {
	if e == nil {
		return ""
	}
	if e.WrappedError != nil {
		return e.WrappedError.Error()
	}
	return string(e.Kind)
}

func (e *PatchRunnerError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.WrappedError
}

// Resolve locates the patcher binary strictly via PATH (dry-run stores logical name only).
func (p *PatcherRunner) Resolve() error {
	if p.DryRun {
		// In dry-run, rely on logical name for logging.
		p.exePath = "patcher"
		return nil
	}
	ep, err := exec.LookPath("patcher")
	if err != nil {
		return fmt.Errorf("patcher not found in PATH: %w", err)
	}
	p.exePath = ep
	return nil
}

// RunJSON executes: patcher --repo . --session <sessionID> --input - [--verbose]
// It returns captured stdout and stderr. In DryRun, returns "{}" stdout and empty stderr.
func (p *PatcherRunner) RunJSON(ctx context.Context, stdinBytes []byte, verbose bool) ([]byte, []byte, error) {
	if p.exePath == "" {
		return nil, nil, errors.New("patcher unresolved: call Resolve() first")
	}
	if p.DryRun {
		if p.Verbose || verbose {
			fmt.Fprintln(os.Stderr, "[patcher]", p.exePath, "--repo . --session", p.SessionID, "--input -")
		}
		return []byte("{}"), []byte(""), nil
	}
	args := []string{"--repo", ".", "--session", p.SessionID, "--input", "-"}
	if p.Verbose || verbose {
		args = append(args, "--verbose")
	}
	cmd := exec.CommandContext(ctx, p.exePath, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	cmd.Stdin = bytes.NewReader(stdinBytes)
	if p.Verbose || verbose {
		fmt.Fprintln(os.Stderr, "[patcher]", p.exePath, strings.Join(args, " "))
	}
	err := cmd.Run()
	if err != nil {
		if diag, ok := parseValidationDiagnostics(errBuf.String()); ok {
			return outBuf.Bytes(), errBuf.Bytes(), &PatchRunnerError{
				Kind:         PatchErrorValidation,
				Diagnostics:  diag,
				WrappedError: err,
			}
		}
	}
	return outBuf.Bytes(), errBuf.Bytes(), err
}

func parseValidationDiagnostics(stderr string) (PatchValidationDiagnostics, bool) {
	raw := strings.TrimSpace(stderr)
	if raw == "" {
		return PatchValidationDiagnostics{}, false
	}
	lower := strings.ToLower(raw)
	if !strings.Contains(lower, "patch does not apply cleanly") && !strings.Contains(lower, "git apply") {
		return PatchValidationDiagnostics{}, false
	}
	messages := ParseGitApplyMessages(raw)
	diag := PatchValidationDiagnostics{
		Operation: "git apply --check",
		Stderr:    raw,
		Messages:  messages,
		Raw:       raw,
	}
	return diag, true
}

func ParseGitApplyMessages(raw string) []PatchValidationMessage {
	lines := strings.Split(raw, "\n")
	var msgs []PatchValidationMessage
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "patch does not apply cleanly:") {
			line = strings.TrimSpace(line[len("patch does not apply cleanly:"):])
		}
		if line == "" {
			continue
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
	if strings.HasPrefix(strings.ToLower(text), "patch failed:") {
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
