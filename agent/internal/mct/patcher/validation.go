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
		if err := validateHunkAgainstContent(lines, h, idx, hIdx); err != nil {
			return err
		}
	}
	return nil
}

func validatePatchHunkMetadata(h Hunk, editIdx, hunkIdx int) error {
	if h.OldStart < 1 {
		return fmt.Errorf("edit[%d] hunk[%d]: old_start must be >= 1", editIdx, hunkIdx)
	}
	if h.NewStart < 1 {
		return fmt.Errorf("edit[%d] hunk[%d]: new_start must be >= 1", editIdx, hunkIdx)
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

func validateHunkAgainstContent(lines []string, h Hunk, editIdx, hunkIdx int) error {
	start := h.OldStart - 1
	end := start + h.OldCount
	if start < 0 || end > len(lines) {
		return fmt.Errorf("edit[%d] hunk[%d]: hunk range [%d,%d) out of bounds for file with %d lines", editIdx, hunkIdx, start, end, len(lines))
	}
	segment := lines[start:end]
	beforeLen := len(h.ContextBefore)
	delLen := len(h.Deletions)
	afterLen := len(h.ContextAfter)
	if beforeLen > 0 {
		if !slicesEqual(h.ContextBefore, segment[:beforeLen]) {
			return fmt.Errorf("edit[%d] hunk[%d]: context_before does not match", editIdx, hunkIdx)
		}
	}
	if delLen > 0 {
		if !slicesEqual(h.Deletions, segment[beforeLen:beforeLen+delLen]) {
			return fmt.Errorf("edit[%d] hunk[%d]: deletions do not match", editIdx, hunkIdx)
		}
	}
	if afterLen > 0 {
		if !slicesEqual(h.ContextAfter, segment[beforeLen+delLen:]) {
			return fmt.Errorf("edit[%d] hunk[%d]: context_after does not match", editIdx, hunkIdx)
		}
	}
	return nil
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
