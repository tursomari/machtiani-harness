package artifacts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/git"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
)

const (
	machtianiRootDir  = ".machtiani"
	sessionsDirName   = "sessions"
	chatDirName       = "chat"
	readmeDirName     = "readme"
	artifactDirName   = "artifacts"
	llmInputsDirName  = "llm"
	trajectoryDirName = "trajectory"
	scratchDirName    = "tmp"
)

// SessionsRoot returns the root directory containing all session directories.
// Initialized projects use their UUID home store; legacy projects retain their
// local path until migration. Clean Git projects must be initialized first.
func SessionsRoot() (string, error) {
	ctx, err := projectstore.Discover("")
	if err != nil {
		return "", err
	}
	if ctx.Status == projectstore.StatusInitialized {
		return ctx.SessionsRoot(), nil
	}
	if ctx.Status == projectstore.StatusUninitialized && git.IsGitRepo(ctx.ProjectRoot) {
		return "", projectInitializationError(ctx.ProjectRoot)
	}
	root, local, err := projectRoot()
	if err != nil {
		return "", err
	}
	if local {
		return filepath.Join(root, machtianiRootDir, sessionsDirName), nil
	}
	return globalMachtianiPath(sessionsDirName)
}

// SessionDirectory resolves the root directory for a session-scoped run.
// The caller must provide a non-empty session identifier.
func SessionDirectory(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session id required")
	}
	base, err := SessionsRoot()
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

// SessionConversationFile returns the canonical path for the session
// conversation JSON.
func SessionConversationFile(sessionID string) (string, error) {
	artifactsDir, err := SessionArtifactsDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(artifactsDir, "conversation.json"), nil
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

// SessionScratchDirectory resolves the scratch workspace root for a session.
// The caller must provide a non-empty session identifier.
func SessionScratchDirectory(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session id required")
	}
	root, err := ScratchRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, sessionID), nil
}

// ScratchRoot returns the base directory that should contain session scratch
// data for the current working directory. Initialized projects use their UUID
// home store and legacy projects retain their local scratch path.
func ScratchRoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv("MACHTIANI_TMP_ROOT")); root != "" {
		return root, nil
	}
	ctx, err := projectstore.Discover("")
	if err != nil {
		return "", err
	}
	if ctx.Status == projectstore.StatusInitialized {
		return ctx.ScratchRoot(), nil
	}
	if ctx.Status == projectstore.StatusUninitialized && git.IsGitRepo(ctx.ProjectRoot) {
		return "", projectInitializationError(ctx.ProjectRoot)
	}
	root, local, err := projectRoot()
	if err != nil {
		return "", err
	}
	if local {
		return filepath.Join(root, machtianiRootDir, scratchDirName), nil
	}
	return globalMachtianiPath(scratchDirName)
}

// ScratchRoots returns the scratch roots relevant to the current context.
func ScratchRoots() ([]string, error) {
	var roots []string
	ctx, err := projectstore.Discover("")
	if err != nil {
		return nil, err
	}
	if ctx.Status == projectstore.StatusInitialized {
		return []string{ctx.ScratchRoot()}, nil
	}
	if ctx.Status == projectstore.StatusUninitialized && git.IsGitRepo(ctx.ProjectRoot) {
		return nil, projectInitializationError(ctx.ProjectRoot)
	}
	root, local, err := projectRoot()
	if err != nil {
		return nil, err
	}
	if local {
		roots = append(roots, filepath.Join(root, machtianiRootDir, scratchDirName))
	}
	globalRoot, err := globalMachtianiPath(scratchDirName)
	if err != nil {
		return nil, err
	}
	dup := false
	for _, existing := range roots {
		if filepath.Clean(existing) == filepath.Clean(globalRoot) {
			dup = true
			break
		}
	}
	if !dup {
		roots = append(roots, globalRoot)
	}
	return roots, nil
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

// SessionLLMDirectory returns the directory under the session artifacts
// directory dedicated to storing LLM request/response logs.
func SessionLLMDirectory(sessionID string) (string, error) {
	artifactsDir, err := SessionArtifactsDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(artifactsDir, llmInputsDirName), nil
}

// SessionLLMInputsFile returns the canonical path for the append-only LLM input
// log within the session directory.
func SessionLLMInputsFile(sessionID string) (string, error) {
	dir, err := SessionLLMDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "inputs.jsonl"), nil
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
func ReadmeDirectory() (string, error) {
	return ReadmeDirectoryAt("")
}

// ReadmeDirectoryAt returns the README artifact directory for the project
// containing start, independent of the process working directory.
func ReadmeDirectoryAt(start string) (string, error) {
	ctx, err := projectstore.Discover(start)
	if err != nil {
		return "", err
	}
	if ctx.Status == projectstore.StatusInitialized {
		return filepath.Join(ctx.ArtifactsRoot(), readmeDirName), nil
	}
	if ctx.Status == projectstore.StatusUninitialized && git.IsGitRepo(ctx.ProjectRoot) {
		return "", projectInitializationError(ctx.ProjectRoot)
	}
	if !git.IsGitRepo(ctx.ProjectRoot) {
		return "", errors.New("readme artifacts require a git repository context")
	}
	return filepath.Join(ctx.ProjectRoot, machtianiRootDir, artifactDirName, readmeDirName), nil
}

func projectInitializationError(projectRoot string) error {
	return fmt.Errorf("project at %s is not initialized; run machtiani init", projectRoot)
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

// ShellAgentTrajectoryPath returns the canonical path for the shell-agent
// trajectory JSON file for a given session and turn number.
func ShellAgentTrajectoryPath(sessionID string, turn int) (string, error) {
	root, err := SessionDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "shell-agent", fmt.Sprintf("%d", turn), "trajectory.json"), nil
}
