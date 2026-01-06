package patcher

import (
	internaldiff "github.com/tursomari/machtiani/agent/internal/patcher/internal/diff"
)

// GenerateDiff produces a unified patch by diffing only the provided files
// between repoRoot and mirrorDir using git.
func GenerateDiff(repoRoot, mirrorDir string, files []string) ([]byte, error) {
	return internaldiff.Generate(repoRoot, mirrorDir, files)
}

