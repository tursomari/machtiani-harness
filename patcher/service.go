package patcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	mctpatcher "github.com/tursomari/machtiani/mct/patcher"
	"github.com/tursomari/machtiani/patcher/internal/check"
	"github.com/tursomari/machtiani/patcher/internal/diff"
	"github.com/tursomari/machtiani/patcher/internal/engine"
	fsutil "github.com/tursomari/machtiani/patcher/internal/fs"
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

	if ok, err := fsutil.IsRepoRoot(repoAbs); err != nil {
		return nil, &mctpatcher.ValidationError{Err: fmt.Errorf("failed to inspect repo: %w", err)}
	} else if !ok {
		return nil, &mctpatcher.ValidationError{Err: fmt.Errorf("repo root must contain .git: %s", repoAbs)}
	}

	if err := mctpatcher.Validate(repoAbs, params.Instructions); err != nil {
		return nil, &mctpatcher.ValidationError{Err: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.logf(params.Verbose, "applying edits in memory")
	afterMap, filesTouched, err := engine.ApplyAll(repoAbs, params.Instructions)
	if err != nil {
		return nil, &mctpatcher.ValidationError{Err: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.logf(params.Verbose, "generating patch via git diff --no-index")
	mirrorDir, cleanup, err := fsutil.MakeTempMirror(repoAbs, afterMap)
	if err != nil {
		return nil, &mctpatcher.PatchGenerationError{Err: err}
	}
	defer cleanup()

	patchBytes, err := diff.Generate(repoAbs, mirrorDir, filesTouched)
	if err != nil {
		return nil, &mctpatcher.PatchGenerationError{Err: err}
	}

	outDir := params.OutputDir
	if outDir == "" {
		outDir = filepath.Join(repoAbs, ".machtiani", "artifacts", "patches", params.SessionID)
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

	s.logf(params.Verbose, "validating patch with git apply --check")
	if err := check.ApplyCheck(repoAbs, patchPath); err != nil {
		raw := strings.TrimSpace(err.Error())
		diag := mctpatcher.PatchValidationDiagnostics{
			Operation: "git apply --check",
			Stderr:    raw,
			Raw:       raw,
			Messages:  mctpatcher.ParseGitApplyMessages(raw),
		}
		return nil, &mctpatcher.PatchNotCleanError{Err: err, Diagnostics: diag}
	}

	stats := diff.ExtractStats(patchBytes)
	// Prefer touched files list for stable relative paths
	files := append([]string(nil), filesTouched...)
	if len(files) > 0 {
		stats.FilesModified = files
	}

	res := &mctpatcher.PatchResult{
		PatchPath:     patchPath,
		FilesModified: stats.FilesModified,
		Insertions:    stats.Insertions,
		Deletions:     stats.Deletions,
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
	return ctx.Err()
}

func (s *Service) logf(verbose bool, format string, args ...any) {
	if !verbose || s.logger == nil {
		return
	}
	s.logger.Printf(format, args...)
}
