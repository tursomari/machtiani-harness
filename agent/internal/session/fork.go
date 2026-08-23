package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/magnifica"
	"github.com/tursomari/machtiani/agent/internal/runner"
	"github.com/tursomari/machtiani/agent/internal/sessionfiles"
)

// ValidateForkDestinationID checks that a fork destination is a single,
// path-safe opaque session ID.
func ValidateForkDestinationID(destinationSessionID string) error {
	if destinationSessionID == "" {
		return fmt.Errorf("destination session id required")
	}
	if destinationSessionID == "." || destinationSessionID == ".." || strings.ContainsAny(destinationSessionID, `/\`) {
		return fmt.Errorf("invalid destination session ID %q", destinationSessionID)
	}
	if strings.IndexFunc(destinationSessionID, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return fmt.Errorf("invalid destination session ID %q", destinationSessionID)
	}
	return nil
}

// ForkSession creates a copy of an existing session under a new session ID.
// The source session must not be active (locked). All files from the source
// session directory are copied to the new session directory, excluding .lock
// files. The session state and conversation are updated to reflect the new
// session ID.
func ForkSession(sourceSessionID, destinationSessionID string) (string, error) {
	return forkSession(sourceSessionID, destinationSessionID, ForkOptions{})
}

// ForkOptions controls optional state initialized specifically for the fork.
type ForkOptions struct {
	MagnificaHumanitas bool
}

// ForkSessionWithOptions creates a fork using the requested optional state.
func ForkSessionWithOptions(sourceSessionID, destinationSessionID string, opts ForkOptions) (string, error) {
	return forkSession(sourceSessionID, destinationSessionID, opts)
}

func forkSession(sourceSessionID, destinationSessionID string, opts ForkOptions) (string, error) {
	destinationSessionID = strings.TrimSpace(destinationSessionID)
	if destinationSessionID != "" {
		if err := ValidateForkDestinationID(destinationSessionID); err != nil {
			return "", err
		}
	}

	active, err := IsSessionActive(sourceSessionID)
	if err != nil {
		return "", fmt.Errorf("check session active: %w", err)
	}
	if active {
		return "", fmt.Errorf("cannot fork session %s: session is currently active", sourceSessionID)
	}

	// Load conversation if available for migration from per-message
	// metadata to top-level fields.
	var forkedConv *conversation.Conversation
	if convPath, convPathErr := artifacts.SessionConversationFile(sourceSessionID); convPathErr == nil {
		if data, readErr := os.ReadFile(convPath); readErr == nil {
			forkedConv, _ = conversation.Unmarshal(data)
		}
	}
	if _, err := sessionStateFromConversation(forkedConv, sourceSessionID); err != nil {
		return "", fmt.Errorf("load source session state: %w", err)
	}
	canonicalHash, err := CanonicalConversationDigest(forkedConv)
	if err != nil {
		return "", fmt.Errorf("hash source conversation: %w", err)
	}
	effectiveParent := sourceSessionID
	if forkedConv.ForkedHash != "" && canonicalHash == forkedConv.ForkedHash {
		effectiveParent = forkedConv.ForkedFrom
	}

	newSessionID := destinationSessionID
	if newSessionID == "" {
		newSessionID = runner.GenerateSessionID()
	}
	var selectedMagnifica *conversation.MagnificaHumanitas
	if opts.MagnificaHumanitas {
		corpus, err := magnifica.LoadEmbedded()
		if err != nil {
			return "", fmt.Errorf("load Magnifica Humanitas quote corpus: %w", err)
		}
		selected, err := magnifica.SelectQuoteForSession(newSessionID, corpus)
		if err != nil {
			return "", fmt.Errorf("select Magnifica Humanitas quote: %w", err)
		}
		selectedMagnifica = &conversation.MagnificaHumanitas{
			Paragraph: selected.Paragraph,
			Line:      selected.Line,
			Quote:     selected.Text,
		}
	}

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

	if _, err := copyForkSessionNode(srcDir, dstDir, ""); err != nil {
		return "", fmt.Errorf("copy session directory: %w", err)
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
	conv.Archived = false
	conv.ForkedFrom = effectiveParent
	conv.ForkedHash = canonicalHash
	conv.MagnificaHumanitas = selectedMagnifica

	marshaled, err := conv.Marshal()
	if err != nil {
		return "", fmt.Errorf("marshal conversation: %w", err)
	}

	if err := writeConversationFileAtomic(convPath, marshaled); err != nil {
		return "", fmt.Errorf("write conversation file: %w", err)
	}

	return newSessionID, nil
}

func copyForkSessionNode(source, destination, relativePath string) (bool, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return false, err
	}
	if relativePath != "" {
		if strings.HasSuffix(info.Name(), ".lock") || sessionfiles.Classify(relativePath) != sessionfiles.DisposableNone {
			return false, nil
		}
	}

	switch {
	case info.IsDir():
		children, err := os.ReadDir(source)
		if err != nil {
			return false, err
		}
		retained := false
		for _, child := range children {
			childRel := child.Name()
			if relativePath != "" {
				childRel = filepath.Join(relativePath, child.Name())
			}
			childRetained, err := copyForkSessionNode(filepath.Join(source, child.Name()), filepath.Join(destination, child.Name()), childRel)
			if err != nil {
				return false, err
			}
			retained = retained || childRetained
		}
		return retained, nil
	case info.Mode().IsRegular():
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return false, err
		}
		srcFile, err := os.Open(source)
		if err != nil {
			return false, err
		}
		dstFile, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = srcFile.Close()
			return false, err
		}
		_, copyErr := io.Copy(dstFile, srcFile)
		srcCloseErr := srcFile.Close()
		dstCloseErr := dstFile.Close()
		if copyErr != nil {
			return false, copyErr
		}
		if srcCloseErr != nil {
			return false, srcCloseErr
		}
		if dstCloseErr != nil {
			return false, dstCloseErr
		}
		return true, nil
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(source)
		if err != nil {
			return false, err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return false, err
		}
		if err := os.Symlink(target, destination); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported file type: %s", source)
	}
}
