package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
)

type fileState int

const (
	fileStateUnknown fileState = iota
	fileStateMissing
	fileStateExists
)

type modeAdjustment struct {
	Index  int
	Path   string
	From   mctpatcher.Mode
	To     mctpatcher.Mode
	Reason string
}

func (a modeAdjustment) String() string {
	return fmt.Sprintf("edit[%d] %s -> %s for %s: %s", a.Index, a.From, a.To, a.Path, a.Reason)
}

type modeConflict struct {
	Index  int
	Path   string
	Mode   mctpatcher.Mode
	Reason string
}

func (c modeConflict) String() string {
	return fmt.Sprintf("edit[%d] %s: %s", c.Index, c.Path, c.Reason)
}

type instructionPrecheckError struct {
	Conflicts []modeConflict
}

func (e *instructionPrecheckError) Error() string {
	if e == nil || len(e.Conflicts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(e.Conflicts))
	for _, c := range e.Conflicts {
		parts = append(parts, c.String())
	}
	return "instruction pre-check failed: " + strings.Join(parts, "; ")
}

func (e *instructionPrecheckError) Summary() string {
	if e == nil || len(e.Conflicts) == 0 {
		return ""
	}
	lines := make([]string, 0, len(e.Conflicts))
	for _, c := range e.Conflicts {
		lines = append(lines, fmt.Sprintf("- %s (mode=%s): %s", c.Path, c.Mode, c.Reason))
	}
	return "Patch instruction pre-check blocked these edits:\n" + strings.Join(lines, "\n")
}

func (e *instructionPrecheckError) Count() int {
	if e == nil {
		return 0
	}
	return len(e.Conflicts)
}

func preprocessPatchInstructions(repoRoot string, instr mctpatcher.Instructions) (mctpatcher.Instructions, []modeAdjustment, *instructionPrecheckError) {
	repo := strings.TrimSpace(repoRoot)
	if repo == "" {
		repo = "."
	}

	prepared := mctpatcher.Instructions{Metadata: instr.Metadata}
	prepared.Edits = make([]mctpatcher.Edit, len(instr.Edits))
	adjustments := []modeAdjustment{}
	conflicts := []modeConflict{}

	for idx, ed := range instr.Edits {
		prepared.Edits[idx] = ed
		normalized, normErr := ed.NormalizedPath(repo)
		if normErr != nil {
			conflicts = append(conflicts, modeConflict{Index: idx, Path: ed.Path, Mode: ed.Mode, Reason: fmt.Sprintf("invalid path: %v", normErr)})
			continue
		}

		state, stateErr := detectFileState(repo, normalized)
		if stateErr != nil {
			// Treat errors other than not-exist as unknown state
			state = fileStateUnknown
		}

		switch ed.Mode {
		case mctpatcher.ModeCreate:
			if state == fileStateExists {
				if strings.TrimSpace(ed.NewContent) == "" {
					conflicts = append(conflicts, modeConflict{Index: idx, Path: normalized, Mode: ed.Mode, Reason: "file exists; provide new_content to rewrite"})
					continue
				}
				prepared.Edits[idx].Mode = mctpatcher.ModeRewrite
				adjustments = append(adjustments, modeAdjustment{Index: idx, Path: normalized, From: ed.Mode, To: mctpatcher.ModeRewrite, Reason: "file already exists"})
			}
			if state == fileStateUnknown && strings.TrimSpace(ed.NewContent) != "" {
				prepared.Edits[idx].Mode = mctpatcher.ModeRewrite
				adjustments = append(adjustments, modeAdjustment{Index: idx, Path: normalized, From: ed.Mode, To: mctpatcher.ModeRewrite, Reason: "unknown file state; defaulting to rewrite"})
			}
		case mctpatcher.ModeRewrite:
			if state == fileStateMissing {
				if strings.TrimSpace(ed.NewContent) == "" {
					conflicts = append(conflicts, modeConflict{Index: idx, Path: normalized, Mode: ed.Mode, Reason: "file missing; supply new_content or use create"})
					continue
				}
				prepared.Edits[idx].Mode = mctpatcher.ModeCreate
				adjustments = append(adjustments, modeAdjustment{Index: idx, Path: normalized, From: ed.Mode, To: mctpatcher.ModeCreate, Reason: "file missing; switching to create"})
			}
			if state == fileStateUnknown && strings.TrimSpace(ed.NewContent) == "" {
				conflicts = append(conflicts, modeConflict{Index: idx, Path: normalized, Mode: ed.Mode, Reason: "unknown file state and no new_content"})
			}
		case mctpatcher.ModeReplace:
			if state == fileStateMissing {
				conflicts = append(conflicts, modeConflict{Index: idx, Path: normalized, Mode: ed.Mode, Reason: "file missing; use create with new_content"})
			} else if state == fileStateUnknown {
				if strings.TrimSpace(ed.NewContent) != "" {
					prepared.Edits[idx].Mode = mctpatcher.ModeRewrite
					adjustments = append(adjustments, modeAdjustment{Index: idx, Path: normalized, From: ed.Mode, To: mctpatcher.ModeRewrite, Reason: "unknown file state; using rewrite"})
				}
			}
		case mctpatcher.ModeDelete:
			if state == fileStateMissing {
				conflicts = append(conflicts, modeConflict{Index: idx, Path: normalized, Mode: ed.Mode, Reason: "file missing; cannot delete"})
			}
		case mctpatcher.ModePatch:
			if state == fileStateMissing {
				if converted, ok := convertPatchToCreate(prepared.Edits[idx]); ok {
					prepared.Edits[idx] = converted
					prepared.Edits[idx].Mode = mctpatcher.ModeCreate
					adjustments = append(adjustments, modeAdjustment{Index: idx, Path: normalized, From: ed.Mode, To: mctpatcher.ModeCreate, Reason: "file missing; converting patch to create"})
					continue
				}
			}
			if state != fileStateExists {
				reason := "file missing"
				if state == fileStateUnknown {
					reason = "unknown file state"
				}
				conflicts = append(conflicts, modeConflict{Index: idx, Path: normalized, Mode: ed.Mode, Reason: reason})
			}
		default:
			conflicts = append(conflicts, modeConflict{Index: idx, Path: normalized, Mode: ed.Mode, Reason: "unknown edit mode"})
		}
	}

	var err *instructionPrecheckError
	if len(conflicts) > 0 {
		err = &instructionPrecheckError{Conflicts: conflicts}
	}
	return prepared, adjustments, err
}

func convertPatchToCreate(ed mctpatcher.Edit) (mctpatcher.Edit, bool) {
	if ed.PatchInfo == nil {
		return mctpatcher.Edit{}, false
	}
	if len(ed.PatchInfo.Hunks) == 0 {
		return mctpatcher.Edit{}, false
	}
	lines := make([]string, 0)
	for _, h := range ed.PatchInfo.Hunks {
		if hasNonWhitespace(h.ContextBefore) || hasNonWhitespace(h.Deletions) || hasNonWhitespace(h.ContextAfter) {
			return mctpatcher.Edit{}, false
		}
		if len(h.Additions) == 0 {
			continue
		}
		lines = append(lines, h.Additions...)
	}
	if len(lines) == 0 {
		return mctpatcher.Edit{}, false
	}
	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	converted := ed
	converted.Mode = mctpatcher.ModeCreate
	converted.NewContent = content
	converted.PatchInfo = nil
	converted.Before = ""
	converted.After = ""
	converted.Occurrence = 0
	return converted, true
}

func hasNonWhitespace(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func detectFileState(repoRoot, rel string) (fileState, error) {
	abs := filepath.Join(repoRoot, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileStateMissing, nil
		}
		return fileStateUnknown, err
	}
	if info.IsDir() {
		return fileStateUnknown, fmt.Errorf("path %s is a directory", rel)
	}
	return fileStateExists, nil
}
