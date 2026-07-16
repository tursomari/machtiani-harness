package environments

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/tempdir"
)

var (
	fallbackRootOnce sync.Once
	fallbackRoot     string
	fallbackErr      error
)

func sessionTempRoot() string {
	// Docker/non-local runs intentionally point MACHTIANI_TMP_ROOT at the
	// snapshot workspace root so shell-agent temp files land inside the mounted
	// snapshot. MACHTIANI_SESSION_TEMP_ROOT remains the host-local lock/marker
	// root and is used as the fallback.
	if root := strings.TrimSpace(os.Getenv("MACHTIANI_TMP_ROOT")); root != "" {
		return root
	}
	if root := strings.TrimSpace(os.Getenv("MACHTIANI_SESSION_TEMP_ROOT")); root != "" {
		return root
	}
	if root := strings.TrimSpace(tempdir.SessionRoot()); root != "" {
		return root
	}
	root, err := resolveFallbackRoot()
	if err != nil {
		return ""
	}
	return root
}

func resolveFallbackRoot() (string, error) {
	fallbackRootOnce.Do(func() {
		base, err := artifacts.ScratchRoot()
		if err != nil {
			fallbackErr = err
			return
		}
		if err := os.MkdirAll(base, 0o755); err != nil {
			fallbackErr = err
			return
		}
		root, err := os.MkdirTemp(base, "session-")
		if err != nil {
			fallbackErr = err
			return
		}
		fallbackRoot = root
	})
	return fallbackRoot, fallbackErr
}

func makeSessionTempDir(pattern string) (string, error) {
	return makeSessionTempDirIn("", pattern)
}

func makeSessionTempDirIn(subdir, pattern string) (string, error) {
	root := sessionTempRoot()
	if root == "" {
		return os.MkdirTemp("", pattern)
	}

	target := filepath.Join(root, "shell-agent")
	if dir := strings.TrimSpace(subdir); dir != "" {
		target = filepath.Join(target, dir)
	}

	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(target, pattern)
}

func sessionMarkerRoot() string {
	if root := strings.TrimSpace(os.Getenv("MACHTIANI_SESSION_TEMP_ROOT")); root != "" {
		return root
	}
	return ""
}

func ensureMarkerDir() (string, error) {
	root := sessionMarkerRoot()
	if root == "" {
		root = os.TempDir()
	}
	target := filepath.Join(root, "shell-agent", "markers")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	return target, nil
}
