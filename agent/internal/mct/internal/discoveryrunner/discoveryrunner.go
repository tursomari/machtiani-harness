package discoveryrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"
	integration "github.com/tursomari/machtiani/agent/internal/file-discovery/integration"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	gitpkg "github.com/tursomari/machtiani/agent/internal/mct/internal/git"
	"github.com/tursomari/machtiani/agent/internal/mct/llm"
)

// Result holds parsed file paths from file-discovery output
type Result struct {
	Paths []string
}

type ModelSettings struct {
	UsingAlias         bool
	Alias              string
	Resolved           llm.ResolvedModel
	ParamPairs         []string
	ParamJSON          []string
	TrajectoryOverride string
}

// Matches blocks like:
// BEGIN_RELEVANT_FILES[session]
// <paths...>
// END_RELEVANT_FILES[session]
// Go's regexp doesn't support backreferences, so capture both session ids and compare in code.
var blockRe = regexp.MustCompile(`(?s)BEGIN_RELEVANT_FILES\[(.+?)\]\n(.*?)\nEND_RELEVANT_FILES\[(.+?)\]`)

const exitCodeNoRelevantFiles = 2

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
func Run(ctx context.Context, prompt string, model ModelSettings, sessionID string, verbose bool) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	effectiveSessionID := strings.TrimSpace(sessionID)
	if effectiveSessionID == "" {
		effectiveSessionID = uuid.New().String()
	}

	extraParams, err := llm.ParseParamOverrides(model.ParamPairs, model.ParamJSON)
	if err != nil {
		return Result{}, fmt.Errorf("parse llm parameter overrides: %w", err)
	}

	resolved := llm.CloneResolvedModel(model.Resolved)
	cfg := integration.Config{
		MaxRounds:      20,
		CmdTimeoutSec:  30,
		MaxStdoutBytes: 20480,
		MaxTranscript:  300000,
		Verbose:        verbose,
		SessionID:      effectiveSessionID,
		ToolCallMode:   integration.ToolCallModeJSON,
		APIKey:         strings.TrimSpace(resolved.APIKey),
		BaseURL:        strings.TrimSpace(resolved.BaseURL),
		Model:          strings.TrimSpace(resolved.Model),
	}
	if cfg.APIKey == "" || cfg.BaseURL == "" || cfg.Model == "" {
		return Result{}, errors.New("file-discovery runtime missing API key, base URL, or model")
	}
	llmSettings := integration.LLMSettings{
		Model:  resolved,
		Extras: extraParams,
	}
	debugf(verbose, "mct: using embedded file-discovery module")

	if override := strings.TrimSpace(model.TrajectoryOverride); override != "" {
		if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
			return Result{}, fmt.Errorf("create file-discovery trajectory directory: %w", err)
		}
		cfg.TrajectoryPath = override
	} else if effectiveSessionID != "" {
		trajectoryPath, err := artifacts.FileDiscoveryTrajectoryPath(effectiveSessionID)
		if err != nil {
			return Result{}, fmt.Errorf("resolve file-discovery trajectory path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(trajectoryPath), 0o755); err != nil {
			return Result{}, fmt.Errorf("create file-discovery trajectory directory: %w", err)
		}
		cfg.TrajectoryPath = trajectoryPath
	}

	var (
		restoreWD      func()
		cleanupSandbox func()
	)
	if useGitFilter() {
		tmpDir, tmpCleanup, err := prepareGitFilteredWorkspace(verbose)
		switch {
		case err != nil:
			debugf(verbose, "mct: discovery sandbox disabled: %v", err)
		case tmpDir == "":
			debugf(verbose, "mct: discovery sandbox skipped: not a git repo or empty")
			if tmpCleanup != nil {
				tmpCleanup()
			}
		default:
			wd, err2 := os.Getwd()
			if err2 != nil {
				tmpCleanup()
				return Result{}, fmt.Errorf("discovery sandbox getwd: %w", err2)
			}
			if err2 = os.Chdir(tmpDir); err2 != nil {
				tmpCleanup()
				return Result{}, fmt.Errorf("discovery sandbox chdir: %w", err2)
			}
			debugf(verbose, "mct: discovery sandbox enabled at %s", tmpDir)
			restoreWD = func() { _ = os.Chdir(wd) }
			cleanupSandbox = tmpCleanup
		}
	} else {
		debugf(verbose, "mct: discovery sandbox disabled via MCT_USE_GIT_FILTER")
	}
	defer func() {
		if restoreWD != nil {
			restoreWD()
		}
		if cleanupSandbox != nil {
			cleanupSandbox()
		}
	}()

	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return Result{}, fmt.Errorf("pipe stdout: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return Result{}, fmt.Errorf("pipe stderr: %w", err)
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return Result{}, fmt.Errorf("pipe stdin: %w", err)
	}

	originalStdout := os.Stdout
	originalStderr := os.Stderr
	originalStdin := os.Stdin
	os.Stdout = stdoutW
	os.Stderr = stderrW
	os.Stdin = stdinR

	originalLogWriter := log.Writer()
	originalLogFlags := log.Flags()
	originalLogPrefix := log.Prefix()
	log.SetOutput(stderrW)
	log.SetFlags(0)
	log.SetPrefix("")

	defer func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
		os.Stdin = originalStdin
		log.SetOutput(originalLogWriter)
		log.SetFlags(originalLogFlags)
		log.SetPrefix(originalLogPrefix)
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		_ = stdinR.Close()
		_ = stdinW.Close()
	}()

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stdoutBuf, stdoutR)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stderrBuf, stderrR)
	}()

	go func() {
		_, _ = io.WriteString(stdinW, prompt)
		_ = stdinW.Close()
	}()

	exitCode := integration.Run(cfg, llmSettings)

	_ = stdoutW.Close()
	_ = stderrW.Close()
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if exitCode == exitCodeNoRelevantFiles {
		return Result{}, nil
	}
	if exitCode != 0 {
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg != "" {
			return Result{}, fmt.Errorf("file-discovery failed: exit code %d\n%s", exitCode, errMsg)
		}
		return Result{}, fmt.Errorf("file-discovery failed: exit code %d", exitCode)
	}

	outStr := stdoutBuf.String()
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
	if v == "false" || v == "0" || v == "no" {
		return false
	}
	return true
}

// debugf writes debug lines to stderr when MCT_DEBUG is set.
func debugf(verbose bool, format string, args ...any) {
	if !verbose {
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// prepareGitFilteredWorkspace creates a temporary directory containing only git-tracked files
// from the repository at the current working directory. It returns "" if not a git repo
// or no files are tracked. Caller should set cmd.Dir to the returned dir and call cleanup afterwards.
func prepareGitFilteredWorkspace(verbose bool) (tmpDir string, cleanup func(), err error) {
	// Lazy import to avoid cycles
	type gitAPI interface {
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
	if err != nil {
		return "", nil, err
	}

	// Check if inside a git repo
	if !gitIsRepo(cwd) {
		return "", nil, nil
	}
	repoRoot, err := gitRepoRoot(cwd)
	if err != nil {
		return "", nil, nil
	}
	files, err := gitListTracked(repoRoot)
	if err != nil {
		return "", nil, nil
	}
	if len(files) == 0 {
		return "", nil, nil
	}

	dir, err := os.MkdirTemp("", "mct-discovery-")
	if err != nil {
		return "", nil, err
	}
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
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
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
