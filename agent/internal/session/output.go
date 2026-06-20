package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func writeFinalAnswer(sessionID, answer, finalFileFlag string, verbose bool, dryRun bool, diagWriter io.Writer) error {
	if dryRun {
		return nil
	}
	path := strings.TrimSpace(finalFileFlag)
	if path == "" {
		dir, err := artifacts.SessionChatDirectory(sessionID)
		if err != nil {
			return err
		}
		path = filepath.Join(dir, "agent-final-answer.md")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(answer+"\n"), 0o644); err != nil {
		return err
	}
	if verbose {
		fmt.Fprintln(diagWriter, "Final answer saved:", path)
	}
	return nil
}

func presentFinalAnswer(display ui.SessionDisplay, answer string, diagWriter io.Writer) {
	rendered, fallback, err := renderWithGlow(answer)
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
	display.ShowFinal(rendered)
}

func renderWithGlow(content string) (string, bool, error) {
	r, err := glamour.NewTermRenderer(glamour.WithAutoStyle())
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
