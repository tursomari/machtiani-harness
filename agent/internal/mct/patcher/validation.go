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
    // Enforce single-change rule for splice edits (ModePatch): at most one splice per file,
    // and do not combine a splice with any other edit to the same file in one instruction set.
    perFile := make(map[string]struct{ patchCount int; otherCount int })
    // Collect normalized paths first to avoid duplicating normalization work later.
    normalized := make([]string, len(in.Edits))
    for i, ed := range in.Edits {
        rel, err := ed.NormalizedPath(repoRoot)
        if err != nil {
            return fmt.Errorf("%w: edit[%d] path invalid: %v", ErrInvalidInstructions, i, err)
        }
        normalized[i] = rel
        entry := perFile[rel]
        if ed.Mode == ModePatch {
            entry.patchCount++
        } else {
            entry.otherCount++
        }
        perFile[rel] = entry
    }
    for path, c := range perFile {
        if c.patchCount > 1 || (c.patchCount == 1 && c.otherCount > 0) {
            return fmt.Errorf("%w: only one splice per file per patch; chain changes across turns (file: %s)", ErrInvalidInstructions, path)
        }
    }
    for i, ed := range in.Edits {
        rel := normalized[i]
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
        if ed.PatchInfo != nil {
            return fmt.Errorf("%w: edit[%d] strict hunk payloads no longer supported; provide start_line/end_line", ErrInvalidInstructions, i)
        }
        onDiskPath := filepath.Join(repoRoot, filepath.FromSlash(rel))
        if !fileExists(onDiskPath) {
            return fmt.Errorf("%w: edit[%d] patch requires existing file", ErrInvalidInstructions, i)
        }
        start := ed.StartLine
        end := ed.EndLine
        if start == 0 {
            start = 1 // tolerate 0 by treating as 1
        }
        if start < 1 || end < 0 || (end > 0 && start > end) {
            return fmt.Errorf("%w: edit[%d] invalid line range: start=%d end=%d", ErrInvalidInstructions, i, start, end)
        }
        data, err := os.ReadFile(onDiskPath)
        if err != nil {
            return fmt.Errorf("%w: edit[%d] failed to read file: %v", ErrInvalidInstructions, i, err)
        }
        lines := splitStrictLines(string(data))
        if end == 0 {
            if start < 1 || start > len(lines)+1 {
                return fmt.Errorf("%w: edit[%d] insert out of bounds: %d in %d lines", ErrInvalidInstructions, i, start, len(lines))
            }
        } else {
            if start < 1 || start > len(lines) || end > len(lines) {
                return fmt.Errorf("%w: edit[%d] range out of bounds: [%d,%d] in %d lines", ErrInvalidInstructions, i, start, end, len(lines))
            }
        }
        default:
            return fmt.Errorf("%w: edit[%d] unknown mode: %s", ErrInvalidInstructions, i, ed.Mode)
        }
    }
    return nil
}

// Deprecated hunk-related validation helpers removed.

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
