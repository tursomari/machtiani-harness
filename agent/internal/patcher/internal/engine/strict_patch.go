package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	patcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
)

func applyStrictPatch(repoRoot, rel string, content string, info *patcher.UnifiedPatchInfo) (string, []patcher.HunkConflictDiagnostic, error) {
	lines, hadTrailing := splitLinesStrict(content)
	var diagnostics []patcher.HunkConflictDiagnostic
	lineOffset := 0

	for idx, h := range info.Hunks {
		newLines, delta, appliedStart, reason, err := applyStrictHunk(repoRoot, rel, lines, h, lineOffset)
		if err != nil {
			diag := computeHunkConflictDiagnostics(lines, h, idx, appliedStart, lineOffset, reason)
			diagnostics = append(diagnostics, diag)
			return "", diagnostics, fmt.Errorf("hunk[%d]: %w", idx, err)
		}
		lines = newLines
		lineOffset += delta
	}

	return joinLinesStrict(lines, hadTrailing), diagnostics, nil
}

func applyStrictHunk(repoRoot, rel string, lines []string, h patcher.Hunk, lineOffset int) ([]string, int, int, string, error) {
	beforeLen := len(h.ContextBefore)
	deleteLen := len(h.Deletions)
	afterLen := len(h.ContextAfter)
	total := beforeLen + deleteLen + afterLen
	approximateStart := -1
	if h.SnippetSource != nil {
		approximateStart = h.SnippetSource.StartLine - 1 + lineOffset
	}
	if approximateStart < 0 && h.OldStart > 0 {
		approximateStart = h.OldStart - 1 + lineOffset
	}
	if h.SnippetSource != nil {
		if trimmed := strings.TrimSpace(h.SnippetSource.Filepath); trimmed != "" {
			normalized, err := (patcher.Edit{Path: trimmed}).NormalizedPath(repoRoot)
			if err != nil {
				return nil, 0, approximateStart, "snippet_path_invalid", fmt.Errorf("invalid snippet_source filepath: %v", err)
			}
			if normalized != rel {
				return nil, 0, approximateStart, "snippet_path_mismatch", fmt.Errorf("snippet_source filepath %s does not match edit path %s", normalized, rel)
			}
		}
		snippetLen := patcher.SnippetRangeLength(h.SnippetSource)
		if snippetLen == -1 {
			return nil, 0, approximateStart, "snippet_range_invalid", fmt.Errorf("invalid snippet_source range")
		}
	}
	start, exactMatch, attempt, reason, err := patcher.FindHunkMatch(lines, h, approximateStart)
	if err != nil {
		return nil, 0, attempt, reason, err
	}

	end := start + total
	if total == 0 {
		if start < 0 || start > len(lines) {
			return nil, 0, start, "out_of_range", fmt.Errorf("insertion point %d out of bounds for file with %d lines", start+1, len(lines))
		}
	} else {
		if start < 0 || end > len(lines) {
			return nil, 0, start, "out_of_range", fmt.Errorf("hunk range [%d,%d) out of bounds for file with %d lines", start, end, len(lines))
		}
	}

	segment := lines[start:end]

	if total > 0 {
		if exactMatch {
			if beforeLen > 0 && !slicesEqual(h.ContextBefore, segment[:beforeLen]) {
				return nil, 0, start, "context_before_mismatch", fmt.Errorf("context before mismatch at line %d", start+1)
			}
			if deleteLen > 0 && !slicesEqual(h.Deletions, segment[beforeLen:beforeLen+deleteLen]) {
				return nil, 0, start, "deletion_mismatch", fmt.Errorf("deletions mismatch at line %d", start+beforeLen+1)
			}
			if afterLen > 0 && !slicesEqual(h.ContextAfter, segment[beforeLen+deleteLen:]) {
				return nil, 0, start, "context_after_mismatch", fmt.Errorf("context after mismatch at line %d", start+beforeLen+deleteLen+1)
			}
		} else {
			if beforeLen > 0 && !patcher.LinesWhitespaceEquivalent(h.ContextBefore, segment[:beforeLen]) {
				return nil, 0, start, "context_before_mismatch", fmt.Errorf("context before mismatch at line %d", start+1)
			}
			if deleteLen > 0 && !patcher.LinesWhitespaceEquivalent(h.Deletions, segment[beforeLen:beforeLen+deleteLen]) {
				return nil, 0, start, "deletion_mismatch", fmt.Errorf("deletions mismatch at line %d", start+beforeLen+1)
			}
			if afterLen > 0 && !patcher.LinesWhitespaceEquivalent(h.ContextAfter, segment[beforeLen+deleteLen:]) {
				return nil, 0, start, "context_after_mismatch", fmt.Errorf("context after mismatch at line %d", start+beforeLen+deleteLen+1)
			}
		}
	}

	result := make([]string, 0, len(lines)-deleteLen+len(h.Additions))
	result = append(result, lines[:start]...)
	result = append(result, h.ContextBefore...)
	result = append(result, h.Additions...)
	result = append(result, h.ContextAfter...)
	result = append(result, lines[end:]...)

	delta := len(h.Additions) - len(h.Deletions)
	return result, delta, start, "", nil
}

func computeHunkConflictDiagnostics(lines []string, h patcher.Hunk, index, attemptStart, lineOffset int, reason string) patcher.HunkConflictDiagnostic {
	start := attemptStart
	if start < 0 {
		if h.SnippetSource != nil {
			start = h.SnippetSource.StartLine - 1 + lineOffset
		} else if h.OldStart > 0 {
			start = h.OldStart - 1 + lineOffset
		}
	}
	if start < 0 {
		start = 0
	}
	total := len(h.ContextBefore) + len(h.Deletions) + len(h.ContextAfter)
	end := start + total
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if end > len(lines) {
		end = len(lines)
	}
	actual := append([]string(nil), lines[start:end]...)

	expectedHash := computeHash(h.ContextBefore, h.Deletions, h.ContextAfter)
	actualHash := computeHash(actual)

	expectedStartLine := start + 1
	preview := buildDiffPreview(h, actual, expectedStartLine)

	return patcher.HunkConflictDiagnostic{
		HunkIndex:     index,
		Reason:        reason,
		ExpectedHash:  expectedHash,
		ActualHash:    actualHash,
		ExpectedLines: total,
		ActualLines:   len(actual),
		DiffPreview:   preview,
	}
}

func buildDiffPreview(h patcher.Hunk, actual []string, expectedStartLine int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Expected %d line(s) starting at %d\n", len(h.ContextBefore)+len(h.Deletions)+len(h.ContextAfter), expectedStartLine))
	if len(h.ContextBefore) > 0 {
		b.WriteString("Context before:\n")
		for _, line := range h.ContextBefore {
			b.WriteString("  = " + line + "\n")
		}
	}
	if len(h.Deletions) > 0 {
		b.WriteString("Deletions:\n")
		for _, line := range h.Deletions {
			b.WriteString("  - " + line + "\n")
		}
	}
	if len(h.Additions) > 0 {
		b.WriteString("Additions:\n")
		for _, line := range h.Additions {
			b.WriteString("  + " + line + "\n")
		}
	}
	if len(h.ContextAfter) > 0 {
		b.WriteString("Context after:\n")
		for _, line := range h.ContextAfter {
			b.WriteString("  = " + line + "\n")
		}
	}
	if len(actual) > 0 {
		b.WriteString("Actual content:\n")
		for _, line := range actual {
			b.WriteString("  ? " + line + "\n")
		}
	} else {
		b.WriteString("Actual content: <empty>\n")
	}
	preview := b.String()
	if len(preview) > 500 {
		return preview[:497] + "..."
	}
	return preview
}

func computeHash(sections ...[]string) string {
	hasher := sha256.New()
	for _, sec := range sections {
		for _, line := range sec {
			hasher.Write([]byte(line))
			hasher.Write([]byte("\n"))
		}
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func splitLinesStrict(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	hadTrailing := strings.HasSuffix(content, "\n")
	trimmed := strings.TrimSuffix(content, "\n")
	if trimmed == "" {
		return []string{""}, hadTrailing
	}
	return strings.Split(trimmed, "\n"), hadTrailing
}

func joinLinesStrict(lines []string, hadTrailing bool) string {
	if len(lines) == 0 {
		return ""
	}
	joined := strings.Join(lines, "\n")
	if hadTrailing {
		return joined + "\n"
	}
	return joined
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
