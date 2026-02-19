package workspace

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/git"
	"github.com/tursomari/machtiani/agent/internal/llm"
	worktreeutils "github.com/tursomari/machtiani/agent/internal/worktree"
)

// EnsureRepoSnapshot creates a detached git worktree at dstRoot/repo.
//
// The snapshot contains full git history and working tree state (including
// staged + unstaged changes) and is suitable for tools that require `.git`.
//
// Ignore rules are applied when recording sync manifests; the snapshot itself is
// a full worktree.
func EnsureRepoSnapshot(workingDir, dstRoot string) (string, func(), error) {
	repoRoot, err := git.RepoRoot(workingDir)
	if err != nil {
		return "", nil, fmt.Errorf("locate git repository: %w", err)
	}
	repoRoot, err = filepath.Abs(repoRoot)
	if err != nil {
		return "", nil, fmt.Errorf("resolve repository path: %w", err)
	}
	repoRoot = filepath.Clean(repoRoot)
	dstRoot = filepath.Clean(dstRoot)
	if strings.TrimSpace(dstRoot) == "" {
		return "", nil, fmt.Errorf("workspace root is empty")
	}
	if filepath.IsAbs(dstRoot) {
		// ok
	} else {
		abs, err := filepath.Abs(dstRoot)
		if err != nil {
			return "", nil, fmt.Errorf("resolve workspace root: %w", err)
		}
		dstRoot = abs
	}
	if filepath.Clean(dstRoot) == filepath.Clean(repoRoot) {
		return "", nil, fmt.Errorf("refusing to use repository root as workspace root: %s", dstRoot)
	}

	snapshotRoot := filepath.Join(dstRoot, "repo")
	if filepath.Clean(snapshotRoot) == filepath.Clean(repoRoot) {
		return "", nil, fmt.Errorf("refusing to overwrite repository root: %s", snapshotRoot)
	}
	if rel, err := filepath.Rel(snapshotRoot, repoRoot); err == nil {
		if rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..") {
			return "", nil, fmt.Errorf("refusing to place snapshot at or above repository root: snapshot=%s repo=%s", snapshotRoot, repoRoot)
		}
	}
	manifestPath := filepath.Join(dstRoot, "manifests", "sync.json")

	m, err := loadOrInitManifest(manifestPath)
	if err != nil {
		return "", nil, fmt.Errorf("load sync manifest: %w", err)
	}
	if m.CreatedPaths != nil {
		for rel := range m.CreatedPaths {
			if hardExcluded(rel) {
				delete(m.CreatedPaths, rel)
			}
		}
	}
	ignoreCfg, _ := loadIgnoreConfigBestEffort()
	cfg, configPath, cfgErr := llm.LoadGlobalConfig()
	hydrationCfg := (*llm.WorkspaceConfig)(nil)
	hydrationCfg = cfg.Workspace
	verbose := strings.TrimSpace(os.Getenv("MACHTIANI_WORKSPACE_DEBUG")) != ""
	if verbose {
		fmt.Fprintf(os.Stderr, "[workspace] config=%s\n", strings.TrimSpace(configPath))
		if cfgErr != nil {
			fmt.Fprintf(os.Stderr, "[workspace] config_load_error=%v\n", cfgErr)
		}
		fmt.Fprintf(os.Stderr, "[workspace] git_hydration_rules=%s\n", formatHydrationRules(hydrationCfg))
	}

	// Ensure the worktree target is removed from both FS and git metadata.
	if err := os.RemoveAll(snapshotRoot); err != nil {
		return "", nil, fmt.Errorf("remove existing snapshot %s: %w", snapshotRoot, err)
	}
	if err := os.RemoveAll(snapshotRoot); err != nil {
		return "", nil, fmt.Errorf("remove existing snapshot %s: %w", snapshotRoot, err)
	}
	// We do NOT call `git worktree remove` or `prune` here because if the snapshotRoot
	// is inside the repo, these commands might affect the host repo's state or history
	// in unexpected ways. os.RemoveAll is sufficient to clear the path.
	if err := os.RemoveAll(snapshotRoot); err != nil {
		return "", nil, fmt.Errorf("remove existing snapshot %s: %w", snapshotRoot, err)
	}

	if _, stderr, err := execGit(repoRoot, nil, "worktree", "add", "--force", "--detach", snapshotRoot, "HEAD"); err != nil {
		sanitized := strings.TrimSpace(string(stderr))
		if strings.Contains(sanitized, "already exists") {
			if err := os.RemoveAll(snapshotRoot); err != nil {
				return "", nil, fmt.Errorf("remove existing snapshot %s: %w", snapshotRoot, err)
			}
			if _, retryStderr, retryErr := execGit(repoRoot, nil, "worktree", "add", "--force", "--detach", snapshotRoot, "HEAD"); retryErr != nil {
				return "", nil, fmt.Errorf("git worktree add failed: %w: %s", retryErr, strings.TrimSpace(string(retryStderr)))
			}
		} else {
			return "", nil, fmt.Errorf("git worktree add failed: %w: %s", err, sanitized)
		}
	}

	cleanup := func() {
		// We do NOT call `git worktree remove` here because if the snapshotRoot
		// is inside the repo, it might affect the host repo.
		_ = os.RemoveAll(snapshotRoot)
	}

	// Critical Safety Check:
	// Ensure the snapshot root is actually a git repository (has .git file/dir)
	// before running any git commands inside it. If we run commands in a plain
	// directory inside the repo, they will execute against the HOST repo and
	// potentially wipe history (e.g. update-ref -d).
	if _, err := os.Stat(filepath.Join(snapshotRoot, ".git")); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("snapshot creation failed: .git not found in %s (refusing to run git commands in unsafe state)", snapshotRoot)
	}

	// We removed limitSnapshotRefsBestEffort because it is unsafe for git worktrees.
	// Worktrees share the same reference database as the main repo, so "hiding" refs
	// in the snapshot by deleting them actually deletes them in the host repo.
	if err := hydrateRootGitMetadata(repoRoot, snapshotRoot, hydrationCfg, verbose); err != nil {
		cleanup()
		return "", nil, err
	}

	if err := hydrateSubmodules(repoRoot, snapshotRoot, hydrationCfg, ignoreCfg, verbose); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := mirrorNestedGitWorktrees(repoRoot, snapshotRoot, hydrationCfg, ignoreCfg, verbose); err != nil {
		cleanup()
		return "", nil, err
	}

	// Apply staged + unstaged diffs, including deletions.
	if err := applyDiff(repoRoot, snapshotRoot, true); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := applyDiff(repoRoot, snapshotRoot, false); err != nil {
		cleanup()
		return "", nil, err
	}

	// Compute baseline hashes for tracked + created files (created paths are
	// session-scoped and can include previously-created files).
	candidates, err := syncCandidateSet(repoRoot, m.CreatedPaths, ignoreCfg)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("resolve sync candidate set: %w", err)
	}
	baseline := make(map[string]manifestEntry, len(candidates))
	for _, rel := range relPathList(candidates) {
		if hardExcluded(rel) || ignoredByConfig(rel, ignoreCfg) {
			continue
		}
		entry, err := computeEntry(repoRoot, rel)
		if err != nil {
			cleanup()
			return "", nil, fmt.Errorf("hash host file %s: %w", rel, err)
		}
		baseline[rel] = entry
	}
	m.Baseline = baseline
	m.IgnoreConfig = ignoreCfg
	if err := saveManifest(manifestPath, m); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("persist sync manifest: %w", err)
	}
	if err := worktreeutils.SanitizeGitdirPointerForContainer(snapshotRoot); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("sanitize gitdir pointer: %w", err)
	}
	if err := worktreeutils.SanitizeWorktreeConfigForContainer(snapshotRoot); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("sanitize worktree config: %w", err)
	}
	if err := worktreeutils.SanitizeSubmoduleURLsForContainer(snapshotRoot, repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to sanitize submodule URLs for container context: %v\n", err)
	}

	return snapshotRoot, cleanup, nil
}

func formatHydrationRules(cfg *llm.WorkspaceConfig) string {
	if cfg == nil {
		return "<nil>"
	}
	var parts []string
	if len(cfg.GitHydration) > 0 {
		for _, rule := range cfg.GitHydration {
			root := strings.TrimSpace(rule.Root)
			if root == "" {
				root = "."
			}
			branches := strings.Join(rule.Branches, ",")
			parts = append(parts, fmt.Sprintf("{root=%s branches=%s}", root, branches))
		}
		return "[" + strings.Join(parts, " ") + "]"
	}
	if len(cfg.GitHydrationRoots) == 0 && len(cfg.GitHydrationBranches) == 0 {
		return "<empty>"
	}
	return fmt.Sprintf("legacy{roots=%q branches=%q}", cfg.GitHydrationRoots, cfg.GitHydrationBranches)
}

func loadIgnoreConfigBestEffort() (*llm.IgnoreConfig, error) {
	cfg, _, err := llm.LoadGlobalConfig()
	if err != nil {
		return nil, err
	}
	if cfg.Ignore == nil {
		return nil, nil
	}
	ignore := *cfg.Ignore
	ignore.Paths = append([]string(nil), cfg.Ignore.Paths...)
	ignore.Extensions = append([]string(nil), cfg.Ignore.Extensions...)
	for i, p := range ignore.Paths {
		ignore.Paths[i] = strings.TrimSpace(p)
	}
	for i, e := range ignore.Extensions {
		ignore.Extensions[i] = strings.TrimSpace(e)
	}
	return &ignore, nil
}

func shouldHydrateGitForPath(rel string, cfg *llm.WorkspaceConfig) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(rel))))
	if clean == "" {
		clean = "."
	}
	if cfg == nil {
		// Default: hydrate only the root repo.
		return clean == "."
	}
	// If [workspace] is present but hydration fields are unset, default to
	// hydrating only the root repo. (Allowlist mode.)
	if len(cfg.GitHydration) == 0 && len(cfg.GitHydrationRoots) == 0 {
		return clean == "."
	}
	if len(cfg.GitHydration) > 0 {
		for _, rule := range cfg.GitHydration {
			r := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(rule.Root))))
			if r == "" {
				continue
			}
			if clean == r || strings.HasPrefix(clean, r+"/") {
				return true
			}
		}
		return false
	}
	if len(cfg.GitHydrationRoots) == 0 {
		// Default when [workspace] exists but hydration fields are unset:
		// preserve legacy/full behavior.
		return true
	}
	for _, root := range cfg.GitHydrationRoots {
		r := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(root))))
		if r == "" {
			continue
		}
		if r == "." {
			return true
		}
		if clean == r || strings.HasPrefix(clean, r+"/") {
			return true
		}
	}
	return false
}

// ShouldHydrateGitForPathForTesting exposes the hydration allowlist logic to
// tests in other packages.

func applyDiff(repoRoot, snapshotRoot string, staged bool) error {
	args := []string{"diff", "--binary"}
	if staged {
		args = append(args, "--cached")
	}
	patch, stderr, err := execGit(repoRoot, nil, args...)
	if err != nil {
		return fmt.Errorf("git diff failed: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	if len(bytes.TrimSpace(patch)) == 0 {
		return nil
	}

	cmd := exec.Command("git", "apply")
	if staged {
		cmd.Args = append(cmd.Args, "--index")
	}
	cmd.Dir = snapshotRoot
	cmd.Stdin = bytes.NewReader(patch)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply failed: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return nil
}

func hydrateRootGitMetadata(repoRoot, snapshotRoot string, cfg *llm.WorkspaceConfig, verbose bool) error {
	if !shouldHydrateGitForPath(".", cfg) {
		return nil
	}
	snapshotGitDir, err := resolveGitDir(snapshotRoot)
	if err != nil {
		return err
	}
	if snapshotGitDir == "" {
		return nil
	}
	if pathWithinRoot(snapshotRoot, snapshotGitDir) {
		return nil
	}
	commonGitDir, err := git.CommonDir(repoRoot)
	if err != nil {
		return err
	}
	hydratedDir := filepath.Join(snapshotRoot, ".git.hydrated")
	if verbose {
		fmt.Fprintf(os.Stderr, "[workspace] root-git hydrate=%s\n", hydratedDir)
	}
	if err := os.RemoveAll(hydratedDir); err != nil {
		return err
	}
	if err := copyDir(snapshotGitDir, hydratedDir); err != nil {
		return fmt.Errorf("copy snapshot gitdir %s: %w", snapshotGitDir, err)
	}
	commonDst := filepath.Join(hydratedDir, "common")
	if err := os.RemoveAll(commonDst); err != nil {
		return err
	}
	if err := copyDir(commonGitDir, commonDst); err != nil {
		return fmt.Errorf("copy common gitdir %s: %w", commonGitDir, err)
	}
	if err := rewriteFileWithMode(filepath.Join(hydratedDir, "commondir"), "common"); err != nil {
		return fmt.Errorf("rewrite commondir: %w", err)
	}
	if err := rewriteFileIfExists(filepath.Join(hydratedDir, "gitdir"), filepath.Join(snapshotRoot, ".git")); err != nil {
		return fmt.Errorf("rewrite gitdir: %w", err)
	}
	if err := rewriteFileWithMode(filepath.Join(snapshotRoot, ".git"), "gitdir: .git.hydrated"); err != nil {
		return fmt.Errorf("rewrite gitfile: %w", err)
	}
	return nil
}

func mirrorNestedGitWorktrees(repoRoot, snapshotRoot string, cfg *llm.WorkspaceConfig, ignoreCfg *llm.IgnoreConfig, verbose bool) error {
	// Copy nested git repos (non-submodule) so tools can inspect fixtures that
	// are themselves git repositories.
	//
	// We do this best-effort and exclude any `.git` directories from the copied
	// working tree to avoid pulling git internals into the snapshot.
	// Always copy nested repo working trees (excluding `.git`) so files are
	// accessible even when git metadata hydration is disabled.
	if err := mirrorNestedRepos(repoRoot, snapshotRoot, ignoreCfg, verbose); err != nil {
		return err
	}
	// Optionally hydrate nested repo git metadata for allowlisted paths.
	return hydrateNestedReposGitMetadata(repoRoot, snapshotRoot, cfg, ignoreCfg, verbose)
}

func hydrateNestedReposGitMetadata(repoRoot, snapshotRoot string, cfg *llm.WorkspaceConfig, ignoreCfg *llm.IgnoreConfig, verbose bool) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.GitHydration) == 0 && len(cfg.GitHydrationRoots) == 0 {
		return nil
	}

	return filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.Clean(rel)
		if rel == "." {
			return nil
		}
		if rel == ".machtiani" || strings.HasPrefix(rel, ".machtiani"+string(os.PathSeparator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(os.PathSeparator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if ignoredByConfig(rel, ignoreCfg) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if filepath.Base(rel) != ".git" {
			return nil
		}

		nestedRel := filepath.Dir(rel)
		if nestedRel == "." || nestedRel == "" {
			return nil
		}
		allowed := shouldHydrateGitForPath(nestedRel, cfg)
		if verbose {
			fmt.Fprintf(os.Stderr, "[workspace] nested-git path=%s hydrate_git=%v\n", filepath.ToSlash(nestedRel), allowed)
		}
		if !allowed {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		srcWT := filepath.Join(repoRoot, nestedRel)
		dstWT := filepath.Join(snapshotRoot, nestedRel)
		if err := hydrateOneNestedRepoGitMetadata(srcWT, dstWT); err != nil {
			return err
		}
		return filepath.SkipDir
	})
}

func hydrateOneNestedRepoGitMetadata(srcWT, dstWT string) error {
	// For nested repos, we may not be able to run git commands. Hydrate the git
	// metadata directly from the filesystem:
	// - If `.git` is a directory: copy it into the snapshot.
	// - If `.git` is a gitfile: copy the referenced gitdir into the snapshot and
	//   rewrite the snapshot gitfile to point at the copied gitdir.
	srcDotGit := filepath.Join(srcWT, ".git")
	info, err := os.Lstat(srcDotGit)
	if err != nil {
		return nil
	}

	if info.IsDir() {
		dstGit := filepath.Join(dstWT, ".git")
		if err := os.RemoveAll(dstGit); err != nil {
			return err
		}
		if err := copyDir(srcDotGit, dstGit); err != nil {
			return fmt.Errorf("copy nested repo gitdir %s: %w", srcWT, err)
		}
		return nil
	}

	if !info.Mode().IsRegular() {
		return nil
	}

	gitdir, ok, err := readGitfile(srcDotGit)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(srcWT, gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	if info, err := os.Stat(gitdir); err != nil || !info.IsDir() {
		return nil
	}

	// Copy the referenced gitdir under the snapshot worktree, then rewrite the
	// snapshot gitfile to point at that copied directory.
	dstGitdir := filepath.Join(dstWT, ".git.hydrated")
	if err := os.RemoveAll(dstGitdir); err != nil {
		return err
	}
	if err := copyDir(gitdir, dstGitdir); err != nil {
		return fmt.Errorf("copy nested repo referenced gitdir %s: %w", srcWT, err)
	}
	gitdirRel, err := filepath.Rel(dstWT, dstGitdir)
	if err != nil {
		return err
	}
	gitdirRel = filepath.ToSlash(gitdirRel)
	if err := os.WriteFile(filepath.Join(dstWT, ".git"), []byte("gitdir: "+gitdirRel+"\n"), 0o644); err != nil {
		return err
	}
	return nil
}

func readGitfile(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", false, nil
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if gitdir == "" {
		return "", false, fmt.Errorf("invalid gitfile %s: empty gitdir", path)
	}
	return gitdir, true, nil
}

func mirrorSubmoduleWorktrees(repoRoot, snapshotRoot string) error {
	modulesPath := filepath.Join(repoRoot, ".gitmodules")
	if _, err := os.Stat(modulesPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	stdout, _, err := execGit(repoRoot, nil, "config", "--file", ".gitmodules", "--get-regexp", "^submodule\\..*\\.path$")
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		rel := strings.TrimSpace(strings.Join(fields[1:], " "))
		rel = filepath.FromSlash(rel)
		if rel == "" {
			continue
		}
		src := filepath.Join(repoRoot, rel)
		dst := filepath.Join(snapshotRoot, rel)
		info, err := os.Stat(src)
		if err != nil || !info.IsDir() {
			continue
		}
		_ = os.RemoveAll(dst)
		if err := copyDirExcludingGit(src, dst); err != nil {
			return fmt.Errorf("copy submodule %s: %w", rel, err)
		}
	}
	return nil
}

func hydrateSubmodules(repoRoot, snapshotRoot string, cfg *llm.WorkspaceConfig, ignoreCfg *llm.IgnoreConfig, verbose bool) error {
	// Offline-only: seed snapshot .git/modules from host and rewrite submodule
	// gitfiles to point at the snapshot-local modules.
	//
	// This intentionally avoids running `git submodule update`, which may attempt
	// network fetches when objects are missing.
	modules := filepath.Join(repoRoot, ".gitmodules")
	if _, err := os.Stat(modules); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	stdout, _, err := execGit(repoRoot, nil, "config", "--file", ".gitmodules", "--get-regexp", "^submodule\\..*\\.path$")
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	snapshotGitDir, err := resolveGitDir(snapshotRoot)
	if err != nil {
		return err
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		rel := filepath.FromSlash(strings.TrimSpace(strings.Join(fields[1:], " ")))
		if rel == "" {
			continue
		}

		if ignoredByConfig(rel, ignoreCfg) {
			continue
		}

		// Always copy the submodule working tree into the snapshot so regular file
		// access works even when git metadata hydration is disabled.
		srcWT := filepath.Join(repoRoot, filepath.FromSlash(rel))
		dstWT := filepath.Join(snapshotRoot, filepath.FromSlash(rel))
		if info, err := os.Stat(srcWT); err == nil && info.IsDir() {
			_ = os.RemoveAll(dstWT)
			if err := copyDir(srcWT, dstWT); err != nil {
				return fmt.Errorf("copy submodule worktree %s: %w", rel, err)
			}
		}

		allowed := shouldHydrateGitForPath(rel, cfg)
		if verbose {
			fmt.Fprintf(os.Stderr, "[workspace] submodule path=%s hydrate_git=%v\n", filepath.ToSlash(rel), allowed)
		}
		if !allowed {
			continue
		}
		if err := seedOneSubmodule(repoRoot, snapshotRoot, snapshotGitDir, rel, cfg, ignoreCfg, rel, verbose); err != nil {
			return err
		}
	}
	return nil
}

func seedOneSubmodule(repoRoot, snapshotRoot, snapshotGitDir, subPath string, cfg *llm.WorkspaceConfig, ignoreCfg *llm.IgnoreConfig, allowPrefix string, verbose bool) error {
	// Copy host module gitdir into snapshot module gitdir.
	hostModule := filepath.Join(repoRoot, ".git", "modules", filepath.FromSlash(subPath))
	snapModule := filepath.Join(snapshotGitDir, "modules", filepath.FromSlash(subPath))

	// Safety check: if the workspace is inside the repo and not a valid worktree,
	// snapshotGitDir might resolve to the host's .git dir.
	if filepath.Clean(hostModule) == filepath.Clean(snapModule) {
		return fmt.Errorf("refusing to overwrite host submodule gitdir: %s", hostModule)
	}

	info, err := os.Stat(hostModule)
	if err != nil || !info.IsDir() {
		if verbose {
			fmt.Fprintf(os.Stderr, "[workspace] submodule path=%s host_gitdir_missing\n", filepath.ToSlash(subPath))
		}
		return nil
	}

	_ = os.RemoveAll(snapModule)
	if err := copyDir(hostModule, snapModule); err != nil {
		return fmt.Errorf("copy submodule gitdir %s: %w", subPath, err)
	}

	srcWT := filepath.Join(repoRoot, filepath.FromSlash(subPath))
	dstWT := filepath.Join(snapshotRoot, filepath.FromSlash(subPath))
	if err := os.MkdirAll(dstWT, 0o755); err != nil {
		return err
	}

	// Ensure the snapshot submodule has a gitfile pointing at snapshot-local modules.
	gitfile := filepath.Join(dstWT, ".git")
	if err := os.MkdirAll(filepath.Dir(gitfile), 0o755); err != nil {
		return err
	}
	gitdirRel, err := filepath.Rel(dstWT, snapModule)
	if err != nil {
		return err
	}
	gitdirRel = filepath.ToSlash(gitdirRel)
	content := []byte("gitdir: " + gitdirRel + "\n")
	if err := os.WriteFile(gitfile, content, 0o644); err != nil {
		return err
	}
	if err := rewriteSubmoduleWorktreeConfig(snapModule, dstWT); err != nil {
		return err
	}

	// Ensure the seeded submodule repo can resolve HEAD without fetching.
	if _, _, err := execGit(dstWT, nil, "rev-parse", "--verify", "HEAD"); err != nil {
		// Best-effort only: allow sessions to proceed without full submodule object
		// availability (e.g., in offline environments). The working tree content is
		// still copied, but git history within the submodule may be incomplete.
		fmt.Fprintf(os.Stderr, "Warning: submodule %s missing git objects locally; initialize on host first\n", subPath)
	}

	// Recurse if the submodule itself has nested submodules.
	if _, err := os.Stat(filepath.Join(srcWT, ".gitmodules")); err == nil {
		stdout, _, err := execGit(srcWT, nil, "config", "--file", ".gitmodules", "--get-regexp", "^submodule\\..*\\.path$")
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
			for _, line := range lines {
				fields := strings.Fields(line)
				if len(fields) < 2 {
					continue
				}
				nested := filepath.FromSlash(strings.TrimSpace(strings.Join(fields[1:], " ")))
				if nested == "" {
					continue
				}
				nestedPath := filepath.ToSlash(filepath.Clean(filepath.Join(subPath, nested)))
				// When using allowlisted hydration rules, don't recursively hydrate
				// nested submodules unless they are also allowlisted.
				if cfg != nil && len(cfg.GitHydration) > 0 {
					allowed := shouldHydrateGitForPath(nestedPath, cfg)
					if verbose {
						fmt.Fprintf(os.Stderr, "[workspace] nested-submodule path=%s allowed=%v\n", filepath.ToSlash(nestedPath), allowed)
					}
					if !allowed {
						continue
					}
				}
				if err := seedOneSubmodule(repoRoot, snapshotRoot, snapshotGitDir, nestedPath, cfg, ignoreCfg, allowPrefix, verbose); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func rewriteSubmoduleWorktreeConfig(gitDir, worktree string) error {
	configPath := filepath.Join(gitDir, "config")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read submodule config: %w", err)
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
	mode := os.FileMode(0o600)
	if info, err := os.Stat(configPath); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(configPath, []byte(content), mode); err != nil {
		return fmt.Errorf("write submodule config: %w", err)
	}
	return nil
}

func resolveGitDir(repoRoot string) (string, error) {
	stdout, stderr, err := execGit(repoRoot, nil, "rev-parse", "--git-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git dir: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	gitDir := strings.TrimSpace(string(stdout))
	if gitDir == "" {
		return "", fmt.Errorf("resolve git dir: empty")
	}
	if filepath.IsAbs(gitDir) {
		return filepath.Clean(gitDir), nil
	}
	return filepath.Clean(filepath.Join(repoRoot, gitDir)), nil
}

func rewriteFileWithMode(path, content string) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	trimmed := strings.TrimRight(content, "\n") + "\n"
	if err := os.WriteFile(path, []byte(trimmed), mode); err != nil {
		return err
	}
	return nil
}

func rewriteFileIfExists(path, content string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return rewriteFileWithMode(path, content)
}

func pathWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
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

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		rel = filepath.Clean(rel)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode&os.ModeSymlink != 0:
			_ = os.RemoveAll(target)
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		case mode.IsDir():
			return os.MkdirAll(target, mode.Perm())
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
			if err != nil {
				return err
			}
			defer out.Close()
			if _, err := io.Copy(out, in); err != nil {
				return err
			}
			return nil
		}
	})
}

func mirrorNestedRepos(repoRoot, snapshotRoot string, ignoreCfg *llm.IgnoreConfig, verbose bool) error {
	// Find nested git repos by looking for `.git` directories/files under the host
	// checkout. Skip `.git` at the repo root and any `.git` under `.git/modules`.
	return filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.Clean(rel)
		if rel == "." {
			return nil
		}
		if rel == ".machtiani" || strings.HasPrefix(rel, ".machtiani"+string(os.PathSeparator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(os.PathSeparator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if ignoredByConfig(rel, ignoreCfg) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		base := filepath.Base(rel)
		if base != ".git" {
			return nil
		}
		nestedRel := filepath.Dir(rel)
		if nestedRel == "." || nestedRel == "" {
			return nil
		}
		src := filepath.Join(repoRoot, nestedRel)
		dst := filepath.Join(snapshotRoot, nestedRel)
		if _, err := os.Stat(dst); err == nil {
			// Already present (submodule copy or tracked checkout).
			return filepath.SkipDir
		}
		if err := copyDirExcludingGit(src, dst); err != nil {
			return fmt.Errorf("copy nested repo %s: %w", nestedRel, err)
		}
		return filepath.SkipDir
	})
}

func copyDirExcludingGit(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		rel = filepath.Clean(rel)
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(os.PathSeparator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode&os.ModeSymlink != 0:
			_ = os.RemoveAll(target)
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		case mode.IsDir():
			return os.MkdirAll(target, mode.Perm())
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
			if err != nil {
				return err
			}
			defer out.Close()
			if _, err := io.Copy(out, in); err != nil {
				return err
			}
			return nil
		}
	})
}

func execGit(dir string, stdin []byte, args ...string) ([]byte, []byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}
