package fulldiff

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/transcript"
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
	Verbose        bool
	Log            Logger
	Baseline       *patchersvc.BaselineState
	FullDiffNote   string
	RecordFullDiff func(step int, file, diff, note string) error
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

	hasLineNumberedMarker := func(line, marker string) bool {
		idx := strings.Index(line, marker)
		if idx == -1 {
			return false
		}
		for _, r := range line[:idx] {
			if r != ' ' && (r < '0' || r > '9') {
				return false
			}
		}
		return true
	}

	containsChangeMarkers := func(section string) bool {
		// We accept baseline diff sections as "changed" if they include any added or
		// removed lines in either the line-numbered full diff or the unified diff rendering.
		scanner := bufio.NewScanner(strings.NewReader(section))
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "+++ "):
				continue
			case strings.HasPrefix(line, "--- "):
				continue
			case strings.HasPrefix(line, "@@"):
				continue
			case strings.HasPrefix(line, "```"):
				continue
			case strings.HasPrefix(line, "+"):
				return true
			case strings.HasPrefix(line, "-"):
				return true
			case hasLineNumberedMarker(line, "  + "):
				return true
			case hasLineNumberedMarker(line, "  - "):
				return true
			}
		}
		return false
	}

	fullDiffNote := strings.TrimSpace(opts.FullDiffNote)
	if fullDiffNote == "" {
		fullDiffNote = strings.TrimSpace(transcript.FullDiffNoteText(nil))
	}

	writeStep := step
	wroteAny := false
	for _, file := range filesModified {
		section, included, err := patchersvc.BuildBaselineFullDiffSection(opts.Baseline, repoRoot, file)
		if err != nil {
			logf("[full-diff] Baseline diff failed for %s: %v\n", file, err)
			return
		}
		if !included {
			continue
		}
		if !containsChangeMarkers(section) {
			continue
		}

		question := fullDiffNote
		fullDiffContent := section
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
		if opts.RecordFullDiff != nil {
			if err := opts.RecordFullDiff(writeStep, file, fullDiffContent, question); err != nil {
				logf("[full-diff] Conversation record failed for %s: %v\n", file, err)
				return
			}
		}
		if err := tr.WriteTurn(writeStep, question, "", nil, fullDiffContent, ""); err != nil {
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
