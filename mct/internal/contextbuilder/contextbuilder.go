package contextbuilder

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	git "github.com/tursomari/machtiani/mct/internal/git"
	"github.com/tursomari/machtiani/mct/llm"
)

const (
	defaultPerFileCap = 100 * 1024      // 100 KB
	defaultTotalCap   = 2 * 1024 * 1024 // 2 MB
)

type Options struct {
	PerFileCap     int
	TotalCap       int
	IncludeHistory bool
	MaxInputTokens int
}

type Message struct {
	Role    string // "user" or "assistant"
	Content string
	Files   []string `json:"Files,omitempty"` // Relevant files supplied to assistant replies
}

func (o Options) withDefaults() Options {
	if o.PerFileCap <= 0 {
		o.PerFileCap = defaultPerFileCap
	}
	if o.TotalCap <= 0 {
		o.TotalCap = defaultTotalCap
	}
	return o
}

// Build concatenates the structured conversation context with the user prompt
// and the contents of the discovered files into a single prompt suitable for an
// LLM call. When opts.IncludeHistory is true and conversationHistory is
// non-empty, the prompt begins with a Conversation History section followed by a
// Current Request section. When opts.MaxInputTokens is greater than zero, file
// contents are truncated from the bottom of each file until the prompt's token
// estimate fits within the provided limit.
func Build(userPrompt string, relPaths []string, conversationHistory []Message, opts Options) (string, []string) {
	opts = opts.withDefaults()

	repoRoot := ""
	if cwd, err := os.Getwd(); err == nil {
		if root, err := git.RepoRoot(cwd); err == nil {
			repoRoot = root
		}
	}

	prelude := buildPrelude(userPrompt, conversationHistory, opts.IncludeHistory)
	totalTokens := llm.EstimateTokens(prelude)

	sections := make([]*fileSection, 0, len(relPaths))
	included := make([]string, 0, len(relPaths))

	bytesUsed := 0

	for _, rel := range relPaths {
		if bytesUsed >= opts.TotalCap {
			break
		}

		if filepath.IsAbs(rel) || strings.Contains(rel, "..") || strings.Contains(rel, "\\") {
			continue
		}

		resolved := filepath.FromSlash(rel)
		if repoRoot != "" {
			candidate := filepath.Clean(filepath.Join(repoRoot, resolved))
			relCandidate, err := filepath.Rel(repoRoot, candidate)
			if err != nil || strings.HasPrefix(relCandidate, "..") {
				continue
			}
			resolved = candidate
		}

		section := newFileSection(rel)
		sections = append(sections, section)
		included = append(included, rel)
		totalTokens += section.HeaderTokens + section.FooterTokens

		f, err := os.Open(resolved)
		if err != nil {
			section.SetError("[ERROR: could not read file]\n")
			totalTokens += section.ErrorTokens
			continue
		}

		remainingBudget := opts.TotalCap - bytesUsed
		perFileBudget := opts.PerFileCap
		if remainingBudget < perFileBudget {
			perFileBudget = remainingBudget
		}
		if perFileBudget <= 0 {
			_ = f.Close()
			break
		}

		contentBytes, bytesRead, readErr := readFileWithBudget(f, perFileBudget)
		bytesUsed += bytesRead

		truncated := false
		if stat, statErr := f.Stat(); statErr == nil {
			truncated = stat.Size() > int64(bytesRead)
		}
		_ = f.Close()

		section.SetContent(string(contentBytes))

		if readErr != nil {
			section.AppendTail(fmt.Sprintf("[READ ERROR: %s]\n", readErr.Error()))
		}
		if truncated {
			section.AppendTail("[TRUNCATED]\n")
		}

		totalTokens += section.RemainingContentTokens + section.TailTokens
	}

	if opts.MaxInputTokens > 0 && totalTokens > opts.MaxInputTokens {
		totalTokens = applyTokenLimit(sections, totalTokens, opts.MaxInputTokens)
	}

	var builder strings.Builder
	builder.WriteString(prelude)
	for _, section := range sections {
		builder.WriteString(section.Header)
		builder.WriteString(section.RenderContent())
		builder.WriteString(section.Footer)
	}

	return builder.String(), included
}

func buildPrelude(userPrompt string, history []Message, includeHistory bool) string {
	var b strings.Builder

	doHistory := includeHistory && len(history) > 0
	if doHistory {
		b.WriteString("Conversation History:\n")
		for idx, msg := range history {
			roleLabel := roleDisplayName(msg.Role)
			fmt.Fprintf(&b, "%d. %s", idx+1, roleLabel)
			if len(msg.Files) > 0 {
				fmt.Fprintf(&b, " (Files: %s)", strings.Join(msg.Files, ", "))
			}
			b.WriteString(":\n")
			b.WriteString(msg.Content)
			if !strings.HasSuffix(msg.Content, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		b.WriteString("Current Request:\n")
		b.WriteString(userPrompt)
	} else {
		b.WriteString(userPrompt)
	}

	b.WriteString("\n\nHere are possible relevant files:\n")
	return b.String()
}

func readFileWithBudget(f *os.File, budget int) ([]byte, int, error) {
	if budget <= 0 {
		return nil, 0, nil
	}

	buf := make([]byte, 0, budget)
	tmpSize := budget
	if tmpSize < 4096 {
		tmpSize = 4096
	}
	tmp := make([]byte, tmpSize)

	totalRead := 0
	var readErr error

	for totalRead < budget {
		n, err := f.Read(tmp)
		if n > 0 {
			space := budget - totalRead
			if n > space {
				n = space
			}
			buf = append(buf, tmp[:n]...)
			totalRead += n
		}
		if err == io.EOF {
			readErr = nil
			break
		}
		if err != nil {
			readErr = err
			break
		}
	}

	return buf, totalRead, readErr
}

func roleDisplayName(role string) string {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return "Unknown"
	}
	lower := strings.ToLower(trimmed)
	switch lower {
	case "assistant":
		return "Assistant"
	case "user":
		return "User"
	}
	runes := []rune(lower)
	runes[0] = unicode.ToUpper(runes[0])
	for i := 1; i < len(runes); i++ {
		runes[i] = unicode.ToLower(runes[i])
	}
	return string(runes)
}

type fileSection struct {
	RelPath                string
	Header                 string
	HeaderTokens           int
	Footer                 string
	FooterTokens           int
	Segments               []string
	SegmentTokens          []int
	RemainingContentTokens int
	RemovedSegments        int
	Stamp                  string
	StampTokens            int
	TailNotes              []string
	TailTokens             int
	ErrorContent           string
	ErrorTokens            int
}

func newFileSection(relPath string) *fileSection {
	header := fmt.Sprintf("\n\n### %s\n\n```\n", relPath)
	footer := "```\n"
	return &fileSection{
		RelPath:      relPath,
		Header:       header,
		HeaderTokens: llm.EstimateTokens(header),
		Footer:       footer,
		FooterTokens: llm.EstimateTokens(footer),
	}
}

func (f *fileSection) SetError(content string) {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	f.ErrorContent = content
	f.ErrorTokens = llm.EstimateTokens(content)
}

func (f *fileSection) SetContent(content string) {
	segments := splitSegments(content)
	if len(segments) == 0 {
		f.Segments = nil
		f.SegmentTokens = nil
		f.RemainingContentTokens = 0
		return
	}

	tokens := make([]int, len(segments))
	total := 0
	for i, seg := range segments {
		tok := llm.EstimateTokens(seg)
		tokens[i] = tok
		total += tok
	}

	f.Segments = segments
	f.SegmentTokens = tokens
	f.RemainingContentTokens = total
}

func (f *fileSection) AppendTail(note string) {
	if strings.TrimSpace(note) == "" {
		return
	}
	if !strings.HasSuffix(note, "\n") {
		note += "\n"
	}
	f.TailNotes = append(f.TailNotes, note)
	f.TailTokens += llm.EstimateTokens(note)
}

func (f *fileSection) RenderContent() string {
	if f.ErrorContent != "" {
		return f.ErrorContent
	}

	var b strings.Builder

	kept := len(f.Segments) - f.RemovedSegments
	lastEndsWithNewline := false

	for i := 0; i < kept; i++ {
		segment := f.Segments[i]
		b.WriteString(segment)
	}
	if kept > 0 {
		lastEndsWithNewline = strings.HasSuffix(f.Segments[kept-1], "\n")
	}

	if f.RemovedSegments > 0 {
		stamp := f.Stamp
		if b.Len() > 0 && !lastEndsWithNewline {
			b.WriteString("\n")
		}
		b.WriteString(stamp)
		lastEndsWithNewline = strings.HasSuffix(stamp, "\n")
	}

	for _, note := range f.TailNotes {
		if b.Len() > 0 && !lastEndsWithNewline {
			b.WriteString("\n")
		}
		b.WriteString(note)
		lastEndsWithNewline = strings.HasSuffix(note, "\n")
	}

	return b.String()
}

func (f *fileSection) trimFromBottom(target int) (int, int) {
	if target <= 0 {
		target = 1
	}

	removed := 0

	for target > 0 && f.RemovedSegments < len(f.Segments) {
		idx := len(f.Segments) - 1 - f.RemovedSegments
		segTokens := f.SegmentTokens[idx]
		f.RemovedSegments++
		f.RemainingContentTokens -= segTokens
		removed += segTokens
		target -= segTokens
	}

	if f.RemainingContentTokens < 0 {
		f.RemainingContentTokens = 0
	}

	oldStampTokens := f.StampTokens
	if f.RemovedSegments > 0 {
		f.Stamp = f.makeStamp()
		f.StampTokens = llm.EstimateTokens(f.Stamp)
	} else {
		f.Stamp = ""
		f.StampTokens = 0
	}

	return removed, f.StampTokens - oldStampTokens
}

func (f *fileSection) makeStamp() string {
	totalLines := len(f.Segments)
	if totalLines == 0 {
		return "------------------------------\nTRUNCATED\nomitted lines 1...0\n------------------------------\n"
	}

	start := totalLines - f.RemovedSegments + 1
	if start < 1 {
		start = 1
	}
	end := totalLines
	return fmt.Sprintf("------------------------------\nTRUNCATED\nomitted lines %d...%d\n------------------------------\n", start, end)
}

func splitSegments(content string) []string {
	if content == "" {
		return nil
	}

	parts := strings.SplitAfter(content, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts
}

func applyTokenLimit(sections []*fileSection, totalTokens, maxTokens int) int {
	for totalTokens > maxTokens {
		excess := totalTokens - maxTokens

		var candidate *fileSection
		var candidatePercent float64

		for _, section := range sections {
			if section == nil {
				continue
			}
			if section.RemainingContentTokens <= 0 || section.RemovedSegments >= len(section.Segments) {
				continue
			}
			percent := float64(excess) / float64(section.RemainingContentTokens)
			if candidate == nil || percent < candidatePercent || (percent == candidatePercent && section.RemainingContentTokens > candidate.RemainingContentTokens) {
				candidate = section
				candidatePercent = percent
			}
		}

		if candidate == nil {
			break
		}

		removed, stampDelta := candidate.trimFromBottom(excess)
		if removed == 0 && stampDelta == 0 {
			candidate.RemainingContentTokens = 0
			continue
		}

		totalTokens = totalTokens - removed + stampDelta
	}

	return totalTokens
}
