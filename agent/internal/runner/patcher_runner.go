package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	repoAbs string
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
	return nil
}

// Apply invokes the patcher service and returns the resulting patch metadata.
func (p *PatcherRunner) Apply(ctx context.Context, instr mctpatcher.Instructions, verbose bool) (result *mctpatcher.PatchResult, err error) {
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

	baselineStatus, err := gitops.WorkspaceStatus(p.workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("inspect workspace status: %w", err)
	}
	baselineSet := make(map[string]struct{}, len(baselineStatus))
	for _, line := range baselineStatus {
		baselineSet[line] = struct{}{}
	}

	params := mctpatcher.PatchParams{
		RepoRoot:      p.repoAbs,
		SessionID:     p.SessionID,
		Instructions:  instr,
		Verbose:       p.Verbose || verbose,
		WorkspaceRoot: p.workspaceDir,
		MirrorDir:     p.mirrorDir,
		Sequence:      seq,
	}
	result, err = p.Service.ApplyAndGeneratePatch(ctx, params)
	if err != nil {
		return nil, err
	}
	if result.Sequence == 0 {
		result.Sequence = seq
	}

	reverseContent, err := gitops.ReversePatchFromFile(result.PatchPath)
	if err != nil {
		return nil, fmt.Errorf("compute reverse patch: %w", err)
	}
	reversePath := result.PatchPath + ".reverse"
	if err = gitops.WriteReversePatchToFile(reverseContent, reversePath); err != nil {
		return nil, fmt.Errorf("write reverse patch: %w", err)
	}

	workspacePatched := result.AppliedInWorkspace
	repoPatched := false
	defer func() {
		if err != nil && workspacePatched && reversePath != "" {
			_ = gitops.ReversePatchInDir(p.workspaceDir, reversePath, false)
			_, _ = gitops.CheckWorkspaceClean(p.workspaceDir)
		}
		if err != nil && repoPatched && reversePath != "" {
			_ = gitops.ReversePatchInDir(p.repoAbs, reversePath, false)
		}
	}()

	if err = gitops.ApplyPatchInDirWithCheck(p.repoAbs, result.PatchPath); err != nil {
		return nil, fmt.Errorf("validate forward patch: %w", err)
	}

	if !workspacePatched {
		if err = gitops.ApplyPatchInDir(p.workspaceDir, result.PatchPath, verbose || p.Verbose); err != nil {
			return nil, fmt.Errorf("apply patch in workspace for verification: %w", err)
		}
		workspacePatched = true
		result.AppliedInWorkspace = true
	}

	if err = gitops.ReversePatchInDir(p.workspaceDir, reversePath, verbose || p.Verbose); err != nil {
		return nil, fmt.Errorf("apply reverse patch for atomicity check: %w", err)
	}
	workspacePatched = false

	currentStatus, cleanErr := gitops.WorkspaceStatus(p.workspaceDir)
	if cleanErr != nil {
		return nil, fmt.Errorf("check workspace status after atomic undo: %w", cleanErr)
	}
	currentSet := make(map[string]struct{}, len(currentStatus))
	for _, line := range currentStatus {
		currentSet[line] = struct{}{}
	}
	if !statusSetsEqual(baselineSet, currentSet) {
		if verbose || p.Verbose {
			fmt.Fprintf(os.Stderr, "[patcher] workspace baseline: %v\n", baselineStatus)
			fmt.Fprintf(os.Stderr, "[patcher] workspace after reverse: %v\n", currentStatus)
		}
		return nil, errors.New("atomic patch application failed: workspace not clean after reverse")
	}

	if err = gitops.ApplyPatchInDir(p.workspaceDir, result.PatchPath, verbose || p.Verbose); err != nil {
		return nil, fmt.Errorf("reapply forward patch after atomic verification: %w", err)
	}
	workspacePatched = true

	if err = gitops.ApplyPatchInDir(p.repoAbs, result.PatchPath, verbose || p.Verbose); err != nil {
		return nil, fmt.Errorf("apply patch in repo root: %w", err)
	}
	repoPatched = true

	result.ReversePatchPath = reversePath
	p.applied = append(p.applied, patchLogEntry{
		Path:             result.PatchPath,
		Order:            result.Sequence,
		WorkspaceApplied: result.AppliedInWorkspace,
		Finalized:        repoPatched,
		ReversePath:      reversePath,
	})
	p.nextSequence = result.Sequence
	return result, nil
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
			ReversePath:      entry.ReversePath,
		}
	}
	return out
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

func statusSetsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// PatchLogEntry describes a generated patch and its finalization status.
type PatchLogEntry struct {
	Path             string
	Order            int
	WorkspaceApplied bool
	Finalized        bool
	ReversePath      string
}

type patchLogEntry struct {
	Path             string
	Order            int
	WorkspaceApplied bool
	Finalized        bool
	ReversePath      string
}
