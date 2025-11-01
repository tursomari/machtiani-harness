package tempdir

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	mu          sync.RWMutex
	sessionRoot string
)

// SetSessionRoot registers the session-scoped temporary directory root.
// The directory is created if it does not already exist. Passing an empty
// string clears the current session root.
func SetSessionRoot(root string) error {
	trimmed := strings.TrimSpace(root)
	if trimmed != "" {
		if err := os.MkdirAll(trimmed, 0o755); err != nil {
			return err
		}
	}

	mu.Lock()
	sessionRoot = trimmed
	mu.Unlock()
	return nil
}

// ClearSessionRoot resets the session-scoped temporary directory.
func ClearSessionRoot() {
	mu.Lock()
	sessionRoot = ""
	mu.Unlock()
}

// SessionRoot returns the currently registered session temporary root.
func SessionRoot() string {
	mu.RLock()
	defer mu.RUnlock()
	return sessionRoot
}

// MkdirTemp creates a temporary directory using the active session root when
// available. When no session root is registered it falls back to the system
// default temp directory.
func MkdirTemp(pattern string) (string, error) {
	return MkdirTempIn("", pattern)
}

// MkdirTempIn creates a temporary directory under the provided subdirectory of
// the active session root. When no session root is registered it falls back to
// the system default temp directory.
func MkdirTempIn(subdir, pattern string) (string, error) {
	base := SessionRoot()
	if base == "" {
		return os.MkdirTemp("", pattern)
	}

	target := base
	if dir := strings.TrimSpace(subdir); dir != "" {
		target = filepath.Join(base, dir)
	}

	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(target, pattern)
}
