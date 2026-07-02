package ui

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	defaultWidth = 80

	ansiReset         = "\033[0m"
	ansiGray          = "\033[37m"
	ansiSaveCursor    = "\033[s"
	ansiRestoreCursor = "\033[u"
	ansiClearLine     = "\033[2K"

	promptWindowLines  = 9
	promptContentLines = promptWindowLines - 1
)

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

func widthFromWriter(out io.Writer) int {
	file, ok := out.(*os.File)
	if !ok {
		return 0
	}
	fd := int(file.Fd())
	if !term.IsTerminal(fd) {
		return 0
	}
	w, _, err := term.GetSize(fd)
	if err != nil || w <= 0 {
		return 0
	}
	return w
}

func isTerminalWriter(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
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
