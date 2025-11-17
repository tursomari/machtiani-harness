package patcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/gitops"
	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	"github.com/tursomari/machtiani/agent/internal/patcher/internal/check"
	"github.com/tursomari/machtiani/agent/internal/patcher/internal/diff"
	"github.com/tursomari/machtiani/agent/internal/patcher/internal/engine"
	fsutil "github.com/tursomari/machtiani/agent/internal/patcher/internal/fs"
)

// Service provides a concrete implementation of the patcher API using the
// internal diff/engine helpers from the patcher module.
type Service struct {
	logger          *log.Logger
	clock           func() time.Time
	strictPatchMode bool
}

// Option configures a Service instance.
type Option func(*Service)

// WithLogger overrides the logger used for verbose output when executing the
// patch workflow.
func WithLogger(l *log.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithStrictPatchMode toggles support for context-anchored patch application.
func WithStrictPatchMode(enabled bool) Option {
	return func(s *Service) {
		s.strictPatchMode = enabled
	}
}

// WithClock overrides the timestamp source for patch filenames. Intended for tests.
func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.clock = clock
		}
	}
}

// NewService constructs a Service with optional configuration.
func NewService(opts ...Option) *Service {
	svc := &Service{
		logger: log.New(io.Discard, "", log.LstdFlags),
		clock:  time.Now,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// ApplyAndGeneratePatch applies the provided instructions in-memory, generates a
// git patch, validates it, and writes it to disk before returning patch metadata.
func (s *Service) ApplyAndGeneratePatch(ctx context.Context, params mctpatcher.PatchParams) (*mctpatcher.PatchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if params.RepoRoot == "" {
		return nil, &mctpatcher.ValidationError{Err: errors.New("repo root is required")}
	}
	if params.SessionID == "" && params.OutputDir == "" {
		return nil, &mctpatcher.ValidationError{Err: errors.New("session id or output directory required")}
	}

	repoAbs, err := filepath.Abs(params.RepoRoot)
	if err != nil {
		return nil, &mctpatcher.ValidationError{Err: fmt.Errorf("invalid repo root: %w", err)}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	workspaceRoot := repoAbs
	if strings.TrimSpace(params.WorkspaceRoot) != "" {
		workspaceRoot, err = filepath.Abs(params.WorkspaceRoot)
		if err != nil {
			return nil, &mctpatcher.ValidationError{Err: fmt.Errorf("invalid workspace root: %w", err)}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	if ok, err := fsutil.IsRepoRoot(repoAbs); err != nil {
		return nil, &mctpatcher.ValidationError{Err: fmt.Errorf("failed to inspect repo: %w", err)}
	} else if !ok {
		return nil, &mctpatcher.ValidationError{Err: fmt.Errorf("repo root must contain .git: %s", repoAbs)}
	}
	if workspaceRoot != repoAbs {
		if ok, err := fsutil.IsRepoRoot(workspaceRoot); err != nil {
			return nil, &mctpatcher.ValidationError{Err: fmt.Errorf("failed to inspect workspace: %w", err)}
		} else if !ok {
			return nil, &mctpatcher.ValidationError{Err: fmt.Errorf("workspace must contain .git: %s", workspaceRoot)}
		}
	}

	mirrorRoot := ""
	if strings.TrimSpace(params.MirrorDir) != "" {
		mirrorRoot, err = filepath.Abs(params.MirrorDir)
		if err != nil {
			return nil, &mctpatcher.ValidationError{Err: fmt.Errorf("invalid mirror dir: %w", err)}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	if err := mctpatcher.Validate(workspaceRoot, params.Instructions); err != nil {
		return nil, &mctpatcher.ValidationError{Err: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if !s.strictPatchMode && instructionsUseStrictPatch(params.Instructions) {
		return nil, &mctpatcher.ValidationError{Err: errors.New("strict patch mode disabled; use rewrite or replace modes")}
	}

	s.logf(params.Verbose, "applying edits in memory")
	instr := cloneInstructions(params.Instructions)
	var (
		afterMap     map[string][]byte
		filesTouched []string
		applyErr     *mctpatcher.PatchApplyError
	)
	for attempt := 1; attempt <= 2; attempt++ {
		afterMap, filesTouched, err = engine.ApplyAll(workspaceRoot, instr)
		if err == nil {
			break
		}
		applyErr = nil
		if !errors.As(err, &applyErr) {
			return nil, &mctpatcher.ValidationError{Err: err}
		}
		diag := applyErr.Diagnostics
		if diag.Operation == "" {
			diag.Operation = "strict patch apply"
		}
		if attempt == 2 || !s.isRecoverablePatchFailure(applyErr) {
			return nil, &mctpatcher.PatchNotCleanError{Err: applyErr.Err, Diagnostics: diag}
		}
		s.logf(params.Verbose, "strict patch apply failed; attempting rewrite fallback and retry")
		updated, ok, fallbackErr := s.attemptFallback(workspaceRoot, instr, applyErr)
		if fallbackErr != nil {
			return nil, &mctpatcher.PatchNotCleanError{Err: fallbackErr, Diagnostics: diag}
		}
		if !ok {
			return nil, &mctpatcher.PatchNotCleanError{Err: applyErr.Err, Diagnostics: diag}
		}
		instr = updated
	}
	if err != nil {
		if applyErr != nil {
			diag := applyErr.Diagnostics
			if diag.Operation == "" {
				diag.Operation = "strict patch apply"
			}
			return nil, &mctpatcher.PatchNotCleanError{Err: applyErr.Err, Diagnostics: diag}
		}
		return nil, &mctpatcher.ValidationError{Err: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.logf(params.Verbose, "generating patch via git diff --no-index")
	mirrorDir, cleanup, err := fsutil.MakeTempMirror(afterMap, mirrorRoot)
	if err != nil {
		return nil, &mctpatcher.PatchGenerationError{Err: err}
	}
	defer cleanup()

	patchBytes, err := diff.Generate(workspaceRoot, mirrorDir, filesTouched)
	if err != nil {
		return nil, &mctpatcher.PatchGenerationError{Err: err}
	}

	outDir := params.OutputDir
	if outDir == "" {
		outDir = filepath.Join(repoAbs, ".machtiani", "sessions", params.SessionID, "artifacts", "patches")
	} else if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(repoAbs, outDir)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, &mctpatcher.PatchGenerationError{Err: err}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	ts := s.clock()
	patchPath := filepath.Join(outDir, fsutil.PatchFilename(ts))
	if err := os.WriteFile(patchPath, patchBytes, 0o644); err != nil {
		return nil, &mctpatcher.PatchGenerationError{Err: err}
	}

	afterDir, manifestPath, err := persistAfterState(afterMap, outDir, patchPath, ts)
	if err != nil {
		return nil, &mctpatcher.PatchGenerationError{Err: err}
	}

	s.logf(params.Verbose, "validating patch with git apply --check")
	if err := check.ApplyCheck(workspaceRoot, patchPath); err != nil {
		raw := strings.TrimSpace(err.Error())
		diag := mctpatcher.PatchValidationDiagnostics{
			Operation: "git apply --check",
			Stderr:    raw,
			Raw:       raw,
			Messages:  mctpatcher.ParseGitApplyMessages(raw),
		}
		return nil, &mctpatcher.PatchNotCleanError{Err: err, Diagnostics: diag}
	}

	workspaceApplied := false
	if strings.TrimSpace(params.WorkspaceRoot) != "" {
		if err := gitops.ApplyPatchInDir(workspaceRoot, patchPath, params.Verbose); err != nil {
			return nil, &mctpatcher.PatchGenerationError{Err: fmt.Errorf("apply patch in workspace: %w", err)}
		}
		workspaceApplied = true
	}

	stats := diff.ExtractStats(patchBytes)
	// Prefer touched files list for stable relative paths
	files := append([]string(nil), filesTouched...)
	if len(files) > 0 {
		stats.FilesModified = files
	}

	res := &mctpatcher.PatchResult{
		PatchPath:          patchPath,
		FilesModified:      stats.FilesModified,
		Insertions:         stats.Insertions,
		Deletions:          stats.Deletions,
		AfterStateDir:      afterDir,
		ManifestPath:       manifestPath,
		Sequence:           params.Sequence,
		AppliedInWorkspace: workspaceApplied,
	}
	if params.Instructions.Metadata != nil {
		res.Description = strings.TrimSpace(params.Instructions.Metadata.Description)
	}
	return res, nil
}

// ValidateInstructions performs the same validation used during the full patch
// workflow without generating any artifacts.
func (s *Service) ValidateInstructions(ctx context.Context, repoRoot string, instructions mctpatcher.Instructions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if repoRoot == "" {
		return &mctpatcher.ValidationError{Err: errors.New("repo root is required")}
	}
	repoAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return &mctpatcher.ValidationError{Err: fmt.Errorf("invalid repo root: %w", err)}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ok, err := fsutil.IsRepoRoot(repoAbs); err != nil {
		return &mctpatcher.ValidationError{Err: fmt.Errorf("failed to inspect repo: %w", err)}
	} else if !ok {
		return &mctpatcher.ValidationError{Err: fmt.Errorf("repo root must contain .git: %s", repoAbs)}
	}
	if err := mctpatcher.Validate(repoAbs, instructions); err != nil {
		return &mctpatcher.ValidationError{Err: err}
	}
	if !s.strictPatchMode && instructionsUseStrictPatch(instructions) {
		return &mctpatcher.ValidationError{Err: errors.New("strict patch mode disabled; use rewrite or replace modes")}
	}
	return ctx.Err()
}

func (s *Service) logf(verbose bool, format string, args ...any) {
	if !verbose || s.logger == nil {
		return
	}
	s.logger.Printf(format, args...)
}

func instructionsUseStrictPatch(instr mctpatcher.Instructions) bool {
	for _, ed := range instr.Edits {
		if ed.Mode == mctpatcher.ModePatch {
			return true
		}
	}
	return false
}

type afterManifest struct {
	GeneratedAt time.Time           `json:"generated_at"`
	PatchFile   string              `json:"patch_file"`
	Files       []afterManifestFile `json:"files"`
}

type afterManifestFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256,omitempty"`
	Size    int    `json:"size,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}

func persistAfterState(after map[string][]byte, outDir, patchPath string, ts time.Time) (string, string, error) {
	baseName := strings.TrimSuffix(filepath.Base(patchPath), filepath.Ext(patchPath))
	root := filepath.Join(outDir, baseName+"-after")
	if err := os.RemoveAll(root); err != nil {
		return "", "", err
	}
	filesDir := filepath.Join(root, "files")
	if err := fsutil.WriteMirror(filesDir, after); err != nil {
		return "", "", err
	}
	keys := make([]string, 0, len(after))
	for rel := range after {
		keys = append(keys, rel)
	}
	sort.Strings(keys)
	manifest := afterManifest{
		GeneratedAt: ts.UTC(),
		PatchFile:   filepath.Base(patchPath),
		Files:       make([]afterManifestFile, 0, len(keys)),
	}
	for _, rel := range keys {
		entry := afterManifestFile{Path: rel}
		if content := after[rel]; content == nil {
			entry.Deleted = true
		} else {
			sum := sha256.Sum256(content)
			entry.SHA256 = hex.EncodeToString(sum[:])
			entry.Size = len(content)
		}
		manifest.Files = append(manifest.Files, entry)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		return "", "", err
	}
	return root, manifestPath, nil
}
