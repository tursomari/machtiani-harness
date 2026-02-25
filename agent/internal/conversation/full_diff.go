package conversation

import (
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

// ExtractFullDiffs returns the latest full diff entries per file, ordered by
// their last occurrence in the conversation.
func ExtractFullDiffs(conv *Conversation) string {
	if conv == nil || len(conv.Messages) == 0 {
		return ""
	}

	type entry struct {
		file    string
		content string
		order   int
	}
	entries := make([]entry, 0)
	indices := map[string]int{}
	order := 0

	for _, msg := range conv.Messages {
		if getType(msg.Metadata) != "full_diff" {
			continue
		}
		diff, ok := coerceString(msg.Metadata["diff"])
		if !ok {
			continue
		}
		diff = strings.TrimSpace(diff)
		if diff == "" {
			continue
		}
		file, _ := coerceString(msg.Metadata["file"])
		fileKey := normalizeFullDiffPath(file)
		order++
		if fileKey == "" {
			entries = append(entries, entry{file: "", content: diff, order: order})
			continue
		}
		if idx, ok := indices[fileKey]; ok {
			entries[idx].content = diff
			entries[idx].order = order
			continue
		}
		indices[fileKey] = len(entries)
		entries = append(entries, entry{file: fileKey, content: diff, order: order})
	}

	if len(entries) == 0 {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].order < entries[j].order
	})
	out := make([]string, 0, len(entries))
	for _, item := range entries {
		if strings.TrimSpace(item.content) == "" {
			continue
		}
		out = append(out, item.content)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n\n")
}

func normalizeFullDiffPath(p string) string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return ""
	}
	for {
		switch {
		case strings.HasSuffix(trimmed, " (deleted in workspace)"):
			trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, " (deleted in workspace)"))
		case strings.HasSuffix(trimmed, " (symlink)"):
			trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, " (symlink)"))
		default:
			cleaned := pathpkg.Clean(filepath.ToSlash(trimmed))
			if cleaned == "." {
				return ""
			}
			return cleaned
		}
	}
}
