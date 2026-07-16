package llm

import (
	"fmt"
	"sort"
	"strings"
)

const DefaultTruncationMarker = "[TRUNCATED: omitted material to fit the active context budget]"

// PromptSection is one independently trimmable part of a structured prompt.
// Lower TrimPriority values are reduced first. OmitFirst sections are removed
// atomically; all other sections retain their prefix/suffix and lose body lines
// from the tail.
type PromptSection struct {
	Name         string
	Prefix       string
	Body         string
	Suffix       string
	TrimPriority int
	OmitFirst    bool
}

// PromptMaterial keeps protocol/instruction text separate from dynamic context
// so the same source material can be rendered against different model budgets.
type PromptMaterial struct {
	Fixed    string
	Sections []PromptSection
}

type FittedPrompt struct {
	Text       string
	Truncated  bool
	Omitted    []string
	Trimmed    []string
	TokenCount int
}

type workingPromptSection struct {
	PromptSection
	body   string
	active bool
}

func (m PromptMaterial) Render(maxTokens int) (FittedPrompt, error) {
	sections := make([]workingPromptSection, len(m.Sections))
	for i, section := range m.Sections {
		sections[i] = workingPromptSection{PromptSection: section, body: section.Body, active: true}
	}
	render := func() string { return renderPromptMaterial(m.Fixed, sections) }

	result := FittedPrompt{Text: render()}
	result.TokenCount = EstimateTokens(result.Text)
	if maxTokens <= 0 || result.TokenCount <= maxTokens {
		return result, nil
	}
	if EstimateTokens(strings.TrimSpace(m.Fixed)) > maxTokens {
		return FittedPrompt{}, fmt.Errorf("context budget %d tokens cannot fit fixed prompt content (%d estimated tokens)", maxTokens, EstimateTokens(strings.TrimSpace(m.Fixed)))
	}

	indices := make([]int, len(sections))
	for i := range sections {
		indices[i] = i
	}
	sort.SliceStable(indices, func(i, j int) bool {
		return sections[indices[i]].TrimPriority < sections[indices[j]].TrimPriority
	})

	for _, idx := range indices {
		section := &sections[idx]
		if !section.active || EstimateTokens(render()) <= maxTokens {
			continue
		}
		if section.OmitFirst || strings.TrimSpace(section.body) == "" {
			section.active = false
			result.Truncated = true
			result.Omitted = append(result.Omitted, section.Name)
			continue
		}

		lines := splitLinesPreserveEnd(section.Body)
		marker := fmt.Sprintf("[TRUNCATED: omitted tail of %s to fit the active context budget]\n", section.Name)
		low, high := 0, len(lines)
		best := ""
		found := false
		for low <= high {
			mid := low + (high-low)/2
			candidate := strings.Join(lines[:mid], "")
			if mid < len(lines) {
				if candidate != "" && !strings.HasSuffix(candidate, "\n") {
					candidate += "\n"
				}
				candidate += marker
			}
			section.body = candidate
			if EstimateTokens(render()) <= maxTokens {
				best = candidate
				found = true
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
		if found {
			section.body = best
			result.Truncated = true
			result.Trimmed = append(result.Trimmed, section.Name)
			continue
		}
		section.active = false
		result.Truncated = true
		result.Omitted = append(result.Omitted, section.Name)
	}

	result.Text = render()
	result.TokenCount = EstimateTokens(result.Text)
	if err := RequireWithinTokenBudget("prompt", result.Text, maxTokens); err != nil {
		return FittedPrompt{}, err
	}
	return result, nil
}

func renderPromptMaterial(fixed string, sections []workingPromptSection) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(fixed))
	for _, section := range sections {
		if !section.active {
			continue
		}
		content := section.Prefix + section.body + section.Suffix
		if strings.TrimSpace(content) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimSpace(content))
	}
	return b.String()
}

func splitLinesPreserveEnd(input string) []string {
	if input == "" {
		return nil
	}
	parts := strings.SplitAfter(input, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// TruncateLinesKeepTail preserves the newest material at the end of a string.
func TruncateLinesKeepTail(input string, maxTokens int, marker string) (string, bool, error) {
	if maxTokens <= 0 || EstimateTokens(input) <= maxTokens {
		return input, false, nil
	}
	if strings.TrimSpace(marker) == "" {
		marker = DefaultTruncationMarker
	}
	marker = strings.TrimSpace(marker) + "\n"
	if EstimateTokens(marker) > maxTokens {
		return "", true, fmt.Errorf("context budget %d tokens cannot fit truncation marker", maxTokens)
	}
	lines := splitLinesPreserveEnd(input)
	low, high := 0, len(lines)
	best := marker
	for low <= high {
		mid := low + (high-low)/2
		candidate := marker + strings.Join(lines[len(lines)-mid:], "")
		if EstimateTokens(candidate) <= maxTokens {
			best = candidate
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if err := RequireWithinTokenBudget("truncated text", best, maxTokens); err != nil {
		return "", true, err
	}
	return best, true, nil
}

func RequireWithinTokenBudget(label, text string, maxTokens int) error {
	if maxTokens <= 0 {
		return nil
	}
	estimated := EstimateTokens(text)
	if estimated > maxTokens {
		return fmt.Errorf("%s exceeds context budget: estimated %d tokens, maximum %d", label, estimated, maxTokens)
	}
	return nil
}

func EstimateMessagesTokens(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += 4 + EstimateTokens(message.Role) + EstimateTokens(message.Content)
	}
	return total
}
