package runner

import (
    "bytes"
    "context"
    "errors"
    "fmt"
    "os"
    "os/exec"
    "strings"
)

type PatcherRunner struct {
    Verbose   bool
    DryRun    bool
    SessionID string
    exePath   string
}

// Resolve selects the patcher binary path with precedence: flag > PATCHER_BIN env > PATH lookup.
func (p *PatcherRunner) Resolve(binFlag string) error {
    if p.DryRun {
        // In dry-run, don't validate; pick the most reasonable name so logs look correct.
        if strings.TrimSpace(binFlag) != "" {
            p.exePath = binFlag
            return nil
        }
        if v := strings.TrimSpace(os.Getenv("PATCHER_BIN")); v != "" {
            p.exePath = v
            return nil
        }
        p.exePath = "patcher"
        return nil
    }
    tryLook := func(name string) (string, bool) {
        if strings.TrimSpace(name) == "" { return "", false }
        // If absolute or relative path provided, honor it when file exists; otherwise try PATH.
        if _, err := os.Stat(name); err == nil {
            return name, true
        }
        if pth, err := exec.LookPath(name); err == nil {
            return pth, true
        }
        return "", false
    }
    if ep, ok := tryLook(binFlag); ok { p.exePath = ep; return nil }
    if ep, ok := tryLook(os.Getenv("PATCHER_BIN")); ok { p.exePath = ep; return nil }
    // Common local location when building from repo root: ./patcher/patcher
    if ep, ok := tryLook("./patcher/patcher"); ok { p.exePath = ep; return nil }
    if ep, ok := tryLook("patcher/patcher"); ok { p.exePath = ep; return nil }
    // Fall back to PATH lookup
    if ep, ok := tryLook("patcher"); ok { p.exePath = ep; return nil }
    return errors.New("patcher binary not found: set --patcher-bin, PATCHER_BIN, or add 'patcher' to PATH")
}

// RunJSON executes: patcher --repo . --session <sessionID> --input - [--verbose]
// It returns captured stdout and stderr. In DryRun, returns "{}" stdout and empty stderr.
func (p *PatcherRunner) RunJSON(ctx context.Context, stdinBytes []byte, verbose bool) ([]byte, []byte, error) {
    if p.exePath == "" {
        return nil, nil, errors.New("patcher unresolved: call Resolve() first")
    }
    if p.DryRun {
        if p.Verbose || verbose {
            fmt.Fprintln(os.Stderr, "[patcher]", p.exePath, "--repo . --session", p.SessionID, "--input -")
        }
        return []byte("{}"), []byte(""), nil
    }
    args := []string{"--repo", ".", "--session", p.SessionID, "--input", "-"}
    if p.Verbose || verbose {
        args = append(args, "--verbose")
    }
    cmd := exec.CommandContext(ctx, p.exePath, args...)
    var outBuf, errBuf bytes.Buffer
    cmd.Stdout = &outBuf
    cmd.Stderr = &errBuf
    cmd.Stdin = bytes.NewReader(stdinBytes)
    if p.Verbose || verbose {
        fmt.Fprintln(os.Stderr, "[patcher]", p.exePath, strings.Join(args, " "))
    }
    err := cmd.Run()
    return outBuf.Bytes(), errBuf.Bytes(), err
}
