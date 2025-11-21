package discoveryrunner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	integration "github.com/tursomari/machtiani/agent/internal/file-discovery/integration"
	gitpkg "github.com/tursomari/machtiani/agent/internal/git"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/session"
	"github.com/tursomari/machtiani/agent/internal/tempdir"
)

// Result holds parsed file paths from file-discovery output
type Result struct {
	Paths []string
}

type ModelSettings struct {
	UsingAlias         bool
	Alias              string
	Resolved           llm.ResolvedModel
	Extras             map[string]any
	ParamPairs         []string
	ParamJSON          []string
	FallbackAliases    []string
	FallbackResolved   []llm.ResolvedModel
	TrajectoryOverride string
	APIKeyOverrides    map[string]string
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
	mergedExtras := mergeExtras(model.Extras, extraParams)

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
		Model:            resolved,
		Extras:           mergedExtras,
		FallbackAliases:  append([]string(nil), model.FallbackAliases...),
		FallbackResolved: cloneResolvedModels(model.FallbackResolved),
		APIKeyOverrides:  llm.CopyAPIKeyOverridesForRuntime(model.APIKeyOverrides),
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
		tmpDir, tmpCleanup, err := prepareGitFilteredWorkspace(effectiveSessionID, verbose)
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

	exitCode := integration.Run(ctx, cfg, llmSettings)

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

// prepareGitFilteredWorkspace returns a directory mirroring the repository for
// file-discovery. When a session identifier is available it incrementally
// syncs a persistent workspace so new and modified files appear in subsequent
// discovery turns. Without a session identifier it falls back to the legacy
// ephemeral snapshot behavior.
func prepareGitFilteredWorkspace(sessionID string, verbose bool) (string, func(), error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return createEphemeralFilteredDir(verbose)
	}
	return ensurePersistentWorkspace(sessionID, verbose)
}

// createEphemeralFilteredDir mirrors the legacy behavior: build a throwaway
// directory with hard links to tracked files only.
func createEphemeralFilteredDir(verbose bool) (string, func(), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, err
	}
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
	dir, err := tempdir.MkdirTemp("mct-discovery-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := syncPathsIntoWorkspace(repoRoot, dir, files, verbose, nil, nil); err != nil {
		cleanup()
		return "", nil, err
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

const symlinkHashPrefix = "symlink:"

func ensurePersistentWorkspace(sessionID string, verbose bool) (string, func(), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, err
	}
	if !gitIsRepo(cwd) {
		return "", nil, nil
	}
	repoRoot, err := gitRepoRoot(cwd)
	if err != nil {
		return "", nil, nil
	}
	tracked, err := gitListTracked(repoRoot)
	if err != nil {
		return "", nil, err
	}
	base, err := artifacts.SessionScratchDirectory(sessionID)
	if err != nil {
		return "", nil, err
	}
	workspace := filepath.Join(base, "file-discovery", "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return "", nil, fmt.Errorf("create discovery workspace: %w", err)
	}

	loadedState, err := session.LoadDiscoveryState()
	if err != nil {
		return "", nil, err
	}
	state := session.FileDiscoveryState{WorkspacePath: workspace, Files: make(map[string]session.FileMeta)}
	if loadedState != nil {
		state = *loadedState
		if state.Files == nil {
			state.Files = make(map[string]session.FileMeta)
		}
	}
	state.WorkspacePath = workspace

	trackedSet := make(map[string]struct{}, len(tracked))
	for _, rel := range tracked {
		norm := normalizeRepoPath(rel)
		if norm == "" {
			continue
		}
		trackedSet[norm] = struct{}{}
	}

	pendingSet := make(map[string]struct{})
	for _, rel := range state.PendingPaths {
		norm := normalizeRepoPath(rel)
		if norm != "" {
			pendingSet[norm] = struct{}{}
		}
	}

	filesCopy := make(map[string]session.FileMeta, len(state.Files))
	for k, v := range state.Files {
		norm := normalizeRepoPath(k)
		if norm == "" {
			continue
		}
		filesCopy[norm] = v
	}

	for _, rel := range drainDiscoveryScratch(sessionID, verbose) {
		if rel != "" {
			pendingSet[rel] = struct{}{}
		}
	}

	if filepath.Clean(state.WorkspacePath) != filepath.Clean(workspace) {
		state.Files = make(map[string]session.FileMeta)
		filesCopy = make(map[string]session.FileMeta)
		pendingSet = make(map[string]struct{})
		for rel := range trackedSet {
			pendingSet[rel] = struct{}{}
		}
	}

	if len(state.Files) == 0 {
		for rel := range trackedSet {
			pendingSet[rel] = struct{}{}
		}
	}

	for rel := range trackedSet {
		meta, ok := filesCopy[rel]
		if ok {
			need, err := needsSync(repoRoot, rel, meta, true)
			if err != nil {
				debugf(verbose, "mct: unable to inspect tracked file %s: %v", rel, err)
				pendingSet[rel] = struct{}{}
				continue
			}
			if !need {
				continue
			}
		}
		pendingSet[rel] = struct{}{}
	}

	for rel, meta := range filesCopy {
		if _, tracked := trackedSet[rel]; tracked {
			continue
		}
		need, err := needsSync(repoRoot, rel, meta, false)
		if err != nil {
			debugf(verbose, "mct: unable to inspect pending file %s: %v", rel, err)
			pendingSet[rel] = struct{}{}
			continue
		}
		if need {
			pendingSet[rel] = struct{}{}
		}
	}

	if len(pendingSet) == 0 {
		return workspace, func() {}, nil
	}

	paths := make([]string, 0, len(pendingSet))
	for rel := range pendingSet {
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	err = syncPathsIntoWorkspace(repoRoot, workspace, paths, verbose, trackedSet, func(rel string, meta *session.FileMeta, removed bool) {
		if removed {
			delete(filesCopy, rel)
			return
		}
		if meta == nil {
			return
		}
		if _, ok := trackedSet[rel]; ok {
			meta.Tracked = true
		} else {
			meta.Tracked = false
		}
		filesCopy[rel] = *meta
	})
	if err != nil {
		return "", nil, err
	}

	_, err = session.UpdateDiscoveryState(func(st *session.FileDiscoveryState) error {
		st.WorkspacePath = workspace
		st.PendingPaths = nil
		if st.Files == nil {
			st.Files = make(map[string]session.FileMeta, len(filesCopy))
		} else {
			for k := range st.Files {
				delete(st.Files, k)
			}
		}
		for k, v := range filesCopy {
			st.Files[k] = v
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}

	return workspace, func() {}, nil
}

func syncPathsIntoWorkspace(repoRoot, workspace string, paths []string, verbose bool, tracked map[string]struct{}, cb func(rel string, meta *session.FileMeta, removed bool)) error {
	for _, raw := range paths {
		rel := normalizeRepoPath(raw)
		if rel == "" {
			continue
		}
		src := filepath.Join(repoRoot, filepath.FromSlash(rel))
		dst := filepath.Join(workspace, filepath.FromSlash(rel))

		fi, err := os.Lstat(src)
		if err != nil {
			if os.IsNotExist(err) {
				if err := os.RemoveAll(dst); err != nil && !os.IsNotExist(err) {
					debugf(verbose, "mct: failed to remove %s from workspace: %v", rel, err)
				}
				if cb != nil {
					cb(rel, nil, true)
				}
				continue
			}
			debugf(verbose, "mct: lstat failed for %s: %v", rel, err)
			continue
		}

		if fi.IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil && !os.IsExist(err) {
				debugf(verbose, "mct: mkdir failed for %s: %v", rel, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			debugf(verbose, "mct: mkdir parent failed for %s: %v", rel, err)
			continue
		}

		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(src)
			if err != nil {
				debugf(verbose, "mct: readlink failed for %s: %v", rel, err)
				continue
			}
			if err := os.RemoveAll(dst); err != nil && !os.IsNotExist(err) {
				debugf(verbose, "mct: remove existing symlink %s: %v", rel, err)
				continue
			}
			if err := os.Symlink(target, dst); err != nil {
				debugf(verbose, "mct: symlink recreate failed for %s: %v", rel, err)
				continue
			}
			if cb != nil {
				meta := session.FileMeta{Hash: symlinkHashPrefix + target}
				if _, ok := tracked[rel]; ok {
					meta.Tracked = true
				}
				cb(rel, &meta, false)
			}
			continue
		}

		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			debugf(verbose, "mct: remove existing file %s: %v", rel, err)
		}
		if err := os.Link(src, dst); err != nil {
			if err := copyFile(src, dst, fi.Mode()); err != nil {
				debugf(verbose, "mct: failed to mirror %s: %v", rel, err)
				continue
			}
		}
		if cb != nil {
			meta, err := computeFileMeta(src, fi)
			if err != nil {
				debugf(verbose, "mct: failed to hash %s: %v", rel, err)
				continue
			}
			if _, ok := tracked[rel]; ok {
				meta.Tracked = true
			}
			cb(rel, &meta, false)
		}
	}
	return nil
}

func computeFileMeta(path string, fi os.FileInfo) (session.FileMeta, error) {
	meta := session.FileMeta{
		Size:    fi.Size(),
		ModTime: fi.ModTime().UnixNano(),
	}
	f, err := os.Open(path)
	if err != nil {
		return meta, err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return meta, err
	}
	meta.Hash = hex.EncodeToString(hash.Sum(nil))
	return meta, nil
}

func needsSync(repoRoot, rel string, meta session.FileMeta, tracked bool) (bool, error) {
	src := filepath.Join(repoRoot, filepath.FromSlash(rel))
	fi, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return true, err
	}
	if fi.IsDir() {
		return false, nil
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return true, err
		}
		expected := symlinkHashPrefix + target
		if meta.Hash != expected || meta.Tracked != tracked {
			return true, nil
		}
		return false, nil
	}
	if meta.Hash == "" || meta.Size != fi.Size() || meta.ModTime != fi.ModTime().UnixNano() || meta.Tracked != tracked {
		return true, nil
	}
	return false, nil
}

func normalizeRepoPath(in string) string {
	trimmed := strings.TrimSpace(in)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." {
		return ""
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, `..`+string(os.PathSeparator)) {
		return ""
	}
	normalized := filepath.ToSlash(cleaned)
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	return normalized
}

func drainDiscoveryScratch(sessionID string, verbose bool) []string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	scratch, err := artifacts.SessionScratchDirectory(sessionID)
	if err != nil {
		debugf(verbose, "mct: unable to resolve discovery scratch directory: %v", err)
		return nil
	}
	path := filepath.Join(scratch, "file-discovery", "pending.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			debugf(verbose, "mct: unable to read discovery pending file: %v", err)
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		debugf(verbose, "mct: unable to clear discovery pending file: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	set := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		norm := normalizeRepoPath(line)
		if norm != "" {
			set[norm] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for rel := range set {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

func mergeExtras(base map[string]any, overrides map[string]any) map[string]any {
	if len(base) == 0 && len(overrides) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(base)+len(overrides))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

func cloneResolvedModels(src []llm.ResolvedModel) []llm.ResolvedModel {
	if len(src) == 0 {
		return nil
	}
	out := make([]llm.ResolvedModel, 0, len(src))
	for _, m := range src {
		out = append(out, llm.CloneResolvedModel(m))
	}
	return out
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

// RefreshSyncedWorkspace updates the persistent discovery workspace immediately
// after a patch is applied. It stages the provided changed paths and performs a
// sync so subsequent discovery runs (or consecutive patch decisions) observe
// the latest files without waiting for the next discovery invocation.
func RefreshSyncedWorkspace(sessionID string, changed []string, verbose bool) error {
    sid := strings.TrimSpace(sessionID)
    if sid == "" {
        // No stable session; nothing to refresh.
        return nil
    }
    if len(changed) > 0 {
        if err := session.AddPendingDiscoveryPaths(changed); err != nil {
            return err
        }
    }
    // ensurePersistentWorkspace will read pending paths (including those we
    // just added) and perform the incremental sync.
    dir, cleanup, err := ensurePersistentWorkspace(sid, verbose)
    if cleanup != nil {
        cleanup()
    }
    if err != nil {
        return err
    }
    if strings.TrimSpace(dir) == "" {
        // Not a git repo or nothing to do; treat as no-op.
        return nil
    }
    return nil
}
