package parser

import (
	"bufio"
	"strings"
)

// ExtractRetrievedFilePaths scans a markdown transcript for a section titled
// "Retrieved File Paths" and returns bullet-listed file paths under it until a blank line
// or next heading.
func ExtractRetrievedFilePaths(md string) []string {
	var out []string
	s := bufio.NewScanner(strings.NewReader(md))
	in := false
	for s.Scan() {
		line := s.Text()
		low := strings.TrimSpace(strings.ToLower(line))
		if strings.HasPrefix(low, "#") && strings.Contains(low, "retrieved file paths") {
			in = true
			continue
		}
		if in {
			if strings.HasPrefix(strings.TrimSpace(line), "-") || strings.HasPrefix(strings.TrimSpace(line), "*") {
				// bullet item
				item := strings.TrimSpace(line)
				item = strings.TrimLeft(item, "-* ")
				// The item may include extra descriptions; take the path up to first space if it looks like a path
				out = append(out, strings.Fields(item)[0])
				continue
			}
			// stop on blank line or next heading
			if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				break
			}
		}
	}
	return out
}

// ExtractAnswerSummary tries to find a concise answer snippet. As a simple heuristic,
// it returns the first paragraph (up to 600 chars) of the markdown body following the first heading.
func ExtractAnswerSummary(md string) string {
	// Prefer the first paragraph under an "Assistant" heading
	assistant := ExtractAssistantAnswer(md)
	if assistant != "" {
		// take the first paragraph
		parts := strings.Split(assistant, "\n\n")
		sum := strings.TrimSpace(parts[0])
		if len(sum) > 600 {
			sum = sum[:600]
		}
		return sum
	}
	// Fallback: first non-heading paragraph in the document
	lines := strings.Split(md, "\n")
	var buf []string
	body := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if !body {
			if t == "" {
				continue
			}
			if strings.HasPrefix(t, "#") {
				continue
			}
			body = true
		}
		if body {
			if t == "" {
				break
			}
			buf = append(buf, l)
		}
	}
	sum := strings.TrimSpace(strings.Join(buf, "\n"))
	if len(sum) > 600 {
		sum = sum[:600]
	}
	return sum
}

// ExtractAssistantAnswer returns the full content under the "# Assistant" section,
// stopping before the next heading or end of document.
func ExtractAssistantAnswer(md string) string {
	lines := strings.Split(md, "\n")
	in := false
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		low := strings.ToLower(t)
		// enter assistant section on a heading containing "assistant"
		if strings.HasPrefix(t, "#") && strings.Contains(low, "assistant") {
			in = true
			// skip the heading line itself
			continue
		}
		if in {
			// stop at the metadata separator or the Retrieved File Paths header
			if t == "---" {
				break
			}
			if strings.HasPrefix(t, "#") && strings.Contains(low, "retrieved file paths") {
				break
			}
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// UpdateSummary appends a brief observation and the latest retrieved files to a running summary.
func UpdateSummary(prev, observation string, retrieved []string) string {
	obs := strings.TrimSpace(observation)
	if prev == "" {
		if len(retrieved) == 0 {
			return obs
		}
		return obs + "\nFiles: " + strings.Join(retrieved, ", ")
	}
	if len(retrieved) == 0 {
		return prev + "\n" + obs
	}
	return prev + "\n" + obs + "\nFiles: " + strings.Join(retrieved, ", ")
}
