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
		default:
			return fmt.Errorf("%w: edit[%d] unknown mode: %s", ErrInvalidInstructions, i, ed.Mode)
		}
	}
	return nil
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
