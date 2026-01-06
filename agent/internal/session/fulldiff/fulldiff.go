package fulldiff

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
)

type Transcript interface {
	DeduplicateFullDiffByFile(path string) error
	WriteTurn(step int, question, savedPath string, retrieved []string, summary string, decision string) error
}

type ProgressTracker interface {
	ShouldDeduplicateFile(path string, contentHash string) bool
	UpdateDeduplicationState(path string, contentHash string)
}

type Logger interface {
	Printf(format string, args ...any)
}

type Options struct {
	Verbose  bool
	Log      Logger
	Baseline *patchersvc.BaselineState
}

func Inject(step int, repoRoot string, filesModified []string, tr Transcript, tracker ProgressTracker, opts Options) {
	if len(filesModified) == 0 || tr == nil {
		return
	}
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			opts.Log.Printf(format, args...)
			return
		}
		fmt.Fprintf(os.Stderr, format, args...)
	}

	if opts.Baseline == nil {
		logf("[full-diff] Missing baseline state; skipping full diff\n")
		return
	}

	stripUnified := func(section string) string {
		marker := "\n\nUnified diff (baseline vs workspace):\n"
		idx := strings.Index(section, marker)
		if idx < 0 {
			return section
		}
		return strings.TrimRight(section[:idx], "\n")
	}

	containsChangeMarkers := func(section string) bool {
		// We accept baseline diff sections as "changed" if they include any added or
		// removed lines in the diff rendering. BuildBaselineDiffSection emits
		// line-numbered full diffs using "  + " and "  - " markers.
		return strings.Contains(section, "  + ") || strings.Contains(section, "  - ")
	}

	writeStep := step
	wroteAny := false
	for _, file := range filesModified {
		section, included, err := patchersvc.BuildBaselineDiffSection(opts.Baseline, repoRoot, file)
		if err != nil {
			logf("[full-diff] Baseline diff failed for %s: %v\n", file, err)
			return
		}
		if !included {
			continue
		}
		section = stripUnified(section)
		if !containsChangeMarkers(section) {
			continue
		}

		question := fmt.Sprintf("Automatic full diff post-patch for: %s", file)
		header := fmt.Sprintf("\n=== FULL DIFF OF PATCHED FILE: %s ===\n", file)
		footer := "\n===\n"
		fullDiffContent := header + section + footer
		contentHash := fmt.Sprintf("%x", sha256.Sum256([]byte(fullDiffContent)))

		// Always dedupe the transcript first to preserve the "single per file"
		// invariant, even if the diff content hasn't changed.
		if err := tr.DeduplicateFullDiffByFile(file); err != nil {
			logf("[full-diff] Deduplication failed for %s: %v\n", file, err)
		}

		if tracker != nil && tracker.ShouldDeduplicateFile(file, contentHash) {
			if opts.Verbose {
				logf("[full-diff] File %s already diffed with same content; skipping\n", file)
			}
			continue
		}

		writeStep++
		if err := tr.WriteTurn(writeStep, question, "", []string{file}, fullDiffContent, "full_diff"); err != nil {
			logf("[full-diff] Transcript write failed: %v\n", err)
			return
		}

		if err := tr.DeduplicateFullDiffByFile(file); err != nil {
			logf("[full-diff] Post-write deduplication failed for %s: %v\n", file, err)
		}
		wroteAny = true

		if tracker != nil {
			tracker.UpdateDeduplicationState(file, contentHash)
		}
	}

	if !wroteAny {
		logf("[full-diff] No baseline diff produced; skipping\n")
		return
	}
}
