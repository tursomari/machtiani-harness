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
func MakeTempMirror(repo string, after map[string][]byte) (string, func(), error) {
    dir, err := os.MkdirTemp("", "patcher-mirror-*")
    if err != nil {
        return "", func(){}, err
    }
    cleanup := func() { _ = os.RemoveAll(dir) }
    for rel, content := range after {
        if content == nil {
            // Deletion: do not write file; absence signals delete in diff.
            continue
        }
        abs := filepath.Join(dir, filepath.FromSlash(rel))
        if !strings.HasPrefix(abs, dir+string(filepath.Separator)) && abs != dir {
            cleanup()
            return "", func(){}, fmt.Errorf("mirror path escapes temp dir: %s", rel)
        }
        if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
            cleanup()
            return "", func(){}, err
        }
        if err := os.WriteFile(abs, content, 0o644); err != nil {
            cleanup()
            return "", func(){}, err
        }
    }
    return dir, cleanup, nil
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

