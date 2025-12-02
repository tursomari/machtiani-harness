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
	logger *log.Logger
	clock  func() time.Time
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
// WithStrictPatchMode is deprecated and ignored. Strict hunk patches are no longer supported.
func WithStrictPatchMode(enabled bool) Option { return func(s *Service) {} }

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

	createdFiles, err := ensurePatchTargets(workspaceRoot, params.Instructions)
	if err != nil {
		return nil, &mctpatcher.ValidationError{Err: err}
	}

	if err := mctpatcher.Validate(workspaceRoot, params.Instructions); err != nil {
		return nil, &mctpatcher.ValidationError{Err: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Determine instruction set to apply: either direct or converted to full rewrites.
	instr := cloneInstructionsLite(params.Instructions)
	if params.FullMode {
		converted, convErr := s.convertToFullRewrite(workspaceRoot, instr, createdFiles)
		if convErr != nil {
			return nil, &mctpatcher.PatchNotCleanError{Err: convErr, Diagnostics: mctpatcher.PatchValidationDiagnostics{Operation: "convert to full rewrite"}}
		}
		instr = converted
	}

	var baselineState *BaselineState
	if strings.TrimSpace(params.SessionID) != "" {
		baselineState, err = EnsureBaseline(params.SessionID, workspaceRoot, s.clock())
		if err != nil {
			return nil, &mctpatcher.PatchGenerationError{Err: fmt.Errorf("ensure baseline state: %w", err)}
		}
	}

	s.logf(params.Verbose, "applying edits in memory")
	afterMap, filesTouched, err := engine.ApplyAll(workspaceRoot, instr)
	if err != nil {
		return nil, &mctpatcher.ValidationError{Err: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if baselineState != nil {
		if err := baselineState.VerifyFiles(filesTouched); err != nil {
			return nil, &mctpatcher.PatchGenerationError{Err: fmt.Errorf("verify baseline state: %w", err)}
		}
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
	// In full-rewrite mode, let the runner handle forward+reverse atomicity in the workspace.
	if strings.TrimSpace(params.WorkspaceRoot) != "" && !params.FullMode {
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
	// Validate only repository and instruction consistency; hunks are not supported.
	if sessionID := strings.TrimSpace(os.Getenv("MACHTIANI_SESSION_ID")); sessionID != "" {
		if _, err := EnsureBaseline(sessionID, repoAbs, s.clock()); err != nil {
			return &mctpatcher.ValidationError{Err: fmt.Errorf("ensure baseline state: %w", err)}
		}
	}
	return ctx.Err()
}

func (s *Service) logf(verbose bool, format string, args ...any) {
	if !verbose || s.logger == nil {
		return
	}
	s.logger.Printf(format, args...)
}

// instructionsUseStrictPatch is deprecated; hunks are no longer supported.
func instructionsUseStrictPatch(_ mctpatcher.Instructions) bool { return false }

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

// convertToFullRewrite converts any instruction set into only create/delete/rewrite
// edits by computing the after-state content for each touched file. This enforces
// full-file replacements for modifications when full-mode is enabled.
func (s *Service) convertToFullRewrite(workspaceRoot string, instr mctpatcher.Instructions, patchedNewFiles map[string]struct{}) (mctpatcher.Instructions, error) {
	afterMap, filesTouched, err := engine.ApplyAll(workspaceRoot, instr)
	if err != nil {
		return instr, err
	}

	out := mctpatcher.Instructions{}
	if instr.Metadata != nil {
		meta := *instr.Metadata
		out.Metadata = &meta
	}
	// Build deterministic full-rewrite edits based on after-state and baseline existence
	for _, rel := range filesTouched {
		onDisk := filepath.Join(workspaceRoot, filepath.FromSlash(rel))
		content := afterMap[rel]
		if content == nil {
			out.Edits = append(out.Edits, mctpatcher.Edit{Path: rel, Mode: mctpatcher.ModeDelete})
			continue
		}
		// Decide between create vs rewrite by checking baseline filesystem
		if _, created := patchedNewFiles[rel]; created {
			out.Edits = append(out.Edits, mctpatcher.Edit{Path: rel, Mode: mctpatcher.ModeCreate, NewContent: string(content)})
			continue
		}
		if st, err := os.Stat(onDisk); err == nil && !st.IsDir() {
			out.Edits = append(out.Edits, mctpatcher.Edit{Path: rel, Mode: mctpatcher.ModeRewrite, NewContent: string(content)})
		} else {
			out.Edits = append(out.Edits, mctpatcher.Edit{Path: rel, Mode: mctpatcher.ModeCreate, NewContent: string(content)})
		}
	}
	return out, nil
}

// cloneInstructionsLite makes a shallow, JSON-stable copy of the instruction set.
func cloneInstructionsLite(instr mctpatcher.Instructions) mctpatcher.Instructions {
	cloned := mctpatcher.Instructions{}
	if instr.Metadata != nil {
		meta := *instr.Metadata
		cloned.Metadata = &meta
	}
	cloned.Edits = make([]mctpatcher.Edit, len(instr.Edits))
	copy(cloned.Edits, instr.Edits)
	return cloned
}

func ensurePatchTargets(workspaceRoot string, instr mctpatcher.Instructions) (map[string]struct{}, error) {
	created := make(map[string]struct{})
	for idx, ed := range instr.Edits {
		if ed.Mode != mctpatcher.ModePatch {
			continue
		}
		rel, err := ed.NormalizedPath(workspaceRoot)
		if err != nil {
			return nil, fmt.Errorf("patch validate: edit[%d] path invalid: %w", idx, err)
		}
		abs := filepath.Join(workspaceRoot, filepath.FromSlash(rel))
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			continue
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("patch validate: stat %s: %w", rel, err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fmt.Errorf("patch validate: ensure dir for %s: %w", rel, err)
		}
		if err := os.WriteFile(abs, nil, 0o644); err != nil {
			return nil, fmt.Errorf("patch validate: create placeholder %s: %w", rel, err)
		}
		created[rel] = struct{}{}
	}
	return created, nil
}
