package artifacts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/mct/internal/git"
)

const (
	machtianiRootDir = ".machtiani"
	chatDirName      = "chats"
	readmeDirName    = "readme"
	artifactDirName  = "artifacts"
)

// ChatDirectory returns the directory to use for chat transcripts.
// If invoked inside a git repository, it resolves relative to the repository root.
// Otherwise it falls back to the user's global ~/.machtiani/chats directory.
func ChatDirectory() (string, error) {
	root, local, err := projectRoot()
	if err != nil {
		return "", err
	}
	if local {
		return filepath.Join(root, machtianiRootDir, chatDirName), nil
	}
	return globalMachtianiPath(chatDirName)
}

// ArtifactsDirectory returns the directory to use for general artifacts such as
// file discovery trajectories. When inside a git repository it resolves at the
// repository root; otherwise it falls back to ~/.machtiani/artifacts.
func ArtifactsDirectory() (string, error) {
	root, local, err := projectRoot()
	if err != nil {
		return "", err
	}
	if local {
		return filepath.Join(root, machtianiRootDir, artifactDirName), nil
	}
	return globalMachtianiPath(artifactDirName)
}

// FileDiscoveryTrajectoryPath returns the canonical path for a file-discovery
// JSONL trajectory given a session identifier.
func FileDiscoveryTrajectoryPath(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session id required for file discovery trajectory path")
	}
	dir, err := ArtifactsDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("file-discovery-%s.jsonl", sessionID)), nil
}

// ReadmeDirectory returns the directory to use for README artifacts.
// It always resolves inside the current git repository root.
func ReadmeDirectory() (string, error) {
	root, local, err := projectRoot()
	if err != nil {
		return "", err
	}
	if !local {
		return "", errors.New("readme artifacts require a git repository context")
	}
	return filepath.Join(root, machtianiRootDir, artifactDirName, readmeDirName), nil
}

// IsLocalContext reports whether the current working directory is inside a git repository.
func IsLocalContext() (bool, error) {
	_, local, err := projectRoot()
	if err != nil {
		return false, err
	}
	return local, nil
}

func projectRoot() (string, bool, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false, fmt.Errorf("getwd: %w", err)
	}
	if git.IsGitRepo(wd) {
		root, err := git.RepoRoot(wd)
		if err != nil {
			return "", false, fmt.Errorf("resolve git root: %w", err)
		}
		return root, true, nil
	}
	return wd, false, nil
}

func globalMachtianiPath(subdir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, machtianiRootDir, subdir), nil
}
