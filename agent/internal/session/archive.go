package session

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
)

type ArchiveOptions struct {
	Since *time.Time
	Until *time.Time
}

type ArchiveReport struct {
	Archived int      `json:"archived"`
	Skipped  int      `json:"skipped"`
	IDs      []string `json:"ids"`
}

type UnarchiveReport struct {
	Unarchived int      `json:"unarchived"`
	Skipped    int      `json:"skipped"`
	IDs        []string `json:"ids"`
}

// ArchiveSession marks a session as archived without moving or deleting any
// session files. Archiving an already archived session is a no-op.
func ArchiveSession(id string) error {
	conv, path, err := loadSessionConversation(id)
	if err != nil {
		return err
	}
	if conv.Archived {
		return nil
	}
	conv.Archived = true
	if err := writeSessionConversation(path, conv); err != nil {
		return fmt.Errorf("archive session %s: %w", id, err)
	}
	return nil
}

// UnarchiveSession restores an archived session to the default session list.
// Unarchiving an active session is a no-op.
func UnarchiveSession(id string) error {
	conv, path, err := loadSessionConversation(id)
	if err != nil {
		return err
	}
	if !conv.Archived {
		return nil
	}
	conv.Archived = false
	if err := writeSessionConversation(path, conv); err != nil {
		return fmt.Errorf("unarchive session %s: %w", id, err)
	}
	return nil
}

// ArchiveSessions archives active sessions whose UpdatedAt timestamp falls in
// the inclusive range. Sessions already archived are reported as skipped.
func ArchiveSessions(options ArchiveOptions) (ArchiveReport, error) {
	var report ArchiveReport
	err := visitSessionsInRange(options, func(id, path string, conv *conversation.Conversation) error {
		if conv.Archived {
			report.Skipped++
			return nil
		}
		conv.Archived = true
		if err := writeSessionConversation(path, conv); err != nil {
			return fmt.Errorf("archive session %s: %w", id, err)
		}
		report.Archived++
		report.IDs = append(report.IDs, id)
		return nil
	})
	return report, err
}

// UnarchiveSessions restores archived sessions whose UpdatedAt timestamp falls
// in the inclusive range. Sessions not archived are reported as skipped.
func UnarchiveSessions(options ArchiveOptions) (UnarchiveReport, error) {
	var report UnarchiveReport
	err := visitSessionsInRange(options, func(id, path string, conv *conversation.Conversation) error {
		if !conv.Archived {
			report.Skipped++
			return nil
		}
		conv.Archived = false
		if err := writeSessionConversation(path, conv); err != nil {
			return fmt.Errorf("unarchive session %s: %w", id, err)
		}
		report.Unarchived++
		report.IDs = append(report.IDs, id)
		return nil
	})
	return report, err
}

func visitSessionsInRange(options ArchiveOptions, visit func(id, path string, conv *conversation.Conversation) error) error {
	if options.Since != nil && options.Until != nil && options.Since.After(*options.Until) {
		return errors.New("archive date range since must not be after until")
	}
	sessionsRoot, err := artifacts.SessionsRoot()
	if err != nil {
		return fmt.Errorf("resolve sessions root: %w", err)
	}
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read sessions directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		conv, path, err := loadSessionConversation(id)
		if err != nil {
			continue
		}
		if options.Since != nil && conv.UpdatedAt.Before(*options.Since) {
			continue
		}
		if options.Until != nil && conv.UpdatedAt.After(*options.Until) {
			continue
		}
		if err := visit(id, path, conv); err != nil {
			return err
		}
	}
	return nil
}

func loadSessionConversation(id string) (*conversation.Conversation, string, error) {
	path, err := artifacts.SessionConversationFile(id)
	if err != nil {
		return nil, "", fmt.Errorf("resolve conversation for session %s: %w", id, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read conversation for session %s: %w", id, err)
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		return nil, "", fmt.Errorf("parse conversation for session %s: %w", id, err)
	}
	return conv, path, nil
}

func writeSessionConversation(path string, conv *conversation.Conversation) error {
	data, err := conv.Marshal()
	if err != nil {
		return err
	}
	return writeConversationFileAtomic(path, data)
}
