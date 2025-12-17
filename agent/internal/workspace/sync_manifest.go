package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

type syncManifest struct {
	Version       int                       `json:"version"`
	Baseline      map[string]manifestEntry  `json:"baseline"`
	CreatedPaths  map[string]struct{}       `json:"created_paths"`
	IgnoreConfig  *llm.IgnoreConfig         `json:"ignore_config,omitempty"`
}

type manifestEntry struct {
	SHA256 string      `json:"sha256"`
	Mode   fs.FileMode `json:"mode"`
	Type   string      `json:"type"` // file|symlink|missing
	Link   string      `json:"link,omitempty"`
}

func loadOrInitManifest(manifestPath string) (*syncManifest, error) {
	if strings.TrimSpace(manifestPath) == "" {
		return nil, errors.New("manifest path is required")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &syncManifest{Version: 1, Baseline: map[string]manifestEntry{}, CreatedPaths: map[string]struct{}{}}, nil
		}
		return nil, err
	}
	var m syncManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.Version == 0 {
		m.Version = 1
	}
	if m.Baseline == nil {
		m.Baseline = map[string]manifestEntry{}
	}
	if m.CreatedPaths == nil {
		m.CreatedPaths = map[string]struct{}{}
	}
	return &m, nil
}

func saveManifest(manifestPath string, m *syncManifest) error {
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, data, 0o644)
}

func relPathList(set map[string]struct{}) []string {
	paths := make([]string, 0, len(set))
	for p := range set {
		p = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(p))))
		if p == "." || p == "" {
			continue
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func computeEntry(root, rel string) (manifestEntry, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return manifestEntry{Type: "missing"}, nil
		}
		return manifestEntry{}, err
	}
	mode := info.Mode()
	if mode&os.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		if err != nil {
			return manifestEntry{}, err
		}
		h := sha256.Sum256([]byte(target))
		return manifestEntry{Type: "symlink", Mode: mode, Link: target, SHA256: hex.EncodeToString(h[:])}, nil
	}
	if !mode.IsRegular() {
		// Treat non-regular files as missing for sync purposes.
		return manifestEntry{Type: "missing"}, nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return manifestEntry{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return manifestEntry{}, err
	}
	return manifestEntry{Type: "file", Mode: mode, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func copyPath(srcRoot, dstRoot, rel string) error {
	src := filepath.Join(srcRoot, filepath.FromSlash(rel))
	dst := filepath.Join(dstRoot, filepath.FromSlash(rel))
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	mode := info.Mode()
	if mode&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		_ = os.RemoveAll(dst)
		return os.Symlink(target, dst)
	}
	if !mode.IsRegular() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

func hardExcluded(rel string) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if clean == "." || clean == "" {
		return true
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") || strings.Contains(clean, "/.git/") || strings.HasSuffix(clean, "/.git") {
		return true
	}
	if clean == ".shell-agent" || strings.HasPrefix(clean, ".shell-agent/") || strings.Contains(clean, "/.shell-agent/") || strings.HasSuffix(clean, "/.shell-agent") {
		return true
	}
	if clean == ".machtiani" || strings.HasPrefix(clean, ".machtiani/") || strings.Contains(clean, "/.machtiani/") || strings.HasSuffix(clean, "/.machtiani") {
		return true
	}
	if clean == ".cache" || strings.HasPrefix(clean, ".cache/") {
		return true
	}
	if clean == "agent/.tmp" || strings.HasPrefix(clean, "agent/.tmp/") {
		return true
	}
	return false
}

func ignoredByConfig(rel string, cfg *llm.IgnoreConfig) bool {
	if cfg == nil {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	for _, p := range cfg.Paths {
		p = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(p))))
		if p == "" || p == "." {
			continue
		}
		if clean == p || strings.HasPrefix(clean, p+"/") {
			return true
		}
	}
	if ext := strings.TrimPrefix(filepath.Ext(clean), "."); ext != "" {
		for _, e := range cfg.Extensions {
			e = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(e)), ".")
			if e == "" {
				continue
			}
			if strings.EqualFold(ext, e) {
				return true
			}
		}
	}
	return false
}

func listHostTracked(repoRoot string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	items := strings.Split(string(output), "\x00")
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, filepath.ToSlash(filepath.Clean(filepath.FromSlash(item))))
	}
	return out, nil
}

func listSnapshotFiles(snapshotRoot string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(snapshotRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(snapshotRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if rel == "." {
			return nil
		}
		if hardExcluded(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func syncCandidateSet(repoRoot string, created map[string]struct{}, ignoreCfg *llm.IgnoreConfig) (map[string]struct{}, error) {
	tracked, err := listHostTracked(repoRoot)
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{}
	for _, p := range tracked {
		if hardExcluded(p) || ignoredByConfig(p, ignoreCfg) {
			continue
		}
		set[p] = struct{}{}
	}
	for p := range created {
		if hardExcluded(p) || ignoredByConfig(p, ignoreCfg) {
			continue
		}
		set[p] = struct{}{}
	}
	return set, nil
}
