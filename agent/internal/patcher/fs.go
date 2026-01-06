package patcher

import (
	internalfs "github.com/tursomari/machtiani/agent/internal/patcher/internal/fs"
)

// MakeTempMirror materializes the provided after-state content into a temporary
// directory and returns the mirror path plus a cleanup function.
func MakeTempMirror(after map[string][]byte, reuseDir string) (string, func(), error) {
	return internalfs.MakeTempMirror(after, reuseDir)
}

