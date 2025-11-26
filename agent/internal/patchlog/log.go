package patchlog

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// Metadata captures optional context associated with a patch prompt log entry.
type Metadata struct {
	Source string
	Model  string
	Alias  string
	Step   int
	Note   string
}

// WritePrompt persists the given prompt to a uniquely named log file in the
// current working directory. It returns the absolute path to the file.
func WritePrompt(prompt string, meta Metadata) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}

	f, err := os.CreateTemp(dir, "mct-patch-llm-input-*.log")
	if err != nil {
		return "", fmt.Errorf("create patch prompt log: %w", err)
	}
	defer f.Close()

	// Make the log world-readable for easier inspection; ignore chmod errors.
	_ = f.Chmod(0o644)

	w := bufio.NewWriter(f)
	if _, err := fmt.Fprintf(w, "Timestamp: %s\n", time.Now().Format(time.RFC3339)); err != nil {
		return "", fmt.Errorf("write timestamp: %w", err)
	}
	if meta.Source != "" {
		if _, err := fmt.Fprintf(w, "Source: %s\n", meta.Source); err != nil {
			return "", fmt.Errorf("write source: %w", err)
		}
	}
	if meta.Model != "" {
		if _, err := fmt.Fprintf(w, "Model: %s\n", meta.Model); err != nil {
			return "", fmt.Errorf("write model: %w", err)
		}
	}
	if meta.Alias != "" {
		if _, err := fmt.Fprintf(w, "Alias: %s\n", meta.Alias); err != nil {
			return "", fmt.Errorf("write alias: %w", err)
		}
	}
	if meta.Step > 0 {
		if _, err := fmt.Fprintf(w, "Step: %d\n", meta.Step); err != nil {
			return "", fmt.Errorf("write step: %w", err)
		}
	}
	if meta.Note != "" {
		if _, err := fmt.Fprintf(w, "Note: %s\n", meta.Note); err != nil {
			return "", fmt.Errorf("write note: %w", err)
		}
	}

	if _, err := w.WriteString("\n=== Prompt ===\n"); err != nil {
		return "", fmt.Errorf("write prompt header: %w", err)
	}
	if _, err := w.WriteString(prompt); err != nil {
		return "", fmt.Errorf("write prompt body: %w", err)
	}
	if !strings.HasSuffix(prompt, "\n") {
		if err := w.WriteByte('\n'); err != nil {
			return "", fmt.Errorf("finalize prompt newline: %w", err)
		}
	}

	if err := w.Flush(); err != nil {
		return "", fmt.Errorf("flush patch prompt log: %w", err)
	}

	return f.Name(), nil
}
