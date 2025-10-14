package artifacts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/mct/internal/git"
)

const (
	machtianiRootDir  = ".machtiani"
	sessionsDirName   = "sessions"
	chatDirName       = "chat"
	readmeDirName     = "readme"
	artifactDirName   = "artifacts"
	patchesDirName    = "patches"
	trajectoryDirName = "trajectory"
)

// SessionDirectory resolves the root directory for a session-scoped run.
// The caller must provide a non-empty session identifier.
func SessionDirectory(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session id required")
	}
	root, local, err := projectRoot()
	if err != nil {
		return "", err
	}
	if local {
		return filepath.Join(root, machtianiRootDir, sessionsDirName, sessionID), nil
	}
	base, err := globalMachtianiPath(sessionsDirName)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, sessionID), nil
}

// SessionChatDirectory returns the directory under the session root dedicated
// to chat transcripts and final answers.
func SessionChatDirectory(sessionID string) (string, error) {

	root, err := SessionDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, chatDirName), nil
}

// SessionArtifactsDirectory returns the directory under the session root used
// for general artifacts such as file-discovery trajectories.
func SessionArtifactsDirectory(sessionID string) (string, error) {
	root, err := SessionDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, artifactDirName), nil
}

// SessionTrajectoryDirectory returns the directory for trajectory JSONL files
// under the session root. The directory may not exist; callers should ensure it
// is created before writing files.
func SessionTrajectoryDirectory(sessionID string) (string, error) {
	root, err := SessionDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, trajectoryDirName), nil
}

// SessionTrajectoryFile constructs the canonical JSONL path for a named
// trajectory stream in the session directory.
func SessionTrajectoryFile(sessionID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("trajectory file name required")
	}
	dir, err := SessionTrajectoryDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".jsonl"), nil
}

// SessionPatchesDirectory returns the directory under the session artifacts
// directory dedicated to storing generated patch files.
func SessionPatchesDirectory(sessionID string) (string, error) {
	artifactsDir, err := SessionArtifactsDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(artifactsDir, patchesDirName), nil
}

// FileDiscoveryTrajectoryPath returns the canonical path for a file-discovery
// JSONL trajectory given a session identifier.
func FileDiscoveryTrajectoryPath(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session id required for file discovery trajectory path")
	}
	dir, err := SessionArtifactsDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "file-discovery.jsonl"), nil
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
