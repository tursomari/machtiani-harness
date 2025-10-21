package fs

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// IsRepoRoot checks presence of a .git directory in the given path.
func IsRepoRoot(path string) (bool, error) {
	st, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return st.IsDir(), nil
}

// MakeTempMirror writes the after-state content into a temp directory structure
// mirroring the repo paths for changed files only. A nil content denotes deletion
// and thus is not written.
func MakeTempMirror(after map[string][]byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", "patcher-mirror-*")
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
