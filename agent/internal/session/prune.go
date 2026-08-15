package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/sessionfiles"
)

type PruneOptions struct {
	SessionID string
	DryRun    bool
}

type PruneReport struct {
	DryRun                      bool     `json:"dry_run"`
	Root                        string   `json:"root"`
	RequestedSession            string   `json:"requested_session,omitempty"`
	SessionsScanned             int      `json:"sessions_scanned"`
	SessionsChanged             int      `json:"sessions_changed"`
	SkippedActiveSessions       []string `json:"skipped_active_sessions"`
	RemovedLLMInputFiles        int      `json:"removed_llm_input_files"`
	RemovedLLMInputBytes        int64    `json:"removed_llm_input_bytes"`
	RemovedShellAgentStateFiles int      `json:"removed_shell_agent_state_files"`
	RemovedShellAgentStateBytes int64    `json:"removed_shell_agent_state_bytes"`
	RemovedDirectories          int      `json:"removed_directories"`
}

type pruneCandidate struct {
	path string
	kind sessionfiles.DisposableKind
	size int64
}

func PruneSessions(opts PruneOptions) (PruneReport, error) {
	root, err := artifacts.SessionsRoot()
	if err != nil {
		return PruneReport{}, err
	}
	report := PruneReport{
		DryRun:           opts.DryRun,
		Root:             root,
		RequestedSession: strings.TrimSpace(opts.SessionID),
	}

	ids, err := pruneSessionIDs(root, report.RequestedSession)
	if err != nil {
		return PruneReport{}, err
	}
	for _, sessionID := range ids {
		report.SessionsScanned++
		active, err := IsSessionActive(sessionID)
		if err != nil {
			return PruneReport{}, fmt.Errorf("check session %s: %w", sessionID, err)
		}
		if active {
			if report.RequestedSession != "" {
				return PruneReport{}, fmt.Errorf("cannot prune session %s: session is currently active", sessionID)
			}
			report.SkippedActiveSessions = append(report.SkippedActiveSessions, sessionID)
			continue
		}

		if opts.DryRun {
			changed, err := pruneOneSession(filepath.Join(root, sessionID), true, &report)
			if err != nil {
				return PruneReport{}, fmt.Errorf("scan session %s: %w", sessionID, err)
			}
			if changed {
				report.SessionsChanged++
			}
			continue
		}

		scratchDir, err := artifacts.SessionScratchDirectory(sessionID)
		if err != nil {
			return PruneReport{}, fmt.Errorf("resolve lock directory for %s: %w", sessionID, err)
		}
		lock, err := acquireSessionLock(sessionID, scratchDir)
		if err != nil {
			activeNow, activeErr := IsSessionActive(sessionID)
			if activeErr == nil && activeNow && report.RequestedSession == "" {
				report.SkippedActiveSessions = append(report.SkippedActiveSessions, sessionID)
				continue
			}
			return PruneReport{}, fmt.Errorf("lock session %s for pruning: %w", sessionID, err)
		}
		changed, pruneErr := pruneOneSession(filepath.Join(root, sessionID), false, &report)
		closeErr := lock.Close()
		if pruneErr != nil {
			return PruneReport{}, fmt.Errorf("prune session %s: %w", sessionID, pruneErr)
		}
		if closeErr != nil {
			return PruneReport{}, fmt.Errorf("release session %s after pruning: %w", sessionID, closeErr)
		}
		if changed {
			report.SessionsChanged++
		}
	}
	sort.Strings(report.SkippedActiveSessions)
	return report, nil
}

func pruneSessionIDs(root, requested string) ([]string, error) {
	if requested != "" {
		if requested == "." || requested == ".." || filepath.Base(requested) != requested || strings.ContainsAny(requested, `/\\`) {
			return nil, fmt.Errorf("invalid session ID %q", requested)
		}
		info, err := os.Lstat(filepath.Join(root, requested))
		if err != nil {
			return nil, fmt.Errorf("inspect session %s: %w", requested, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("session %s is not a directory", requested)
		}
		return []string{requested}, nil
	}

	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func pruneOneSession(sessionDir string, dryRun bool, report *PruneReport) (bool, error) {
	candidates := make([]pruneCandidate, 0)
	err := filepath.WalkDir(sessionDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(sessionDir, path)
		if err != nil {
			return err
		}
		kind := sessionfiles.Classify(relative)
		if kind == sessionfiles.DisposableNone {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		candidates = append(candidates, pruneCandidate{path: path, kind: kind, size: info.Size()})
		return nil
	})
	if err != nil {
		return false, err
	}

	changed := false
	removedPaths := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !dryRun {
			if err := os.Remove(candidate.path); err != nil {
				return changed, err
			}
		}
		removedPaths[filepath.Clean(candidate.path)] = struct{}{}
		changed = true
		switch candidate.kind {
		case sessionfiles.DisposableLLMInputLog:
			report.RemovedLLMInputFiles++
			report.RemovedLLMInputBytes += candidate.size
		case sessionfiles.DisposableShellAgentState:
			report.RemovedShellAgentStateFiles++
			report.RemovedShellAgentStateBytes += candidate.size
		}
	}

	dirs := pruneDirectoryCandidates(sessionDir)
	for _, dir := range dirs {
		empty, err := directoryEmptyAfterRemovals(dir, removedPaths)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return changed, err
		}
		if !empty {
			continue
		}
		if !dryRun {
			if err := os.Remove(dir); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return changed, err
			}
		}
		removedPaths[filepath.Clean(dir)] = struct{}{}
		changed = true
		report.RemovedDirectories++
	}
	return changed, nil
}

func pruneDirectoryCandidates(sessionDir string) []string {
	dirs := []string{filepath.Join(sessionDir, "artifacts", "llm")}
	shellRoot := filepath.Join(sessionDir, "shell-agent")
	entries, err := os.ReadDir(shellRoot)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() && numericTurn(entry.Name()) {
				dirs = append(dirs, filepath.Join(shellRoot, entry.Name()))
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		return strings.Count(filepath.Clean(dirs[i]), string(filepath.Separator)) > strings.Count(filepath.Clean(dirs[j]), string(filepath.Separator))
	})
	dirs = append(dirs, shellRoot)
	return dirs
}

func numericTurn(value string) bool {
	n, err := strconv.Atoi(value)
	return err == nil && n >= 0
}

func directoryEmptyAfterRemovals(path string, removedPaths map[string]struct{}) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if _, removed := removedPaths[filepath.Clean(filepath.Join(path, entry.Name()))]; !removed {
			return false, nil
		}
	}
	return true, nil
}
