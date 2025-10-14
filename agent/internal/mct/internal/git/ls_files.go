package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// IsGitRepo returns true if dir is inside a Git work tree.
func IsGitRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return false
	}
	return strings.TrimSpace(out.String()) == "true"
}

// RepoRoot returns the absolute path to the repo top-level for dir.
func RepoRoot(dir string) (string, error) {
	if !IsGitRepo(dir) {
		return "", errors.New("not a git repository")
	}
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel failed: %w, %s", err, stderr.String())
	}
	root := strings.TrimSpace(out.String())
	if root == "" {
		return "", errors.New("empty toplevel from git")
	}
	// Normalize
	return filepath.Clean(root), nil
}

// ListTrackedFiles returns the list of git-tracked files (index + HEAD) at dir.
// Results are relative paths using '/' separators as emitted by git.
func ListTrackedFiles(dir string) ([]string, error) {
	if !IsGitRepo(dir) {
		return nil, errors.New("not a git repository")
	}
	// Prefer recursing into submodules to include tracked files there.
	// Fallback to non-recursive if the git version doesn't support it or it fails.
	files, err := lsFiles(dir, true)
	if err != nil {
		// Retry without recurse; preserve current behavior on older git or errors.
		files, err2 := lsFiles(dir, false)
		if err2 != nil {
			// Return the original error to hint at recurse-submodules issue, but include fallback error too.
			return nil, fmt.Errorf("git ls-files failed (recurse then fallback): %v; fallback: %v", err, err2)
		}
		return files, nil
	}
	return files, nil
}

// lsFiles runs `git ls-files` with optional `--recurse-submodules` and parses NUL-separated output.
func lsFiles(dir string, recurse bool) ([]string, error) {
	args := []string{"-C", dir, "ls-files"}
	if recurse {
		args = append(args, "--recurse-submodules")
	}
	args = append(args, "-z")
	cmd := exec.Command("git", args...)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-files%v failed: %w, %s", func() string {
			if recurse {
				return " --recurse-submodules"
			}
			return ""
		}(), err, strings.TrimSpace(stderr.String()))
	}
	raw := out.Bytes()
	parts := bytes.Split(raw, []byte{0})
	files := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		// Git emits '/' separators regardless of OS; keep as-is for relative paths.
		files = append(files, string(p))
	}
	return files, nil
}
