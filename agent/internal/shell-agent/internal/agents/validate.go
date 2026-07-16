package agents

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// validateBashSyntax runs `bash -n -c` against the supplied command
// and returns an error (with a one-line excerpt of stderr) when the
// shell rejects the syntax. It is a no-op when the agent is not
// enforcing early-turn behaviour, so the relaxed mode is unaffected.
//
// Safety: `bash -n` parses the command without executing any of it,
// so the validator cannot interfere with background execution or
// trigger side effects. The exec.CommandContext timeout prevents
// pathological strings from hanging the agent loop.
func validateBashSyntax(command string) error {
	ctx, cancel := context.WithTimeout(context.Background(), bashSyntaxValidationTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-n", "-c", command)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	// ctx.Err() is set when the timeout elapsed. Surface a clear
	// message so the model knows the check itself failed (and a
	// future retry should still produce the same result).
	if ctxErr := ctx.Err(); ctxErr == context.DeadlineExceeded {
		return fmt.Errorf("bash syntax check timed out after %s", bashSyntaxValidationTimeout)
	}
	first := firstNonEmptyLine(string(output))
	if first == "" {
		first = err.Error()
	}
	return fmt.Errorf("bash syntax error: %s", strings.TrimSpace(first))
}

func validateSearchScope(command string) (string, error) {
	segments := splitCommandSegments(command)
	if len(segments) == 0 {
		return command, nil
	}
	if hasDirectoryChangeSegment(segments[:len(segments)-1]) {
		return command, nil
	}

	fields := skipLeadingAssignments(strings.Fields(segments[len(segments)-1]))
	if len(fields) == 0 || filepath.Base(fields[0]) != "grep" {
		return command, nil
	}

	hasRecursive := false
	hasRootTarget := false
	for _, field := range fields[1:] {
		if isRecursiveGrepFlag(field) {
			hasRecursive = true
		}
		switch field {
		case ".", "./":
			hasRootTarget = true
		}
	}
	if hasRecursive && hasRootTarget {
		if translated, ok := translateGrepToRg(command); ok {
			log.Printf("grep-to-rg: translated %q to %q", command, translated)
			return translated, nil
		}
		return command, fmt.Errorf("broad recursive grep from the project root is not allowed; use rg or narrow the scope with cd into a relevant subdirectory or explicit paths")
	}
	return command, nil
}

func splitCommandSegments(command string) []string {
	segments := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(command); i++ {
		switch command[i] {
		case ';':
			if seg := strings.TrimSpace(command[start:i]); seg != "" {
				segments = append(segments, seg)
			}
			start = i + 1
		case '&', '|':
			if i+1 < len(command) && command[i+1] == command[i] {
				if seg := strings.TrimSpace(command[start:i]); seg != "" {
					segments = append(segments, seg)
				}
				i++
				start = i + 1
			}
		}
	}
	if seg := strings.TrimSpace(command[start:]); seg != "" {
		segments = append(segments, seg)
	}
	return segments
}

func hasDirectoryChangeSegment(segments []string) bool {
	for _, segment := range segments {
		fields := skipLeadingAssignments(strings.Fields(segment))
		if len(fields) == 0 {
			continue
		}
		switch filepath.Base(fields[0]) {
		case "cd", "pushd":
			return true
		}
	}
	return false
}

func isRecursiveGrepFlag(field string) bool {
	if field == "-r" || field == "-R" || field == "--recursive" || field == "--directories=recurse" {
		return true
	}
	if strings.HasPrefix(field, "--") {
		return false
	}
	if strings.HasPrefix(field, "-") {
		return strings.ContainsAny(field[1:], "rR")
	}
	return false
}

// translateGrepToRg converts a recursive grep command targeting the project
// root into an equivalent rg command. Returns the translated command and true
// on success, or an empty string and false if the command cannot be translated.
func translateGrepToRg(command string) (string, bool) {
	segments := splitCommandSegments(command)
	if len(segments) == 0 {
		return "", false
	}
	if hasDirectoryChangeSegment(segments[:len(segments)-1]) {
		return "", false
	}

	fields := skipLeadingAssignments(strings.Fields(segments[len(segments)-1]))
	if len(fields) == 0 || filepath.Base(fields[0]) != "grep" {
		return "", false
	}

	hasRecursive := false
	hasRootTarget := false
	for _, field := range fields[1:] {
		if isRecursiveGrepFlag(field) {
			hasRecursive = true
		}
		if field == "." || field == "./" {
			hasRootTarget = true
		}
	}
	if !hasRecursive || !hasRootTarget {
		return "", false
	}

	if _, err := exec.LookPath("rg"); err != nil {
		return "", false
	}

	resultFields := []string{"rg"}
	for _, field := range fields[1:] {
		// Preserve stderr redirect.
		if strings.HasPrefix(field, "2>") {
			resultFields = append(resultFields, field)
			continue
		}

		// Skip root target paths.
		if field == "." || field == "./" {
			continue
		}

		// -P flag: rg --pcre2 may not be available.
		if field == "-P" {
			log.Printf("grep-to-rg: dropping -P flag, rg --pcre2 may not be available")
			continue
		}

		// -z flag: NUL-delimited input not supported by rg.
		if field == "-z" {
			log.Printf("grep-to-rg: dropping -z flag, NUL-delimited input not supported by rg")
			continue
		}

		// -R standalone: convert to -L (follow symlinks).
		if field == "-R" {
			resultFields = append(resultFields, "-L")
			continue
		}

		// Simple flags to skip (rg handles these by default or they are
		// not needed).
		switch field {
		case "-r", "--recursive", "--directories=recurse",
			"-I", "-E", "-G", "-H", "-h", "-T", "-Z", "-s",
			"--no-messages",
			"--color", "--color=always", "--color=never", "--color=auto",
			"--line-buffered", "--null",
			"--binary-files", "--binary-files=without-match",
			"--binary-files=binary", "--binary-files=text":
			continue
		}

		// --include=VALUE → -g VALUE
		if strings.HasPrefix(field, "--include=") {
			value := field[len("--include="):]
			if value == "*" {
				continue // -g * is a no-op
			}
			resultFields = append(resultFields, "-g", value)
			continue
		}

		// --exclude=VALUE → -g !VALUE
		if strings.HasPrefix(field, "--exclude=") {
			value := field[len("--exclude="):]
			if value == "*" {
				continue
			}
			resultFields = append(resultFields, "-g", "!"+value)
			continue
		}

		// --exclude-dir=VALUE → -g !VALUE/**
		if strings.HasPrefix(field, "--exclude-dir=") {
			value := field[len("--exclude-dir="):]
			if value == "*" {
				continue
			}
			resultFields = append(resultFields, "-g", "!"+value+"/**")
			continue
		}

		// Combined flags like -rnI: unpack, skip characters that are
		// irrelevant to rg.
		if strings.HasPrefix(field, "-") && !strings.HasPrefix(field, "--") && len(field) > 2 {
			for _, ch := range field[1:] {
				c := string(ch)
				if ch == 'R' {
					resultFields = append(resultFields, "-L")
				} else if strings.ContainsRune("rIEGHhTZs", ch) {
					continue
				} else {
					resultFields = append(resultFields, "-"+c)
				}
			}
			continue
		}

		// Keep everything else.
		resultFields = append(resultFields, field)
	}

	return strings.Join(resultFields, " "), true
}

// earlyTurnEnforcementEnabled reports whether stricter early-turn
// behaviour is currently active: the agent has emitted fewer than 3
// commands AND the caller has opted in via SetEnforceEarlyCommands(true).
// Used by renderFormatError and validateBashSyntax to gate per-turn logic.
func (a *DefaultAgent) earlyTurnEnforcementEnabled() bool {
	return a.RunConfig.EnforceEarlyCommands && a.State.commandsExecuted < 3
}

const bashSyntaxValidationTimeout = 2 * time.Second

func firstNonEmptyLine(input string) string {
	for _, line := range strings.Split(input, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func skipLeadingAssignments(fields []string) []string {
	idx := 0
	for idx < len(fields) && looksLikeEnvAssignment(fields[idx]) {
		idx++
	}
	return fields[idx:]
}

func looksLikeEnvAssignment(token string) bool {
	eq := strings.IndexByte(token, 61)
	if eq <= 0 {
		return false
	}
	name := token[:eq]
	for i, r := range name {
		if r == 95 || (r >= 65 && r <= 90) || (r >= 97 && r <= 122) {
			continue
		}
		if i > 0 && r >= 48 && r <= 57 {
			continue
		}
		return false
	}
	return true
}
