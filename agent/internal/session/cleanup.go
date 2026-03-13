package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

const (
	defaultCleanupAge             = 24 * time.Hour
	sessionLockFileName           = "session.lock"
	sessionLockStaleDuration      = 3 * time.Second
	defaultShellAgentMarkerMaxAge = time.Hour
	shellAgentMarkerMaxAgeEnv     = "MACHTIANI_SHELL_AGENT_MARKER_MAX_AGE"
	shellAgentMarkerPrefix        = "mct-swe-agent-finale"
	shellAgentMarkerSuffix        = ".txt"
)

var cleanupPrefixes = []string{
	"mini-swe-trajectories-",
	"shell-agent-worktree-",
	"patcher-mirror-",
	"patcher-workspace-",
	"workspace-",
	"session-",
}

func cleanupOrphanedTempDirs(verbose bool) error {
	now := time.Now()

	var errs []error

	scratchRoots, err := artifacts.ScratchRoots()
	if err != nil {
		errs = append(errs, fmt.Errorf("resolve scratch roots: %w", err))
	} else {
		for _, root := range scratchRoots {
			trimmed := strings.TrimSpace(root)
			if trimmed == "" {
				continue
			}
			if err := cleanupOrphanedSessionDirs(trimmed, now, sessionLockStaleDuration, verbose); err != nil {
				errs = append(errs, err)
			}
			if err := cleanupOrphanedTempDirsInternal(trimmed, now, defaultCleanupAge, verbose); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// Remove legacy session directories under /tmp/mct for backwards compatibility.
	legacySessionRoot := filepath.Join(os.TempDir(), "mct")
	if err := cleanupOrphanedSessionDirs(legacySessionRoot, now, sessionLockStaleDuration, verbose); err != nil {
		errs = append(errs, err)
	}
	if err := cleanupOrphanedTempDirsInternal(os.TempDir(), now, defaultCleanupAge, verbose); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func cleanupStaleShellAgentMarkers(root string, maxAge time.Duration, verbose bool) error {
	return cleanupStaleShellAgentMarkersAt(root, time.Now(), maxAge, verbose)
}

func shellAgentMarkerMaxAge() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(shellAgentMarkerMaxAgeEnv))
	if raw == "" {
		return defaultShellAgentMarkerMaxAge, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return defaultShellAgentMarkerMaxAge, fmt.Errorf("parse %s=%q: %w", shellAgentMarkerMaxAgeEnv, raw, err)
	}
	if parsed <= 0 {
		return defaultShellAgentMarkerMaxAge, fmt.Errorf("%s must be > 0 (got %q)", shellAgentMarkerMaxAgeEnv, raw)
	}
	return parsed, nil
}

func cleanupStaleShellAgentMarkersAt(root string, now time.Time, maxAge time.Duration, verbose bool) error {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	markerDir := filepath.Join(root, "shell-agent", "markers")
	entries, err := os.ReadDir(markerDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read marker dir %s: %w", markerDir, err)
	}

	cutoff := now.Add(-maxAge)
	var errs []error

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isShellAgentMarkerFile(name) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			warnCleanupError(filepath.Join(markerDir, name), infoErr)
			errs = append(errs, fmt.Errorf("stat %s: %w", filepath.Join(markerDir, name), infoErr))
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(markerDir, name)
		if removeErr := os.Remove(path); removeErr != nil {
			warnCleanupError(path, removeErr)
			errs = append(errs, fmt.Errorf("remove %s: %w", path, removeErr))
			continue
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "[cleanup] removed stale marker file: %s\n", path)
		}
	}

	if entries, dirErr := os.ReadDir(markerDir); dirErr == nil && len(entries) == 0 {
		if removeErr := os.Remove(markerDir); removeErr != nil && !os.IsNotExist(removeErr) {
			warnCleanupError(markerDir, removeErr)
			errs = append(errs, fmt.Errorf("remove marker dir %s: %w", markerDir, removeErr))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func cleanupOrphanedTempDirsInternal(tempDir string, now time.Time, maxAge time.Duration, verbose bool) error {
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read temp dir %s: %w", tempDir, err)
	}

	cutoff := now.Add(-maxAge)
	var errs []error

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !hasCleanupPrefix(name) {
			continue
		}
		path := filepath.Join(tempDir, name)
		info, infoErr := entry.Info()
		if infoErr != nil {
			warnCleanupError(path, infoErr)
			errs = append(errs, fmt.Errorf("stat %s: %w", path, infoErr))
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		orphaned, orphanErr := shouldRemoveCandidate(path)
		if orphanErr != nil {
			warnCleanupError(path, orphanErr)
			errs = append(errs, fmt.Errorf("inspect %s: %w", path, orphanErr))
			continue
		}
		if !orphaned {
			continue
		}
		if removeErr := os.RemoveAll(path); removeErr != nil {
			warnCleanupError(path, removeErr)
			errs = append(errs, fmt.Errorf("remove %s: %w", path, removeErr))
			continue
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "[cleanup] removed orphaned temp dir: %s\n", path)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func cleanupOrphanedSessionDirs(root string, now time.Time, staleThreshold time.Duration, verbose bool) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read session temp dir %s: %w", root, err)
	}

	var errs []error

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionPath := filepath.Join(root, entry.Name())
		lockPath := filepath.Join(sessionPath, sessionLockFileName)
		lockInfo, statErr := os.Stat(lockPath)

		removalReason := ""
		if statErr != nil {
			if os.IsNotExist(statErr) {
				removalReason = "missing lock file"
			} else {
				warnCleanupError(lockPath, statErr)
				errs = append(errs, fmt.Errorf("stat %s: %w", lockPath, statErr))
				continue
			}
		} else if now.Sub(lockInfo.ModTime()) > staleThreshold {
			removalReason = "stale lock"
		}

		if removalReason == "" {
			continue
		}

		if err := os.RemoveAll(sessionPath); err != nil {
			warnCleanupError(sessionPath, err)
			errs = append(errs, fmt.Errorf("remove %s: %w", sessionPath, err))
			continue
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "[cleanup] removed orphaned session dir: %s (%s)\n", sessionPath, removalReason)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func warnCleanupError(path string, err error) {
	fmt.Fprintf(os.Stderr, "[cleanup] warning: %s: %v\n", path, err)
}

func hasCleanupPrefix(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range cleanupPrefixes {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

func isShellAgentMarkerFile(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	if !strings.HasPrefix(trimmed, shellAgentMarkerPrefix) {
		return false
	}
	if !strings.HasSuffix(trimmed, shellAgentMarkerSuffix) {
		return false
	}
	return true
}

func shouldRemoveCandidate(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		return true, nil
	}

	hasLock, err := containsLockFile(path, 2)
	if err != nil {
		return false, err
	}
	if hasLock {
		return false, nil
	}

	return true, nil
}

func containsLockFile(path string, depth int) (bool, error) {
	if depth <= 0 {
		return false, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		name := strings.TrimSpace(strings.ToLower(entry.Name()))
		if name == "" {
			continue
		}
		if !entry.IsDir() {
			if isLockFile(name) {
				return true, nil
			}
			continue
		}
		hasLock, nestedErr := containsLockFile(filepath.Join(path, entry.Name()), depth-1)
		if nestedErr != nil {
			return false, nestedErr
		}
		if hasLock {
			return true, nil
		}
	}
	return false, nil
}

func isLockFile(name string) bool {
	if name == "" {
		return false
	}
	if name == "lock" || name == ".lock" {
		return true
	}
	lockSuffixes := []string{".lock", ".lck"}
	for _, suffix := range lockSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	if strings.Contains(name, "lock") {
		return true
	}
	return false
}
