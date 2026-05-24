package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const SharedGitMountRelative = ".shell-agent/git-root"

func SharedGitMountPath(worktreePath string) string {
	return filepath.Join(worktreePath, SharedGitMountRelative)
}


func SanitizeGitdirPointerForContainer(worktreePath string) error {
	gitPath := filepath.Join(worktreePath, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat .git: %w", err)
	}

	if info.IsDir() {
		return nil
	}

	perm := info.Mode().Perm()
	if perm == 0 {
		perm = 0o600
	}

	var gitdirPath string
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(gitPath)
		if err != nil {
			return fmt.Errorf("read .git symlink: %w", err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(gitPath), target)
		}
		gitdirPath = target
	default:
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return fmt.Errorf("read .git pointer: %w", err)
		}
		content := strings.TrimSpace(string(data))
		if !strings.HasPrefix(strings.ToLower(content), "gitdir:") {
			return nil
		}
		gitdirPath = strings.TrimSpace(content[len("gitdir:"):])
		if gitdirPath == "" {
			return nil
		}
		if !filepath.IsAbs(gitdirPath) {
			gitdirPath = filepath.Join(worktreePath, filepath.FromSlash(gitdirPath))
		}
	}

	absGitdir, err := filepath.Abs(gitdirPath)
	if err != nil {
		return fmt.Errorf("resolve gitdir path: %w", err)
	}
	if gitdirWithinWorktree(absGitdir, worktreePath) {
		return nil
	}

	repoGitDir, err := resolveCommonGitDir(absGitdir)
	if err != nil {
		return fmt.Errorf("resolve shared git directory: %w", err)
	}

	if err := ensureSharedGitLink(worktreePath, sharedGitLinkTarget(repoGitDir)); err != nil {
		return err
	}

	sharedTarget := filepath.Join(SharedGitMountPath(worktreePath), "worktrees", filepath.Base(absGitdir))
	rel, err := filepath.Rel(worktreePath, sharedTarget)
	if err != nil {
		return fmt.Errorf("compute relative gitdir: %w", err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "" {
		rel = "."
	}

	newContent := fmt.Sprintf("gitdir: %s\n", rel)
	if err := os.WriteFile(gitPath, []byte(newContent), perm); err != nil {
		return fmt.Errorf("write .git pointer: %w", err)
	}

	return nil
}

func locateWorktreeGitDir(worktreePath string) (string, error) {
	gitPath := filepath.Join(worktreePath, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat .git: %w", err)
	}

	if info.IsDir() {
		return gitPath, nil
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(gitPath)
		if err != nil {
			return "", fmt.Errorf("read .git symlink: %w", err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(gitPath), target)
		}
		return target, nil
	}

	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", fmt.Errorf("read .git pointer: %w", err)
	}
	content := strings.TrimSpace(string(data))
	if !strings.HasPrefix(strings.ToLower(content), "gitdir:") {
		return "", nil
	}
	gitdirPath := strings.TrimSpace(content[len("gitdir:"):])
	if gitdirPath == "" {
		return "", nil
	}
	if !filepath.IsAbs(gitdirPath) {
		gitdirPath = filepath.Join(worktreePath, filepath.FromSlash(gitdirPath))
	}
	return gitdirPath, nil
}


func sharedGitLinkTarget(repoGitDir string) string {
	if override := strings.TrimSpace(os.Getenv("MACHTIANI_DOCKER_GIT_ROOT")); override != "" {
		return filepath.Clean(override)
	}
	return repoGitDir
}

func ensureSharedGitLink(worktreePath, linkTarget string) error {
	linkPath := SharedGitMountPath(worktreePath)
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return fmt.Errorf("create shared git link parent: %w", err)
	}
	if err := createOrReplaceSymlink(linkPath, linkTarget); err != nil {
		return fmt.Errorf("ensure shared git link: %w", err)
	}
	return nil
}

func createOrReplaceSymlink(path, target string) error {
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve symlink target: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			existing, err := os.Readlink(path)
			if err == nil {
				if !filepath.IsAbs(existing) {
					existing = filepath.Join(filepath.Dir(path), existing)
				}
				if filepath.Clean(existing) == filepath.Clean(absoluteTarget) {
					return nil
				}
			}
		}
		if err := os.Remove(path); err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("remove existing symlink: %w", err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat symlink path: %w", err)
	}
	if err := os.Symlink(absoluteTarget, path); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}
	return nil
}

func resolveCommonGitDir(gitdirPath string) (string, error) {
	commondirPath := filepath.Join(gitdirPath, "commondir")
	data, err := os.ReadFile(commondirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return filepath.Dir(gitdirPath), nil
		}
		return "", fmt.Errorf("read commondir: %w", err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return filepath.Dir(gitdirPath), nil
	}
	if !filepath.IsAbs(content) {
		content = filepath.Join(gitdirPath, filepath.FromSlash(content))
	}
	absContent, err := filepath.Abs(content)
	if err != nil {
		return "", fmt.Errorf("resolve commondir path: %w", err)
	}
	return absContent, nil
}

func gitdirWithinWorktree(gitdirPath, worktreePath string) bool {
	rel, err := filepath.Rel(worktreePath, gitdirPath)
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}
