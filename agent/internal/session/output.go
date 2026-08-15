package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/presentation"
)

func writeFinalAnswer(sessionID, answer, finalFileFlag string, dryRun bool) (string, error) {
	if dryRun {
		return "", nil
	}
	path := strings.TrimSpace(finalFileFlag)
	if path == "" {
		dir, err := artifacts.SessionChatDirectory(sessionID)
		if err != nil {
			return "", err
		}
		path = filepath.Join(dir, "agent-final-answer.md")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(answer+"\n"), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func renderFinalAnswer(answer string, diagWriter io.Writer, theme ...presentation.Theme) string {
	var resolved presentation.Theme
	if len(theme) > 0 {
		resolved = theme[0]
	}
	rendered, fallback, err := renderWithGlow(answer, resolved)
	if fallback {
		if err != nil {
			fmt.Fprintln(diagWriter, "[warning] markdown render failed; showing plain text:", err)
		}
	} else if err != nil {
		fmt.Fprintln(diagWriter, "[warning] markdown render warning:", err)
	}
	if strings.TrimSpace(rendered) == "" {
		rendered = strings.TrimSpace(answer)
	}
	return rendered
}

func renderWithGlow(content string, themes ...presentation.Theme) (string, bool, error) {
	var theme presentation.Theme
	if len(themes) > 0 {
		theme = themes[0]
	}
	r, err := presentation.NewMarkdownRenderer(theme, false)
	if err != nil {
		return strings.TrimSpace(content), true, err
	}
	rendered, err := r.Render(content)
	if err != nil {
		return strings.TrimSpace(content), true, err
	}
	rendered = strings.TrimRight(rendered, "\n")
	if strings.TrimSpace(rendered) == "" {
		return strings.TrimSpace(content), false, nil
	}
	return rendered, false, nil
}
