package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/runner"
)

// ForkSession creates a copy of an existing session under a new session ID.
// The source session must not be active (locked). All files from the source
// session directory are copied to the new session directory, excluding .lock
// files. The session state and conversation are updated to reflect the new
// session ID.
func ForkSession(sourceSessionID string) (string, error) {
	active, err := IsSessionActive(sourceSessionID)
	if err != nil {
		return "", fmt.Errorf("check session active: %w", err)
	}
	if active {
		return "", fmt.Errorf("cannot fork session %s: session is currently active", sourceSessionID)
	}

	sourceState, err := LoadSessionState(sourceSessionID)
	if err != nil {
		return "", fmt.Errorf("load source session state: %w", err)
	}

	newSessionID := runner.GenerateSessionID()

	srcDir, err := artifacts.SessionDirectory(sourceSessionID)
	if err != nil {
		return "", fmt.Errorf("resolve source session directory: %w", err)
	}

	dstDir, err := artifacts.SessionDirectory(newSessionID)
	if err != nil {
		return "", fmt.Errorf("resolve destination session directory: %w", err)
	}

	if _, err := os.Stat(dstDir); err == nil {
		return "", fmt.Errorf("session directory already exists: %s", dstDir)
	}

	if err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if strings.HasSuffix(d.Name(), ".lock") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("resolve relative path: %w", err)
		}

		destPath := filepath.Join(dstDir, relPath)

		if d.IsDir() {
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", destPath, err)
			}
			return nil
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("create parent directory for %s: %w", destPath, err)
		}

		srcFile, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open source file %s: %w", path, err)
		}
		defer srcFile.Close()

		dstFile, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("create destination file %s: %w", destPath, err)
		}
		defer dstFile.Close()

		if _, err := io.Copy(dstFile, srcFile); err != nil {
			return fmt.Errorf("copy file %s: %w", path, err)
		}

		return nil
	}); err != nil {
		return "", fmt.Errorf("copy session directory: %w", err)
	}

	sourceState.SessionID = newSessionID

	if err := SaveSessionState(*sourceState); err != nil {
		return "", fmt.Errorf("save forked session state: %w", err)
	}

	convPath, err := artifacts.SessionConversationFile(newSessionID)
	if err != nil {
		return "", fmt.Errorf("resolve conversation file: %w", err)
	}

	convData, err := os.ReadFile(convPath)
	if err != nil {
		return "", fmt.Errorf("read conversation file: %w", err)
	}

	conv, err := conversation.Unmarshal(convData)
	if err != nil {
		return "", fmt.Errorf("unmarshal conversation: %w", err)
	}

	conv.SessionID = newSessionID

	marshaled, err := conv.Marshal()
	if err != nil {
		return "", fmt.Errorf("marshal conversation: %w", err)
	}

	if err := os.WriteFile(convPath, marshaled, 0o644); err != nil {
		return "", fmt.Errorf("write conversation file: %w", err)
	}

	return newSessionID, nil
}
