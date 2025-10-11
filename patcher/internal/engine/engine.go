package engine

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	patcher "github.com/tursomari/machtiani/mct/patcher"
)

var (
	ErrEditFailed   = errors.New("edit failed")
	ErrEditConflict = errors.New("edit conflict")
)

// ApplyAll applies all edits deterministically in input order.
// It returns a map of relative file paths to their final content after applying
// all edits, and a list of unique files touched (relative paths).
func ApplyAll(repoRoot string, instr patcher.Instructions) (map[string][]byte, []string, error) {
	after := make(map[string][]byte)
	touchedSet := make(map[string]struct{})

	for idx, ed := range instr.Edits {
		rel, err := ed.NormalizedPath(repoRoot)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: edit[%d]: %v", ErrEditFailed, idx, err)
		}
		onDiskPath := filepath.Join(repoRoot, filepath.FromSlash(rel))
		var base []byte
		var exists bool
		if b, ok := after[rel]; ok {
			base = b
			exists = true
		} else {
			// Load from disk if exists.
			b, err := os.ReadFile(onDiskPath)
			if err == nil {
				base = b
				exists = true
			} else if os.IsNotExist(err) {
				exists = false
				base = nil
			} else if err != nil {
				return nil, nil, fmt.Errorf("%w: edit[%d] reading %s: %v", ErrEditFailed, idx, rel, err)
			}
		}

		switch ed.Mode {
		case patcher.ModeCreate:
			if exists {
				return nil, nil, fmt.Errorf("%w: edit[%d] create but file exists: %s", ErrEditConflict, idx, rel)
			}
			if !utf8.ValidString(ed.NewContent) {
				return nil, nil, fmt.Errorf("%w: edit[%d] create has non-utf8 content", ErrEditFailed, idx)
			}
			after[rel] = []byte(ed.NewContent)
		case patcher.ModeDelete:
			if !exists {
				return nil, nil, fmt.Errorf("%w: edit[%d] delete but file missing: %s", ErrEditConflict, idx, rel)
			}
			// Represent deletion by absence in after map with a sentinel nil pointer.
			after[rel] = nil
		case patcher.ModeRewrite:
			if !exists {
				return nil, nil, fmt.Errorf("%w: edit[%d] rewrite but file missing: %s", ErrEditConflict, idx, rel)
			}
			if !utf8.ValidString(ed.NewContent) {
				return nil, nil, fmt.Errorf("%w: edit[%d] rewrite has non-utf8 content", ErrEditFailed, idx)
			}
			after[rel] = []byte(ed.NewContent)
		case patcher.ModeReplace:
			if !exists {
				return nil, nil, fmt.Errorf("%w: edit[%d] replace but file missing: %s", ErrEditConflict, idx, rel)
			}
			if !utf8.ValidString(string(base)) {
				return nil, nil, fmt.Errorf("%w: edit[%d] base file not utf8: %s", ErrEditFailed, idx, rel)
			}
			out, ok := replaceNth(string(base), ed.Before, ed.After, ed.Occurrence)
			if !ok {
				return nil, nil, fmt.Errorf("%w: edit[%d] replace could not find nth occurrence", ErrEditFailed, idx)
			}
			after[rel] = []byte(out)
		default:
			return nil, nil, fmt.Errorf("%w: edit[%d] unknown mode", ErrEditFailed, idx)
		}
		touchedSet[rel] = struct{}{}
	}

	// Compile touched list in stable order of first appearance.
	var touched []string
	seen := make(map[string]struct{})
	for _, ed := range instr.Edits {
		rel, _ := ed.NormalizedPath(repoRoot)
		if _, ok := touchedSet[rel]; ok {
			if _, s := seen[rel]; !s {
				touched = append(touched, rel)
				seen[rel] = struct{}{}
			}
		}
	}

	return after, touched, nil
}

// replaceNth replaces the nth occurrence (1-based) of old with new in s.
func replaceNth(s, old, new string, n int) (string, bool) {
	if n < 1 {
		n = 1
	}
	if old == "" {
		return s, false
	}
	idx := -1
	from := 0
	for i := 0; i < n; i++ {
		j := strings.Index(s[from:], old)
		if j < 0 {
			return s, false
		}
		idx = from + j
		from = idx + len(old)
	}
	var b strings.Builder
	b.Grow(len(s) - len(old) + len(new))
	io.WriteString(&b, s[:idx])
	io.WriteString(&b, new)
	io.WriteString(&b, s[idx+len(old):])
	return b.String(), true
}
