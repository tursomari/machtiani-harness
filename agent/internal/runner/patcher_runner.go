package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/gitops"
	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	promptsvc "github.com/tursomari/machtiani/agent/internal/mct/prompt"
	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/tempdir"
)

// PatcherRunner orchestrates applying planner-provided instructions using the
// shared patcher service implementation.
type PatcherRunner struct {
	Enabled         bool
	Verbose         bool
	DryRun          bool
	SessionID       string
	Service         mctpatcher.Service
	Runtime         promptsvc.ModelRuntime
	RepoRoot        string
	PersistTmpData  bool
	SessionTempRoot string

	workspaceDir     string
	workspaceCleanup func()
	mirrorDir        string
	mirrorCleanup    func()
	nextSequence     int
	applied          []patchLogEntry

	WorkspaceFactory func(string) (string, func(), error)
	MirrorFactory    func() (string, func(), error)
	ApplyFunc        func(string, string, bool) error
	repoAbs          string
}

// Resolve ensures a usable patcher service is available before execution.
func (p *PatcherRunner) Resolve() error {
	if !p.Enabled {
		return nil
	}
	if p.SessionID == "" {
		return errors.New("patcher session id not set")
	}
	repoRoot := strings.TrimSpace(p.RepoRoot)
	if repoRoot == "" {
		repoRoot = "."
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return fmt.Errorf("resolve repo root: %w", err)
	}
	p.repoAbs = abs
	p.RepoRoot = abs
	if p.Service == nil {
		p.Service = patchersvc.NewService()
	}
	if p.DryRun {
		return nil
	}
	if p.WorkspaceFactory == nil {
		p.WorkspaceFactory = patchersvc.CreateWorkspace
	}
	if p.workspaceDir == "" {
		ws, cleanup, err := p.WorkspaceFactory(abs)
		if err != nil {
			return fmt.Errorf("create patch workspace: %w", err)
		}
		p.workspaceDir = ws
		p.workspaceCleanup = cleanup
	}
	if p.MirrorFactory == nil {
		p.MirrorFactory = defaultMirrorFactory
	}
	if p.mirrorDir == "" {
		mirrorDir, cleanup, err := p.MirrorFactory()
		if err != nil {
			p.cleanupWorkspace()
			return fmt.Errorf("create patch mirror: %w", err)
		}
		p.mirrorDir = mirrorDir
		p.mirrorCleanup = cleanup
	}
	if p.ApplyFunc == nil {
		p.ApplyFunc = gitops.ApplyPatchInDir
	}
	return nil
}

// Apply invokes the patcher service and returns the resulting patch metadata.
func (p *PatcherRunner) Apply(ctx context.Context, instr mctpatcher.Instructions, verbose bool) (*mctpatcher.PatchResult, error) {
	if !p.Enabled {
		return nil, errors.New("patch runner disabled")
	}
	if p.DryRun {
		seq := p.nextSequence + 1
		p.nextSequence = seq
		return &mctpatcher.PatchResult{Sequence: seq}, nil
	}
	if p.Service == nil {
		return nil, errors.New("patcher service not initialized")
	}
	if p.repoAbs == "" {
		return nil, errors.New("patcher repo root not resolved")
	}
	if p.workspaceDir == "" {
		return nil, errors.New("patcher workspace not initialized")
	}
	if p.mirrorDir == "" {
		return nil, errors.New("patcher mirror not initialized")
	}
	seq := p.nextSequence + 1

	params := mctpatcher.PatchParams{
		RepoRoot:      p.repoAbs,
		SessionID:     p.SessionID,
		Instructions:  instr,
		Verbose:       p.Verbose || verbose,
		WorkspaceRoot: p.workspaceDir,
		MirrorDir:     p.mirrorDir,
		Sequence:      seq,
	}
	res, err := p.Service.ApplyAndGeneratePatch(ctx, params)
	if err != nil {
		return nil, err
	}
	if res.Sequence == 0 {
		res.Sequence = seq
	}
	p.applied = append(p.applied, patchLogEntry{Path: res.PatchPath, Order: res.Sequence, WorkspaceApplied: res.AppliedInWorkspace})
	p.nextSequence = res.Sequence
	return res, nil
}

// Finalize reapplies all generated patches to the original repository in
// sequence order. It stops at the first failure and returns the associated error.
func (p *PatcherRunner) Finalize(verbose bool) error {
	if !p.Enabled || p.DryRun {
		return nil
	}
	if p.repoAbs == "" {
		return errors.New("patcher repo root not resolved")
	}
	if len(p.applied) == 0 {
		return nil
	}
	copyLog := make([]patchLogEntry, len(p.applied))
	copy(copyLog, p.applied)
	sort.Slice(copyLog, func(i, j int) bool { return copyLog[i].Order < copyLog[j].Order })
	for _, entry := range copyLog {
		if entry.Finalized {
			continue
		}
		if strings.TrimSpace(entry.Path) == "" {
			continue
		}
		if err := p.ApplyFunc(p.repoAbs, entry.Path, verbose || p.Verbose); err != nil {
			return fmt.Errorf("finalize patch %d (%s): %w", entry.Order, entry.Path, err)
		}
		p.markFinalized(entry.Order)
	}
	return nil
}

// Close releases any temporary resources allocated for the patch session.
func (p *PatcherRunner) Close() {
	if p.PersistTmpData {
		return
	}
	p.cleanupMirror()
	p.cleanupWorkspace()
}

// PatchLog returns a snapshot of the patch application timeline for reporting.
func (p *PatcherRunner) PatchLog() []PatchLogEntry {
	out := make([]PatchLogEntry, len(p.applied))
	for i, entry := range p.applied {
		out[i] = PatchLogEntry{
			Path:             entry.Path,
			Order:            entry.Order,
			WorkspaceApplied: entry.WorkspaceApplied,
			Finalized:        entry.Finalized,
		}
	}
	return out
}

func (p *PatcherRunner) markFinalized(order int) {
	for i := range p.applied {
		if p.applied[i].Order == order {
			p.applied[i].Finalized = true
			return
		}
	}
}

func (p *PatcherRunner) cleanupWorkspace() {
	if p.workspaceCleanup != nil {
		p.workspaceCleanup()
		p.workspaceCleanup = nil
		p.workspaceDir = ""
	}
}

func (p *PatcherRunner) cleanupMirror() {
	if p.mirrorCleanup != nil {
		p.mirrorCleanup()
		p.mirrorCleanup = nil
		p.mirrorDir = ""
	}
}

func defaultMirrorFactory() (string, func(), error) {
	dir, err := tempdir.MkdirTemp("patcher-mirror-*")
	if err != nil {
		return "", func() {}, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// PatchLogEntry describes a generated patch and its finalization status.
type PatchLogEntry struct {
	Path             string
	Order            int
	WorkspaceApplied bool
	Finalized        bool
}

type patchLogEntry struct {
	Path             string
	Order            int
	WorkspaceApplied bool
	Finalized        bool
}
