package patcher

import "fmt"

// ValidateStrictPatchForMissingFile ensures a strict patch payload is compatible
// with creating a brand-new file instead of modifying an existing one. All
// hunks must be pure insertions anchored at the start of the file so that
// applying them to an empty file produces deterministic results.
func ValidateStrictPatchForMissingFile(info *UnifiedPatchInfo) error {
	if info == nil {
		return fmt.Errorf("patch payload missing")
	}
	if len(info.Hunks) == 0 {
		return fmt.Errorf("patch requires at least one hunk")
	}
	for idx, h := range info.Hunks {
		if len(h.ContextBefore) > 0 || len(h.ContextAfter) > 0 {
			return fmt.Errorf("hunk[%d]: context not allowed when creating new file", idx)
		}
		if len(h.Deletions) > 0 {
			return fmt.Errorf("hunk[%d]: deletions not allowed when creating new file", idx)
		}
		if len(h.Additions) == 0 {
			return fmt.Errorf("hunk[%d]: additions required when creating new file", idx)
		}
		if h.OldCount != 0 {
			return fmt.Errorf("hunk[%d]: old_count must be 0 when creating new file", idx)
		}
		if h.SnippetSource == nil {
			return fmt.Errorf("hunk[%d]: snippet_source required when creating new file", idx)
		}
		start := h.SnippetSource.StartLine
		end := h.SnippetSource.EndLine
		if start != 1 {
			return fmt.Errorf("hunk[%d]: snippet_source start_line must be 1 when creating new file", idx)
		}
		if end != start-1 {
			return fmt.Errorf("hunk[%d]: snippet_source end_line must equal start_line-1 when creating new file", idx)
		}
	}
	return nil
}
