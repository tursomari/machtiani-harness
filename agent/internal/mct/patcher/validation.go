package patcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var ErrInvalidInstructions = errors.New("invalid instructions")

// Validate ensures the provided set of instructions is consistent with the
// on-disk repository state without performing any modifications.
func Validate(repoRoot string, in Instructions) error {
	if len(in.Edits) == 0 {
		return fmt.Errorf("%w: no edits provided", ErrInvalidInstructions)
	}
	for i, ed := range in.Edits {
		rel, err := ed.NormalizedPath(repoRoot)
		if err != nil {
			return fmt.Errorf("%w: edit[%d] path invalid: %v", ErrInvalidInstructions, i, err)
		}
		_ = rel
		switch ed.Mode {
		case ModeReplace:
			if ed.Before == "" {
				return fmt.Errorf("%w: edit[%d] replace requires 'before'", ErrInvalidInstructions, i)
			}
			if !utf8.ValidString(ed.Before) || !utf8.ValidString(ed.After) {
				return fmt.Errorf("%w: edit[%d] non-utf8 text in replace", ErrInvalidInstructions, i)
			}
			if ed.Occurrence < 1 {
				ed.Occurrence = 1
			}
			if ed.After == "" {
				return fmt.Errorf("%w: edit[%d] replace requires 'after'", ErrInvalidInstructions, i)
			}
			if !fileExists(filepath.Join(repoRoot, filepath.FromSlash(rel))) {
				return fmt.Errorf("%w: edit[%d] replace requires existing file", ErrInvalidInstructions, i)
			}
			b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
			if err != nil {
				return fmt.Errorf("%w: edit[%d] failed to read file for validation: %v", ErrInvalidInstructions, i, err)
			}
			if countOccurrences(string(b), ed.Before) < ed.Occurrence {
				return fmt.Errorf("%w: edit[%d] before pattern not found at occurrence %d", ErrInvalidInstructions, i, ed.Occurrence)
			}
		case ModeRewrite:
			if !utf8.ValidString(ed.NewContent) {
				return fmt.Errorf("%w: edit[%d] non-utf8 text in rewrite", ErrInvalidInstructions, i)
			}
			if !fileExists(filepath.Join(repoRoot, filepath.FromSlash(rel))) {
				return fmt.Errorf("%w: edit[%d] rewrite requires existing file", ErrInvalidInstructions, i)
			}
		case ModeCreate:
			if !utf8.ValidString(ed.NewContent) {
				return fmt.Errorf("%w: edit[%d] non-utf8 text in create", ErrInvalidInstructions, i)
			}
			if fileExists(filepath.Join(repoRoot, filepath.FromSlash(rel))) {
				return fmt.Errorf("%w: edit[%d] create requires missing file", ErrInvalidInstructions, i)
			}
		case ModeDelete:
			if !fileExists(filepath.Join(repoRoot, filepath.FromSlash(rel))) {
				return fmt.Errorf("%w: edit[%d] delete requires existing file", ErrInvalidInstructions, i)
			}
		case ModePatch:
			if err := validateStrictPatch(repoRoot, rel, ed, i); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidInstructions, err)
			}
		default:
			return fmt.Errorf("%w: edit[%d] unknown mode: %s", ErrInvalidInstructions, i, ed.Mode)
		}
	}
	return nil
}

func validateStrictPatch(repoRoot, rel string, ed Edit, idx int) error {
	if ed.PatchInfo == nil {
		return fmt.Errorf("edit[%d] patch mode requires 'patch' payload", idx)
	}
	if len(ed.PatchInfo.Hunks) == 0 {
		return fmt.Errorf("edit[%d] patch requires at least one hunk", idx)
	}
	for hIdx, h := range ed.PatchInfo.Hunks {
		if err := validatePatchHunkMetadata(h, idx, hIdx); err != nil {
			return err
		}
	}
	onDiskPath := filepath.Join(repoRoot, filepath.FromSlash(rel))
	if !fileExists(onDiskPath) {
		return fmt.Errorf("edit[%d] patch requires existing file: %s", idx, rel)
	}
	content, err := os.ReadFile(onDiskPath)
	if err != nil {
		return fmt.Errorf("edit[%d] failed to read file: %v", idx, err)
	}
	text := string(content)
	if !utf8.ValidString(text) {
		return fmt.Errorf("edit[%d] base file is not valid utf-8", idx)
	}
	lines := splitStrictLines(text)
	for hIdx, h := range ed.PatchInfo.Hunks {
		approximateStart := -1
		if h.SnippetSource != nil {
			snippetPath := rel
			if trimmed := strings.TrimSpace(h.SnippetSource.Filepath); trimmed != "" {
				normalized, err := (Edit{Path: trimmed}).NormalizedPath(repoRoot)
				if err != nil {
					return fmt.Errorf("edit[%d] hunk[%d]: invalid snippet_source filepath: %v", idx, hIdx, err)
				}
				snippetPath = normalized
			}
			if snippetPath != rel {
				return fmt.Errorf("edit[%d] hunk[%d]: snippet_source filepath %s does not match edit path %s", idx, hIdx, snippetPath, rel)
			}
			if h.SnippetSource.StartLine > 0 {
				approximateStart = h.SnippetSource.StartLine - 1
			}
			snippetLen := SnippetRangeLength(h.SnippetSource)
			if snippetLen == -1 {
				return fmt.Errorf("edit[%d] hunk[%d]: snippet_source range is invalid", idx, hIdx)
			}
		}
		if approximateStart < 0 && h.OldStart > 0 {
			approximateStart = h.OldStart - 1
		}
		if _, _, _, reason, err := FindHunkMatch(lines, h, approximateStart); err != nil {
			return fmt.Errorf("edit[%d] hunk[%d]: %s: %v", idx, hIdx, reason, err)
		}
	}
	return nil
}

func validatePatchHunkMetadata(h Hunk, editIdx, hunkIdx int) error {
	if h.OldStart < 0 {
		return fmt.Errorf("edit[%d] hunk[%d]: old_start must be >= 0", editIdx, hunkIdx)
	}
	if h.NewStart < 0 {
		return fmt.Errorf("edit[%d] hunk[%d]: new_start must be >= 0", editIdx, hunkIdx)
	}
	if h.OldCount < 0 {
		return fmt.Errorf("edit[%d] hunk[%d]: old_count must be >= 0", editIdx, hunkIdx)
	}
	if h.NewCount < 0 {
		return fmt.Errorf("edit[%d] hunk[%d]: new_count must be >= 0", editIdx, hunkIdx)
	}
	oldTotal := len(h.ContextBefore) + len(h.Deletions) + len(h.ContextAfter)
	if h.OldCount != oldTotal {
		return fmt.Errorf("edit[%d] hunk[%d]: old_count (%d) mismatch with context/deletions size (%d)", editIdx, hunkIdx, h.OldCount, oldTotal)
	}
	newTotal := len(h.ContextBefore) + len(h.Additions) + len(h.ContextAfter)
	if h.NewCount != newTotal {
		return fmt.Errorf("edit[%d] hunk[%d]: new_count (%d) mismatch with context/additions size (%d)", editIdx, hunkIdx, h.NewCount, newTotal)
	}
	for _, seq := range [][]string{h.ContextBefore, h.Deletions, h.Additions, h.ContextAfter} {
		for _, line := range seq {
			if !utf8.ValidString(line) {
				return fmt.Errorf("edit[%d] hunk[%d]: non-utf8 text detected", editIdx, hunkIdx)
			}
		}
	}
	return nil
}

func assembleBeforeLines(h Hunk) []string {
	total := len(h.ContextBefore) + len(h.Deletions) + len(h.ContextAfter)
	if total == 0 {
		return nil
	}
	out := make([]string, 0, total)
	out = append(out, h.ContextBefore...)
	out = append(out, h.Deletions...)
	out = append(out, h.ContextAfter...)
	return out
}

// LoadSnippetLines returns the inclusive range [startLine, endLine] (1-based)
// from the file located at relPath under repoRoot. It validates the range
// and returns a defensive copy of the lines without trailing newlines.
func LoadSnippetLines(repoRoot, relPath string, startLine, endLine int) ([]string, error) {
	if strings.TrimSpace(relPath) == "" {
		return nil, fmt.Errorf("snippet_source filepath must not be empty")
	}
	if startLine < 1 {
		return nil, fmt.Errorf("snippet_source start_line must be >= 1")
	}
	absPath := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read snippet source %s: %w", relPath, err)
	}
	lines := splitStrictLines(string(data))
	if endLine < startLine {
		if endLine != startLine-1 {
			return nil, fmt.Errorf("snippet_source end_line %d precedes start_line %d", endLine, startLine)
		}
		if startLine-1 > len(lines) {
			return nil, fmt.Errorf("snippet_source start_line %d exceeds file line count %d", startLine, len(lines))
		}
		return []string{}, nil
	}
	if startLine > len(lines) {
		return nil, fmt.Errorf("snippet_source start_line %d exceeds file line count %d", startLine, len(lines))
	}
	if endLine > len(lines) {
		return nil, fmt.Errorf("snippet_source end_line %d exceeds file line count %d", endLine, len(lines))
	}
	idxStart := startLine - 1
	idxEnd := endLine
	snippet := append([]string(nil), lines[idxStart:idxEnd]...)
	return snippet, nil
}

func splitStrictLines(content string) []string {
	if content == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(content, "\n")
	if trimmed == "" {
		return []string{""}
	}
	return strings.Split(trimmed, "\n")
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

// NormalizedPath returns a cleaned, slash-separated path relative to repoRoot.
func (e Edit) NormalizedPath(repoRoot string) (string, error) {
	if e.Path == "" {
		return "", fmt.Errorf("empty path")
	}
	p := filepath.Clean(e.Path)
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("path must be relative: %s", e.Path)
	}
	abs := filepath.Join(repoRoot, p)
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil {
		return "", fmt.Errorf("cannot relativize path: %w", err)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes repo root: %s", e.Path)
	}
	return filepath.ToSlash(rel), nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !st.IsDir()
}

func countOccurrences(s, sub string) int {
	if sub == "" {
		return 0
	}
	n := 0
	i := 0
	for {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			break
		}
		n++
		i += j + len(sub)
	}
	return n
}
