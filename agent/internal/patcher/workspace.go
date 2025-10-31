package patcher

import (
	"path/filepath"

	fsutil "github.com/tursomari/machtiani/agent/internal/patcher/internal/fs"
)

// CreateWorkspace mirrors the repository at repoRoot into a temporary working
// directory that maintains its own git metadata. The returned cleanup function
// removes the workspace when invoked.
func CreateWorkspace(repoRoot string) (string, func(), error) {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", func() {}, err
	}
	return fsutil.MakeSessionWorkspace(abs)
}
