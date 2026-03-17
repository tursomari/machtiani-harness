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

func SanitizeWorktreeConfigForContainer(worktreePath string) error {
	gitDir, err := locateWorktreeGitDir(worktreePath)
	if err != nil {
		return err
	}
	if gitDir == "" {
		return nil
	}

	configPath := filepath.Join(gitDir, "config")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read config: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	var updated []string
	removed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "worktree =") {
			removed = true
			continue
		}
		updated = append(updated, line)
	}

	if !removed {
		return nil
	}

	content := strings.Join(updated, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	mode := os.FileMode(0o600)
	if info, err := os.Stat(configPath); err == nil {
		mode = info.Mode().Perm()
	}

	if err := os.WriteFile(configPath, []byte(content), mode); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func SanitizeSubmoduleURLsForContainer(worktreePath, originalRepoRoot string) error {
	modulesPath := filepath.Join(worktreePath, ".gitmodules")
	data, err := os.ReadFile(modulesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read .gitmodules: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	updated := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "url =") || !strings.Contains(trimmed, "file://") {
			continue
		}

		urlStart := strings.Index(line, "file://")
		if urlStart < 0 {
			continue
		}
		afterURL := line[urlStart+len("file://"):]
		pathEnd := strings.IndexFunc(afterURL, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '#'
		})
		var remainder string
		pathComponent := afterURL
		if pathEnd >= 0 {
			pathComponent = afterURL[:pathEnd]
			remainder = afterURL[pathEnd:]
		}
		absPath := strings.TrimSpace(pathComponent)
		if absPath == "" || !filepath.IsAbs(absPath) {
			continue
		}

		rel, relErr := filepath.Rel(originalRepoRoot, absPath)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			continue
		}

		prefix := line[:urlStart]
		relPath := filepath.ToSlash(rel)
		lines[i] = fmt.Sprintf("%sfile://%s%s", prefix, relPath, remainder)
		updated = true
	}

	if !updated {
		return nil
	}

	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	mode := os.FileMode(0o600)
	if info, err := os.Stat(modulesPath); err == nil {
		mode = info.Mode().Perm()
	}

	if err := os.WriteFile(modulesPath, []byte(content), mode); err != nil {
		return fmt.Errorf("write .gitmodules: %w", err)
	}
	return nil
}

func SanitizeSubmoduleWorktreeConfigsForContainer(snapshotRoot, containerRoot string) error {
	if containerRoot == "" {
		containerRoot = strings.TrimSpace(os.Getenv("MACHTIANI_DOCKER_WORKTREE_ROOT"))
	}
	if containerRoot == "" {
		return nil
	}

	gitDir, err := locateWorktreeGitDir(snapshotRoot)
	if err != nil {
		return err
	}
	if gitDir == "" {
		return nil
	}

	commonGitDir, err := resolveCommonGitDir(gitDir)
	if err != nil {
		return fmt.Errorf("resolve common git dir: %w", err)
	}

	moduleRoots := []string{filepath.Join(gitDir, "modules")}
	if commonGitDir != "" && filepath.Clean(commonGitDir) != filepath.Clean(gitDir) {
		moduleRoots = append(moduleRoots, filepath.Join(commonGitDir, "modules"))
	}

	seen := make(map[string]struct{}, len(moduleRoots))
	for _, modulesRoot := range moduleRoots {
		modulesRoot = filepath.Clean(modulesRoot)
		if _, ok := seen[modulesRoot]; ok {
			continue
		}
		seen[modulesRoot] = struct{}{}
		if err := sanitizeSubmoduleWorktreeConfigsUnderModules(modulesRoot, containerRoot); err != nil {
			return err
		}
	}

	return nil
}

func sanitizeSubmoduleWorktreeConfigsUnderModules(modulesRoot, containerRoot string) error {
	if _, err := os.Stat(modulesRoot); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat submodule gitdir: %w", err)
	}

	if err := filepath.WalkDir(modulesRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() || filepath.Base(path) != "config" {
			return nil
		}
		relDir, err := filepath.Rel(modulesRoot, filepath.Dir(path))
		if err != nil {
			return err
		}
		relDir = filepath.Clean(relDir)
		if relDir == "." || relDir == ".." || strings.HasPrefix(relDir, ".."+string(os.PathSeparator)) {
			return nil
		}
		subWorktree := filepath.Join(containerRoot, filepath.FromSlash(filepath.ToSlash(relDir)))
		if err := rewriteCoreWorktreeConfig(path, subWorktree); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return fmt.Errorf("sanitize submodule worktree configs: %w", err)
	}

	return nil
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

func rewriteCoreWorktreeConfig(configPath, worktree string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read config: %w", err)
	}

	absWorktree := worktree
	if !filepath.IsAbs(absWorktree) {
		if resolved, err := filepath.Abs(absWorktree); err == nil {
			absWorktree = resolved
		}
	}
	worktreeVal := filepath.ToSlash(filepath.Clean(absWorktree))

	lines := strings.Split(string(data), "\n")
	updated := make([]string, 0, len(lines)+2)
	inCore := false
	replaced := false
	inserted := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inCore && !replaced && !inserted {
				updated = append(updated, "\tworktree = "+worktreeVal)
				inserted = true
			}
			inCore = strings.EqualFold(trimmed, "[core]")
			updated = append(updated, line)
			continue
		}
		if inCore && strings.HasPrefix(strings.TrimSpace(line), "worktree =") {
			if !replaced {
				updated = append(updated, "\tworktree = "+worktreeVal)
				replaced = true
			}
			continue
		}
		updated = append(updated, line)
	}
	if !replaced && !inserted {
		if inCore {
			updated = append(updated, "\tworktree = "+worktreeVal)
		} else {
			updated = append(updated, "[core]", "\tworktree = "+worktreeVal)
		}
	}

	content := strings.Join(updated, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if content == string(data) {
		return nil
	}

	mode := os.FileMode(0o600)
	if info, err := os.Stat(configPath); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(configPath, []byte(content), mode); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
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
