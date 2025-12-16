package diff

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Generate produces a unified patch by diffing only the provided files between
// repoRoot and mirrorDir using git. If files is empty, it falls back to
// directory diff (may be large). For deletions, it diffs repo file vs /dev/null.
func Generate(repoRoot, mirrorDir string, files []string) ([]byte, error) {
	var out bytes.Buffer
	var stderr bytes.Buffer

	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				if ee.ExitCode() != 1 { // 1 means differences found
					return fmt.Errorf("git diff failed: %s", strings.TrimSpace(stderr.String()))
				}
				return nil
			}
			return fmt.Errorf("git diff error: %v", err)
		}
		return nil
	}

	if len(files) == 0 {
		// Fallback: full dir diff (may include many deletions)
		if err := run("diff", "--no-index", "--binary", "--relative", repoRoot, mirrorDir); err != nil {
			return nil, err
		}
	} else {
		// Per-file diffs to avoid unrelated deletions.
		for _, rel := range files {
			rel = filepath.ToSlash(rel)
			left := filepath.Join(repoRoot, rel)
			right := filepath.Join(mirrorDir, rel)

			// Treat missing repo-side files as creations by diffing against /dev/null.
			if _, err := os.Stat(left); err != nil {
				if os.IsNotExist(err) {
					left = os.DevNull
				} else {
					return nil, fmt.Errorf("stat %s: %w", left, err)
				}
			}
			// If the mirror copy is missing, it represents a deletion.
			if _, err := os.Stat(right); err != nil {
				right = os.DevNull
			}
			// Use paths directly; --relative keeps paths concise
			if err := run("diff", "--no-index", "--binary", "--relative", left, right); err != nil {
				return nil, err
			}
		}
	}

	patch := out.Bytes()
	// Sanitize patch headers so that paths are repo-relative (e.g., README.md)
	// instead of absolute paths (e.g., /home/user/repo/README.md). This makes
	// the patch apply cleanly from the repo root without extra normalization.
	if len(files) > 0 {
		patch = sanitizePaths(patch, files)
	}
	if len(bytes.TrimSpace(patch)) == 0 {
		return nil, fmt.Errorf("no changes produced by edits")
	}
	return patch, nil
}

type Stats struct {
	FilesModified []string
	Insertions    int
	Deletions     int
}

// ExtractStats parses a git patch to count insertions and deletions and list files.
func ExtractStats(patch []byte) Stats {
	s := Stats{}
	seen := make(map[string]struct{})
	sc := bufio.NewScanner(bytes.NewReader(patch))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "+++ b/") {
			path := strings.TrimPrefix(line, "+++ b/")
			path = filepath.ToSlash(path)
			if _, ok := seen[path]; !ok {
				s.FilesModified = append(s.FilesModified, path)
				seen[path] = struct{}{}
			}
			continue
		}
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "index ") {
			continue
		}
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '+':
			s.Insertions++
		case '-':
			s.Deletions++
		}
	}
	return s
}

// sanitizePaths rewrites patch headers (diff --git/---/+++) to use repo-relative
// paths for the files we changed, by trimming any absolute or mirror prefixes
// and leaving only the trailing relative path under the repo.
func sanitizePaths(patch []byte, files []string) []byte {
	rels := make([]string, 0, len(files))
	for _, f := range files {
		rels = append(rels, filepath.ToSlash(f))
	}
	// Helper to trim a header path to the known relative file, if it matches.
	trimToRel := func(p string) string {
		ps := filepath.ToSlash(p)
		for _, r := range rels {
			if strings.HasSuffix(ps, r) {
				return r
			}
		}
		return p
	}
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(patch))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git a/"):
			rest := strings.TrimPrefix(line, "diff --git a/")
			parts := strings.SplitN(rest, " b/", 2)
			if len(parts) == 2 {
				left := trimToRel(parts[0])
				right := trimToRel(parts[1])
				line = "diff --git a/" + left + " b/" + right
			}
		case strings.HasPrefix(line, "--- a/"):
			path := strings.TrimPrefix(line, "--- a/")
			if path == "/dev/null" || strings.HasSuffix(path, "/dev/null") {
				line = "--- /dev/null"
			} else {
				path = trimToRel(path)
				line = "--- a/" + path
			}
		case strings.HasPrefix(line, "+++ b/"):
			path := strings.TrimPrefix(line, "+++ b/")
			if path == "/dev/null" || strings.HasSuffix(path, "/dev/null") {
				line = "+++ /dev/null"
			} else {
				path = trimToRel(path)
				line = "+++ b/" + path
			}
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	// If scanning fails, fall back to original patch.
	if out.Len() == 0 {
		return patch
	}
	return out.Bytes()
}
