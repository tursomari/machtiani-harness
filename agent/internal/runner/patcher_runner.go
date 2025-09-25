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

// Resolve locates the patcher binary strictly via PATH (dry-run stores logical name only).
func (p *PatcherRunner) Resolve() error {
	if p.DryRun {
		// In dry-run, rely on logical name for logging.
		p.exePath = "patcher"
		return nil
	}
	ep, err := exec.LookPath("patcher")
	if err != nil {
		return fmt.Errorf("patcher not found in PATH: %w", err)
	}
	p.exePath = ep
	return nil
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
