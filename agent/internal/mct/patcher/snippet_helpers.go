package patcher

import "strings"

// normalizeLineForComparison trims leading and trailing whitespace to allow
// lenient comparisons when models drift on indentation.
func normalizeLineForComparison(s string) string {
	return strings.TrimSpace(s)
}

// normalizeLineForTolerance collapses internal whitespace so that minor spacing
// differences (e.g. double spaces vs single spaces) are treated as equivalent.
func normalizeLineForTolerance(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

// LinesWhitespaceEquivalent returns true when two line slices are the same
// length and equal after trimming leading and trailing whitespace from each
// line. Exact matches also return true.
func LinesWhitespaceEquivalent(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] == b[i] {
			continue
		}
		if normalizeLineForComparison(a[i]) != normalizeLineForComparison(b[i]) {
			return false
		}
	}
	return true
}

// SnippetRangeLength returns the number of lines covered by the provided
// snippet source. Zero-length ranges (where end_line == start_line-1) return
// zero. Invalid ranges return -1.
func SnippetRangeLength(src *SnippetSource) int {
	if src == nil {
		return 0
	}
	if src.EndLine < src.StartLine {
		if src.EndLine == src.StartLine-1 {
			return 0
		}
		return -1
	}
	return src.EndLine - src.StartLine + 1
}
