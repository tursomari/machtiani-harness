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

// BuildBaselineDiffSection renders a numbered full-context diff between the captured baseline and the
// current workspace file for relPath. The section is wrapped in a ```diff fenced block and includes the
// file path header. When neither baseline nor workspace copy exists, it returns included=false.
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

	var fullBody string
	trimmed := strings.TrimSpace(diffText)
	switch {
	case trimmed == "" && workspaceExists:
		fullBody, err = renderNumberedFile(workspacePath)
	case trimmed == "" && baselineExists:
		fullBody, err = renderDeletedBaseline(baselinePath)
	case strings.Contains(diffText, "Binary files"):
		fullBody = "[binary file diff omitted]"
	default:
		fullBody = formatDiffWithLineNumbers(diffText)
	}
	if err != nil {
		return "", false, err
	}
	fullBody = strings.TrimRight(fullBody, "\n")
	trimmedBody := strings.TrimSpace(fullBody)
	if trimmedBody == "" {
		if workspaceExists || baselineExists {
			fullBody = fmt.Sprintf("%6s    (empty file)", "")
		} else {
			return "", false, nil
		}
	}

	unifiedBody := ""
	if trimmed != "" && !strings.Contains(diffText, "Binary files") {
		unifiedText, diffErr := limitedContextDiff(left, right)
		if diffErr != nil {
			return "", false, fmt.Errorf("diff %s: %w", rel, diffErr)
		}
		if strings.TrimSpace(unifiedText) != "" {
			unifiedBody = strings.TrimRight(unifiedText, "\n")
		}
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
	b.WriteString("Full diff (baseline vs workspace):\n")
	b.WriteString("```diff\n")
	b.WriteString(fullBody)
	b.WriteString("\n```")
	if unifiedBody != "" {
		b.WriteString("\n\nUnified diff (baseline vs workspace):\n")
		b.WriteString("```diff\n")
		b.WriteString(unifiedBody)
		b.WriteString("\n```")
	}
	return b.String(), true, nil
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

func formatDiffWithLineNumbers(diffText string) string {
	scanner := bufio.NewScanner(strings.NewReader(diffText))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var builder strings.Builder
	newline := 0
	inHunk := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "@@") {
			newline = parseNewLineStart(line) - 1
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
			continue
		}
		if len(line) == 0 {
			newline++
			fmt.Fprintf(&builder, "%6d    \n", newline)
			continue
		}
		switch line[0] {
		case ' ':
			newline++
			fmt.Fprintf(&builder, "%6d    %s\n", newline, line[1:])
		case '+':
			newline++
			fmt.Fprintf(&builder, "%6d  + %s\n", newline, line[1:])
		case '-':
			fmt.Fprintf(&builder, "%6s  - %s\n", "", line[1:])
		case '\\':
			fmt.Fprintf(&builder, "%6s    %s\n", "", line)
		default:
			newline++
			fmt.Fprintf(&builder, "%6d    %s\n", newline, line)
		}
	}
	return builder.String()
}

func renderNumberedFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file %s: %w", path, err)
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var builder strings.Builder
	line := 0
	for scanner.Scan() {
		line++
		fmt.Fprintf(&builder, "%6d    %s\n", line, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read file %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close file %s: %w", path, err)
	}
	return builder.String(), nil
}

func renderDeletedBaseline(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open baseline file %s: %w", path, err)
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var builder strings.Builder
	for scanner.Scan() {
		fmt.Fprintf(&builder, "%6s  - %s\n", "", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read baseline file %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close baseline file %s: %w", path, err)
	}
	return builder.String(), nil
}

func parseNewLineStart(header string) int {
	idx := strings.Index(header, "+")
	if idx == -1 {
		return 1
	}
	start := idx + 1
	end := start
	for end < len(header) && header[end] >= '0' && header[end] <= '9' {
		end++
	}
	if start == end {
		return 1
	}
	val, err := strconv.Atoi(header[start:end])
	if err != nil {
		return 1
	}
	return val
}
