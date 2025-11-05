package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// CommonDir returns the absolute path to the Git common directory for the repository
// containing dir. This corresponds to the location that holds shared metadata for
// all worktrees (typically the .git directory in the primary checkout).
func CommonDir(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse --git-common-dir failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	path := strings.TrimSpace(stdout.String())
	if path == "" {
		return "", fmt.Errorf("git rev-parse --git-common-dir returned empty output")
	}
	return filepath.Clean(path), nil
}
