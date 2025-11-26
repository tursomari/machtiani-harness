package patcher

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/git"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

const (
	baselineDirName  = "baseline"
	baselineFilesDir = "files"
	baselineManifest = "manifest.json"
)

// BaselineManifest describes the captured workspace snapshot recorded at session start.
type BaselineManifest struct {
	CapturedAt time.Time            `json:"captured_at"`
	Files      []BaselineFileRecord `json:"files"`
}

// BaselineFileRecord captures metadata for a file stored in the baseline mirror.
type BaselineFileRecord struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256,omitempty"`
	Size          int    `json:"size,omitempty"`
	SymlinkTarget string `json:"symlink_target,omitempty"`
}

// BaselineState points to the on-disk baseline mirror and exposes manifest lookups.
type BaselineState struct {
	Root         string
	FilesDir     string
	ManifestPath string
	Manifest     BaselineManifest

	manifestIndex map[string]BaselineFileRecord
}

// EnsureBaseline loads the session baseline if it exists or captures a new snapshot from workspaceRoot.
// When sessionID is empty, it returns nil without error.
func EnsureBaseline(sessionID, workspaceRoot string, now time.Time) (*BaselineState, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, errors.New("workspace root required for baseline capture")
	}

	state, err := LoadBaseline(sessionID)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return captureBaseline(sessionID, workspaceRoot, now)
}

// LoadBaseline loads the persisted baseline metadata for sessionID.
// It returns os.ErrNotExist if the manifest is missing.
func LoadBaseline(sessionID string) (*BaselineState, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, os.ErrNotExist
	}
	root, filesDir, manifestPath, err := sessionBaselinePaths(sessionID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("read baseline manifest: %w", err)
	}
	var manifest BaselineManifest
	if err := json.Unmarshal(bytes.TrimSpace(data), &manifest); err != nil {
		return nil, fmt.Errorf("parse baseline manifest: %w", err)
	}
	if len(manifest.Files) == 0 {
		manifest.Files = []BaselineFileRecord{}
	}
	state := &BaselineState{
		Root:          root,
		FilesDir:      filesDir,
		ManifestPath:  manifestPath,
		Manifest:      manifest,
		manifestIndex: make(map[string]BaselineFileRecord, len(manifest.Files)),
	}
	for _, rec := range manifest.Files {
		state.manifestIndex[filepath.ToSlash(rec.Path)] = rec
	}
	return state, nil
}

// ManifestEntry returns the baseline metadata for relPath if present.
func (b *BaselineState) ManifestEntry(relPath string) (BaselineFileRecord, bool) {
	if b == nil {
		return BaselineFileRecord{}, false
	}
	rel := filepath.ToSlash(strings.TrimSpace(relPath))
	rec, ok := b.manifestIndex[rel]
	return rec, ok
}

// FilePath returns the on-disk baseline copy for relPath.
func (b *BaselineState) FilePath(relPath string) string {
	if b == nil {
		return ""
	}
	rel := filepath.FromSlash(strings.TrimSpace(relPath))
	if rel == "" {
		return ""
	}
	return filepath.Join(b.FilesDir, rel)
}

// VerifyFiles recomputes SHA256 digests for the requested baseline files and compares them to the manifest.
// Missing entries are ignored. Use to detect baseline drift or corruption.
func (b *BaselineState) VerifyFiles(relPaths []string) error {
	if b == nil {
		return nil
	}
	checked := make(map[string]struct{})
	for _, rel := range relPaths {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel == "" {
			continue
		}
		if _, ok := checked[rel]; ok {
			continue
		}
		rec, ok := b.manifestIndex[rel]
		if !ok {
			continue
		}
		path := b.FilePath(rel)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("stat baseline file %s: %w", rel, err)
		}
		var digest string
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read baseline symlink %s: %w", rel, err)
			}
			sum := sha256.Sum256([]byte(target))
			digest = hex.EncodeToString(sum[:])
		case info.Mode().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("open baseline file %s: %w", rel, err)
			}
			hasher := sha256.New()
			if _, err := io.Copy(hasher, f); err != nil {
				_ = f.Close()
				return fmt.Errorf("hash baseline file %s: %w", rel, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close baseline file %s: %w", rel, err)
			}
			digest = hex.EncodeToString(hasher.Sum(nil))
		default:
			// Skip special files
			checked[rel] = struct{}{}
			continue
		}
		if digest != strings.TrimSpace(rec.SHA256) {
			return fmt.Errorf("baseline checksum mismatch for %s", rel)
		}
		checked[rel] = struct{}{}
	}
	return nil
}

func captureBaseline(sessionID, workspaceRoot string, now time.Time) (*BaselineState, error) {
	root, filesDir, manifestPath, err := sessionBaselinePaths(sessionID)
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(root); err != nil {
		return nil, fmt.Errorf("reset baseline directory: %w", err)
	}
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create baseline files dir: %w", err)
	}

	paths, err := collectWorkspacePaths(workspaceRoot)
	if err != nil {
		return nil, err
	}

	manifest := BaselineManifest{CapturedAt: now.UTC(), Files: make([]BaselineFileRecord, 0, len(paths))}
	for _, rel := range paths {
		rec, copyErr := copyBaselineEntry(workspaceRoot, filesDir, rel)
		if copyErr != nil {
			return nil, copyErr
		}
		if rec != nil {
			manifest.Files = append(manifest.Files, *rec)
		}
	}
	sort.Slice(manifest.Files, func(i, j int) bool {
		return manifest.Files[i].Path < manifest.Files[j].Path
	})

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal baseline manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("write baseline manifest: %w", err)
	}

	state := &BaselineState{
		Root:          root,
		FilesDir:      filesDir,
		ManifestPath:  manifestPath,
		Manifest:      manifest,
		manifestIndex: make(map[string]BaselineFileRecord, len(manifest.Files)),
	}
	for _, rec := range manifest.Files {
		state.manifestIndex[filepath.ToSlash(rec.Path)] = rec
	}
	return state, nil
}

func sessionBaselinePaths(sessionID string) (root, filesDir, manifestPath string, err error) {
	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve session directory: %w", err)
	}
	root = filepath.Join(dir, baselineDirName)
	filesDir = filepath.Join(root, baselineFilesDir)
	manifestPath = filepath.Join(root, baselineManifest)
	return root, filesDir, manifestPath, nil
}

func copyBaselineEntry(workspaceRoot, destRoot, rel string) (*BaselineFileRecord, error) {
	abs := filepath.Join(workspaceRoot, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat baseline source %s: %w", rel, err)
	}
	target := filepath.Join(destRoot, filepath.FromSlash(rel))
	cleanDest := filepath.Clean(destRoot)
	if !strings.HasPrefix(filepath.Clean(target), cleanDest+string(filepath.Separator)) && filepath.Clean(target) != cleanDest {
		return nil, fmt.Errorf("baseline path escapes destination: %s", rel)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, fmt.Errorf("ensure baseline directory for %s: %w", rel, err)
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		linkTarget, err := os.Readlink(abs)
		if err != nil {
			return nil, fmt.Errorf("read symlink %s: %w", rel, err)
		}
		if err := os.Symlink(linkTarget, target); err != nil {
			return nil, fmt.Errorf("copy symlink %s: %w", rel, err)
		}
		sum := sha256.Sum256([]byte(linkTarget))
		return &BaselineFileRecord{
			Path:          rel,
			SHA256:        hex.EncodeToString(sum[:]),
			Size:          len(linkTarget),
			SymlinkTarget: linkTarget,
		}, nil
	case info.Mode().IsRegular():
		src, err := os.Open(abs)
		if err != nil {
			return nil, fmt.Errorf("open baseline source %s: %w", rel, err)
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			_ = src.Close()
			return nil, fmt.Errorf("create baseline file %s: %w", rel, err)
		}
		hasher := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(dst, hasher), src)
		srcCloseErr := src.Close()
		dstCloseErr := dst.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("copy baseline file %s: %w", rel, copyErr)
		}
		if srcCloseErr != nil {
			return nil, fmt.Errorf("close baseline source %s: %w", rel, srcCloseErr)
		}
		if dstCloseErr != nil {
			return nil, fmt.Errorf("close baseline file %s: %w", rel, dstCloseErr)
		}
		sum := hasher.Sum(nil)
		return &BaselineFileRecord{
			Path:   rel,
			SHA256: hex.EncodeToString(sum),
			Size:   int(written),
		}, nil
	default:
		return nil, nil
	}
}

func collectWorkspacePaths(root string) ([]string, error) {
	tracked, err := git.ListTrackedFiles(root)
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	untracked, err := listUntrackedFiles(root)
	if err != nil {
		return nil, fmt.Errorf("list untracked files: %w", err)
	}

	seen := make(map[string]struct{}, len(tracked)+len(untracked))
	combined := make([]string, 0, len(tracked)+len(untracked))
	appendPath := func(rel string) {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel == "" || rel == "." {
			return
		}
		if strings.Contains(rel, "..") {
			return
		}
		if shouldSkipBaselinePath(rel) {
			return
		}
		if _, ok := seen[rel]; ok {
			return
		}
		seen[rel] = struct{}{}
		combined = append(combined, rel)
	}
	for _, rel := range tracked {
		appendPath(rel)
	}
	for _, rel := range untracked {
		appendPath(rel)
	}
	sort.Strings(combined)
	return combined, nil
}

func listUntrackedFiles(dir string) ([]string, error) {
	cmd := exec.Command("git", "-C", dir, "ls-files", "--others", "--exclude-standard", "-z")
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-files --others failed: %w, %s", err, strings.TrimSpace(stderr.String()))
	}
	if out.Len() == 0 {
		return nil, nil
	}
	parts := bytes.Split(out.Bytes(), []byte{0})
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		files = append(files, string(part))
	}
	return files, nil
}

func shouldSkipBaselinePath(rel string) bool {
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, ".git/") || rel == ".git" {
		return true
	}
	if strings.HasPrefix(rel, ".machtiani/") || rel == ".machtiani" {
		return true
	}
	return false
}
