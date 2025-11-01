package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultCleanupAge = 24 * time.Hour
)

var cleanupPrefixes = []string{
	"mini-swe-trajectories-",
	"shell-agent-worktree-",
	"patcher-mirror-",
	"patcher-workspace-",
	"session-",
}

func cleanupOrphanedTempDirs(verbose bool) error {
	tempDir := os.TempDir()
	return cleanupOrphanedTempDirsInternal(tempDir, time.Now(), defaultCleanupAge, verbose)
}

func cleanupOrphanedTempDirsInternal(tempDir string, now time.Time, maxAge time.Duration, verbose bool) error {
	entries, err := os.ReadDir(tempDir)
	if err != nil {
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
