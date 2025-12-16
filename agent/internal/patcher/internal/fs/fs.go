package fs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/tempdir"
)

// IsRepoRoot checks presence of a .git directory in the given path.
func IsRepoRoot(path string) (bool, error) {
	gitPath := filepath.Join(path, ".git")
	st, err := os.Stat(gitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if st.IsDir() {
		return true, nil
	}
	// Worktrees use a gitfile `.git` that points at the real gitdir.
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return false, err
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return false, nil
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if gitdir == "" {
		return false, nil
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(path, filepath.FromSlash(gitdir))
	}
	info, err := os.Stat(gitdir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir(), nil
}

// MakeTempMirror materializes the provided after-state content either into a
// fresh temporary directory or into the provided existing directory. Supplying
// an empty reuseDir preserves the original behaviour of allocating a new temp
// directory and returning a cleanup function that removes it. When reuseDir is
// non-empty, the caller is responsible for lifecycle management and cleanup is
// a no-op.
func MakeTempMirror(after map[string][]byte, reuseDir string) (string, func(), error) {
	if strings.TrimSpace(reuseDir) != "" {
		if err := os.RemoveAll(reuseDir); err != nil {
			return "", func() {}, err
		}
		if err := os.MkdirAll(reuseDir, 0o755); err != nil {
			return "", func() {}, err
		}
		if err := WriteMirror(reuseDir, after); err != nil {
			return "", func() {}, err
		}
		return reuseDir, func() {}, nil
	}

	dir, err := tempdir.MkdirTemp("patcher-mirror-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := WriteMirror(dir, after); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return dir, cleanup, nil
}

// WriteMirror materializes the provided after-state map into the target
// directory, ensuring all paths remain within the mirror root. Nil entries signal
// deletions and are therefore skipped.
func WriteMirror(root string, after map[string][]byte) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	for rel, content := range after {
		if content == nil {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if !strings.HasPrefix(abs, root+string(filepath.Separator)) && abs != root {
			return fmt.Errorf("mirror path escapes target dir: %s", rel)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// PatchFilename generates a timestamped filename with a short random suffix.
func PatchFilename(t time.Time) string {
	ts := t.UTC().Format("2006-01-02T15-04-05Z")
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback to timestamp only on failure.
		return fmt.Sprintf("%s.patch", ts)
	}
	return fmt.Sprintf("%s-%s.patch", ts, hex.EncodeToString(b[:]))
}

// MakeSessionWorkspace creates a temporary directory containing a full copy of
// the source repository, including the working tree and the .git directory. The
// returned cleanup function removes the workspace when invoked.
func MakeSessionWorkspace(src string) (string, func(), error) {
	ws, err := tempdir.MkdirTemp("patcher-workspace-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(ws) }
	if err := copyRepoWorkspace(src, ws); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := ensureGitRepo(ws); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return ws, cleanup, nil
}

func ensureGitRepo(dir string) error {
	ok, err := IsRepoRoot(dir)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git init failed: %v\n%s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

func copyRepoWorkspace(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	files, err := listTrackedFiles(src)
	if err != nil {
		return err
	}
	for _, rel := range files {
		rel = filepath.Clean(rel)
		if rel == "." || rel == "" {
			continue
		}
		srcPath := filepath.Join(src, rel)
		info, err := os.Lstat(srcPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		dstPath := filepath.Join(dst, rel)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(target, dstPath); err != nil {
				return err
			}
		case info.IsDir():
			if err := copyTree(srcPath, dstPath); err != nil {
				return err
			}
		default:
			if err := copyFile(srcPath, dstPath, info.Mode()); err != nil {
				return err
			}
		}
	}
	return copyGitMetadata(src, dst)
}

func copyGitMetadata(src, dst string) error {
	srcGit := filepath.Join(src, ".git")
	info, err := os.Lstat(srcGit)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dstGit := filepath.Join(dst, ".git")
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(srcGit)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dstGit), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(target, dstGit); err != nil {
			return err
		}
		return nil
	case info.IsDir():
		return copyTree(srcGit, dstGit)
	default:
		return copyFile(srcGit, dstGit, info.Mode())
	}
}

func listTrackedFiles(repo string) ([]string, error) {
	cmd := exec.Command("git", "-C", repo, "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	parts := bytes.Split(out, []byte{0})
	files := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		files = append(files, string(p))
	}
	return files, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.MkdirAll(target, info.Mode())
		default:
			return copyFile(path, target, info.Mode())
		}
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	// Ensure files are created with write permissions for subsequent edits.
	perm := mode
	if perm&0o200 == 0 {
		perm |= 0o200
	}
	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		return err
	}
	return dstFile.Close()
}
