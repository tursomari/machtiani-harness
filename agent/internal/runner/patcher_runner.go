package runner

import (
	"context"
	"errors"

	mctpatcher "github.com/tursomari/machtiani/mct/patcher"
	patchersvc "github.com/tursomari/machtiani/patcher"
)

// PatcherRunner orchestrates applying planner-provided instructions using the
// shared patcher service implementation.
type PatcherRunner struct {
	Enabled   bool
	Verbose   bool
	DryRun    bool
	SessionID string
	Service   mctpatcher.Service
}

// Resolve ensures a usable patcher service is available before execution.
func (p *PatcherRunner) Resolve() error {
	if !p.Enabled {
		return nil
	}
	if p.DryRun {
		return nil
	}
	if p.SessionID == "" {
		return errors.New("patcher session id not set")
	}
	if p.Service == nil {
		p.Service = patchersvc.NewService()
	}
	return nil
}

// Apply invokes the patcher service and returns the resulting patch metadata.
func (p *PatcherRunner) Apply(ctx context.Context, instr mctpatcher.Instructions, verbose bool) (*mctpatcher.PatchResult, error) {
	if !p.Enabled {
		return nil, errors.New("patch runner disabled")
	}
	if p.DryRun {
		return &mctpatcher.PatchResult{}, nil
	}
	if p.Service == nil {
		return nil, errors.New("patcher service not initialized")
	}

	params := mctpatcher.PatchParams{
		RepoRoot:     ".",
		SessionID:    p.SessionID,
		Instructions: instr,
		Verbose:      p.Verbose || verbose,
	}
	return p.Service.ApplyAndGeneratePatch(ctx, params)
}
