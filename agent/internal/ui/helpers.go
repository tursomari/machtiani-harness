package ui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/term"
)

const (
	defaultWidth  = 80
	defaultHeight = 24

	ansiReset             = "\033[0m"
	ansiSaveCursor        = "\033[s"
	ansiRestoreCursor     = "\033[u"
	ansiClearLine         = "\033[2K"
	ansiResetScrollRegion = "\033[r"

	promptWindowLines  = 9
	promptContentLines = promptWindowLines - 1
	minFooterHeight    = promptWindowLines + 3
)

var commandTagBlockPattern = regexp.MustCompile(`(?is)<command(?:-[a-z0-9_-]+)?\b[^>]*>.*?</command(?:-[a-z0-9_-]+)?>`)

// PromptOptions controls how prompts are rendered in the terminal chain.
type PromptOptions struct {
	Metadata      []string
	ModeIndicator string
}

// ModeTaskDisplay captures the metadata required to render mode task progress.
type ModeTaskDisplay struct {
	Index  int
	Title  string
	Mode   string
	Status string
}

func CleanActionDescription(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = commandTagBlockPattern.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	end := len(lines)
	for end > 0 && isPunctuationOnlyLine(lines[end-1]) {
		end--
	}
	return strings.TrimSpace(strings.Join(lines[:end], "\n"))
}

func isPunctuationOnlyLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return true
	}
	for _, r := range line {
		if !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			return false
		}
	}
	return true
}

func sanitizeLine(text string) string {
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
	text = replacer.Replace(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	fields := strings.Fields(text)
	return strings.Join(fields, " ")
}

func sanitizeLinePreserveIndent(text string) string {
	replacer := strings.NewReplacer("\r", " ", "\t", " ")
	text = replacer.Replace(text)
	text = strings.TrimRight(text, " ")
	if text == "" {
		return ""
	}
	leading := len(text) - len(strings.TrimLeft(text, " "))
	core := strings.TrimSpace(text)
	if core == "" {
		return ""
	}
	fields := strings.Fields(core)
	if len(fields) == 0 {
		return ""
	}
	collapsed := strings.Join(fields, " ")
	if leading == 0 {
		return collapsed
	}
	return strings.Repeat(" ", leading) + collapsed
}

func sanitizeLines(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	parts := strings.Split(text, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		clean := sanitizeLinePreserveIndent(part)
		if clean == "" {
			continue
		}
		lines = append(lines, clean)
	}
	return lines
}

func detectWidth(out io.Writer) int {
	if w := widthFromWriter(out); w > 0 {
		return w
	}
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if n, err := strconv.Atoi(cols); err == nil && n > 0 {
			return n
		}
	}
	return defaultWidth
}

func detectHeight(out io.Writer) int {
	fd, ok := writerFD(out)
	if !ok {
		return 0
	}
	if !term.IsTerminal(int(fd)) {
		return 0
	}
	if _, h, err := term.GetSize(int(fd)); err == nil && h > 0 {
		return h
	}
	if lines := os.Getenv("LINES"); lines != "" {
		if n, err := strconv.Atoi(lines); err == nil && n > 1 {
			return n
		}
	}
	return defaultHeight
}

func widthFromWriter(out io.Writer) int {
	fd, ok := writerFD(out)
	if !ok {
		return 0
	}
	if !term.IsTerminal(int(fd)) {
		return 0
	}
	w, _, err := term.GetSize(int(fd))
	if err != nil || w <= 0 {
		return 0
	}
	return w
}

func isTerminalWriter(out io.Writer) bool {
	fd, ok := writerFD(out)
	if !ok {
		return false
	}
	return term.IsTerminal(int(fd))
}

type fdWriter interface {
	Fd() uintptr
}

func writerFD(out io.Writer) (uintptr, bool) {
	file, ok := out.(fdWriter)
	if !ok {
		return 0, false
	}
	return file.Fd(), true
}

func truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func lastNLines(text string, n int) []string {
	if n <= 0 {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	start := len(lines) - n
	if start < 0 {
		start = 0
	}
	result := make([]string, len(lines)-start)
	copy(result, lines[start:])
	return result
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	remaining := seconds % 60
	return fmt.Sprintf("%d:%02d", minutes, remaining)
}

func formatTokenCount(n int) string {
	if n < 0 {
		n = 0
	}
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	prefix := len(s) % 3
	if prefix == 0 {
		prefix = 3
	}
	b.WriteString(s[:prefix])
	for i := prefix; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func formatFooterCWD(cwd string) string {
	if cwd == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return cwd
	}
	home = filepath.Clean(home)
	cleanCWD := filepath.Clean(cwd)
	if cleanCWD == home {
		return "~"
	}
	homePrefix := home + string(os.PathSeparator)
	if home == string(os.PathSeparator) {
		homePrefix = home
	}
	if strings.HasPrefix(cleanCWD, homePrefix) {
		return "~" + strings.TrimPrefix(cleanCWD, home)
	}
	return cwd
}

func shortenFooterPath(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	parts := strings.Split(strings.TrimPrefix(path, "~/"), "/")
	for i, part := range parts {
		if runes := []rune(part); len(runes) > 0 {
			parts[i] = string(runes[0])
		}
	}
	return "~/" + strings.Join(parts, "/")
}
