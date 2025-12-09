package contextbuilder

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	git "github.com/tursomari/machtiani/agent/internal/git"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/prompts"
)

const (
	defaultPerFileCap = 100 * 1024      // 100 KB
	defaultTotalCap   = 2 * 1024 * 1024 // 2 MB
)

const fileSectionPrelude = "\n\nHere are possible relevant files:\n"

var fenceLanguageByExt = map[string]string{
	".bash":       "bash",
	".c":          "c",
	".cc":         "cpp",
	".cpp":        "cpp",
	".cs":         "csharp",
	".css":        "css",
	".dockerfile": "dockerfile",
	".go":         "go",
	".h":          "c",
	".hpp":        "cpp",
	".html":       "html",
	".java":       "java",
	".js":         "javascript",
	".json":       "json",
	".jsx":        "javascript",
	".kt":         "kotlin",
	".m":          "objectivec",
	".md":         "markdown",
	".php":        "php",
	".py":         "python",
	".rb":         "ruby",
	".rs":         "rust",
	".scss":       "scss",
	".sh":         "bash",
	".sql":        "sql",
	".swift":      "swift",
	".ts":         "typescript",
	".tsx":        "tsx",
	".yaml":       "yaml",
	".yml":        "yaml",
}

var fenceLanguageByName = map[string]string{
	"dockerfile": "dockerfile",
	"gemfile":    "ruby",
	"makefile":   "makefile",
	"rakefile":   "ruby",
}

type Options struct {
	PerFileCap      int
	TotalCap        int
	IncludeHistory  bool
	MaxInputTokens  int
	PreludeTemplate string
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
func Build(userPrompt string, relPaths []string, conversationHistory []Message, opts Options) (string, []string, error) {
	opts = opts.withDefaults()

	repoRoot := ""
	if cwd, err := os.Getwd(); err == nil {
		if root, err := git.RepoRoot(cwd); err == nil {
			repoRoot = root
		}
	}

	prelude, err := buildPrelude(userPrompt, conversationHistory, opts.IncludeHistory, opts.PreludeTemplate)
	if err != nil {
		return "", nil, err
	}
	totalTokens := llm.EstimateTokens(prelude)
	filePreludeTokens := llm.EstimateTokens(fileSectionPrelude)
	addedFilePrelude := false

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
		if !addedFilePrelude {
			totalTokens += filePreludeTokens
			addedFilePrelude = true
		}
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
			nextLine := section.visibleLines() + 1
			section.AppendTail(fmt.Sprintf("[TRUNCATED: omitted content starting at line %d]\n", nextLine))
		}

		totalTokens += section.RemainingContentTokens + section.TailTokens
	}

	if opts.MaxInputTokens > 0 && totalTokens > opts.MaxInputTokens {
		totalTokens = applyTokenLimit(sections, totalTokens, opts.MaxInputTokens)
	}

	var builder strings.Builder
	builder.WriteString(prelude)
	if len(sections) > 0 {
		builder.WriteString(fileSectionPrelude)
	}
	for _, section := range sections {
		builder.WriteString(section.Header)
		builder.WriteString(section.RenderContent())
		builder.WriteString(section.Footer)
	}

	return builder.String(), included, nil
}

type conversationTemplateEntry struct {
	Index       int
	Role        string
	DisplayRole string
	Files       []string
	Content     string
}

type conversationTemplateData struct {
	IncludeHistory bool
	History        []conversationTemplateEntry
	UserPrompt     string
}

func buildPrelude(userPrompt string, history []Message, includeHistory bool, templateStr string) (string, error) {
	trimmed := strings.TrimSpace(templateStr)
	if trimmed == "" {
		return "", fmt.Errorf("conversation history template is required")
	}
	renderHistory := includeHistory && len(history) > 0
	entries := make([]conversationTemplateEntry, 0, len(history))
	if renderHistory {
		for idx, msg := range history {
			content := msg.Content
			if !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			entries = append(entries, conversationTemplateEntry{
				Index:       idx + 1,
				Role:        strings.TrimSpace(msg.Role),
				DisplayRole: roleDisplayName(msg.Role),
				Files:       append([]string(nil), msg.Files...),
				Content:     content,
			})
		}
	}
	data := conversationTemplateData{
		IncludeHistory: renderHistory,
		History:        entries,
		UserPrompt:     userPrompt,
	}
	rendered, err := prompts.Render("mct_conversation_history", trimmed, data, nil)
	if err != nil {
		return "", fmt.Errorf("render conversation history template: %w", err)
	}
	return rendered, nil
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
	Language               string
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
	section := &fileSection{
		RelPath:  relPath,
		Language: detectFenceLanguage(relPath),
		Footer:   "```\n",
	}
	section.FooterTokens = llm.EstimateTokens(section.Footer)
	section.refreshHeader()
	return section
}

func (f *fileSection) SetError(content string) {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	f.ErrorContent = content
	f.ErrorTokens = llm.EstimateTokens(content)
	f.refreshHeader()
}

func (f *fileSection) SetContent(content string) {
	segments := splitSegments(content)
	if len(segments) == 0 {
		f.Segments = nil
		f.SegmentTokens = nil
		f.RemainingContentTokens = 0
		f.refreshHeader()
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
	f.refreshHeader()
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
		lastEndsWithNewline = strings.HasSuffix(segment, "\n")
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

func (f *fileSection) trimFromBottom(target int) (int, int, int) {
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
	oldHeaderTokens := f.HeaderTokens
	if f.RemovedSegments > 0 {
		f.Stamp = f.makeStamp()
		f.StampTokens = llm.EstimateTokens(f.Stamp)
	} else {
		f.Stamp = ""
		f.StampTokens = 0
	}

	f.refreshHeader()

	return removed, f.StampTokens - oldStampTokens, f.HeaderTokens - oldHeaderTokens
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

func (f *fileSection) refreshHeader() {
	visible := f.visibleLines()
	start := 1
	if visible <= 0 {
		start = 0
		visible = 0
	}

	var b strings.Builder
	b.WriteString("\n\n### ")
	b.WriteString(f.RelPath)
	b.WriteString("\n\n```")
	lang := strings.TrimSpace(f.Language)
	if lang != "" {
		b.WriteString(lang)
	}
	b.WriteString(" ")
	b.WriteString(fmt.Sprintf("%d:%d\n", start, visible))

	f.Header = b.String()
	f.HeaderTokens = llm.EstimateTokens(f.Header)
}

func (f *fileSection) visibleLines() int {
	return len(f.Segments) - f.RemovedSegments
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

		removed, stampDelta, headerDelta := candidate.trimFromBottom(excess)
		if removed == 0 && stampDelta == 0 {
			candidate.RemainingContentTokens = 0
			continue
		}

		totalTokens = totalTokens - removed + stampDelta + headerDelta
	}

	return totalTokens
}

func detectFenceLanguage(relPath string) string {
	trimmed := strings.TrimSpace(relPath)
	if trimmed == "" {
		return ""
	}
	base := filepath.Base(trimmed)
	if base == "" {
		base = trimmed
	}
	lowerBase := strings.ToLower(base)
	if lang, ok := fenceLanguageByName[lowerBase]; ok {
		return lang
	}
	ext := strings.ToLower(filepath.Ext(base))
	if lang, ok := fenceLanguageByExt[ext]; ok {
		return lang
	}
	if ext != "" {
		return strings.TrimPrefix(ext, ".")
	}
	return ""
}

// DetectFenceLanguage resolves the most appropriate code fence language for a
// relative file path. It mirrors the heuristic used when constructing prompt
// context so other layers (e.g. snippet injection) can render matching fences.
func DetectFenceLanguage(relPath string) string {
	return detectFenceLanguage(relPath)
}
