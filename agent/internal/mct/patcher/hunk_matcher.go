package patcher

import "fmt"

// FindHunkMatch scans the provided file lines to identify the starting index of
// the hunk's before-state (context + deletions + context). approximateStart is
// an optional hint (0-based) that may be derived from stale planner metadata.
func FindHunkMatch(lines []string, h Hunk, approximateStart int) (int, bool, int, string, error) {
	total := len(h.ContextBefore) + len(h.Deletions) + len(h.ContextAfter)
	attempt := approximateStart
	if total == 0 {
		if attempt < 0 {
			return -1, false, attempt, "anchor_missing", fmt.Errorf("hunk has no context to anchor insertion")
		}
		if attempt < 0 || attempt > len(lines) {
			return -1, false, attempt, "out_of_range", fmt.Errorf("insertion point %d out of bounds for file with %d lines", attempt+1, len(lines))
		}
		return attempt, true, attempt, "", nil
	}

	expected := assembleBeforeLines(h)
	exactMatches := collectMatches(lines, expected, slicesEqual)
	if len(exactMatches) == 1 {
		idx := exactMatches[0]
		return idx, true, idx, "", nil
	}
	if len(exactMatches) > 1 {
		if candidate, ok := disambiguateMatches(exactMatches, h, approximateStart); ok {
			return candidate, true, candidate, "", nil
		}
		return -1, false, attempt, "match_ambiguous", fmt.Errorf("found %d exact matches for hunk", len(exactMatches))
	}

	whitespaceMatches := collectMatches(lines, expected, LinesWhitespaceEquivalent)
	if len(whitespaceMatches) == 0 {
		return -1, false, attempt, "match_not_found", fmt.Errorf("unable to locate matching context in file")
	}
	if len(whitespaceMatches) == 1 {
		idx := whitespaceMatches[0]
		return idx, false, idx, "", nil
	}
	if candidate, ok := disambiguateMatches(whitespaceMatches, h, approximateStart); ok {
		return candidate, false, candidate, "", nil
	}
	return -1, false, attempt, "match_ambiguous", fmt.Errorf("found %d lenient matches for hunk", len(whitespaceMatches))
}

func collectMatches(lines []string, expected []string, cmp func([]string, []string) bool) []int {
	total := len(expected)
	if total == 0 {
		return nil
	}
	matches := make([]int, 0)
	for idx := 0; idx+total <= len(lines); idx++ {
		segment := lines[idx : idx+total]
		if cmp(segment, expected) {
			matches = append(matches, idx)
		}
	}
	return matches
}

func disambiguateMatches(matches []int, h Hunk, approximateStart int) (int, bool) {
	if len(matches) == 0 {
		return 0, false
	}
	if h.SnippetSource != nil && approximateStart >= 0 {
		if cand, ok := exactMatchForTarget(matches, approximateStart); ok {
			return cand, true
		}
	}
	if approximateStart >= 0 {
		cand, ok := nearestMatch(matches, approximateStart)
		if ok {
			return cand, true
		}
	}
	return 0, false
}

func exactMatchForTarget(matches []int, target int) (int, bool) {
	for _, idx := range matches {
		if idx == target {
			return idx, true
		}
	}
	return 0, false
}

func nearestMatch(matches []int, target int) (int, bool) {
	if target < 0 || len(matches) == 0 {
		return 0, false
	}
	best := matches[0]
	bestDist := absInt(best - target)
	unique := true
	for _, idx := range matches[1:] {
		dist := absInt(idx - target)
		if dist < bestDist {
			best = idx
			bestDist = dist
			unique = true
		} else if dist == bestDist {
			unique = false
		}
	}
	if !unique {
		return 0, false
	}
	return best, true
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
