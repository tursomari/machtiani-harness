package session

import (
	"fmt"
	"os"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

// DeleteSession removes a session directory when it is not active.
func DeleteSession(sessionID string) error {
	active, err := IsSessionActive(sessionID)
	if err != nil {
		return fmt.Errorf("check session active state: %w", err)
	}
	if active {
		return fmt.Errorf("cannot delete session %s: session is currently active", sessionID)
	}

	sessionDir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		return fmt.Errorf("resolve session directory: %w", err)
	}

	if _, err := os.Stat(sessionDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("session %s does not exist", sessionID)
		}
		return fmt.Errorf("check session directory: %w", err)
	}

	if err := os.RemoveAll(sessionDir); err != nil {
		return fmt.Errorf("remove session directory: %w", err)
	}
	return nil
}
