package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RefreshSnapshotFromHost rebuilds the snapshot repo under dstRoot.
func RefreshSnapshotFromHost(hostRepoRoot, dstRoot string) error {
	if strings.TrimSpace(hostRepoRoot) == "" || strings.TrimSpace(dstRoot) == "" {
		return nil
	}
	_, _, err := EnsureRepoSnapshot(hostRepoRoot, dstRoot)
	// Note: We intentionally discard the cleanup function here because the
	// snapshot is meant to persist for the duration of the session (or until
	// the next refresh).
	if err != nil {
		return err
	}
	return nil
}

func SnapshotRepoPath(sessionTmpRoot string) string {
	return filepath.Join(strings.TrimSpace(sessionTmpRoot), "repo")
}

func SyncPatchDir(sessionTmpRoot string) string {
	root := strings.TrimSpace(sessionTmpRoot)
	if root == "" {
		return ""
	}
	return filepath.Join(root, "patches")
}

func ValidateRoots(sessionTmpRoot, hostRepoRoot string) error {
	if strings.TrimSpace(sessionTmpRoot) == "" {
		return fmt.Errorf("session tmp root not set")
	}
	if strings.TrimSpace(hostRepoRoot) == "" {
		return fmt.Errorf("host repo root not set")
	}
	return nil
}
