package session

import (
	"fmt"
	"os"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

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
	return os.WriteFile(path, data, 0o644)
}
