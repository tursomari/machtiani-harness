package utils

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

// EnsureDirExists creates a directory if it doesn't already exist.
func EnsureDirExists(dirPath string) error {
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dirPath, err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to check directory status %s: %w", dirPath, err)
	}
	return nil
}

// CreateTempMarkdownFile writes a chat transcript to the session-scoped chat directory.
func CreateTempMarkdownFile(content string, filename string, sessionID string) (string, error) {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = strings.TrimSpace(os.Getenv("MACHTIANI_SESSION_ID"))
	}
	if sid == "" {
		return "", errors.New("session id required to write chat artifacts")
	}
	chatDir, err := artifacts.SessionChatDirectory(sid)
	if err != nil {
		return "", err
	}
	if err := EnsureDirExists(chatDir); err != nil {
		return "", err
	}

	primaryPath := filepath.Join(chatDir, fmt.Sprintf("%s.md", filename))
	if err := os.WriteFile(primaryPath, []byte(content), 0o644); err != nil {
		return "", err
	}

	secondaryPath := filepath.Join(chatDir, "machtiani-response.md")
	if err := os.WriteFile(secondaryPath, []byte(content), 0o644); err != nil {
		log.Printf("Warning: failed to write secondary chat copy (%s): %v", secondaryPath, err)
	}

	return primaryPath, nil
}

var dryRun bool

// SetDryRun toggles dry-run state used by higher level workflows.
func SetDryRun(state bool) {
	dryRun = state
}

// IsDryRunEnabled reports whether dry-run mode is active.
func IsDryRunEnabled() bool {
	return dryRun
}

// ReadIgnoreFile reads a `.machtiani.ignore` file and returns trimmed entries.
func ReadIgnoreFile(fileName string) ([]string, error) {
	var filePaths []string

	file, err := os.Open(fileName)
	if os.IsNotExist(err) {
		return filePaths, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", fileName, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		filePaths = append(filePaths, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file %s: %w", fileName, err)
	}

	return filePaths, nil
}

// IsAnswerOnlyMode reports whether the CLI runs in answer-only mode.
func IsAnswerOnlyMode() bool {
	for i, arg := range os.Args {
		if arg == "--mode" && i+1 < len(os.Args) && os.Args[i+1] == "answer-only" {
			return true
		}
		if strings.HasPrefix(arg, "--mode=answer-only") {
			return true
		}
	}
	return false
}

// PrintIfNotAnswerOnly prints formatted output when not in answer-only mode.
func PrintIfNotAnswerOnly(isAnswerOnly bool, format string, args ...interface{}) {
	if !isAnswerOnly {
		fmt.Printf(format, args...)
	}
}

// LogIfNotAnswerOnly logs formatted output when not in answer-only mode.
func LogIfNotAnswerOnly(isAnswerOnly bool, format string, args ...interface{}) {
	if !isAnswerOnly {
		log.Printf(format, args...)
	}
}

// LogErrorIfNotAnswerOnly logs an error when not in answer-only mode.
func LogErrorIfNotAnswerOnly(isAnswerOnly bool, err error, message string) {
	if err != nil && !isAnswerOnly {
		log.Printf("%s: %v", message, err)
	}
}
