package patcher

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// BuildBaselineDiffSection renders a unified diff between the captured baseline and the current
// workspace file for relPath. The section is wrapped in a ```diff fenced block and includes the file
// path header. When neither baseline nor workspace copy exists, it returns included=false.
func BuildBaselineDiffSection(state *BaselineState, workspaceRoot, relPath string) (section string, included bool, err error) {
	rel := filepath.ToSlash(strings.TrimSpace(relPath))
	if rel == "" {
		return "", false, nil
	}
	workspacePath := filepath.Join(workspaceRoot, filepath.FromSlash(rel))
	workspaceInfo, workspaceErr := os.Lstat(workspacePath)
	workspaceExists := workspaceErr == nil
	if workspaceErr != nil && !errors.Is(workspaceErr, os.ErrNotExist) {
		return "", false, fmt.Errorf("stat workspace file %s: %w", rel, workspaceErr)
	}

	baselinePath := ""
	baselineExists := false
	if state != nil {
		if _, err := state.ensureManifestRecord(rel); err != nil {
			return "", false, fmt.Errorf("ensure baseline record %s: %w", rel, err)
		}
		candidate := state.FilePath(rel)
		if candidate != "" {
			if info, err := os.Lstat(candidate); err == nil {
				baselinePath = candidate
				baselineExists = !info.IsDir()
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", false, fmt.Errorf("stat baseline file %s: %w", rel, err)
			}
		}
	}

	if !workspaceExists && !baselineExists {
		return "(new file)", true, nil
	}

	left := baselinePath
	if !baselineExists {
		left = os.DevNull
	}
	right := workspacePath
	if !workspaceExists {
		right = os.DevNull
	}
	diffText, err := limitedContextDiff(left, right)
	if err != nil {
		return "", false, fmt.Errorf("diff %s: %w", rel, err)
	}
	trimmed := strings.TrimSpace(diffText)
	var diffBody string
	switch {
	case strings.Contains(diffText, "Binary files"):
		diffBody = "[binary file diff omitted]"
	case trimmed == "":
		diffBody = "(no changes)"
	default:
		diffBody = strings.TrimRight(diffText, "\n")
	}

	var b strings.Builder
	b.WriteString("File: ")
	b.WriteString(rel)
	if !workspaceExists && baselineExists {
		b.WriteString(" (deleted in workspace)")
	}
	if workspaceExists && workspaceInfo != nil && workspaceInfo.Mode()&os.ModeSymlink != 0 {
		b.WriteString(" (symlink)")
	}
	b.WriteString("\n")
	b.WriteString("Unified diff (baseline vs workspace):\n")
	b.WriteString("```diff\n")
	b.WriteString(diffBody)
	b.WriteString("\n```")
	return b.String(), true, nil
}

// BuildBaselineFullDiffSection renders a full-context, line-numbered diff between the captured
// baseline and the current workspace file for relPath. The section is wrapped in a ```diff fenced
// block and includes the file path header. When there are no diff hunks, it returns included=false.
func BuildBaselineFullDiffSection(state *BaselineState, workspaceRoot, relPath string) (section string, included bool, err error) {
	rel := filepath.ToSlash(strings.TrimSpace(relPath))
	if rel == "" {
		return "", false, nil
	}
	workspacePath := filepath.Join(workspaceRoot, filepath.FromSlash(rel))
	workspaceInfo, workspaceErr := os.Lstat(workspacePath)
	workspaceExists := workspaceErr == nil
	if workspaceErr != nil && !errors.Is(workspaceErr, os.ErrNotExist) {
		return "", false, fmt.Errorf("stat workspace file %s: %w", rel, workspaceErr)
	}

	baselinePath := ""
	baselineExists := false
	if state != nil {
		if _, err := state.ensureManifestRecord(rel); err != nil {
			return "", false, fmt.Errorf("ensure baseline record %s: %w", rel, err)
		}
		candidate := state.FilePath(rel)
		if candidate != "" {
			if info, err := os.Lstat(candidate); err == nil {
				baselinePath = candidate
				baselineExists = !info.IsDir()
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", false, fmt.Errorf("stat baseline file %s: %w", rel, err)
			}
		}
	}

	if !workspaceExists && !baselineExists {
		return "", false, nil
	}

	left := baselinePath
	if !baselineExists {
		left = os.DevNull
	}
	right := workspacePath
	if !workspaceExists {
		right = os.DevNull
	}
	diffText, err := fullContextDiff(left, right)
	if err != nil {
		return "", false, fmt.Errorf("diff %s: %w", rel, err)
	}

	trimmed := strings.TrimSpace(diffText)
	if trimmed == "" {
		return "", false, nil
	}

	var diffBody string
	switch {
	case strings.Contains(diffText, "Binary files"):
		diffBody = "[binary file diff omitted]"
	default:
		diffBody = formatDiffWithLineNumbers(diffText)
	}
	if strings.TrimSpace(diffBody) == "" {
		return "", false, nil
	}

	var b strings.Builder
	b.WriteString("File: ")
	b.WriteString(rel)
	if !workspaceExists && baselineExists {
		b.WriteString(" (deleted in workspace)")
	}
	if workspaceExists && workspaceInfo != nil && workspaceInfo.Mode()&os.ModeSymlink != 0 {
		b.WriteString(" (symlink)")
	}
	b.WriteString("\n\n")
	b.WriteString("Full diff (post-patch above):\n")
	b.WriteString("```diff\n")
	b.WriteString(strings.TrimRight(diffBody, "\n"))
	b.WriteString("\n```")
	return b.String(), true, nil
}

func limitedContextDiff(left, right string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.Command("git", "diff", "--no-index", "--text", "--unified=6", left, right)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == 1 {
				return stdout.String(), nil
			}
			return "", fmt.Errorf("git diff failed: %s", strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("git diff error: %w", err)
	}
	return stdout.String(), nil
}

func fullContextDiff(left, right string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.Command("git", "diff", "--no-index", "--text", "--unified=99999999", left, right)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == 1 {
				return stdout.String(), nil
			}
			return "", fmt.Errorf("git diff failed: %s", strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("git diff error: %w", err)
	}
	return stdout.String(), nil
}

func formatDiffWithLineNumbers(diffData string) string {
	scanner := bufio.NewScanner(strings.NewReader(diffData))
	var b strings.Builder
	newLineNum := 0

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "@@") {
			if start, ok := parseNewLineStart(line); ok {
				if start > 0 {
					newLineNum = start - 1
				} else {
					newLineNum = 0
				}
			}
			continue
		}
		if strings.HasPrefix(line, "diff --git ") ||
			strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "--- ") ||
			strings.HasPrefix(line, "+++ ") ||
			strings.HasPrefix(line, "\\ No newline at end of file") {
			continue
		}
		if line == "" {
			continue
		}

		prefix := line[0]
		content := line[1:]
		switch prefix {
		case ' ':
			newLineNum++
			fmt.Fprintf(&b, "%6d    %s\n", newLineNum, content)
		case '+':
			newLineNum++
			fmt.Fprintf(&b, "%6d  + %s\n", newLineNum, content)
		case '-':
			fmt.Fprintf(&b, "        - %s\n", content)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func parseNewLineStart(line string) (int, bool) {
	if !strings.HasPrefix(line, "@@") {
		return 0, false
	}
	parts := strings.Fields(line)
	for _, part := range parts {
		if strings.HasPrefix(part, "+") {
			value := strings.TrimPrefix(part, "+")
			if idx := strings.IndexByte(value, ','); idx != -1 {
				value = value[:idx]
			}
			start, err := strconv.Atoi(value)
			if err != nil {
				return 0, false
			}
			return start, true
		}
	}
	return 0, false
}
