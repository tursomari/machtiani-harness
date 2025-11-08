package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	patcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
)

func applyStrictPatch(content string, info *patcher.UnifiedPatchInfo) (string, []patcher.HunkConflictDiagnostic, error) {
	lines, hadTrailing := splitLinesStrict(content)
	var diagnostics []patcher.HunkConflictDiagnostic
	lineOffset := 0

	for idx, h := range info.Hunks {
		newLines, delta, reason, err := applyStrictHunk(lines, h, lineOffset)
		if err != nil {
			diag := computeHunkConflictDiagnostics(lines, h, idx, lineOffset, reason)
			diagnostics = append(diagnostics, diag)
			return "", diagnostics, fmt.Errorf("hunk[%d]: %w", idx, err)
		}
		lines = newLines
		lineOffset += delta
	}

	return joinLinesStrict(lines, hadTrailing), diagnostics, nil
}

func applyStrictHunk(lines []string, h patcher.Hunk, lineOffset int) ([]string, int, string, error) {
	expectedBefore := len(h.ContextBefore)
	expectedDelete := len(h.Deletions)
	expectedAfter := len(h.ContextAfter)
	total := expectedBefore + expectedDelete + expectedAfter

	start := h.OldStart - 1 + lineOffset
	end := start + total
	if start < 0 || end > len(lines) {
		return nil, 0, "out_of_range", fmt.Errorf("hunk range [%d,%d) out of bounds for file with %d lines", start, end, len(lines))
	}

	segment := lines[start:end]

	if expectedBefore > 0 {
		if !slicesEqual(h.ContextBefore, segment[:expectedBefore]) {
			return nil, 0, "context_before_mismatch", fmt.Errorf("context before mismatch at line %d", h.OldStart)
		}
	}
	if expectedDelete > 0 {
		if !slicesEqual(h.Deletions, segment[expectedBefore:expectedBefore+expectedDelete]) {
			return nil, 0, "deletion_mismatch", fmt.Errorf("deletions mismatch at line %d", h.OldStart+expectedBefore)
		}
	}
	if expectedAfter > 0 {
		if !slicesEqual(h.ContextAfter, segment[expectedBefore+expectedDelete:]) {
			return nil, 0, "context_after_mismatch", fmt.Errorf("context after mismatch at line %d", h.OldStart+expectedBefore+expectedDelete)
		}
	}

	result := make([]string, 0, len(lines)-expectedDelete+len(h.Additions))
	result = append(result, lines[:start]...)
	result = append(result, h.ContextBefore...)
	result = append(result, h.Additions...)
	result = append(result, h.ContextAfter...)
	result = append(result, lines[end:]...)

	delta := len(h.Additions) - len(h.Deletions)
	return result, delta, "", nil
}

func computeHunkConflictDiagnostics(lines []string, h patcher.Hunk, index, lineOffset int, reason string) patcher.HunkConflictDiagnostic {
	start := h.OldStart - 1 + lineOffset
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

	preview := buildDiffPreview(h, actual)

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

func buildDiffPreview(h patcher.Hunk, actual []string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Expected %d line(s) starting at %d\n", len(h.ContextBefore)+len(h.Deletions)+len(h.ContextAfter), h.OldStart))
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
