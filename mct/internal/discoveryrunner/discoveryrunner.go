package discoveryrunner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	gitpkg "github.com/tursomari/machtiani/mct/internal/git"
)

// Result holds parsed file paths from file-discovery output
type Result struct {
	Paths []string
}

// Matches blocks like:
// BEGIN_RELEVANT_FILES[session]
// <paths...>
// END_RELEVANT_FILES[session]
// Go's regexp doesn't support backreferences, so capture both session ids and compare in code.
var blockRe = regexp.MustCompile(`(?s)BEGIN_RELEVANT_FILES\[(.+?)\]\n(.*?)\nEND_RELEVANT_FILES\[(.+?)\]`)

// findBinary resolves the file-discovery binary path using precedence:
// 1) FILE_DISCOVERY_BIN env
// 2) file-discovery in PATH
// 3) <exec-dir>/bin/file-discovery
func findBinary() (string, error) {
	if override := os.Getenv("FILE_DISCOVERY_BIN"); override != "" {
		return override, nil
	}
	if p, err := exec.LookPath("file-discovery"); err == nil {
		return p, nil
	}
	exe, err := os.Executable()
	if err == nil {
		base := filepath.Dir(exe)
		candidate := filepath.Join(base, "bin", "file-discovery")
		if st, err2 := os.Stat(candidate); err2 == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("file-discovery binary not found; set FILE_DISCOVERY_BIN or add to PATH")
}

// isSafePath performs defense-in-depth checks on a relative file path
func isSafePath(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return false
	}
	if strings.Contains(p, "\\") { // no backslashes
		return false
	}
	if strings.Contains(p, "..") { // disallow parent traversal
		return false
	}
	if strings.HasSuffix(p, "/") { // no trailing slash directories
		return false
	}
	clean := filepath.Clean(p)
	if strings.HasPrefix(clean, "..") { // extra guard after Clean
		return false
	}
	return true
}

// uniqOrder deduplicates while preserving order
func uniqOrder(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// Run executes file-discovery with the given prompt and environment mapping.
// It returns the list of discovered relative paths.
func Run(ctx context.Context, prompt, model, apiKey, baseURL, sessionID string, verbose bool) (Result, error) {
    bin, err := findBinary()
    if err != nil {
        return Result{}, err
    }
    debugf(verbose, "mct: using file-discovery binary: %s", bin)

	args := []string{}
	if sessionID != "" {
		args = append(args, "-session-id", sessionID)
	}

	cmd := exec.CommandContext(ctx, bin, args...)

	// Optionally run file-discovery in a temp workspace filtered to git-tracked files only.
	// Controlled by env MCT_USE_GIT_FILTER (default: true). Set to "false"/"0"/"no" to disable.
    if useGitFilter() {
        if tmpDir, cleanup, err := prepareGitFilteredWorkspace(verbose); err == nil && tmpDir != "" {
            defer cleanup()
            cmd.Dir = tmpDir
            debugf(verbose, "mct: discovery sandbox enabled at %s", tmpDir)
        } else {
            if err != nil {
                debugf(verbose, "mct: discovery sandbox disabled: %v", err)
            } else {
                debugf(verbose, "mct: discovery sandbox skipped: not a git repo or empty")
            }
        }
    } else {
        debugf(verbose, "mct: discovery sandbox disabled via MCT_USE_GIT_FILTER")
    }
	// Build env with OPENAI_* expected by file-discovery
	env := os.Environ()
	if apiKey != "" {
		env = append(env, "OPENAI_API_KEY="+apiKey)
	}
	if baseURL != "" {
		env = append(env, "OPENAI_BASE_URL="+baseURL)
	}
	if model != "" {
		env = append(env, "OPENAI_MODEL="+model)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr // explicitly ignore logs; useful for debug on failure

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("failed to open stdin: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("failed to start file-discovery: %w", err)
	}

	// Stream prompt into stdin
	go func() {
		w := bufio.NewWriter(stdin)
		_, _ = w.WriteString(prompt)
		_ = w.Flush()
		_ = stdin.Close()
	}()

	if err := cmd.Wait(); err != nil {
		// include a hint with stderr for troubleshooting
		return Result{}, fmt.Errorf("file-discovery failed: %w\n%s", err, stderr.String())
	}

	outStr := stdout.String()
	m := blockRe.FindStringSubmatch(outStr)
	if len(m) != 4 {
		return Result{}, errors.New("failed to parse relevant files block from file-discovery output")
	}
	if m[1] != m[3] {
		return Result{}, errors.New("mismatched session ids in relevant files block")
	}

	block := m[2]
	lines := strings.Split(block, "\n")
	var paths []string
	for _, line := range lines {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		if !isSafePath(p) {
			continue
		}
		paths = append(paths, p)
	}

	paths = uniqOrder(paths)
	return Result{Paths: paths}, nil
}

// useGitFilter reads MCT_USE_GIT_FILTER and returns true unless explicitly disabled.
func useGitFilter() bool {
    v := strings.ToLower(strings.TrimSpace(os.Getenv("MCT_USE_GIT_FILTER")))
    if v == "false" || v == "0" || v == "no" { return false }
    return true
}

// debugf writes debug lines to stderr when MCT_DEBUG is set.
func debugf(verbose bool, format string, args ...any) {
    if !verbose { return }
    fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// prepareGitFilteredWorkspace creates a temporary directory containing only git-tracked files
// from the repository at the current working directory. It returns "" if not a git repo
// or no files are tracked. Caller should set cmd.Dir to the returned dir and call cleanup afterwards.
func prepareGitFilteredWorkspace(verbose bool) (tmpDir string, cleanup func(), err error) {
    // Lazy import to avoid cycles
    type gitAPI interface{
        IsGitRepo(dir string) bool
        RepoRoot(dir string) (string, error)
        ListTrackedFiles(dir string) ([]string, error)
    }
    // Use the concrete functions from internal/git
    // Import at top-level
    return createFilteredDir(verbose)
}

// createFilteredDir performs the actual work; split for clarity.
func createFilteredDir(verbose bool) (string, func(), error) {
    // Import git package
    // Note: import path fixed at top file imports
    cwd, err := os.Getwd()
    if err != nil { return "", nil, err }

    // Check if inside a git repo
    if !gitIsRepo(cwd) {
        return "", nil, nil
    }
    repoRoot, err := gitRepoRoot(cwd)
    if err != nil { return "", nil, nil }
    files, err := gitListTracked(repoRoot)
    if err != nil { return "", nil, nil }
    if len(files) == 0 { return "", nil, nil }

    dir, err := os.MkdirTemp("", "mct-discovery-")
    if err != nil { return "", nil, err }
    cleanup := func() { _ = os.RemoveAll(dir) }

    for _, rel := range files {
        // Normalize to OS separators for filesystem ops
        relFS := filepath.FromSlash(rel)
        src := filepath.Join(repoRoot, relFS)
        dst := filepath.Join(dir, relFS)
        // Ensure parent directory exists
        if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
            // Best-effort: skip this file
            continue
        }
        // Stat with Lstat to detect symlinks
        fi, err := os.Lstat(src)
        if err != nil {
            // Likely an uninitialized submodule file; log in verbose mode.
            debugf(verbose, "mct: skipping missing path: %s (possibly uninitialized submodule)", rel)
            continue
        }
        if fi.Mode()&os.ModeSymlink != 0 {
            // Recreate symlink
            target, err := os.Readlink(src)
            if err != nil {
                debugf(verbose, "mct: failed to read symlink %s: %v", rel, err)
                continue
            }
            _ = os.Symlink(target, dst)
            continue
        }
        // Try hard link first
        if err := os.Link(src, dst); err != nil {
            // Fallback to copy
            if err2 := copyFile(src, dst, fi.Mode()); err2 != nil {
                debugf(verbose, "mct: failed to copy %s: %v", rel, err2)
            }
        }
    }
    return dir, cleanup, nil
}

// copyFile copies contents from src to dst with perms; best-effort.
func copyFile(src, dst string, mode os.FileMode) error {
    in, err := os.Open(src)
    if err != nil { return err }
    defer in.Close()
    out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
    if err != nil { return err }
    defer func() { _ = out.Close() }()
    if _, err := io.Copy(out, in); err != nil { return err }
    return nil
}

// Thin wrappers to call internal/git without import cycles in helper decl.
// We keep them here to avoid dragging large git_utils into tests of this package.
func gitIsRepo(dir string) bool {
	return gitpkg.IsGitRepo(dir)
}
func gitRepoRoot(dir string) (string, error) {
	return gitpkg.RepoRoot(dir)
}
func gitListTracked(dir string) ([]string, error) {
	return gitpkg.ListTrackedFiles(dir)
}
