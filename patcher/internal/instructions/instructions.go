package instructions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid instructions")

type Instructions struct {
	Edits    []Edit    `json:"edits"`
	Metadata *Metadata `json:"metadata,omitempty"`
}

type Metadata struct {
	Description string `json:"description,omitempty"`
	Author      string `json:"author,omitempty"`
	Email       string `json:"email,omitempty"`
}

type Mode string

const (
	ModeReplace Mode = "replace"
	ModeRewrite Mode = "rewrite"
	ModeCreate  Mode = "create"
	ModeDelete  Mode = "delete"
)

type Edit struct {
	Path       string `json:"path"`
	Mode       Mode   `json:"mode"`
	Before     string `json:"before,omitempty"`
	After      string `json:"after,omitempty"`
	Occurrence int    `json:"occurrence,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}

// NormalizedPath returns a cleaned, slash-separated path relative to repoRoot.
func (e Edit) NormalizedPath(repoRoot string) (string, error) {
	if e.Path == "" {
		return "", fmt.Errorf("empty path")
	}
	// Clean and convert to absolute then back to relative to ensure under root.
	p := filepath.Clean(e.Path)
	// Disallow absolute and traversal outside repo.
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
	// Normalize separators to forward slashes for git patch stability.
	return filepath.ToSlash(rel), nil
}

// Validate validates the instructions and ensures that each edit is consistent
// with the on-disk state without modifying anything.
func Validate(repoRoot string, in Instructions) error {
	if len(in.Edits) == 0 {
		return fmt.Errorf("%w: no edits provided", ErrInvalid)
	}
	for i, ed := range in.Edits {
		rel, err := ed.NormalizedPath(repoRoot)
		if err != nil {
			return fmt.Errorf("%w: edit[%d] path invalid: %v", ErrInvalid, i, err)
		}
		_ = rel
		switch ed.Mode {
		case ModeReplace:
			if ed.Before == "" {
				return fmt.Errorf("%w: edit[%d] replace requires 'before'", ErrInvalid, i)
			}
			if !utf8.ValidString(ed.Before) || !utf8.ValidString(ed.After) {
				return fmt.Errorf("%w: edit[%d] non-utf8 text in replace", ErrInvalid, i)
			}
			if ed.Occurrence < 1 {
				ed.Occurrence = 1
			}
			if ed.After == "" {
				return fmt.Errorf("%w: edit[%d] replace requires 'after'", ErrInvalid, i)
			}
			// File must exist.
			if !fileExists(filepath.Join(repoRoot, filepath.FromSlash(rel))) {
				return fmt.Errorf("%w: edit[%d] replace requires existing file", ErrInvalid, i)
			}
			// Ensure the before pattern appears at least occurrence times.
			b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
			if err != nil {
				return fmt.Errorf("%w: edit[%d] failed to read file for validation: %v", ErrInvalid, i, err)
			}
			if countOccurrences(string(b), ed.Before) < ed.Occurrence {
				return fmt.Errorf("%w: edit[%d] before pattern not found at occurrence %d", ErrInvalid, i, ed.Occurrence)
			}
		case ModeRewrite:
			if !utf8.ValidString(ed.NewContent) {
				return fmt.Errorf("%w: edit[%d] non-utf8 text in rewrite", ErrInvalid, i)
			}
			if ed.NewContent == "" {
				// Empty is allowed; it becomes an empty file.
			}
			if !fileExists(filepath.Join(repoRoot, filepath.FromSlash(rel))) {
				return fmt.Errorf("%w: edit[%d] rewrite requires existing file", ErrInvalid, i)
			}
		case ModeCreate:
			if !utf8.ValidString(ed.NewContent) {
				return fmt.Errorf("%w: edit[%d] non-utf8 text in create", ErrInvalid, i)
			}
			if fileExists(filepath.Join(repoRoot, filepath.FromSlash(rel))) {
				return fmt.Errorf("%w: edit[%d] create requires missing file", ErrInvalid, i)
			}
		case ModeDelete:
			if !fileExists(filepath.Join(repoRoot, filepath.FromSlash(rel))) {
				return fmt.Errorf("%w: edit[%d] delete requires existing file", ErrInvalid, i)
			}
		default:
			return fmt.Errorf("%w: edit[%d] unknown mode: %s", ErrInvalid, i, ed.Mode)
		}
	}
	return nil
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
