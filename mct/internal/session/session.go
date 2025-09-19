package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tursomari/machtiani/mct/internal/contextbuilder"
)

const (
	sessionFileName = ".machtiani-session.json"
	historyLimit    = 10 // Maximum number of conversation turns to keep
)

// loadHistory loads the conversation history from the session file
func LoadHistory() ([]contextbuilder.Message, error) {
	sessionPath := getSessionPath()
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		// Return empty history if file doesn't exist
		return []contextbuilder.Message{}, nil
	}

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	var history []contextbuilder.Message
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, fmt.Errorf("failed to parse session data: %w", err)
	}

	// Limit history length
	if len(history) > historyLimit {
		history = history[len(history)-historyLimit:]
	}

	return history, nil
}

// SaveHistory saves the conversation history to the session file
func SaveHistory(history []contextbuilder.Message) error {
	sessionPath := getSessionPath()

	// Ensure directory exists
	dir := filepath.Dir(sessionPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	// Limit history before saving
	if len(history) > historyLimit {
		history = history[len(history)-historyLimit:]
	}

	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode session data: %w", err)
	}

	if err := os.WriteFile(sessionPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}

	return nil
}

// AddMessage adds a new message to the history and saves it
func AddMessage(role, content string) error {
	history, err := LoadHistory()
	if err != nil {
		return err
	}

	history = append(history, contextbuilder.Message{
		Role:    role,
		Content: content,
	})

	return SaveHistory(history)
}

// getSessionPath returns the path to the session file
func getSessionPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}

	// Use a session directory in ~/.machtiani/sessions
	sessionDir := filepath.Join(homeDir, ".machtiani", "sessions")

	// Use current timestamp to create unique sessions per day
	timestamp := time.Now().Format("2006-01-02")
	sessionFile := fmt.Sprintf("session-%s.json", timestamp)

	return filepath.Join(sessionDir, sessionFile)
}
