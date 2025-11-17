package patcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	"github.com/tursomari/machtiani/agent/internal/patcher/internal/engine"
)

var recoverableConflictReasons = map[string]struct{}{
	"context_before_mismatch": {},
	"context_after_mismatch":  {},
	"deletion_mismatch":       {},
	"match_not_found":         {},
	"match_ambiguous":         {},
	"anchor_missing":          {},
}

func cloneInstructions(instr mctpatcher.Instructions) mctpatcher.Instructions {
	cloned := mctpatcher.Instructions{}
	if instr.Metadata != nil {
		meta := *instr.Metadata
		cloned.Metadata = &meta
	}
	cloned.Edits = make([]mctpatcher.Edit, len(instr.Edits))
	for i, ed := range instr.Edits {
		cloned.Edits[i] = cloneEdit(ed)
	}
	return cloned
}

func cloneEdit(ed mctpatcher.Edit) mctpatcher.Edit {
	copied := ed
	if ed.PatchInfo != nil {
		copied.PatchInfo = clonePatchInfo(ed.PatchInfo)
	}
	return copied
}

func clonePatchInfo(info *mctpatcher.UnifiedPatchInfo) *mctpatcher.UnifiedPatchInfo {
	if info == nil {
		return nil
	}
	cloned := *info
	cloned.Hunks = make([]mctpatcher.Hunk, len(info.Hunks))
	for i, h := range info.Hunks {
		cloned.Hunks[i] = cloneHunk(h)
	}
	return &cloned
}

func cloneHunk(h mctpatcher.Hunk) mctpatcher.Hunk {
	cloned := h
	cloned.ContextBefore = append([]string(nil), h.ContextBefore...)
	cloned.ContextAfter = append([]string(nil), h.ContextAfter...)
	cloned.Deletions = append([]string(nil), h.Deletions...)
	cloned.Additions = append([]string(nil), h.Additions...)
	if h.SnippetSource != nil {
		snippet := *h.SnippetSource
		cloned.SnippetSource = &snippet
	}
	return cloned
}

func (s *Service) isRecoverablePatchFailure(err *mctpatcher.PatchApplyError) bool {
	if err == nil {
		return false
	}
	if len(err.ConflictedEdits) == 0 {
		return false
	}
	diags := err.Diagnostics
	if len(diags.ContentConflicts) == 0 {
		return false
	}
	for _, conflict := range diags.ContentConflicts {
		if !isRecoverableConflict(conflict) {
			return false
		}
	}
	return true
}

func isRecoverableConflict(conflict mctpatcher.ContentConflictDiagnostic) bool {
	if conflict.Reason != "" {
		if _, ok := recoverableConflictReasons[conflict.Reason]; !ok {
			return false
		}
	}
	for _, hunk := range conflict.HunkConflicts {
		if hunk.Reason == "" {
			continue
		}
		if _, ok := recoverableConflictReasons[hunk.Reason]; !ok {
			return false
		}
	}
	return true
}

func (s *Service) attemptFallback(workspaceRoot string, instr mctpatcher.Instructions, applyErr *mctpatcher.PatchApplyError) (mctpatcher.Instructions, bool, error) {
	if applyErr == nil || len(applyErr.ConflictedEdits) == 0 {
		return instr, false, nil
	}

	updated := cloneInstructions(instr)
	idxs := append([]int(nil), applyErr.ConflictedEdits...)
	sort.Ints(idxs)

	for _, idx := range idxs {
		if idx < 0 || idx >= len(updated.Edits) {
			return instr, false, fmt.Errorf("fallback: conflicted edit index %d out of range", idx)
		}
		ed := updated.Edits[idx]
		if ed.Mode != mctpatcher.ModePatch || ed.PatchInfo == nil {
			return instr, false, fmt.Errorf("fallback: edit[%d] is not a strict patch", idx)
		}

		prefix := mctpatcher.Instructions{Edits: append([]mctpatcher.Edit(nil), updated.Edits[:idx]...)}
		baseState, _, err := engine.ApplyAll(workspaceRoot, prefix)
		if err != nil {
			return instr, false, fmt.Errorf("fallback: apply prefix edits: %w", err)
		}

		rel, err := ed.NormalizedPath(workspaceRoot)
		if err != nil {
			return instr, false, fmt.Errorf("fallback: normalize path for edit[%d]: %w", idx, err)
		}

		base := baseState[rel]
		if base == nil {
			base, err = os.ReadFile(filepath.Join(workspaceRoot, filepath.FromSlash(rel)))
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return instr, false, fmt.Errorf("fallback: base file missing for edit[%d]: %s", idx, rel)
				}
				return instr, false, fmt.Errorf("fallback: read base file for edit[%d]: %w", idx, err)
			}
		}

		rewritten, err := applyFallbackPatch(base, ed.PatchInfo)
		if err != nil {
			return instr, false, fmt.Errorf("fallback: rewrite content for edit[%d]: %w", idx, err)
		}

		updated.Edits[idx].Mode = mctpatcher.ModeRewrite
		updated.Edits[idx].PatchInfo = nil
		updated.Edits[idx].NewContent = rewritten
		updated.Edits[idx].Before = ""
		updated.Edits[idx].After = ""
	}

	return updated, true, nil
}

func applyFallbackPatch(base []byte, info *mctpatcher.UnifiedPatchInfo) (string, error) {
	lines, hadTrailing := splitLinesPreserve(string(base))
	lineOffset := 0

	for _, h := range info.Hunks {
		start := fallbackStart(lines, h, lineOffset)
		totalOld := len(h.ContextBefore) + len(h.Deletions) + len(h.ContextAfter)
		start, end := clampRange(start, totalOld, len(lines))

		replacement := make([]string, 0, len(h.ContextBefore)+len(h.Additions)+len(h.ContextAfter))
		replacement = append(replacement, h.ContextBefore...)
		replacement = append(replacement, h.Additions...)
		replacement = append(replacement, h.ContextAfter...)

		lines = spliceLines(lines, start, end, replacement)
		removed := end - start
		added := len(replacement)
		lineOffset += added - removed
	}

	return joinLinesPreserve(lines, hadTrailing), nil
}

func fallbackStart(lines []string, h mctpatcher.Hunk, lineOffset int) int {
	approx := approximateStart(h, lineOffset)
	start, _, attempt, _, err := mctpatcher.FindHunkMatch(lines, h, approx)
	if err == nil && start >= 0 {
		return start
	}
	candidate := attempt
	if candidate < 0 {
		candidate = approx
	}
	if candidate < 0 {
		return 0
	}
	if candidate > len(lines) {
		return len(lines)
	}
	return candidate
}

func approximateStart(h mctpatcher.Hunk, lineOffset int) int {
	if h.SnippetSource != nil && h.SnippetSource.StartLine > 0 {
		return h.SnippetSource.StartLine - 1 + lineOffset
	}
	if h.OldStart > 0 {
		return h.OldStart - 1 + lineOffset
	}
	return -1
}

func clampRange(start, totalOld, lineCount int) (int, int) {
	if start < 0 {
		start = 0
	}
	if start > lineCount {
		start = lineCount
	}
	if totalOld < 0 {
		totalOld = 0
	}
	end := start + totalOld
	if totalOld == 0 {
		if end > lineCount {
			end = lineCount
		}
		return start, end
	}
	if end > lineCount {
		end = lineCount
		if end < start {
			start = end
		}
	}
	return start, end
}

func spliceLines(lines []string, start, end int, replacement []string) []string {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if end > len(lines) {
		end = len(lines)
	}
	out := make([]string, 0, len(lines)-(end-start)+len(replacement))
	out = append(out, lines[:start]...)
	out = append(out, replacement...)
	out = append(out, lines[end:]...)
	return out
}

func splitLinesPreserve(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	hadTrailing := strings.HasSuffix(content, "\n")
	trimmed := strings.TrimSuffix(content, "\n")
	if trimmed == "" {
		return []string{""}, hadTrailing
	}
	return strings.Split(trimmed, "\n"), hadTrailing
}

func joinLinesPreserve(lines []string, hadTrailing bool) string {
	if len(lines) == 0 {
		return ""
	}
	joined := strings.Join(lines, "\n")
	if hadTrailing {
		return joined + "\n"
	}
	return joined
}
