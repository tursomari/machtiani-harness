package runner

import (
    "context"
    "errors"
    "fmt"
    "math/rand"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "time"
)

type Runner struct {
    MCTBin  string
    Verbose bool
    DryRun  bool
    exePath string
}

// Resolve locates the mct binary, honoring MCT_BIN env or PATH.
func (r *Runner) Resolve() error {
    if r.DryRun {
        r.exePath = firstNonEmpty(r.MCTBin, os.Getenv("MCT_BIN"))
        if r.exePath == "" {
            r.exePath = "mct"
        }
        return nil
    }
    if r.MCTBin != "" {
        r.exePath = r.MCTBin
        if _, err := os.Stat(r.exePath); err == nil {
            return nil
        }
        // try PATH even if provided path missing
    }
    if v := os.Getenv("MCT_BIN"); v != "" {
        if _, err := os.Stat(v); err == nil {
            r.exePath = v
            return nil
        }
    }
    p, err := exec.LookPath("mct")
    if err != nil {
        return fmt.Errorf("mct not found in PATH and MCT_BIN not set")
    }
    r.exePath = p
    return nil
}

// RunPrompt executes `mct prompt --mode=default [--model model] <question>`
// It returns the path mct indicated it saved to if detectable; otherwise empty string.
func (r *Runner) RunPrompt(ctx context.Context, sessionID string, args ...string) (string, error) {
    if r.exePath == "" {
        return "", errors.New("runner unresolved: call Resolve() first")
    }
    env := os.Environ()
    if sessionID != "" {
        env = append(env, "MACHTIANI_SESSION_ID="+sessionID)
    }

    // Compose command
    cmd := exec.CommandContext(ctx, r.exePath, args...)
    cmd.Env = env
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr

    if r.Verbose || r.DryRun {
        fmt.Println("[mct]", r.exePath, strings.Join(args, " "))
    }
    if r.DryRun {
        return "", nil
    }

    if err := cmd.Run(); err != nil {
        return "", err
    }

    // mct updates .machtiani/chat/machtiani-response.md; return the latest file under that dir if present
    saved := findLatestChatFile(".machtiani/chat")
    return saved, nil
}

func findLatestChatFile(dir string) string {
    entries, err := os.ReadDir(dir)
    if err != nil {
        return ""
    }
    var latest string
    var latestMod time.Time
    for _, e := range entries {
        if e.IsDir() { continue }
        name := e.Name()
        if !strings.HasSuffix(name, ".md") { continue }
        // skip our own transcript files (agent-*.md)
        if strings.HasPrefix(name, "agent-") { continue }
        p := filepath.Join(dir, name)
        fi, err := os.Stat(p)
        if err != nil { continue }
        if fi.ModTime().After(latestMod) {
            latestMod = fi.ModTime()
            latest = p
        }
    }
    return latest
}

func GenerateSessionID() string {
    // Simple timestamp+rand; good enough for correlation
    now := time.Now().UTC().Format("20060102T150405")
    return fmt.Sprintf("agent-%s-%04d", now, rand.Intn(10000))
}

func firstNonEmpty(vals ...string) string {
    for _, v := range vals {
        if strings.TrimSpace(v) != "" {
            return v
        }
    }
    return ""
}

